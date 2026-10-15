// Package web serves the per-user HTML pages, RSS feeds and the REST API.
package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hbroek/text-thread-casting/internal/fetch"
	"github.com/hbroek/text-thread-casting/internal/store"
)

// Config controls how hosts map to users.
type Config struct {
	// BaseDomain is the hosted domain: name.<BaseDomain> serves user "name".
	BaseDomain string
	// Scheme is "http" or "https", used in absolute URLs.
	Scheme string
	// AllowPrivate lets the server fetch feeds from loopback and private addresses.
	// Needed for local development and tests; leave off when hosting.
	AllowPrivate bool
	// Verbose logs every API call (and inbound Webmention) with its status and timing.
	Verbose bool
}

type Server struct {
	cfg Config
	st  *store.Store
	mux *http.ServeMux
	web *fetch.Client
	// wake nudges the background worker after a Webmention job is queued.
	wake chan struct{}
}

func New(cfg Config, st *store.Store) *Server {
	if cfg.Scheme == "" {
		cfg.Scheme = "http"
	}
	s := &Server{cfg: cfg, st: st, mux: http.NewServeMux(), web: fetch.New(cfg.AllowPrivate), wake: make(chan struct{}, 1)}
	// public pages
	s.mux.HandleFunc("GET /{$}", s.user(s.home))
	s.mux.HandleFunc("GET /feed.xml", s.user(s.feed))
	s.mux.HandleFunc("GET /posts/{slug}", s.user(s.permalink))
	// human UI (session cookie)
	s.mux.HandleFunc("GET /login", s.user(s.loginForm))
	s.mux.HandleFunc("POST /login", s.user(s.login))
	s.mux.HandleFunc("POST /logout", s.user(s.logout))
	s.mux.HandleFunc("GET /compose", s.user(s.composeForm))
	s.mux.HandleFunc("POST /compose", s.user(s.compose))
	s.mux.HandleFunc("GET /posts/{id}/edit", s.user(s.editForm))
	s.mux.HandleFunc("POST /posts/{id}/edit", s.user(s.edit))
	s.mux.HandleFunc("POST /posts/{id}/delete", s.user(s.deleteForm))
	s.mux.HandleFunc("POST /webmention", s.user(s.webmention))
	s.mux.HandleFunc("GET /thread", s.user(s.thread))
	s.mux.HandleFunc("GET /reactions.xml", s.user(s.reactionsFeed))
	s.mux.HandleFunc("GET /reactions/{id}", s.user(s.reactionPage))
	s.mux.HandleFunc("POST /react", s.user(s.reactForm))
	s.mux.HandleFunc("POST /unreact", s.user(s.unreactForm))
	s.mux.HandleFunc("POST /api/reactions", s.user(s.apiReact))
	s.mux.HandleFunc("DELETE /api/reactions", s.user(s.apiUnreact))
	s.mux.HandleFunc("GET /api/reactions/received", s.user(s.apiReactionsReceived))
	s.mux.HandleFunc("GET /posts/{slug}/replies.xml", s.user(s.repliesFeed))
	s.mux.HandleFunc("GET /replies", s.user(s.moderation))
	s.mux.HandleFunc("POST /replies/{id}/status", s.user(s.moderateForm))
	s.mux.HandleFunc("GET /api/replies", s.user(s.apiReplies))
	s.mux.HandleFunc("POST /api/replies/{id}/status", s.user(s.apiModerate))
	s.mux.HandleFunc("GET /following.opml", s.user(s.opml))
	s.mux.HandleFunc("GET /timeline", s.user(s.timeline))
	s.mux.HandleFunc("GET /following", s.user(s.followingPage))
	s.mux.HandleFunc("POST /following", s.user(s.followForm))
	s.mux.HandleFunc("POST /following/remove", s.user(s.unfollowForm))
	s.mux.HandleFunc("POST /following/refresh", s.user(s.refreshForm))
	// REST API (bearer token)
	s.mux.HandleFunc("GET /api/thread", s.user(s.apiThread))
	s.mux.HandleFunc("GET /api/timeline", s.user(s.apiTimeline))
	s.mux.HandleFunc("GET /api/following", s.user(s.apiFollowing))
	s.mux.HandleFunc("POST /api/following", s.user(s.apiFollow))
	s.mux.HandleFunc("DELETE /api/following", s.user(s.apiUnfollow))
	s.mux.HandleFunc("POST /api/posts", s.user(s.apiCreate))
	s.mux.HandleFunc("PUT /api/posts/{id}", s.user(s.apiUpdate))
	s.mux.HandleFunc("DELETE /api/posts/{id}", s.user(s.apiDelete))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Verbose && (strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/webmention") {
		s.logged(w, r)
		return
	}
	s.mux.ServeHTTP(w, r)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) {
	w.status = c
	w.ResponseWriter.WriteHeader(c)
}

// logged serves r and logs one line: host, method, path, request body (cut short), status, time.
// Tokens travel in the Authorization header, which is never logged.
func (s *Server) logged(w http.ResponseWriter, r *http.Request) {
	var body string
	if r.Body != nil && r.Method != http.MethodGet {
		b, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		r.Body = io.NopCloser(bytes.NewReader(b))
		body = strings.Join(strings.Fields(string(b)), " ")
		if rs := []rune(body); len(rs) > 200 {
			body = string(rs[:200]) + "…"
		}
	}
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	start := time.Now()
	s.mux.ServeHTTP(sw, r)
	line := fmt.Sprintf("api %s %s %s -> %d (%s)", r.Host, r.Method, r.URL.RequestURI(), sw.status, time.Since(start).Round(time.Millisecond))
	if body != "" {
		line += " " + body
	}
	log.Print(line)
}

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

// postURL is a post's permanent address: the author's host plus the post's slug.
func (s *Server) postURL(r *http.Request, id int64) string {
	slug, err := s.st.SlugByID(id)
	if err != nil {
		return s.base(r) + "/posts/"
	}
	return s.base(r) + "/posts/" + slug
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
