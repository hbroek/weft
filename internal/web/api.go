package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/store"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

type postReq struct {
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
	// Keywords are optional words or phrases, published as the item's <category> elements.
	Keywords []string `json:"keywords"`
	// Slug is an optional address for the post: the end of its permalink. It is cleaned to lowercase letters,
	// digits and hyphens, and cannot be changed after the post is made.
	Slug string `json:"slug"`
	// Canonical is optional: the web address where the content first appeared, for a post that republishes it.
	// An edit replaces it, so leaving it out of an edit removes it.
	Canonical string `json:"canonical"`
	// InReplyTo makes the post a reply to the post at this permalink. InReplyToFeed is an
	// optional hint: the parent's feed URL.
	InReplyTo     string `json:"in_reply_to"`
	InReplyToFeed string `json:"in_reply_to_feed"`
	// Context is optional: what the parent page looked like (title, description, image), for a reply
	// to a page that publishes no feed. It is published as attributes of weft:context.
	Context *postContext `json:"context"`
}

type postContext struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image"`
}

type postResp struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}

// decode reads the body of a write: JSON, or with Content-Type text/markdown a document (see postdoc.go).
// id is the post a document says it is, 0 if it does not say.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, q *req) (in postReq, id int64, ok bool) {
	if err := s.bearer(r, q); err != nil {
		s.denied(w, r, q, err)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "the body is larger than 1 MB", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "could not read the body", http.StatusBadRequest)
		}
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "text/markdown" {
		if in, id, err = parseDoc(string(body)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else if err := json.NewDecoder(bytes.NewReader(body)).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	return in, id, true
}

func (s *Server) reply(w http.ResponseWriter, r *http.Request, status int, id int64) {
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusCreated {
		w.Header().Set("Location", s.postURL(r, id))
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(postResp{ID: id, URL: s.postURL(r, id)})
}

func (s *Server) apiCreate(w http.ResponseWriter, r *http.Request, q *req) {
	in, _, ok := s.decode(w, r, q)
	if !ok {
		return
	}
	if in.InReplyTo != "" && httpURL(in.InReplyTo) == "" {
		http.Error(w, "in_reply_to must be an http(s) URL", http.StatusBadRequest)
		return
	}
	var parent feed.ParentInfo
	if in.Context != nil {
		parent = feed.ParentInfo{Title: in.Context.Title, Description: in.Context.Description, Image: in.Context.Image}
	}
	p, msg := s.create(r, q.host, in.Title, in.Markdown, in.Slug, feed.Keywords(in.Keywords), in.Canonical, httpURL(in.InReplyTo), httpURL(in.InReplyToFeed), parent)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	s.reply(w, r, http.StatusCreated, p.ID)
}

// postByRef finds the host user's post that ref names: its number, or its slug (the end of its address).
func (s *Server) postByRef(q *req, ref string) (*store.Post, error) {
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return q.sp.Post(q.host.ID, id)
	}
	return q.sp.PostBySlug(q.host.ID, ref)
}

func (s *Server) apiUpdate(w http.ResponseWriter, r *http.Request, q *req) {
	in, id, ok := s.decode(w, r, q)
	if !ok {
		return
	}
	p, err := s.postByRef(q, r.PathValue("ref"))
	if err != nil || p.Deleted {
		http.NotFound(w, r)
		return
	}
	if id != 0 && id != p.ID {
		http.Error(w, "the document is post "+itoa(id)+", not post "+itoa(p.ID), http.StatusConflict)
		return
	}
	if in.Slug != "" && in.Slug != p.Slug { // the address is chosen once, when the post is made
		http.Error(w, "the address of a post cannot be changed: this one is "+p.Slug, http.StatusConflict)
		return
	}
	if _, msg := s.update(r, q.host, p.ID, in.Title, in.Markdown, feed.Keywords(in.Keywords), in.Canonical); msg != "" {
		code := http.StatusBadRequest
		if msg == "not found" {
			code = http.StatusNotFound
		}
		http.Error(w, msg, code)
		return
	}
	s.reply(w, r, http.StatusOK, p.ID)
}

func (s *Server) apiDelete(w http.ResponseWriter, r *http.Request, q *req) {
	if err := s.bearer(r, q); err != nil {
		s.denied(w, r, q, err)
		return
	}
	p, err := s.postByRef(q, r.PathValue("ref"))
	if err != nil || p.Deleted {
		http.NotFound(w, r)
		return
	}
	if err := s.deletePost(r, q.host, p); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// postJSON is a post as the API gives it. The list leaves out the body.
type postJSON struct {
	ID            int64        `json:"id"`
	URL           string       `json:"url"`
	Title         string       `json:"title,omitempty"`
	Markdown      string       `json:"markdown,omitempty"`
	Keywords      []string     `json:"keywords,omitempty"`
	Canonical     string       `json:"canonical,omitempty"`
	InReplyTo     string       `json:"in_reply_to,omitempty"`
	InReplyToFeed string       `json:"in_reply_to_feed,omitempty"`
	Context       *postContext `json:"context,omitempty"`
	Published     time.Time    `json:"published"`
	Updated       *time.Time   `json:"updated,omitempty"`
}

func (s *Server) postJSON(r *http.Request, p *store.Post, body bool) postJSON {
	j := postJSON{ID: p.ID, URL: s.base(r) + "/posts/" + p.Slug, Title: p.Title, Keywords: p.Keywords, Canonical: p.Canonical,
		InReplyTo: p.InReplyTo, InReplyToFeed: p.InReplyToFeed, Published: p.Created, Updated: p.Updated}
	if body {
		j.Markdown = p.Markdown
	}
	if !p.Parent.Empty() {
		j.Context = &postContext{Title: p.Parent.Title, Description: p.Parent.Description, Image: p.Parent.Image}
	}
	return j
}

// wantsDocument reports whether the Accept header prefers a Markdown document to JSON. JSON is the default.
func wantsDocument(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		mt, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		switch strings.ToLower(mt) {
		case "text/markdown":
			return true
		case "application/json", "*/*", "application/*":
			return false
		}
	}
	return false
}

// apiGetPost gives one of the host user's posts, with its Markdown as stored: JSON, or with
// Accept: text/markdown a document that apiUpdate takes back.
func (s *Server) apiGetPost(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	p, err := s.postByRef(q, r.PathValue("ref"))
	if err != nil || p.Deleted {
		http.NotFound(w, r)
		return
	}
	w.Header().Add("Vary", "Accept")
	if wantsDocument(r.Header.Get("Accept")) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		io.WriteString(w, postDoc(p, s.base(r)+"/posts/"+p.Slug))
		return
	}
	writeJSON(w, http.StatusOK, s.postJSON(r, p, true))
}

