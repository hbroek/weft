package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/store"
)

func (s *Server) likeURL(r *http.Request, id int64) string {
	return fmt.Sprintf("%s/likes/%d", s.base(r), id)
}

// setLike publishes the user's like of target and notifies the target's site. Liking twice changes nothing.
func (s *Server) setLike(r *http.Request, u *store.User, target, feedHint string) (*store.Like, string) {
	target, feedHint = httpURL(target), httpURL(feedHint)
	if target == "" {
		return nil, "target must be an http(s) URL"
	}
	x, err := s.st.SetLike(u.ID, target, feedHint)
	if err != nil {
		return nil, err.Error()
	}
	s.notify(s.likeURL(r, x.ID), target)
	return x, ""
}

func (s *Server) removeLike(r *http.Request, u *store.User, target string) error {
	x, err := s.st.RemoveLike(u.ID, httpURL(target))
	if err != nil {
		return err
	}
	s.notify(s.likeURL(r, x.ID), x.Target)
	return nil
}

func (s *Server) likesFeed(w http.ResponseWriter, r *http.Request, q *req) {
	xs, err := s.st.ActiveLikes(q.host.ID, 500)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	ch := feed.Channel{
		Title: q.host.Display + " likes", Link: s.base(r) + "/", Description: "Likes published by " + q.host.Display,
		Links: []feed.AtomLink{{Href: s.base(r) + "/likes.xml", Rel: "self", Type: "application/rss+xml"}},
	}
	var last time.Time
	for _, x := range xs {
		u := s.likeURL(r, x.ID)
		it := feed.Item{GUID: feed.GUID{IsPermaLink: true, Value: u}, Link: u, PubDate: feed.RFC822(x.Created),
			LikeOf: &feed.LikeRef{Href: x.Target},
			Feed:   &feed.FeedRef{Href: s.base(r) + "/feed.xml", Name: q.host.Display}} // who liked: the channel title says "<name> likes"
		if x.Created.After(last) {
			last = x.Created
		}
		if x.Updated != nil {
			it.Updated = feed.RFC822(*x.Updated)
			if x.Updated.After(last) {
				last = *x.Updated
			}
		}
		ch.Items = append(ch.Items, it)
	}
	if last.IsZero() {
		last = time.Now()
	}
	ch.LastBuildDate = feed.RFC822(last)
	b, err := feed.New(ch).Marshal()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write(b)
}

// likePage is the HTML permalink of a like. Webmention receivers check that it links
// to the target, and its feed link leads them to the likes feed.
func (s *Server) likePage(w http.ResponseWriter, r *http.Request, q *req) {
	id, ok := postID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	x, err := s.st.Like(q.host.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d := s.data(r, q, q.host.Display+" liked")
	d.FeedURL = s.base(r) + "/likes.xml"
	if x.Deleted {
		s.show(w, "tombstone", http.StatusGone, d)
		return
	}
	d.Like = &likeData{Author: q.host.Display, Target: x.Target}
	s.show(w, "like", http.StatusOK, d)
}

type likeData struct{ Author, Target string }

func (s *Server) likeForm(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	if _, msg := s.setLike(r, q.host, r.FormValue("target"), r.FormValue("feed")); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/timeline", http.StatusSeeOther)
}

func (s *Server) unlikeForm(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	if err := s.removeLike(r, q.host, r.FormValue("target")); err != nil && err != store.ErrNotFound {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/timeline", http.StatusSeeOther)
}

type likeReq struct {
	Target     string `json:"target"`
	TargetFeed string `json:"target_feed"`
	Polarity   string `json:"polarity"` // only read by the old /api/reactions route
}

func (s *Server) apiLike(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	var in likeReq
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	x, msg := s.setLike(r, q.host, in.Target, in.TargetFeed)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, postResp{ID: x.ID, URL: s.likeURL(r, x.ID)})
}

// apiReact is the route from before there were only likes. A "+" reaction is a like; anything with
// a "-" polarity is refused, and the emoji is ignored. It goes away in the next release.
func (s *Server) apiReact(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	var in likeReq
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if in.Polarity == "-" {
		http.Error(w, "downvotes are gone; there are only likes (POST /api/likes)", http.StatusBadRequest)
		return
	}
	x, msg := s.setLike(r, q.host, in.Target, in.TargetFeed)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, postResp{ID: x.ID, URL: s.likeURL(r, x.ID)})
}

func (s *Server) apiUnlike(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	var in likeReq
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	switch err := s.removeLike(r, q.host, in.Target); err {
	case nil:
		w.WriteHeader(http.StatusNoContent)
	case store.ErrNotFound:
		http.NotFound(w, r)
	default:
		http.Error(w, err.Error(), 500)
	}
}

type receivedJSON struct {
	Source  string `json:"source"`
	Author  string `json:"author"`
	Trusted bool   `json:"trusted"`
}

// apiLikesReceived lists every verified like of one of the user's posts, with a flag
// for senders the user follows (only those count in the public tally).
func (s *Server) apiLikesReceived(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	var id int64
	if _, err := fmt.Sscan(r.URL.Query().Get("post_id"), &id); err != nil {
		http.Error(w, "post_id required", http.StatusBadRequest)
		return
	}
	if _, err := s.st.Post(q.host.ID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	xs, err := s.st.LikesInFor(id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out := make([]receivedJSON, 0, len(xs))
	for _, x := range xs {
		out = append(out, receivedJSON{Source: x.Source, Author: x.Author, Trusted: x.Trusted})
	}
	writeJSON(w, http.StatusOK, out)
}

// likesText renders a like count for display ("1 like", "3 likes"), or "" for none.
func likesText(n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return "1 like"
	}
	return fmt.Sprintf("%d likes", n)
}

// likedBy is the tooltip of a like count: the names this reader knows, then how many more there are.
// total is the count the post publishes.
func likedBy(names []string, total int) string {
	seen := map[string]bool{}
	var list []string
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" && !seen[n] && len(list) < 10 {
			seen[n] = true
			list = append(list, n)
		}
	}
	others := max(total-len(list), 0)
	if len(list) == 0 {
		return "Likes from people the author follows"
	}
	text := list[0]
	if len(list) > 1 {
		if others > 0 {
			text = strings.Join(list, ", ")
		} else {
			text = strings.Join(list[:len(list)-1], ", ") + " and " + list[len(list)-1]
		}
	}
	switch {
	case others == 1:
		text += " and 1 other"
	case others > 1:
		text += fmt.Sprintf(" and %d others", others)
	}
	return "Liked by " + text
}
