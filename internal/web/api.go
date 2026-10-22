package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/hbroek/weft/internal/feed"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

type postReq struct {
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
	// Keywords are optional words or phrases, published as the item's <category> elements.
	Keywords []string `json:"keywords"`
	// InReplyTo makes the post a reply to the post at this permalink. InReplyToFeed is an
	// optional hint: the parent's feed URL.
	InReplyTo     string `json:"in_reply_to"`
	InReplyToFeed string `json:"in_reply_to_feed"`
	// Context is optional: what the parent page looked like (title, description, image), for a reply
	// to a page that publishes no feed. It is published as attributes of weft:context.
	Context *struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Image       string `json:"image"`
	} `json:"context"`
}

type postResp struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, q *req) (postReq, bool) {
	var in postReq
	if err := s.bearer(r, q); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return in, false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return in, false
	}
	in.Title = strings.TrimSpace(in.Title)
	return in, true
}

func (s *Server) reply(w http.ResponseWriter, r *http.Request, status int, id int64) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(postResp{ID: id, URL: s.postURL(r, id)})
}

func (s *Server) apiCreate(w http.ResponseWriter, r *http.Request, q *req) {
	in, ok := s.decode(w, r, q)
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
	p, msg := s.create(r, q.host, in.Title, in.Markdown, feed.Keywords(in.Keywords), httpURL(in.InReplyTo), httpURL(in.InReplyToFeed), parent)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	s.reply(w, r, http.StatusCreated, p.ID)
}

func (s *Server) apiUpdate(w http.ResponseWriter, r *http.Request, q *req) {
	id, ok := postID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	in, ok := s.decode(w, r, q)
	if !ok {
		return
	}
	if _, err := q.sp.Post(q.host.ID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	if _, msg := s.update(r, q.host, id, in.Title, in.Markdown, feed.Keywords(in.Keywords)); msg != "" {
		code := http.StatusBadRequest
		if msg == "not found" {
			code = http.StatusNotFound
		}
		http.Error(w, msg, code)
		return
	}
	s.reply(w, r, http.StatusOK, id)
}

func (s *Server) apiDelete(w http.ResponseWriter, r *http.Request, q *req) {
	id, ok := postID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.bearer(r, q); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	p, err := q.sp.Post(q.host.ID, id)
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
