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
	"sync"
	"time"

	"github.com/hbroek/weft/internal/fetch"
	"github.com/hbroek/weft/internal/store"
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
	// CNAMETarget is the host name users point a custom domain's CNAME record at; shown on the profile
	// page. Empty when the instance has no single name for it.
	CNAMETarget string
	// LookupTXT and LookupCNAME read DNS when a custom domain is verified. Nil means the system resolver;
	// tests replace them.
	LookupTXT   func(ctx context.Context, name string) ([]string, error)
	LookupCNAME func(ctx context.Context, name string) (string, error)
	// ReachDomain fetches /.well-known/weft-domain from a custom domain, to see that requests to it arrive
	// here. Nil means a real request; tests replace it.
	ReachDomain func(ctx context.Context, domain string) (string, error)
}

type Server struct {
	cfg Config
	st  *store.Instance
	mux *http.ServeMux
	web *fetch.Client
	// wake nudges the background worker after a Webmention job is queued.
	wake chan struct{}
	// adj holds the user's own likes and unlikes not yet in the authors' published counts.
	adjMu sync.Mutex
	adj   map[adjKey]likeAdj
}

func New(cfg Config, st *store.Instance) *Server {
	if cfg.Scheme == "" {
		cfg.Scheme = "http"
	}
	s := &Server{cfg: cfg, st: st, mux: http.NewServeMux(), web: fetch.New(cfg.AllowPrivate), wake: make(chan struct{}, 1)}
	// public pages
	s.mux.Handle("GET /static/", s.staticHandler())
	s.mux.Handle("GET /favicon.ico", s.staticHandler())
	s.mux.HandleFunc("GET /{$}", s.user(s.home))
	s.mux.HandleFunc("GET /feed.xml", s.user(s.feed))
	s.mux.HandleFunc("GET /posts/{slug}", s.user(s.permalink))
	// human UI (session cookie)
	s.mux.HandleFunc("GET /login", s.user(s.loginForm))
	s.mux.HandleFunc("POST /login", s.user(s.login))
	s.mux.HandleFunc("POST /logout", s.user(s.logout))
	s.mux.HandleFunc("GET /compose", s.user(s.composeForm))
	s.mux.HandleFunc("POST /compose", s.user(s.compose))
	s.mux.HandleFunc("POST /preview", s.user(s.preview))
	s.mux.HandleFunc("GET /posts/{slug}/edit", s.user(s.editForm))
	s.mux.HandleFunc("POST /posts/{slug}/edit", s.user(s.edit))
	s.mux.HandleFunc("POST /posts/{slug}/delete", s.user(s.deleteForm))
	s.mux.HandleFunc("POST /webmention", s.user(s.webmention))
	s.mux.HandleFunc("GET /thread", s.user(s.thread))
	s.mux.HandleFunc("GET /likes.xml", s.user(s.likesFeed))
	s.mux.HandleFunc("GET /likes/{id}", s.user(s.likePage))
	s.mux.HandleFunc("POST /like", s.user(s.likeForm))
	s.mux.HandleFunc("POST /unlike", s.user(s.unlikeForm))
	s.mux.HandleFunc("POST /api/likes", s.user(s.apiLike))
	s.mux.HandleFunc("DELETE /api/likes", s.user(s.apiUnlike))
	s.mux.HandleFunc("GET /api/likes/received", s.user(s.apiLikesReceived))
	// the names from before there were only likes, kept for one release
	s.mux.HandleFunc("GET /reactions.xml", s.user(s.likesFeed))
	s.mux.HandleFunc("GET /reactions/{id}", s.user(s.likePage))
	s.mux.HandleFunc("POST /api/reactions", s.user(s.apiReact))
	s.mux.HandleFunc("DELETE /api/reactions", s.user(s.apiUnlike))
	s.mux.HandleFunc("GET /posts/{slug}/replies.xml", s.user(s.repliesFeed))
	s.mux.HandleFunc("GET /replies", s.user(s.moderation))
	s.mux.HandleFunc("POST /replies/{id}/status", s.user(s.moderateForm))
	s.mux.HandleFunc("GET /api/replies", s.user(s.apiReplies))
	s.mux.HandleFunc("POST /api/replies/{id}/status", s.user(s.apiModerate))
	s.mux.HandleFunc("GET /following.opml", s.user(s.opml))
	s.mux.HandleFunc("GET /timeline", s.user(s.timeline))
	s.mux.HandleFunc("GET /liked-by", s.user(s.likedByPage))
	s.mux.HandleFunc("GET /profile", s.user(s.profileForm))
	s.mux.HandleFunc("POST /profile", s.user(s.profile))
	s.mux.HandleFunc("POST /export", s.user(s.export))
	s.mux.HandleFunc("GET /.well-known/weft-domain", s.user(s.domainProof))
	s.mux.HandleFunc("POST /domain", s.user(s.domainSet))
	s.mux.HandleFunc("POST /domain/check", s.user(s.domainCheck))
	s.mux.HandleFunc("POST /domain/remove", s.user(s.domainRemove))
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
	if s.cfg.Verbose && (r.Method == http.MethodPost || !strings.HasPrefix(r.URL.Path, "/static/")) {
		s.loggedForm(w, r) // every page request but the static files: it is how a redirect is traced
		return
	}
	s.mux.ServeHTTP(w, r)
}

type statusWriter struct {
	http.ResponseWriter
	status   int
	location string
}

func (w *statusWriter) WriteHeader(c int) {
	w.status = c
	w.location = w.Header().Get("Location")
	w.ResponseWriter.WriteHeader(c)
}

