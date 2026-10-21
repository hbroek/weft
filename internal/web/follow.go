package web

import (
	"context"
	"encoding/xml"
	"errors"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/fetch"
	"github.com/hbroek/weft/internal/md"
	"github.com/hbroek/weft/internal/store"
)

type tlRow struct {
	Author, AuthorHref, Title, Link, Date, InReplyTo, ReplyHref string
	Target, FeedURL, Tally, TallyTip, ThreadHref                string
	Mine                                                        bool
	HasThread                                                   bool // it replies to something or has replies
	HTML                                                        template.HTML
	Edited                                                      bool
	Keywords                                                    []string
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
func (s *Server) iLike(userID int64, target string) bool {
	if target == "" {
		return false
	}
	_, err := s.st.ActiveLike(userID, target)
	return err == nil
}

// replyHref links to the compose form prefilled to reply to the item.
func replyHref(it store.TimelineItem) string {
	target := itemTarget(it)
	if target == "" {
		return ""
	}
	return "/compose?reply_to=" + url.QueryEscape(target) + "&reply_feed=" + url.QueryEscape(it.FeedURL)
}

func (s *Server) rows(userID int64, feedURL string) ([]tlRow, []store.TimelineItem, error) {
	var items []store.TimelineItem
	var err error
	if feedURL != "" {
		items, err = s.st.FeedItems(userID, feedURL, 200)
	} else {
		items, err = s.st.Timeline(userID, 200)
	}
	if err != nil {
		return nil, nil, err
	}
	likers, _ := s.st.FollowedLikers(userID)
	out := make([]tlRow, 0, len(items))
	for _, it := range items {
		mine := s.iLike(userID, itemTarget(it))
		n := s.tally(userID, itemTarget(it), it.Likes)
		names := likers[itemTarget(it)]
		if mine {
			names = append([]string{"You"}, names...)
		}
		author := it.FeedTitle
		if author == "" {
			author = it.FeedURL
		}
		out = append(out, tlRow{Author: author, AuthorHref: "/timeline?feed=" + url.QueryEscape(it.FeedURL), Title: it.Title, Link: httpURL(it.Link), InReplyTo: httpURL(it.InReplyTo),
			Date: it.Published.Format("2 Jan 2006 15:04"), HTML: remoteHTML(it), Edited: it.Updated != nil, Keywords: it.Keywords,
			ReplyHref: replyHref(it), ThreadHref: threadHref(itemTarget(it), ""), HasThread: it.InReplyTo != "" || it.RepliesCount != 0, Target: itemTarget(it), FeedURL: it.FeedURL, Tally: likesText(n), TallyTip: likedBy(names, n),
			Mine: mine, when: it.Published})
	}
	return out, items, nil
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
func (s *Server) likedAs(r *http.Request, q *req, target string) string {
	if id, ok := s.localPostID(q.host.ID, s.base(r), target); ok {
		if p, err := s.st.Post(q.host.ID, id); err == nil {
			return "your post “" + oneLine(orStr(p.Title, p.Markdown), 90) + "”"
		}
	}
	if it, err := s.st.ItemByTarget(q.host.ID, target); err == nil {
		who := orStr(it.FeedTitle, it.FeedURL)
		return who + "’s post “" + oneLine(orStr(it.Title, it.Text), 90) + "”"
	}
	return target
}

// withLikes adds the likes of followed people, and the user's own, to the timeline rows, in date order.
func (s *Server) withLikes(r *http.Request, q *req, only string, rows []tlRow) ([]tlRow, error) {
	ls, err := s.st.FeedLikes(q.host.ID, only, 200)
	if err != nil {
		return rows, err
	}
	for _, l := range ls {
		rows = append(rows, tlRow{IsLike: true, Author: orStr(l.FeedTitle, l.FeedURL), AuthorHref: "/timeline?feed=" + url.QueryEscape(l.FeedURL),
			Date: l.Published.Format("2 Jan 2006 15:04"), LikedHref: httpURL(l.Target), LikedAs: s.likedAs(r, q, l.Target), when: l.Published})
	}
	mine, err := s.st.ActiveLikes(q.host.ID, 200)
	if err != nil {
		return rows, err
	}
	for _, l := range mine {
		if only != "" { // on one person's page, only likes of their posts
			if it, err := s.st.ItemByTarget(q.host.ID, l.Target); err != nil || it.FeedURL != only {
				continue
			}
		}
		rows = append(rows, tlRow{IsLike: true, Author: "You", Date: l.Created.Format("2 Jan 2006 15:04"),
			LikedHref: httpURL(l.Target), LikedAs: s.likedAs(r, q, l.Target), when: l.Created})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].when.After(rows[j].when) })
	return rows, nil
}

