package web

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/fetch"
	"github.com/hbroek/weft/internal/md"
	"github.com/hbroek/weft/internal/store"
)

type tlRow struct {
	Anchor                                                      string // the row's id, for links back to it
	Author, AuthorHref, Title, Link, Date, InReplyTo, ReplyHref string
	ReplyWho                                                    string // whose post it replies to, for "replying to …"
	Target, FeedURL, Tally, TallyTip, ThreadHref                string
	Mine                                                        bool
	Canonical, CanonicalHost                                    string // a repost: where it first appeared
	HasThread                                                   bool   // it replies to something or has replies
	HTML                                                        template.HTML
	Edited                                                      bool
	Keywords                                                    []string
	ISO, Ago                                                    string
	MD                                                          string // the post as written, for the Markdown form of the page
	Avatar, Initial                                             string
	Parent                                                      *parentCard
	Own                                                         bool // one of your own posts, shown in the timeline
	EditHref                                                    string
	// a like by someone you follow, shown only when likes are switched on
	IsLike             bool
	LikedHref, LikedAs string
	when               time.Time
}

type followRow struct{ URL, Title, Err string }

// httpURL returns s if it is an absolute http(s) URL, else "".
func httpURL(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}

// remoteHTML renders an untrusted remote item for display: Markdown goes through the safe
// renderer, plain RSS text is escaped. Remote HTML is never passed through.
func remoteHTML(it store.TimelineItem) template.HTML { return renderRemote(it.Markdown, it.Text) }

func renderRemote(markdown, text string) template.HTML {
	if markdown != "" {
		if h, err := md.Render(markdown); err == nil {
			return template.HTML(h) // goldmark safe mode: raw HTML dropped, dangerous URLs removed
		}
	}
	var b strings.Builder
	for _, para := range strings.Split(text, "\n\n") {
		if para = strings.TrimSpace(para); para != "" {
			b.WriteString("<p>" + strings.ReplaceAll(html.EscapeString(para), "\n", "<br>") + "</p>\n")
		}
	}
	return template.HTML(b.String())
}

// itemTarget is the permalink other posts use to point at the item.
func itemTarget(it store.TimelineItem) string {
	if t := httpURL(it.GUID); t != "" {
		return t
	}
	return httpURL(it.Link)
}

// iLike reports whether the user currently likes target.
func (s *Server) iLike(sp *store.Space, target string) bool {
	userID := sp.UID()
	if target == "" {
		return false
	}
	_, err := sp.ActiveLike(userID, target)
	return err == nil
}

// replyHref links to the compose form prefilled to reply to the item.
func replyHref(it store.TimelineItem, back string) string {
	target := itemTarget(it)
	if target == "" {
		return ""
	}
	return "/compose?reply_to=" + url.QueryEscape(target) + "&reply_feed=" + url.QueryEscape(it.FeedURL) + backParam(back)
}

// backParam makes the reply link remember which timeline view it was clicked in, so that the reply returns there.
func backParam(back string) string {
	if back = timelineBack(back); back == "" {
		return ""
	}
	return "&back=" + url.QueryEscape(back)
}

// timelinePage is how many cards one page of the timeline shows; "Older posts" leads to the next.
const timelinePage = 50

// tlPage is one page of the timeline being read: the sources (followed posts, likes, your own posts)
// each give their newest limit entries older than before. A source that gave all limit of them may
// have more; edge is the newest date at which such a source stops, and nothing older is complete.
type tlPage struct {
	before int64 // Unix time: only older entries; 0 for the first page
	limit  int
	edge   int64
}

// took notes how many entries a source gave and when its oldest was made.
func (pg *tlPage) took(n int, oldest time.Time) {
	if n >= pg.limit && n > 0 && oldest.Unix() > pg.edge {
		pg.edge = oldest.Unix()
	}
}

