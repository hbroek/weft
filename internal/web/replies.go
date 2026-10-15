package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/hbroek/text-thread-casting/internal/feed"
	"github.com/hbroek/text-thread-casting/internal/store"
)

type replyRow struct {
	Author, Source, Title, Date string
	HTML                        template.HTML
}

type modRow struct {
	ID, PostID                             int64
	Author, Source, Title, Status, PostURL string
	HTML                                   template.HTML
}

// repliesFor returns a post's approved replies for display, and how many await approval.
func (s *Server) repliesFor(q *req, postID int64) (rows []replyRow, pending int) {
	approved, _ := s.st.RepliesFor(postID, store.StatusApproved)
	for _, r := range approved {
		rows = append(rows, replyRow{Author: orStr(r.Author, r.FeedURL), Source: httpURL(r.Source), Title: r.Title,
			Date: r.Published.Format("2 Jan 2006 15:04"), HTML: renderRemote(r.Markdown, r.Text)})
	}
	if q.owner {
		p, _ := s.st.RepliesFor(postID, store.StatusPending)
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
	p, err := s.st.PostBySlug(q.host.ID, r.PathValue("slug"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	id := p.ID
	if p.Deleted {
		http.Error(w, "This post was removed.", http.StatusGone)
		return
	}
	replies, err := s.st.RepliesFor(id, store.StatusApproved)
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
		Self:      feed.AtomLink{Href: purl + "/replies.xml", Rel: "self", Type: "application/rss+xml"},
		RepliesTo: &feed.Ref{Href: purl}, Depth: 2,
	}
	last := p.Created
	for _, rp := range replies {
		it := feed.Item{Title: rp.Title, GUID: feed.GUID{IsPermaLink: true, Value: rp.Source}, Link: rp.Source,
			Description: excerpt(rp.Text), PubDate: feed.RFC822(rp.Published),
			Feed: &feed.FeedRef{Href: rp.FeedURL, Name: rp.Author}, InReplyTo: &feed.Ref{Href: purl}}
		ch.Items = append(ch.Items, it)
		if rp.Received.After(last) {
			last = rp.Received
		}
	}
	// second level: approved replies to the approved replies, as those replies' feeds report them
	subs, _ := s.st.SubrepliesFor(id)
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
	b, err := feed.New(ch).Marshal()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write(b)
}

func (s *Server) moderation(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	// pending first, then the rest
	var rows []modRow
	for _, st := range []string{store.StatusPending, store.StatusApproved, store.StatusRejected} {
		rs, err := s.st.RepliesForUser(q.host.ID, st)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		for _, x := range rs {
			rows = append(rows, modRow{ID: x.ID, PostID: x.PostID, Author: orStr(x.Author, x.FeedURL), Source: httpURL(x.Source),
				Title: x.Title, Status: x.Status, PostURL: s.postURL(r, x.PostID), HTML: renderRemote(x.Markdown, x.Text)})
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
	postID, err := s.st.SetReplyStatus(q.host.ID, id, r.FormValue("status"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.bubble(q.host.ID, postID, s.postURL(r, postID))
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
	rs, err := s.st.RepliesForUser(q.host.ID, r.URL.Query().Get("status"))
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
	postID, err := s.st.SetReplyStatus(q.host.ID, id, in.Status)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.bubble(q.host.ID, postID, s.postURL(r, postID))
	w.WriteHeader(http.StatusNoContent)
}
