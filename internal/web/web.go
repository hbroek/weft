// Package web serves the per-user HTML pages, RSS feeds and the REST API.
package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/hbroek/text-thread-casting/internal/store"
)

// Config controls how hosts map to users.
type Config struct {
	// BaseDomain is the hosted domain: name.<BaseDomain> serves user "name".
	BaseDomain string
	// Scheme is "http" or "https", used in absolute URLs.
	Scheme string
}

type Server struct {
	cfg Config
	st  *store.Store
	mux *http.ServeMux
}

func New(cfg Config, st *store.Store) *Server {
	if cfg.Scheme == "" {
		cfg.Scheme = "http"
	}
	s := &Server{cfg: cfg, st: st, mux: http.NewServeMux()}
	// public pages
	s.mux.HandleFunc("GET /{$}", s.user(s.home))
	s.mux.HandleFunc("GET /feed.xml", s.user(s.feed))
	s.mux.HandleFunc("GET /posts/{id}", s.user(s.permalink))
	// human UI (session cookie)
	s.mux.HandleFunc("GET /login", s.user(s.loginForm))
	s.mux.HandleFunc("POST /login", s.user(s.login))
	s.mux.HandleFunc("POST /logout", s.user(s.logout))
	s.mux.HandleFunc("GET /compose", s.user(s.composeForm))
	s.mux.HandleFunc("POST /compose", s.user(s.compose))
	s.mux.HandleFunc("GET /posts/{id}/edit", s.user(s.editForm))
	s.mux.HandleFunc("POST /posts/{id}/edit", s.user(s.edit))
	s.mux.HandleFunc("POST /posts/{id}/delete", s.user(s.deleteForm))
	// REST API (bearer token)
	s.mux.HandleFunc("POST /api/posts", s.user(s.apiCreate))
	s.mux.HandleFunc("PUT /api/posts/{id}", s.user(s.apiUpdate))
	s.mux.HandleFunc("DELETE /api/posts/{id}", s.user(s.apiDelete))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// hostUser extracts the user name from the Host header, or "" if it is not a user host.
func (s *Server) hostUser(host string) string {
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	host = strings.ToLower(host)
	suffix := "." + s.cfg.BaseDomain
	if strings.HasSuffix(host, suffix) {
		return strings.TrimSuffix(host, suffix)
	}
	return ""
}

func (s *Server) base(r *http.Request) string { return s.cfg.Scheme + "://" + r.Host }

func (s *Server) postURL(r *http.Request, id int64) string {
	return fmt.Sprintf("%s/posts/%d", s.base(r), id)
}

type ctxKey struct{}

// req carries what handlers need about the request's host user and visitor.
type req struct {
	host  *store.User
	owner bool // visitor is logged in as the host user
}

func (s *Server) user(h func(http.ResponseWriter, *http.Request, *req)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := s.hostUser(r.Host)
		if name == "" {
			http.NotFound(w, r)
			return
		}
		u, err := s.st.UserByName(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		q := &req{host: u}
		if c, err := r.Cookie(cookieName); err == nil {
			if su, err := s.st.UserBySession(c.Value); err == nil && su.ID == u.ID {
				q.owner = true
			}
		}
		if r.Method == http.MethodPost && !strings.HasPrefix(r.URL.Path, "/api/") && !sameOrigin(r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, q)), q)
	}
}

// sameOrigin refuses form posts that a browser reports as coming from another origin.
func sameOrigin(r *http.Request) bool {
	for _, h := range []string{"Origin", "Referer"} {
		if v := r.Header.Get(h); v != "" {
			u, err := url.Parse(v)
			return err == nil && u.Host == r.Host
		}
	}
	return true
}

// bearer checks the API token against the host's user.
func (s *Server) bearer(r *http.Request, q *req) error {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return errors.New("missing bearer token")
	}
	u, err := s.st.UserByToken(tok)
	if err != nil || u.ID != q.host.ID {
		return errors.New("invalid token for this host")
	}
	return nil
}