func (s *Server) timeline(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	only := r.FormValue("feed")
	rows, _, err := s.rows(q.host.ID, only)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	kw := feed.Keyword(r.FormValue("keyword"))
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
		if rows, err = s.withLikes(r, q, only, rows); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	d := s.data(r, q, "Timeline")
	d.ShowLikes = showLikes
	if kw != "" {
		d.Keyword, d.ClearKeyword = kw, "/timeline"
		if only != "" {
			d.ClearKeyword += "?feed=" + url.QueryEscape(only)
		}
	}
	toggle := url.Values{}
	if only != "" {
		toggle.Set("feed", only)
	}
	if !showLikes {
		toggle.Set("likes", "1")
	}
	d.LikesToggle = "/timeline"
	if len(toggle) > 0 {
		d.LikesToggle += "?" + toggle.Encode()
	}
	if only != "" {
		d.Only = only
		d.Heading = only
		if fs, err := s.st.Follows(q.host.ID); err == nil {
			for _, f := range fs {
				if f.URL == only && f.Title != "" {
					d.Heading = f.Title
				}
			}
		}
	}
	d.Timeline = rows
	s.show(w, "timeline", http.StatusOK, d)
}

func (s *Server) followingPage(w http.ResponseWriter, r *http.Request, q *req) {
	if s.requireOwner(w, r, q) {
		s.renderFollowing(w, r, q, http.StatusOK, "")
	}
}

func (s *Server) renderFollowing(w http.ResponseWriter, r *http.Request, q *req, status int, msg string) {
	fs, err := s.st.Follows(q.host.ID)
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
	if _, err := s.follow(r.Context(), q.host.ID, r.FormValue("url")); err != nil {
		s.renderFollowing(w, r, q, http.StatusBadRequest, err.Error())
		return
	}
	back(w, r, "/following")
}

func (s *Server) unfollowForm(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	if err := s.st.Unfollow(q.host.ID, r.FormValue("url")); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	back(w, r, "/following")
}

func (s *Server) refreshForm(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	fs, _ := s.st.Follows(q.host.ID)
	for _, f := range fs {
		s.Refresh(r.Context(), f.URL)
	}
	http.Redirect(w, r, "/timeline", http.StatusSeeOther)
}

// follow resolves a feed or site URL to a feed, fetches it and records the follow.
func (s *Server) follow(ctx context.Context, userID int64, raw string) (string, error) {
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
			return "", errors.New("That page does not advertise an RSS feed.")
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
	return res.URL, s.st.Follow(userID, res.URL)
}

// Follow makes the user follow a feed or site URL: the same as the Following page, for admin tools.
func (s *Server) Follow(ctx context.Context, userID int64, raw string) (string, error) {
	return s.follow(ctx, userID, raw)
}

// Refresh re-fetches one feed and replaces its cached items. Failures are recorded, not returned.
func (s *Server) Refresh(ctx context.Context, feedURL string) {
	res, err := s.web.Get(ctx, feedURL)
	if err == nil {
		var p *feed.Parsed
		if p, err = feed.Parse(res.Body, time.Now()); err == nil {
			if !s.st.HasItems(feedURL) {
				s.readArchive(ctx, feedURL, p)
			}
			err = s.st.SaveFeed(feedURL, p)
			if err == nil {
				s.refreshLikes(ctx, feedURL, p)
			}
		}
	}
	if err != nil {
		s.st.SaveFeedError(feedURL, err.Error())
	}
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

// RefreshAll refreshes every followed feed once.
func (s *Server) RefreshAll(ctx context.Context) {
	urls, err := s.st.FollowedFeedURLs()
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
func (s *Server) StartPoller(ctx context.Context, every time.Duration) {
	go func() {
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
			case <-s.wake:
				s.ProcessQueues(ctx)
			}
		}
	}()
}

// opml publishes the follow list (the public follow graph).
func (s *Server) opml(w http.ResponseWriter, r *http.Request, q *req) {
	fs, err := s.st.Follows(q.host.ID)
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
