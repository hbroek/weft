package web

import (
	"context"
	"html/template"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/md"
	"github.com/hbroek/weft/internal/store"
)

const cookieName = "weft_session"

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request, q *req) {
	s.show(w, "login", http.StatusOK, s.data(r, q, "Log in"))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, q *req) {
	u, err := s.st.CheckPassword(q.host.Name, r.FormValue("password"))
	if err != nil {
		d := s.data(r, q, "Log in")
		d.Error = "Wrong password."
		s.show(w, "login", http.StatusUnauthorized, d)
		return
	}
	tok, err := s.st.CreateSession(u.ID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.cfg.Scheme == "https", MaxAge: int(store.SessionTTL.Seconds())})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, q *req) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.st.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// requireOwner redirects visitors who are not logged in as the host user.
func (s *Server) requireOwner(w http.ResponseWriter, r *http.Request, q *req) bool {
	if !q.owner {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
	return q.owner
}

func (s *Server) editor(w http.ResponseWriter, r *http.Request, q *req, status int, heading, action, button, del, title, markdown string, keywords []string, errMsg string) {
	d := s.data(r, q, heading)
	d.ReplyTo, d.ReplyFeed = httpURL(r.FormValue("reply_to")), httpURL(r.FormValue("reply_feed"))
	d.Heading, d.Action, d.Button, d.Delete, d.Error = heading, action, button, del, errMsg
	d.Form.Title, d.Form.Markdown, d.Form.Keywords = title, markdown, feed.JoinKeywords(keywords)
	d.PreviewKeywords = keywords
	if strings.TrimSpace(markdown) != "" {
		if h, err := md.Render(markdown); err == nil {
			d.Preview = template.HTML(h) // goldmark safe mode: raw HTML dropped, dangerous URLs removed
		}
	}
	s.show(w, "editor", status, d)
}

// preview renders Markdown from the editor as the published post will look. It is read-only and
// uses the same renderer as publishing.
func (s *Server) preview(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	h, err := md.Render(r.FormValue("markdown"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(h))
}

func (s *Server) composeForm(w http.ResponseWriter, r *http.Request, q *req) {
	if s.requireOwner(w, r, q) {
		// title, body and keywords prefill the form (e.g. from a bookmarklet); nothing is published until the owner submits
		v := r.URL.Query()
		s.editor(w, r, q, http.StatusOK, "New post", "/compose", "Publish", "", strings.TrimSpace(v.Get("title")), v.Get("body"), feed.SplitKeywords(v.Get("keywords")), "")
	}
}

func (s *Server) compose(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	title, body, kws := strings.TrimSpace(r.FormValue("title")), r.FormValue("markdown"), feed.SplitKeywords(r.FormValue("keywords"))
	p, msg := s.create(r, q.host, title, body, kws, httpURL(r.FormValue("reply_to")), httpURL(r.FormValue("reply_feed")))
	if msg != "" {
		s.editor(w, r, q, http.StatusBadRequest, "New post", "/compose", "Publish", "", title, body, kws, msg)
		return
	}
	http.Redirect(w, r, "/posts/"+p.Slug, http.StatusSeeOther)
}

func (s *Server) ownPost(w http.ResponseWriter, r *http.Request, q *req) *store.Post {
	if !s.requireOwner(w, r, q) {
		return nil
	}
	p, err := s.st.PostBySlug(q.host.ID, r.PathValue("slug"))
	if err != nil || p.Deleted {
		http.NotFound(w, r)
		return nil
	}
	return p
}

func (s *Server) editForm(w http.ResponseWriter, r *http.Request, q *req) {
	if p := s.ownPost(w, r, q); p != nil {
		path := "/posts/" + p.Slug
		s.editor(w, r, q, http.StatusOK, "Edit post", path+"/edit", "Save", path+"/delete", p.Title, p.Markdown, p.Keywords, "")
	}
}

func (s *Server) edit(w http.ResponseWriter, r *http.Request, q *req) {
	p := s.ownPost(w, r, q)
	if p == nil {
		return
	}
	title, body, kws := strings.TrimSpace(r.FormValue("title")), r.FormValue("markdown"), feed.SplitKeywords(r.FormValue("keywords"))
	path := "/posts/" + p.Slug
	if _, msg := s.update(r, q.host, p.ID, title, body, kws); msg != "" {
		s.editor(w, r, q, http.StatusBadRequest, "Edit post", path+"/edit", "Save", path+"/delete", title, body, kws, msg)
		return
	}
	http.Redirect(w, r, "/posts/"+p.Slug, http.StatusSeeOther)
}

func (s *Server) deleteForm(w http.ResponseWriter, r *http.Request, q *req) {
	p := s.ownPost(w, r, q)
	if p == nil {
		return
	}
	if err := s.deletePost(r, q.host, p); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// create validates, renders and stores a new post, and for a reply, links it into its thread
// and queues a Webmention to the parent. msg is a user-facing validation error.
func (s *Server) create(r *http.Request, u *store.User, title, markdown string, keywords []string, replyTo, replyFeed string) (*store.Post, string) {
	if strings.TrimSpace(markdown) == "" {
		return nil, "Write something first."
	}
	html, err := md.Render(markdown)
	if err != nil {
		return nil, err.Error()
	}
	n := store.NewPost{UserID: u.ID, Title: title, Markdown: markdown, HTML: html, Keywords: keywords}
	if replyTo != "" {
		n.InReplyTo = replyTo
		n.InReplyToFeed, n.Root, n.RootFeed = s.resolveThread(r.Context(), s.base(r), u, replyTo, replyFeed)
	}
	p, err := s.st.AddPost(n)
	if err != nil {
		return nil, err.Error()
	}
	if p.InReplyTo != "" {
		s.notify(s.postURL(r, p.ID), p.InReplyTo)
	}
	s.refreshOwnFeed(r)
	return p, ""
}

// update replaces a post's content and, for a reply, tells the parent it changed.
func (s *Server) update(r *http.Request, u *store.User, id int64, title, markdown string, keywords []string) (*store.Post, string) {
	if strings.TrimSpace(markdown) == "" {
		return nil, "Write something first."
	}
	html, err := md.Render(markdown)
	if err != nil {
		return nil, err.Error()
	}
	p, err := s.st.UpdatePost(u.ID, id, title, markdown, html, keywords)
	if err != nil {
		return nil, err.Error()
	}
	if p.InReplyTo != "" {
		s.notify(s.postURL(r, p.ID), p.InReplyTo)
	}
	s.refreshOwnFeed(r)
	return p, ""
}

// deletePost tombstones a post and, for a reply, tells the parent it is gone.
func (s *Server) deletePost(r *http.Request, u *store.User, p *store.Post) error {
	if err := s.st.DeletePost(u.ID, p.ID); err != nil {
		return err
	}
	if p.InReplyTo != "" {
		s.notify(s.postURL(r, p.ID), p.InReplyTo)
	}
	s.refreshOwnFeed(r)
	return nil
}

// refreshOwnFeed updates this instance's cached copy of the host user's feed right after a change,
// when anyone on this instance follows it, so local followers see it at once instead of at the next
// poll. Followers on other instances still wait for their own poll: RSS is pull-based.
func (s *Server) refreshOwnFeed(r *http.Request) {
	own := s.base(r) + "/feed.xml"
	urls, err := s.st.FollowedFeedURLs()
	if err != nil || !slices.Contains(urls, own) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	s.Refresh(ctx, own)
}
