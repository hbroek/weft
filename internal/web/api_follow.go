package web

import (
	"encoding/json"
	"net/http"
	"time"
)

type timelineJSON struct {
	FeedURL   string     `json:"feed_url"`
	FeedTitle string     `json:"feed_title"`
	GUID      string     `json:"guid"`
	Link      string     `json:"link"`
	Title     string     `json:"title,omitempty"`
	Text      string     `json:"text"`
	Markdown  string     `json:"markdown,omitempty"`
	Published time.Time  `json:"published"`
	Updated   *time.Time `json:"updated,omitempty"`
	InReplyTo string     `json:"in_reply_to,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) apiAuth(w http.ResponseWriter, r *http.Request, q *req) bool {
	if err := s.bearer(r, q); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return false
	}
	return true
}

func (s *Server) apiTimeline(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	items, err := s.st.Timeline(q.host.ID, 200)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out := make([]timelineJSON, 0, len(items))
	for _, it := range items {
		out = append(out, timelineJSON{FeedURL: it.FeedURL, FeedTitle: it.FeedTitle, GUID: it.GUID, Link: it.Link,
			Title: it.Title, Text: it.Text, Markdown: it.Markdown, Published: it.Published, Updated: it.Updated, InReplyTo: it.InReplyTo})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiFollowing(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	fs, err := s.st.Follows(q.host.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	urls := make([]string, 0, len(fs))
	for _, f := range fs {
		urls = append(urls, f.URL)
	}
	writeJSON(w, http.StatusOK, urls)
}

type followReq struct {
	URL string `json:"url"`
}

func (s *Server) apiFollow(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	var in followReq
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	u, err := s.follow(r.Context(), q.host.ID, in.URL)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, followReq{URL: u})
}

func (s *Server) apiUnfollow(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	var in followReq
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := s.st.Unfollow(q.host.ID, in.URL); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
