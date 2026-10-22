package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/store"
)

// replyRow is one reply under a post, with the replies to it.
type replyRow struct {
	Author, AuthorHref, Source, Title, Date string
	ISO, Ago, Avatar, Initial               string
	ReplyHref                               string // the owner's link to answer it
	Edited                                  bool
	HTML                                    template.HTML
	Children                                []replyRow
}

type modRow struct {
	ID, PostID                             int64
	Author, Source, Title, Status, PostURL string
	HTML                                   template.HTML
	MD                                     string // the reply as written, for the Markdown form of the page
}

// repliesFor returns a post's approved replies for display, the replies to them nested under each, how many
// there are in all, and how many await approval.
func (s *Server) repliesFor(r *http.Request, q *req, postID int64) (rows []replyRow, total, pending int) {
	approved, _ := q.sp.RepliesFor(postID, store.StatusApproved)
	var subs []store.Subreply
	if len(approved) > 0 {
		subs, _ = q.sp.SubrepliesFor(postID)
	}
	direct := map[string]bool{}
	var feeds []string
	for _, rp := range approved {
		direct[rp.Source] = true
		feeds = append(feeds, rp.FeedURL)
	}
	var shown []store.Subreply // only the replies to replies the owner approved
	for _, sr := range subs {
		if direct[sr.Via] {
			shown = append(shown, sr)
			feeds = append(feeds, sr.FeedURL)
		}
	}
	infos, _ := s.st.FeedInfos(feeds)
	row := func(feedURL, source, author, title, text, markdown string, published time.Time, updated *time.Time) replyRow {
		name := orStr(author, orStr(infos[feedURL].Title, feedURL))
		rr := replyRow{Author: name, AuthorHref: siteOf(feedURL), Source: httpURL(source), Title: title, Avatar: httpURL(infos[feedURL].Image), Initial: initial(name),
			Date: stampAt(published), ISO: published.UTC().Format("2006-01-02T15:04:05Z"), Ago: ago(published), Edited: updated != nil,
			HTML: renderRemote(markdown, text)}
		if q.owner && rr.Source != "" {
			rr.ReplyHref = "/compose?reply_to=" + url.QueryEscape(rr.Source) + "&reply_feed=" + url.QueryEscape(feedURL)
		}
		return rr
	}
	// a tree is built from paths, as the rows are values: each reply knows the source of its parent
	type node struct {
		row      replyRow
		parent   string
		children []*node
	}
	byLink := map[string]*node{}
	var top []*node
	for _, rp := range approved {
		n := &node{row: row(rp.FeedURL, rp.Source, rp.Author, rp.Title, rp.Text, rp.Markdown, rp.Published, rp.Updated)}
		byLink[rp.Source] = n
		top = append(top, n)
	}
	for _, sr := range shown {
		n := &node{row: row(sr.FeedURL, sr.Source, sr.Author, sr.Title, sr.Text, "", sr.Published, nil)}
		parent := byLink[sr.Parent]
		if parent == nil {
			parent = byLink[sr.Via]
		}
		byLink[sr.Source] = n
		parent.children = append(parent.children, n)
		total++
	}
	var build func(n *node) replyRow
	build = func(n *node) replyRow {
		out := n.row
		for _, c := range n.children {
			out.Children = append(out.Children, build(c))
		}
		return out
	}
	for _, n := range top {
		rows = append(rows, build(n))
	}
	total += len(top)
	if q.owner {
		p, _ := q.sp.RepliesFor(postID, store.StatusPending)
		pending = len(p)
	}
	return
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

const excerptLen = 280

func excerpt(text string) string {
	r := []rune(text)
	if len(r) > excerptLen {
		return string(r[:excerptLen]) + "…"
	}
	return text
}

// repliesFeed publishes the approved replies to one post: the parent's downward link.
func (s *Server) repliesFeed(w http.ResponseWriter, r *http.Request, q *req) {
	p, err := q.sp.PostBySlug(q.host.ID, r.PathValue("slug"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	id := p.ID
	if p.Deleted {
		http.Error(w, "This post was removed.", http.StatusGone)
		return
	}
	replies, err := q.sp.RepliesFor(id, store.StatusApproved)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	purl := s.postURL(r, id)
	name := p.Title
	if name == "" {
		name = q.host.Display + "'s post " + strconv.FormatInt(id, 10)
	}
	ch := feed.Channel{
		Title: "Replies to: " + name, Link: purl, Description: "Approved replies",
		Links:     []feed.AtomLink{{Href: purl + "/replies.xml", Rel: "self", Type: "application/rss+xml"}},
		RepliesTo: &feed.Ref{Href: purl}, Depth: 2,
	}
	last := p.Created
	for _, rp := range replies {
		it := feed.Item{Title: rp.Title, GUID: feed.GUID{IsPermaLink: true, Value: rp.Source}, Link: rp.Source,
			Description: excerpt(rp.Text), PubDate: feed.RFC822(rp.Published),
			Feed: &feed.FeedRef{Href: rp.FeedURL, Name: rp.Author}, InReplyTo: &feed.Ref{Href: purl}}
		if rp.RepliesHref != "" {
			// where this reply's own replies are published, and how many of them are cached here
			it.Replies = &feed.Replies{Href: rp.RepliesHref, Count: q.sp.ApprovedSubreplyCount(id, rp.Source)}
		}
		ch.Items = append(ch.Items, it)
		if rp.Received.After(last) {
			last = rp.Received
		}
	}
	// second level: approved replies to the approved replies, as those replies' feeds report them
	subs, _ := q.sp.SubrepliesFor(id)
	approved := map[string]bool{}
	for _, rp := range replies {
		approved[rp.Source] = true
	}
	for _, sr := range subs {
		if !approved[sr.Via] {
			continue
		}
		ch.Items = append(ch.Items, feed.Item{Title: sr.Title, GUID: feed.GUID{IsPermaLink: true, Value: sr.Source}, Link: sr.Source,
			Description: excerpt(sr.Text), PubDate: feed.RFC822(sr.Published),
			Feed: &feed.FeedRef{Href: sr.FeedURL, Name: sr.Author}, InReplyTo: &feed.Ref{Href: sr.Parent}})
	}
	ch.LastBuildDate = feed.RFC822(last)
	s.writeChannel(w, r, ch)
}

func (s *Server) moderation(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	// pending first, then the rest
	var rows []modRow
	for _, st := range []string{store.StatusPending, store.StatusApproved, store.StatusRejected} {
		rs, err := q.sp.RepliesForUser(q.host.ID, st)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		for _, x := range rs {
			rows = append(rows, modRow{ID: x.ID, PostID: x.PostID, Author: orStr(x.Author, x.FeedURL), Source: httpURL(x.Source),
				Title: x.Title, Status: x.Status, PostURL: s.postURL(r, x.PostID), HTML: renderRemote(x.Markdown, x.Text), MD: orStr(x.Markdown, x.Text)})
		}
	}
	d := s.data(r, q, "Replies")
	d.Moderation = rows
	s.show(w, "moderation", http.StatusOK, d)
}

func (s *Server) moderateForm(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	id, ok := postID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	postID, err := q.sp.SetReplyStatus(q.host.ID, id, r.FormValue("status"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.bubble(q.sp, postID, s.postURL(r, postID))
	http.Redirect(w, r, "/replies", http.StatusSeeOther)
}

type replyJSON struct {
	ID        int64     `json:"id"`
	PostID    int64     `json:"post_id"`
	Source    string    `json:"source"`
	Author    string    `json:"author"`
	Title     string    `json:"title,omitempty"`
	Text      string    `json:"text"`
	Markdown  string    `json:"markdown,omitempty"`
	Status    string    `json:"status"`
	Published time.Time `json:"published"`
}

// apiReplies lists replies to the user's posts; ?status=pending|approved|rejected filters.
func (s *Server) apiReplies(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	rs, err := q.sp.RepliesForUser(q.host.ID, r.URL.Query().Get("status"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out := make([]replyJSON, 0, len(rs))
	for _, x := range rs {
		out = append(out, replyJSON{ID: x.ID, PostID: x.PostID, Source: x.Source, Author: x.Author, Title: x.Title,
			Text: x.Text, Markdown: x.Markdown, Status: x.Status, Published: x.Published})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiModerate(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	id, ok := postID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in) != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	postID, err := q.sp.SetReplyStatus(q.host.ID, id, in.Status)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.bubble(q.sp, postID, s.postURL(r, postID))
	w.WriteHeader(http.StatusNoContent)
}
