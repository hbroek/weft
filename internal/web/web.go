// Package web serves the per-user HTML pages, RSS feeds and the REST API.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/hbroek/text-thread-casting/internal/feed"
	"github.com/hbroek/text-thread-casting/internal/md"
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

type ctxKey struct{}

func New(cfg Config, st *store.Store) *Server {
	if cfg.Scheme == "" {
		cfg.Scheme = "http"
	}
	s := &Server{cfg: cfg, st: st, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /{$}", s.user(s.home))
	s.mux.HandleFunc("GET /feed.xml", s.user(s.feed))
	s.mux.HandleFunc("GET /posts/{id}", s.user(s.permalink))
	s.mux.HandleFunc("POST /api/posts", s.user(s.apiPost))
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

func (s *Server) user(h func(http.ResponseWriter, *http.Request, *store.User)) http.HandlerFunc {
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
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)), u)
	}
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
<link rel="alternate" type="application/rss+xml" href="{{.FeedURL}}">
<link rel="webmention" href="{{.WebmentionURL}}">
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:40rem;margin:2rem auto;padding:0 1rem}
article{margin:2rem 0;border-bottom:1px solid #8884;padding-bottom:1rem}time{color:#888;font-size:.85em}</style>
</head><body>
<header><h1><a href="/">{{.Author}}</a></h1><p>{{.Bio}}</p></header>
<main>
{{range .Posts}}<article class="h-entry">
{{if .Title}}<h2 class="p-name"><a class="u-url" href="{{.URL}}">{{.Title}}</a></h2>{{end}}
<div class="e-content">{{.HTML}}</div>
<a class="u-url" href="{{.URL}}"><time class="dt-published" datetime="{{.ISO}}">{{.Date}}</time></a>
</article>{{end}}
</main></body></html>`))

type pagePost struct {
	Title, URL, ISO, Date string
	HTML                  template.HTML
}

type pageData struct {
	Title, FeedURL, WebmentionURL, Author, Bio string
	Posts                                      []pagePost
}

func (s *Server) postURL(r *http.Request, id int64) string {
	return fmt.Sprintf("%s/posts/%d", s.base(r), id)
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, u *store.User, title string, posts []*store.Post) {
	d := pageData{Title: title, FeedURL: s.base(r) + "/feed.xml", WebmentionURL: s.base(r) + "/webmention", Author: u.Display, Bio: u.Bio}
	for _, p := range posts {
		d.Posts = append(d.Posts, pagePost{
			Title: p.Title, URL: s.postURL(r, p.ID), HTML: template.HTML(p.HTML), // HTML was rendered by goldmark in safe mode
			ISO: p.Created.UTC().Format("2006-01-02T15:04:05Z"), Date: p.Created.Format("2 Jan 2006 15:04"),
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTmpl.Execute(w, d); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (s *Server) home(w http.ResponseWriter, r *http.Request, u *store.User) {
	posts, err := s.st.RecentPosts(u.ID, 50)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, r, u, u.Display, posts)
}

func (s *Server) permalink(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.st.Post(u.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if p.Deleted {
		http.Error(w, "This post was removed.", http.StatusGone)
		return
	}
	title := p.Title
	if title == "" {
		title = u.Display
	}
	s.render(w, r, u, title, []*store.Post{p})
}

func (s *Server) feed(w http.ResponseWriter, r *http.Request, u *store.User) {
	posts, err := s.st.RecentPosts(u.ID, 100)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	ch := feed.Channel{
		Title: u.Display, Link: s.base(r) + "/", Description: u.Bio,
		Self:       feed.AtomLink{Href: s.base(r) + "/feed.xml", Rel: "self", Type: "application/rss+xml"},
		Webmention: s.base(r) + "/webmention",
	}
	for i, p := range posts {
		if i == 0 {
			ch.LastBuildDate = feed.RFC822(p.Created)
		}
		url := s.postURL(r, p.ID)
		it := feed.Item{Title: p.Title, GUID: feed.GUID{IsPermaLink: true, Value: url}, Link: url,
			Description: p.HTML, Markdown: p.Markdown, PubDate: feed.RFC822(p.Created)}
		if p.Updated != nil {
			it.Updated = feed.RFC822(*p.Updated)
		}
		ch.Items = append(ch.Items, it)
	}
	if ch.LastBuildDate == "" {
		ch.LastBuildDate = feed.RFC822(timeNow())
	}
	b, err := feed.New(ch).Marshal()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write(b)
}

// authed returns the user identified by the bearer token, which must match the host's user.
func (s *Server) authed(r *http.Request, host *store.User) error {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return errors.New("missing bearer token")
	}
	u, err := s.st.UserByToken(tok)
	if err != nil || u.ID != host.ID {
		return errors.New("invalid token for this host")
	}
	return nil
}

type postReq struct {
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
}

type postResp struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}

func (s *Server) apiPost(w http.ResponseWriter, r *http.Request, u *store.User) {
	if err := s.authed(r, u); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req postReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if strings.TrimSpace(req.Markdown) == "" {
		http.Error(w, "markdown is required", http.StatusBadRequest)
		return
	}
	html, err := md.Render(req.Markdown)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	p, err := s.st.AddPost(u.ID, req.Title, req.Markdown, html)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(postResp{ID: p.ID, URL: s.postURL(r, p.ID)})
}
