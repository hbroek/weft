package web

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
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
	u, err := q.sp.CheckPassword(r.FormValue("password"))
	if err != nil {
		d := s.data(r, q, "Log in")
		d.Error = "Wrong password."
		s.show(w, "login", http.StatusUnauthorized, d)
		return
	}
	tok, err := q.sp.CreateSession(u.ID)
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
		q.sp.DeleteSession(c.Value)
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

func (s *Server) editor(w http.ResponseWriter, r *http.Request, q *req, status int, heading, action, button, del, title, markdown string, keywords []string, errMsg string, tweak ...func(*pageData)) {
	d := s.data(r, q, heading)
	d.ReplyTo, d.ReplyFeed = httpURL(r.FormValue("reply_to")), httpURL(r.FormValue("reply_feed"))
	d.Back = timelineBack(r.FormValue("back"))
	d.PInfo = parentFromForm(r)
	if d.ReplyTo != "" {
		d.Parent = newParentCard(d.ReplyTo, d.PInfo)
	}
	d.Heading, d.Action, d.Button, d.Delete, d.Error = heading, action, button, del, errMsg
	d.Form.Title, d.Form.Markdown, d.Form.Keywords = title, markdown, feed.JoinKeywords(keywords)
	d.PreviewKeywords = keywords
	if strings.TrimSpace(markdown) != "" {
		if h, err := md.Render(markdown); err == nil {
			d.Preview = template.HTML(h) // goldmark safe mode: raw HTML dropped, dangerous URLs removed
		}
	}
	for _, f := range tweak {
		if f != nil {
			f(&d)
		}
	}
	if del == "" && d.Form.Slug == "" { // a new post: suggest the words of its address, which the author may reword
		d.Form.Slug, d.SlugAuto = store.SlugBase(title, markdown), true
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
	w.Header().Set("X-Slug-Base", store.SlugBase(strings.TrimSpace(r.FormValue("title")), r.FormValue("markdown"))) // the address the form suggests
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(h))
}

func (s *Server) composeForm(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	// title, body and keywords prefill the form (e.g. from a bookmarklet); nothing is published until the owner submits
	v := r.URL.Query()
	title, body, kws := strings.TrimSpace(v.Get("title")), v.Get("body"), feed.SplitKeywords(v.Get("keywords"))
	var d func(*pageData)
	if page := httpURL(v.Get("page")); page != "" {
		// the bookmarklet's page: a bookmark card, a reply to it (the default when the page can take a
		// Webmention), or a like of it
		info := feed.ParentInfo{Title: v.Get("ptitle"), Description: v.Get("pdesc"), Image: v.Get("pimg")}.Clean()
		mode := v.Get("mode")
		if mode != "reply" && mode != "bookmark" && mode != "like" {
			mode = "bookmark"
			if v.Get("wm") == "1" {
				mode = "reply"
			}
		}
		var modes []modeLink
		for _, m := range []struct{ key, label string }{{"bookmark", "Bookmark"}, {"reply", "Reply"}, {"like", "Like"}} {
			q2 := url.Values{}
			for k, vs := range v {
				q2[k] = vs
			}
			q2.Set("mode", m.key)
			modes = append(modes, modeLink{Label: m.label, Href: "/compose?" + q2.Encode(), Current: m.key == mode})
		}
		feeds := pageFeeds(q, page, v["feed"], v["ftitle"])
		d = func(d *pageData) { d.Modes, d.PageFeeds = modes, feeds }
		switch mode {
		case "like":
			dd := s.data(r, q, "Like this page")
			dd.Modes, dd.ReplyTo, dd.PInfo, dd.Parent, dd.PageFeeds = modes, page, info, newParentCard(page, info), feeds
			s.show(w, "likepage", http.StatusOK, dd)
			return
		case "reply":
			r.ParseForm()
			r.Form.Set("reply_to", page) // the editor reads these from the form
			r.Form.Set("ptitle", info.Title)
			r.Form.Set("pdesc", info.Description)
			r.Form.Set("pimg", info.Image)
		default:
			title, body = info.Title, bookmarkBody(page, info)
		}
	}
	s.editor(w, r, q, http.StatusOK, "New post", "/compose", "Publish", "", title, body, kws, "", d)
}

