package web

import (
	"context"
	"encoding/xml"
	"errors"
	"html"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hbroek/text-thread-casting/internal/feed"
	"github.com/hbroek/text-thread-casting/internal/fetch"
	"github.com/hbroek/text-thread-casting/internal/md"
	"github.com/hbroek/text-thread-casting/internal/store"
)

type tlRow struct {
	Author, Title, Link, Date, InReplyTo, ReplyHref string
	HTML                                            template.HTML
	Edited                                          bool
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

// replyHref links to the compose form prefilled to reply to the item.
func replyHref(it store.TimelineItem) string {
	target := httpURL(it.GUID)
	if target == "" {
		target = httpURL(it.Link)
	}
	if target == "" {
		return ""
	}
	return "/compose?reply_to=" + url.QueryEscape(target) + "&reply_feed=" + url.QueryEscape(it.FeedURL)
}

func (s *Server) rows(userID int64) ([]tlRow, []store.TimelineItem, error) {
	items, err := s.st.Timeline(userID, 200)
	if err != nil {
		return nil, nil, err
	}
	out := make([]tlRow, 0, len(items))
	for _, it := range items {
		author := it.FeedTitle
		if author == "" {
			author = it.FeedURL
		}
		out = append(out, tlRow{Author: author, Title: it.Title, Link: httpURL(it.Link), InReplyTo: httpURL(it.InReplyTo),
			Date: it.Published.Format("2 Jan 2006 15:04"), HTML: remoteHTML(it), Edited: it.Updated != nil,
			ReplyHref: replyHref(it)})
	}
	return out, items, nil
}

func (s *Server) timeline(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	rows, _, err := s.rows(q.host.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	d := s.data(r, q, "Timeline")
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
	http.Redirect(w, r, "/following", http.StatusSeeOther)
}

func (s *Server) unfollowForm(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	if err := s.st.Unfollow(q.host.ID, r.FormValue("url")); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/following", http.StatusSeeOther)
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
	if err := s.st.SaveFeed(res.URL, p); err != nil {
		return "", err
	}
	return res.URL, s.st.Follow(userID, res.URL)
}

// Refresh re-fetches one feed and replaces its cached items. Failures are recorded, not returned.
func (s *Server) Refresh(ctx context.Context, feedURL string) {
	res, err := s.web.Get(ctx, feedURL)
	if err == nil {
		var p *feed.Parsed
		if p, err = feed.Parse(res.Body, time.Now()); err == nil {
			err = s.st.SaveFeed(feedURL, p)
		}
	}
	if err != nil {
		s.st.SaveFeedError(feedURL, err.Error())
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
		feeds := time.NewTicker(every)
		queues := time.NewTicker(time.Minute) // retries of failed jobs
		defer feeds.Stop()
		defer queues.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-feeds.C:
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
