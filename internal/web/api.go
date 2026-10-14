package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

type postReq struct {
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
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
	p, msg := s.create(q.host, in.Title, in.Markdown)
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
	if _, err := s.st.Post(q.host.ID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	if _, msg := s.update(q.host, id, in.Title, in.Markdown); msg != "" {
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
	if err := s.st.DeletePost(q.host.ID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