// cut keeps the first page of rows (newest first): only those the sources have given completely, at
// most one page of them, and every row of the minute the page ends in, since the address of the next
// page names a minute. next is the Unix time the next page starts before (a whole minute), or 0 when
// there is no more.
func (pg *tlPage) cut(rows []tlRow, size int) (page []tlRow, next int64) {
	minute := func(i int) int64 { return rows[i].when.Unix() / 60 }
	more := false
	if pg.edge > 0 { // the minute the edge is in is not complete either
		n := sort.Search(len(rows), func(i int) bool { return rows[i].when.Unix() < pg.edge/60*60+60 })
		rows, more = rows[:n], true
	}
	if len(rows) > size {
		end := size
		for end < len(rows) && minute(end) == minute(size-1) {
			end++
		}
		if end < len(rows) {
			rows, more = rows[:end], true
		}
	}
	switch {
	case !more:
		return rows, 0
	case len(rows) == 0:
		return rows, pg.edge / 60 * 60 // a minute with more posts than a read holds is skipped rather than shown again forever
	}
	return rows, rows[len(rows)-1].when.Unix() / 60 * 60
}

// beforeStamp is how an address names a minute: 2026-10-27_14-35 (UTC, 24 hours). Its characters need
// no escaping in an address.
const beforeStamp = "2006-01-02_15-04"

// parseBefore reads that, or the same with a colon, a T or a space between the day and the hour, or a Unix time.
func parseBefore(v string) int64 {
	v = strings.TrimSpace(v)
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	for _, layout := range []string{beforeStamp, "2006-01-02:15:04", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func (s *Server) rows(r *http.Request, q *req, feedURL string, pg *tlPage) ([]tlRow, []store.TimelineItem, error) {
	sp := q.sp
	userID := sp.UID()
	var items []store.TimelineItem
	var err error
	if feedURL != "" {
		items, err = sp.FeedItemsBefore(userID, feedURL, pg.before, pg.limit)
	} else {
		items, err = sp.TimelineBefore(userID, pg.before, pg.limit)
	}
	if err != nil {
		return nil, nil, err
	}
	if n := len(items); n > 0 {
		pg.took(n, items[n-1].Published)
	}
	likers, _ := sp.FollowedLikers(userID)
	out := make([]tlRow, 0, len(items))
	for _, it := range items {
		mine := s.iLike(sp, itemTarget(it))
		n := s.tally(userID, itemTarget(it), it.Likes)
		names := likers[itemTarget(it)]
		if mine {
			names = append([]string{"You"}, names...)
		}
		author := it.FeedTitle
		if author == "" {
			author = it.FeedURL
		}
		out = append(out, tlRow{Anchor: anchorID(itemTarget(it)), Parent: newParentCard(it.InReplyTo, it.Parent), Avatar: httpURL(it.FeedImage), Initial: initial(author), Author: author, AuthorHref: "/timeline?feed=" + url.QueryEscape(it.FeedURL), Title: it.Title, Link: httpURL(it.Link), InReplyTo: httpURL(it.InReplyTo),
			Date: stampAt(it.Published), ISO: it.Published.UTC().Format("2006-01-02T15:04:05Z"), Ago: ago(it.Published), MD: orStr(it.Markdown, it.Text), HTML: remoteHTML(it), Edited: it.Updated != nil, Keywords: it.Keywords,
			ReplyHref: replyHref(it, r.URL.RequestURI()), ThreadHref: threadHref(itemTarget(it), ""), HasThread: it.InReplyTo != "" || it.RepliesCount != 0, Canonical: httpURL(it.Canonical), CanonicalHost: siteHost(httpURL(it.Canonical)), Target: itemTarget(it), FeedURL: it.FeedURL, Tally: likesText(n), TallyTip: likedBy(names, n),
			Mine: mine, when: it.Published})
	}
	return out, items, nil
}

// addContext tells who each reply answers and shows the post it answers, as far as it is known; only
// the rows of the page need it, because it may fetch the post.
func (s *Server) addContext(r *http.Request, q *req, rows []tlRow) {
	for i := range rows {
		row := &rows[i]
		if row.InReplyTo == "" {
			continue
		}
		who, card := s.replyContext(r, q, row.InReplyTo)
		row.ReplyWho = who
		if row.Parent == nil {
			row.Parent = card
		}
	}
}

// oneLine shortens text to about n runes on one line.
func oneLine(text string, n int) string {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > n {
		return string(r[:n]) + "…"
	}
	return text
}

// likedAs describes the post at target for a "liked …" line: one of the user's own posts, a post in a
// followed feed, or just its address.
func (s *Server) likedAs(r *http.Request, q *req, target string, info feed.ParentInfo) string {
	if id, ok := s.localPostID(q.sp, s.base(r), target); ok {
		if p, err := q.sp.Post(q.host.ID, id); err == nil {
			return "your post “" + oneLine(orStr(p.Title, p.Markdown), 90) + "”"
		}
	}
	if it, err := q.sp.ItemByTarget(q.host.ID, target); err == nil {
		who := orStr(it.FeedTitle, it.FeedURL)
		return who + "’s post “" + oneLine(orStr(it.Title, it.Text), 90) + "”"
	}
	if info.Title != "" { // a page that publishes no feed: what the like said it was
		return "“" + oneLine(info.Title, 90) + "” (" + siteHost(target) + ")"
	}
	return target
}

// withLikes adds the likes of followed people, and the user's own, to the timeline rows, in date order.
func (s *Server) withLikes(r *http.Request, q *req, only string, rows []tlRow, pg *tlPage) ([]tlRow, error) {
	ls, err := q.sp.FeedLikesBefore(q.host.ID, only, pg.before, pg.limit)
	if err != nil {
		return rows, err
	}
	if n := len(ls); n > 0 {
		pg.took(n, ls[n-1].Published)
	}
	for _, l := range ls {
		rows = append(rows, tlRow{IsLike: true, Author: orStr(l.FeedTitle, l.FeedURL), AuthorHref: "/timeline?feed=" + url.QueryEscape(l.FeedURL),
			Date: stamp(l.Published), LikedHref: httpURL(l.Target), LikedAs: s.likedAs(r, q, l.Target, l.Parent), when: l.Published})
	}
	mine, err := q.sp.ActiveLikesBefore(q.host.ID, pg.before, pg.limit)
	if err != nil {
		return rows, err
	}
	if n := len(mine); n > 0 {
		pg.took(n, mine[n-1].Created)
	}
	for _, l := range mine {
		if only != "" { // on one person's page, only likes of their posts
			if it, err := q.sp.ItemByTarget(q.host.ID, l.Target); err != nil || it.FeedURL != only {
				continue
			}
		}
		rows = append(rows, tlRow{IsLike: true, Author: "You", Date: stamp(l.Created),
			Mine: true, Target: l.Target, LikedHref: httpURL(l.Target), LikedAs: s.likedAs(r, q, l.Target, l.Parent), when: l.Created})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].when.After(rows[j].when) })
	return rows, nil
}

