package web

import (
	"context"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"net/http"
	"strings"
)

// likerRow is one person who liked a post, with what the owner can do about following them.
type likerRow struct {
	Name, FeedURL string
	State         string // "unfollow", "follow", "following" (followed by another address), "you", or "" for no action
	Counted       bool   // in the post's like count (the owner follows them)
	Tag           string // a short note shown beside the name
	Site          string // the liker's home page, for people not followed
}

// likedByPage lists who liked a post, like the Following page: on one of this site's posts everyone
// whose like was received (the count only includes the people the owner follows), on a post in a
// followed feed the owner and the people they follow who liked it. Each has a Follow or
// Unfollow button for the owner.
func (s *Server) likedByPage(w http.ResponseWriter, r *http.Request, q *req) {
	target := httpURL(r.FormValue("target"))
	if target == "" {
		http.NotFound(w, r)
		return
	}
	d := s.data(r, q, "Likes")
	d.LikeTarget, d.LikeTitle = target, target
	if id, ok := s.localPostID(q.sp, s.base(r), target); ok {
		p, err := q.sp.Post(q.host.ID, id)
		if err != nil || p.Deleted {
			http.NotFound(w, r)
			return
		}
		d.LikeTitle = orStr(p.Title, oneLine(p.Markdown, 60))
		xs, err := q.sp.LikesInFor(id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		for _, x := range xs {
			x.FeedURL = personFeed(x.FeedURL)
			row := likerRow{Name: orStr(x.Author, orStr(x.FeedURL, siteHost(x.Source))), FeedURL: x.FeedURL, Counted: true}
			if q.owner && x.FeedURL != "" {
				if on, _ := q.sp.IsFollowing(q.host.ID, x.FeedURL); on {
					row.State = "unfollow"
				} else if x.Trusted {
					row.State = "following"
				} else {
					row.State = "follow"
				}
			} else if q.owner && x.Trusted {
				row.State = "following"
			}
			d.Likers = append(d.Likers, row)
		}
	} else {
		if !s.requireOwner(w, r, q) {
			return
		}
		it, err := q.sp.ItemByTarget(q.host.ID, target)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		d.LikeTitle = orStr(it.Title, oneLine(it.Text, 60))
		if s.iLike(q.sp, target) {
			d.Likers = append(d.Likers, likerRow{Name: "You", State: "you", Counted: true})
		}
		// only people this user follows: the likes cache is shared by every user on the server
		fl, err := q.sp.FeedLikes(q.host.ID, "", 1000)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		own := s.base(r) + "/feed.xml"
		for i := len(fl) - 1; i >= 0; i-- { // oldest first
			if l := fl[i]; l.Target == target && l.FeedURL != own {
				d.Likers = append(d.Likers, likerRow{Name: orStr(l.FeedTitle, l.FeedURL), FeedURL: l.FeedURL, State: "unfollow", Counted: true})
			}
		}
		counted := 0
		for _, l := range d.Likers {
			if l.Counted {
				counted++
			}
		}
		n := s.tally(q.host.ID, target, it.Likes)
		if it.LikesHref != "" {
			d.Others = s.otherLikers(r.Context(), q, own, target, it.LikesHref, d.Likers)
		}
		if n > counted+len(d.Others) {
			d.LikeNote = "The author counts " + likesText(n) + ", more than can be listed here."
		}
	}
	s.show(w, "likers", http.StatusOK, d)
}

// maxOtherLikers bounds how many strangers a Likes page lists.
const maxOtherLikers = 200

// otherLikers reads the liked-by feed a followed post advertises and returns the people on it whom the
// owner does not follow, each with a Follow button. A feed that cannot be read leaves the section out.
func (s *Server) otherLikers(ctx context.Context, q *req, own, target, href string, known []likerRow) []likerRow {
	if httpURL(href) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := s.web.Get(ctx, href)
	if err != nil {
		return nil
	}
	p, err := feed.Parse(res.Body, time.Now())
	if err != nil {
		return nil
	}
	seen := map[string]bool{own: true}
	for _, k := range known {
		seen[k.FeedURL] = true
	}
	var out []likerRow
	for _, it := range p.Items {
		if it.LikeOf != "" && it.LikeOf != target {
			continue // a like of something else
		}
		fu := personFeed(it.FeedHref)
		if fu == "" || httpURL(fu) == "" || seen[fu] {
			continue
		}
		seen[fu] = true
		if on, _ := q.sp.IsFollowing(q.host.ID, fu); on {
			continue // followed, but its like is not in the cache yet
		}
		out = append(out, likerRow{Name: orStr(it.FeedName, siteHost(fu)), FeedURL: fu, State: "follow", Site: siteOf(fu)})
		if len(out) == maxOtherLikers {
			break
		}
	}
	return out
}

// siteHost is the host part of a URL, for naming a sender that gave no name.
func siteHost(raw string) string {
	raw = strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
	if i := strings.IndexAny(raw, "/?#"); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

// personFeed maps a likes feed to the same site's main feed, for likes recorded before the like
// named its author's feed.
func personFeed(u string) string {
	for _, tail := range []string{"/likes.xml", "/reactions.xml"} {
		if base, ok := strings.CutSuffix(u, tail); ok {
			return base + "/feed.xml"
		}
	}
	return u
}