// loggedForm serves a page request or form post and logs one line: host, method, path, status, where it
// redirected to and the page it came from. The query and the body are not logged: they can hold a password.
func (s *Server) loggedForm(w http.ResponseWriter, r *http.Request) {
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	s.mux.ServeHTTP(sw, r)
	line := fmt.Sprintf("form %s %s %s -> %d", r.Host, r.Method, r.URL.Path, sw.status)
	if sw.location != "" {
		line += " " + sw.location
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		line += " (from " + ref + ")"
	} else {
		line += " (no Referer)"
	}
	log.Print(line)
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

// splitHost separates a Host header into the lowercase host name and the ":port" suffix ("" if none).
func splitHost(host string) (name, port string) {
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
		return strings.ToLower(host[:i]), host[i:]
	}
	return strings.ToLower(host), ""
}

// hostUser extracts the user name from the Host header, or "" if it is not a user host: either
// name.<BaseDomain>, or the verified custom domain of a user.
func (s *Server) hostUser(host string) string {
	h, _ := splitHost(host)
	if suffix := "." + s.cfg.BaseDomain; strings.HasSuffix(h, suffix) {
		return strings.TrimSuffix(h, suffix)
	}
	if name, ok := s.st.HostUser(h); ok {
		return name
	}
	return ""
}

// originOf is the origin a user is served at: their verified custom domain, or name.<BaseDomain>. The port
// of the request is kept, which only matters where the server is reached on one (development).
func (s *Server) originOf(name string, r *http.Request) string {
	_, port := splitHost(r.Host)
	if d := s.st.Dir(name).Domain; d != "" {
		return s.cfg.Scheme + "://" + d + port
	}
	return s.cfg.Scheme + "://" + name + "." + s.cfg.BaseDomain + port
}

// redirectCache is how long a browser may keep a redirect to a user's current address before it asks
// again. The status says "permanent", which browsers otherwise keep for ever; an hour lets a mistake,
// or a removed custom domain, be undone.
const redirectCache = "max-age=3600"

// domainRedirectCache is the same for the redirect from the instance's address to a custom domain, kept short
// because that is the redirect that goes wrong: a domain whose DNS or certificate stops working strands anyone
// whose browser remembers it, and five minutes is how long that lasts.
const domainRedirectCache = "max-age=300"

// moveRedirect sends a request to the same place at the origin to, with a permanent redirect: 301 for
// GET and HEAD, which is the one every feed reader and old browser understands as "update your
// address", and 308 for other methods, the only permanent redirect that keeps the method and the body.
func moveRedirect(w http.ResponseWriter, r *http.Request, to, cache string) {
	code := http.StatusMovedPermanently
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		code = http.StatusPermanentRedirect
	}
	w.Header().Set("Cache-Control", cache)
	http.Redirect(w, r, to+r.URL.RequestURI(), code)
}

// route sends a request that is not for the user's current address — the old address of a user who
// moved to another server, or the instance's address of one who has a verified custom domain — to
// the current one, and reports false. Everything is redirected, the feed too: a feed reader that
// sees a permanent redirect knows the feed moved.
func (s *Server) route(w http.ResponseWriter, r *http.Request, name string) bool {
	e := s.st.Dir(name)
	if e.Status == store.StatusMoved {
		moveRedirect(w, r, e.MovedTo, redirectCache)
		return false
	}
	if h, port := splitHost(r.Host); e.Domain != "" && h != e.Domain && !keepsOldAddress(r.URL.Path) {
		moveRedirect(w, r, s.cfg.Scheme+"://"+e.Domain+port, domainRedirectCache)
		return false
	}
	return true
}

// keepsOldAddress is true for the owner's own pages for signing in and managing the address. They are never
// redirected to a custom domain: if the domain stops reaching this server (its DNS changed, a certificate
// failed), the owner can still sign in at the instance's address and remove it, instead of being sent after it.
func keepsOldAddress(path string) bool {
	switch path {
	case "/login", "/logout", "/profile", "/domain", "/domain/check", "/domain/remove":
		return true
	}
	return false
}

func (s *Server) base(r *http.Request) string { return s.cfg.Scheme + "://" + r.Host }

// postURL is a post's permanent address: the author's host plus the post's slug.
func (s *Server) postURL(r *http.Request, id int64) string {
	slug, err := reqOf(r).sp.SlugByID(id)
	if err != nil {
		return s.base(r) + "/posts/"
	}
	return s.base(r) + "/posts/" + slug
}

type ctxKey struct{}

// reqOf returns the request context that user() attached: the host user and visitor.
func reqOf(r *http.Request) *req { return r.Context().Value(ctxKey{}).(*req) }

// req carries what handlers need about the request's host user and visitor.
type req struct {
	host  *store.User
	sp    *store.Space // the host user's data
	owner bool         // visitor is logged in as the host user
}

func (s *Server) user(h func(http.ResponseWriter, *http.Request, *req)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := s.hostUser(r.Host)
		if name == "" {
			http.NotFound(w, r)
			return
		}
		if !s.route(w, r, name) {
			return
		}
		sp, release, err := s.st.Space(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer release()
		u, err := sp.User()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		q := &req{host: u, sp: sp}
		if c, err := r.Cookie(cookieName); err == nil {
			if su, err := sp.UserBySession(c.Value); err == nil && su.ID == u.ID {
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
	u, err := q.sp.UserByToken(tok)
	if err != nil || u.ID != q.host.ID {
		return errors.New("invalid token for this host")
	}
	return nil
}