// bookmarkBody is the Markdown of a bookmark card: the page's image, a quoted description and a link.
func bookmarkBody(page string, p feed.ParentInfo) string {
	var b strings.Builder
	if p.Image != "" {
		b.WriteString("![" + strings.NewReplacer("[", "", "]", "").Replace(p.Title) + "](" + p.Image + ")\n\n")
	}
	if p.Description != "" {
		b.WriteString("> " + p.Description + "\n\n")
	}
	b.WriteString("[" + siteHost(page) + "](" + strings.ReplaceAll(page, ")", "%29") + ")\n")
	return b.String()
}

func (s *Server) compose(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	title, body, kws := strings.TrimSpace(r.FormValue("title")), r.FormValue("markdown"), feed.SplitKeywords(r.FormValue("keywords"))
	slug := strings.TrimSpace(r.FormValue("slug"))
	p, msg := s.create(r, q.host, title, body, slug, kws, httpURL(r.FormValue("reply_to")), httpURL(r.FormValue("reply_feed")), parentFromForm(r))
	if msg != "" {
		s.editor(w, r, q, http.StatusBadRequest, "New post", "/compose", "Publish", "", title, body, kws, msg, func(d *pageData) { d.Form.Slug = slug })
		return
	}
	if p.InReplyTo != "" { // a reply belongs to a conversation: back to the timeline, at the post it answers
		dest := timelineBack(r.FormValue("back"))
		if dest == "" {
			dest = "/timeline"
		}
		http.Redirect(w, r, withAnchor(dest, p.InReplyTo), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/posts/"+p.Slug, http.StatusSeeOther)
}

func (s *Server) ownPost(w http.ResponseWriter, r *http.Request, q *req) *store.Post {
	if !s.requireOwner(w, r, q) {
		return nil
	}
	p, err := q.sp.PostBySlug(q.host.ID, r.PathValue("slug"))
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
func (s *Server) create(r *http.Request, u *store.User, title, markdown, slug string, keywords []string, replyTo, replyFeed string, parent feed.ParentInfo) (*store.Post, string) {
	if strings.TrimSpace(markdown) == "" {
		return nil, "Write something first."
	}
	html, err := md.Render(markdown)
	if err != nil {
		return nil, err.Error()
	}
	n := store.NewPost{UserID: u.ID, Title: title, Markdown: markdown, HTML: html, Keywords: keywords, Slug: slug}
	if replyTo != "" {
		n.Parent = parent
		n.InReplyTo = replyTo
		n.InReplyToFeed, n.Root, n.RootFeed = s.resolveThread(r.Context(), s.base(r), reqOf(r).sp, replyTo, replyFeed)
	}
	p, err := reqOf(r).sp.AddPost(n)
	if err != nil {
		if msg, ok := slugMessage(err); ok {
			return nil, msg
		}
		return nil, err.Error()
	}
	if p.InReplyTo != "" {
		s.notify(reqOf(r).sp, s.postURL(r, p.ID), p.InReplyTo)
	}
	s.refreshOwnFeed(r)
	return p, ""
}

// slugMessage is the sentence for an address that cannot be used.
func slugMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, store.ErrBadSlug):
		return "The address needs letters or digits, and at most 80 characters.", true
	}
	return "", false
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
	p, err := reqOf(r).sp.UpdatePost(u.ID, id, title, markdown, html, keywords)
	if err != nil {
		return nil, err.Error()
	}
	if p.InReplyTo != "" {
		s.notify(reqOf(r).sp, s.postURL(r, p.ID), p.InReplyTo)
	}
	s.refreshOwnFeed(r)
	return p, ""
}

// deletePost tombstones a post and, for a reply, tells the parent it is gone.
func (s *Server) deletePost(r *http.Request, u *store.User, p *store.Post) error {
	if err := reqOf(r).sp.DeletePost(u.ID, p.ID); err != nil {
		return err
	}
	if p.InReplyTo != "" {
		s.notify(reqOf(r).sp, s.postURL(r, p.ID), p.InReplyTo)
	}
	s.refreshOwnFeed(r)
	return nil
}

// refreshOwnFeed updates this instance's cached copy of the host user's feed right after a change,
// when anyone on this instance follows it, so local followers see it at once instead of at the next
// poll. Followers on other instances still wait for their own poll: RSS is pull-based.
func (s *Server) refreshOwnFeed(r *http.Request) {
	own := s.base(r) + "/feed.xml"
	if !s.st.Followed(own) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	s.Refresh(ctx, own)
}