// ownRows are the user's own posts as timeline rows, so a post (or a reply to someone) shows up
// where its conversation is read. They carry the like tally of the post's page, not a like button.
func (s *Server) ownRows(r *http.Request, q *req, pg *tlPage) ([]tlRow, error) {
	posts, err := q.sp.PostsBefore(q.host.ID, pg.before, pg.limit)
	if err != nil {
		return nil, err
	}
	if n := len(posts); n > 0 {
		pg.took(n, posts[n-1].Created)
	}
	tallies, _ := q.sp.Tallies(q.host.ID)
	likers, _ := q.sp.Likers(q.host.ID)
	counts, _ := q.sp.ApprovedCounts(q.host.ID)
	out := make([]tlRow, 0, len(posts))
	for _, p := range posts {
		pu := s.postURL(r, p.ID)
		out = append(out, tlRow{Anchor: anchorID(pu), Own: true, Canonical: p.Canonical, CanonicalHost: siteHost(p.Canonical), Parent: newParentCard(p.InReplyTo, p.Parent), Avatar: q.host.Avatar, Initial: initial(orStr(q.host.Display, q.host.Name)), Author: "You", AuthorHref: "/", Title: p.Title, Link: pu, InReplyTo: httpURL(p.InReplyTo),
			Date: stampAt(p.Created), ISO: p.Created.UTC().Format("2006-01-02T15:04:05Z"), Ago: ago(p.Created), MD: absMedia(p.Markdown, s.base(r)), HTML: template.HTML(p.HTML), // goldmark safe mode
			Edited: p.Updated != nil, Keywords: p.Keywords, EditHref: "/posts/" + p.Slug + "/edit",
			ReplyHref: "/compose?reply_to=" + url.QueryEscape(pu) + backParam(r.URL.RequestURI()), ThreadHref: threadHref(pu, ""), HasThread: p.InReplyTo != "" || counts[p.ID] > 0,
			Tally: likesText(tallies[p.ID]), TallyTip: likedBy(likers[p.ID], tallies[p.ID]), when: p.Created})
	}
	return out, nil
}