// maxAPIPosts is the most posts one call of the post list gives.
const maxAPIPosts = 200

// apiListPosts lists the host user's own posts, newest first, without their bodies. limit (default 50) and
// before (a date like 2026-10-27_14-35 in UTC, or a Unix time) page back through older ones; a Link header with
// rel="next" names the next page and is missing on the last.
func (s *Server) apiListPosts(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	limit := 50
	if v := r.FormValue("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxAPIPosts {
			http.Error(w, "limit must be a number from 1 to "+strconv.Itoa(maxAPIPosts), http.StatusBadRequest)
			return
		}
		limit = n
	}
	before := int64(0)
	if v := r.FormValue("before"); v != "" {
		if before = parseBefore(v); before == 0 {
			http.Error(w, "before must be a date like 2026-10-27_14-35 (UTC) or a Unix time", http.StatusBadRequest)
			return
		}
	}
	posts, err := q.sp.PostsBefore(q.host.ID, before, limit)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// A full read may have more. Posts are paged by the second they were made, so the oldest second, which
	// may be cut short, is left for the next page, unless the whole read is that one second.
	if len(posts) == limit {
		keep := len(posts)
		for keep > 0 && posts[keep-1].Created.Unix() == posts[len(posts)-1].Created.Unix() {
			keep--
		}
		if keep == 0 {
			keep = len(posts)
		}
		posts = posts[:keep]
		v := url.Values{"before": {itoa(posts[keep-1].Created.Unix())}}
		if r.FormValue("limit") != "" {
			v.Set("limit", strconv.Itoa(limit))
		}
		w.Header().Add("Link", "</api/posts?"+v.Encode()+`>; rel="next"`)
	}
	out := make([]postJSON, 0, len(posts))
	for _, p := range posts {
		out = append(out, s.postJSON(r, p, false))
	}
	writeJSON(w, http.StatusOK, out)
}