func (s *Server) timeline(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	only := r.FormValue("feed")
	kw := feed.Keyword(r.FormValue("keyword"))
	pg := &tlPage{limit: 2 * timelinePage} // a page is cut to what every source gave whole, so read more than a page of each
	if kw != "" {                          // the filter drops most of what the sources give, so read more of it
		pg.limit = 8 * timelinePage
	}
	pg.before = parseBefore(r.FormValue("before"))
	rows, _, err := s.rows(r, q, only, pg)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// your own posts join the full timeline; on one person's page they would not belong
	showMine := only == "" && r.FormValue("mine") != "0"
	if showMine {
		own, err := s.ownRows(r, q, pg)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		rows = append(rows, own...)
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].when.After(rows[j].when) })
	}
	if kw != "" {
		// a keyword filter shows posts only; likes carry no keywords
		kept := rows[:0]
		for _, row := range rows {
			if !row.IsLike && feed.HasKeyword(row.Keywords, kw) {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	showLikes := kw == "" && r.FormValue("likes") == "1"
	if showLikes {
		if rows, err = s.withLikes(r, q, only, rows, pg); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	rows, next := pg.cut(rows, timelinePage)
	s.addContext(r, q, rows)
	d := s.data(r, q, "Timeline")
	d.ShowLikes = showLikes
	d.ShowMine = showMine
	// link builds a timeline address that keeps the feed, likes and own-posts choices except the one flipped
	link := func(flipLikes, flipMine bool) string {
		v := url.Values{}
		if only != "" {
			v.Set("feed", only)
		}
		if showLikes != flipLikes && kw == "" {
			v.Set("likes", "1")
		}
		if showMine == flipMine && only == "" {
			v.Set("mine", "0")
		}
		if len(v) == 0 {
			return "/timeline"
		}
		return "/timeline?" + v.Encode()
	}
	if kw != "" {
		d.Keyword, d.ClearKeyword = kw, link(false, false)
	}
	d.LikesToggle, d.MineToggle = link(true, false), link(false, true)
	if only != "" {
		d.Only = only
		d.Heading = only
		if fs, err := q.sp.Follows(q.host.ID); err == nil {
			for _, f := range fs {
				if f.URL == only && f.Title != "" {
					d.Heading = f.Title
				}
			}
		}
	}
	d.Timeline = rows
	if next != 0 {
		v := r.URL.Query()
		v.Set("before", time.Unix(next, 0).UTC().Format(beforeStamp))
		d.Older = "/timeline?" + v.Encode()
	}
	if pg.before > 0 {
		v := r.URL.Query()
		v.Del("before")
		d.Newest = "/timeline"
		if len(v) > 0 {
			d.Newest += "?" + v.Encode()
		}
	}
	s.show(w, "timeline", http.StatusOK, d)
}

func (s *Server) followingPage(w http.ResponseWriter, r *http.Request, q *req) {
	if s.requireOwner(w, r, q) {
		s.renderFollowing(w, r, q, http.StatusOK, "")
	}
}

func (s *Server) renderFollowing(w http.ResponseWriter, r *http.Request, q *req, status int, msg string) {
	fs, err := q.sp.Follows(q.host.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	d := s.data(r, q, "Following")
	d.Error = msg
	for _, f := range fs {
		d.Follows = append(d.Follows, followRow{URL: f.URL, Title: f.Title, Err: f.Error})
	}
	s.show(w, "following", status, d)
}

func (s *Server) followForm(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	if _, err := s.follow(r.Context(), q.sp, r.FormValue("url")); err != nil {
		s.renderFollowing(w, r, q, http.StatusBadRequest, err.Error())
		return
	}
	back(w, r, "/following", "")
}

func (s *Server) unfollowForm(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	if err := s.unfollow(q.sp, r.FormValue("url")); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	back(w, r, "/following", "")
}

func (s *Server) refreshForm(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	fs, _ := q.sp.Follows(q.host.ID)
	for _, f := range fs {
		s.Refresh(r.Context(), f.URL)
	}
	http.Redirect(w, r, "/timeline", http.StatusSeeOther)
}

// follow resolves a feed or site URL to a feed, fetches it and records the follow.
func (s *Server) follow(ctx context.Context, sp *store.Space, raw string) (string, error) {
	userID := sp.UID()
	raw = httpURL(raw)
	if raw == "" {
		return "", errors.New("Enter a full http:// or https:// address.")
	}
	res, err := s.web.Get(ctx, raw)
	if err != nil {
		return "", errors.New("Could not fetch that address: " + err.Error())
	}
	if res.LooksLikeHTML() {
		link, ok := fetch.FindFeedLink(res.Body, res.URL)
		if !ok {
			return "", errors.New("That page does not advertise an RSS or Atom feed.")
		}
		if res, err = s.web.Get(ctx, link); err != nil {
			return "", errors.New("Could not fetch the feed: " + err.Error())
		}
	}
	p, err := feed.Parse(res.Body, time.Now())
	if err != nil {
		return "", err
	}
	if !s.st.HasItems(res.URL) {
		s.readArchive(ctx, res.URL, p)
	}
	if err := s.st.SaveFeed(res.URL, p); err != nil {
		return "", err
	}
	s.refreshLikes(ctx, res.URL, p)
	if err := sp.Follow(userID, res.URL); err != nil {
		return "", err
	}
	s.retrustLikes(sp)
	return res.URL, nil
}

// unfollow stops following a feed and recounts the likes on the user's posts.
func (s *Server) unfollow(sp *store.Space, feedURL string) error {
	userID := sp.UID()
	if err := sp.Unfollow(userID, feedURL); err != nil {
		return err
	}
	s.retrustLikes(sp)
	return nil
}

// retrustLikes makes the likes on the user's posts count exactly when their senders are followed
// now, so following someone counts the likes they already gave and unfollowing drops them.
func (s *Server) retrustLikes(sp *store.Space) {
	userID := sp.UID()
	xs, err := sp.LikesInOf(userID)
	if err != nil {
		return
	}
	for _, x := range xs {
		if x.FeedURL == "" {
			continue
		}
		want, err := sp.IsFollowingSite(userID, personFeed(x.FeedURL), siteOf(x.FeedURL))
		if err == nil && want != x.Trusted {
			sp.SetLikeInTrust(x.PostID, x.Source, want)
		}
	}
}

// Follow makes the user follow a feed or site URL: the same as the Following page, for admin tools.
func (s *Server) Follow(ctx context.Context, name, raw string) (string, error) {
	sp, release, err := s.st.Space(name)
	if err != nil {
		return "", err
	}
	defer release()
	return s.follow(ctx, sp, raw)
}

// Refresh re-fetches one feed and replaces its cached items. Failures are recorded, not returned.
func (s *Server) Refresh(ctx context.Context, feedURL string) {
	etag, modified := s.st.Validators(feedURL)
	res, err := s.web.GetIf(ctx, feedURL, etag, modified)
	if err == nil && res.NotModified {
		s.st.FeedUnchanged(feedURL)
		return
	}
	if err == nil {
		var p *feed.Parsed
		if p, err = feed.Parse(res.Body, time.Now()); err == nil {
			if u := httpURL(res.URL); res.Moved && u != "" && u != feedURL {
				// permanently redirected, and what it redirects to is a feed: it moved
				s.followMove(ctx, feedURL, u)
				return
			}
			if !s.st.HasItems(feedURL) {
				s.readArchive(ctx, feedURL, p)
			}
			err = s.st.SaveFeed(feedURL, p)
			if err == nil {
				s.st.SaveValidators(feedURL, res.Header.Get("ETag"), res.Header.Get("Last-Modified"))
				s.refreshLikes(ctx, feedURL, p)
			}
		}
	}
	if err != nil {
		var se *fetch.StatusError
		var wait time.Duration
		if errors.As(err, &se) {
			wait = se.RetryAfter
		}
		s.st.SaveFeedErrorRetry(feedURL, err.Error(), wait)
	}
}

type hopsKey struct{}

// maxMoveHops bounds how many moves one refresh follows.
const maxMoveHops = 3

// followMove acts on a feed that was permanently redirected (301 or 308) to another feed, which is how
// a feed says it has moved: every user here who follows oldURL follows newURL instead, and the old
// feed's cached posts are dropped, so each post is seen once. The old address is trusted to say where
// its own feed went; that it leads to a feed was already checked, since the redirect was only
// acted on after the feed there parsed. Feeds that send readers round in a circle stop after a few hops.
func (s *Server) followMove(ctx context.Context, oldURL, newURL string) {
	hops, _ := ctx.Value(hopsKey{}).(int)
	if hops >= maxMoveHops {
		return
	}
	ctx = context.WithValue(ctx, hopsKey{}, hops+1)
	names, err := s.st.FollowersOf(oldURL)
	if err != nil {
		return
	}
	for _, n := range names {
		sp, release, err := s.st.Space(n)
		if err != nil {
			continue
		}
		if err := sp.ReplaceFollow(sp.UID(), oldURL, newURL); err != nil {
			log.Printf("moving %s from %s to %s: %v", n, oldURL, newURL, err)
		}
		release()
	}
	s.Refresh(ctx, newURL)
	s.st.DropFeed(oldURL)
	log.Printf("feed %s moved to %s: %d followers moved with it", oldURL, newURL, len(names))
}

// refreshLikes caches the likes of the person who publishes feedURL, from the likes feed their main feed
// advertises. A missing or broken likes feed leaves what is cached alone.
func (s *Server) refreshLikes(ctx context.Context, feedURL string, p *feed.Parsed) {
	if p.LikesFeed == "" {
		return
	}
	base, err := url.Parse(feedURL)
	if err != nil {
		return
	}
	u, err := base.Parse(p.LikesFeed)
	if err != nil || httpURL(u.String()) == "" {
		return
	}
	res, err := s.web.Get(ctx, u.String())
	if err != nil {
		return
	}
	lp, err := feed.Parse(res.Body, time.Now())
	if err != nil {
		return
	}
	s.st.SaveFeedLikes(feedURL, lp.Items)
}

// maxArchivePages bounds how many older pages of a paged feed (RFC 5005) are read when a feed is
// followed for the first time.
const maxArchivePages = 20

// readArchive follows rel="next" links from the first page of a feed and adds the older items to p,
// so a new follower gets the history and not only the newest page. It stays on the feed's host.
func (s *Server) readArchive(ctx context.Context, feedURL string, p *feed.Parsed) {
	base, err := url.Parse(feedURL)
	if err != nil {
		return
	}
	seen := map[string]bool{feedURL: true}
	next := p.Next
	for i := 0; i < maxArchivePages && next != "" && ctx.Err() == nil; i++ {
		u, err := base.Parse(next)
		if err != nil || u.Host != base.Host || seen[u.String()] {
			return
		}
		seen[u.String()] = true
		res, err := s.web.Get(ctx, u.String())
		if err != nil {
			return
		}
		older, err := feed.Parse(res.Body, time.Now())
		if err != nil {
			return
		}
		p.Items = append(p.Items, older.Items...)
		next = older.Next
	}
}

// RefreshAll refreshes every followed feed once, except those waiting out a backoff after failures.
// "Refresh now" does not wait.
func (s *Server) RefreshAll(ctx context.Context) {
	urls, err := s.st.DueFeedURLs(time.Now())
	if err != nil {
		return
	}
	for _, u := range urls {
		if ctx.Err() != nil {
			return
		}
		s.Refresh(ctx, u)
	}
}

// StartPoller runs the background work until ctx is done: refreshing followed feeds every
// interval, and processing the Webmention queues now and then and whenever one is kicked.
//
// It returns a channel that is closed once the loop has stopped (after ctx is cancelled), so a
// shutdown can wait for a refresh in progress before it closes the database.
func (s *Server) StartPoller(ctx context.Context, every time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		// An interval of zero or less switches the background refresh off; followed feeds then
		// refresh only on "Refresh now". A nil channel never fires.
		var refresh <-chan time.Time
		if every > 0 {
			feeds := time.NewTicker(every)
			defer feeds.Stop()
			refresh = feeds.C
		}
		queues := time.NewTicker(time.Minute) // retries of failed jobs
		defer queues.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-refresh:
				s.RefreshAll(ctx)
			case <-queues.C:
				s.ProcessQueues(ctx)
				s.endMoves()
				go s.checkDomains(ctx, time.Now())
			case <-s.wake:
				s.ProcessQueues(ctx)
			}
		}
	}()
	return done
}

// endMoves puts the files of users who moved away into the trash, and empties the trash of old ones.
func (s *Server) endMoves() {
	now := time.Now()
	if names, err := s.st.EndLapsed(now); err != nil {
		log.Printf("ending accounts whose domain lapsed: %v", err)
	} else if len(names) > 0 {
		log.Printf("%d account(s) removed because their domain stopped verifying for %d days: %s", len(names), store.GraceDays, strings.Join(names, ", "))
	}
	s.retractRemoved()
	if n, err := s.st.TrashMoved(now); err != nil {
		log.Printf("trashing moved accounts: %v", err)
	} else if n > 0 {
		log.Printf("%d moved account(s): their files wait in the trash", n)
	}
	s.st.PurgeTrash(now)
	s.st.ExpireMoves(now)
}

// opml publishes the follow list (the public follow graph).
func (s *Server) opml(w http.ResponseWriter, r *http.Request, q *req) {
	fs, err := q.sp.Follows(q.host.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	type outline struct {
		Type    string `xml:"type,attr"`
		Text    string `xml:"text,attr"`
		XMLURL  string `xml:"xmlUrl,attr"`
		HTMLURL string `xml:"htmlUrl,attr,omitempty"`
	}
	var doc struct {
		XMLName xml.Name  `xml:"opml"`
		Version string    `xml:"version,attr"`
		Title   string    `xml:"head>title"`
		Body    []outline `xml:"body>outline"`
	}
	doc.Version, doc.Title = "2.0", q.host.Display+" follows"
	for _, f := range fs {
		t := f.Title
		if t == "" {
			t = f.URL
		}
		doc.Body = append(doc.Body, outline{Type: "rss", Text: t, XMLURL: f.URL, HTMLURL: f.Link})
	}
	w.Header().Add("Vary", "Accept")
	if wantsMarkdown(r) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		var b strings.Builder
		fmt.Fprintf(&b, "# %s\n\n", mdLine(doc.Title))
		for _, o := range doc.Body {
			line := "- " + mdLink(o.Text, o.XMLURL)
			if o.HTMLURL != "" {
				line += " (" + o.HTMLURL + ")"
			}
			b.WriteString(line + "\n")
		}
		if len(doc.Body) == 0 {
			b.WriteString("Not following anyone yet.\n")
		}
		io.WriteString(w, b.String())
		return
	}
	b, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/x-opml; charset=utf-8")
	w.Write(append([]byte(xml.Header), b...))
}

// siteOf is the home page of the site that publishes feedURL: the feed's address without its
// feed.xml, or "" when it is not an http(s) URL.
func siteOf(feedURL string) string {
	u, err := url.Parse(httpURL(feedURL))
	if err != nil || u.Host == "" {
		return ""
	}
	u.RawQuery, u.Fragment = "", ""
	if i := strings.LastIndex(u.Path, "/"); strings.HasSuffix(u.Path, ".xml") || strings.HasSuffix(u.Path, ".rss") || strings.HasSuffix(u.Path, ".atom") {
		u.Path = u.Path[:i+1]
	}
	return u.String()
}
