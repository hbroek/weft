package web

import (
	"net/http"
	"strings"

	"github.com/hbroek/text-thread-casting/internal/md"
	"github.com/hbroek/text-thread-casting/internal/store"
)

const cookieName = "ttc_session"

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

func (s *Server) editor(w http.ResponseWriter, r *http.Request, q *req, status int, heading, action, button, del, title, markdown, errMsg string) {
	d := s.data(r, q, heading)
	d.Heading, d.Action, d.Button, d.Delete, d.Error = heading, action, button, del, errMsg
	d.Form.Title, d.Form.Markdown = title, markdown
	s.show(w, "editor", status, d)
}

func (s *Server) composeForm(w http.ResponseWriter, r *http.Request, q *req) {
	if s.requireOwner(w, r, q) {
		s.editor(w, r, q, http.StatusOK, "New post", "/compose", "Publish", "", "", "", "")
	}
}

func (s *Server) compose(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	title, body := strings.TrimSpace(r.FormValue("title")), r.FormValue("markdown")
	p, msg := s.create(q.host, title, body)
	if msg != "" {
		s.editor(w, r, q, http.StatusBadRequest, "New post", "/compose", "Publish", "", title, body, msg)
		return
	}
	http.Redirect(w, r, "/posts/"+itoa(p.ID), http.StatusSeeOther)
}

func (s *Server) ownPost(w http.ResponseWriter, r *http.Request, q *req) *store.Post {
	if !s.requireOwner(w, r, q) {
		return nil
	}
	id, ok := postID(r)
	if !ok {
		http.NotFound(w, r)
		return nil
	}
	p, err := s.st.Post(q.host.ID, id)
	if err != nil || p.Deleted {
		http.NotFound(w, r)
		return nil
	}
	return p
}

func (s *Server) editForm(w http.ResponseWriter, r *http.Request, q *req) {
	if p := s.ownPost(w, r, q); p != nil {
		path := "/posts/" + itoa(p.ID)
		s.editor(w, r, q, http.StatusOK, "Edit post", path+"/edit", "Save", path+"/delete", p.Title, p.Markdown, "")
	}
}

func (s *Server) edit(w http.ResponseWriter, r *http.Request, q *req) {
	p := s.ownPost(w, r, q)
	if p == nil {
		return
	}
	title, body := strings.TrimSpace(r.FormValue("title")), r.FormValue("markdown")
	path := "/posts/" + itoa(p.ID)
	if _, msg := s.update(q.host, p.ID, title, body); msg != "" {
		s.editor(w, r, q, http.StatusBadRequest, "Edit post", path+"/edit", "Save", path+"/delete", title, body, msg)
		return
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

func (s *Server) deleteForm(w http.ResponseWriter, r *http.Request, q *req) {
	p := s.ownPost(w, r, q)
	if p == nil {
		return
	}
	if err := s.st.DeletePost(q.host.ID, p.ID); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// create validates, renders and stores a new post. msg is a user-facing validation error.
func (s *Server) create(u *store.User, title, markdown string) (*store.Post, string) {
	if strings.TrimSpace(markdown) == "" {
		return nil, "Write something first."
	}
	html, err := md.Render(markdown)
	if err != nil {
		return nil, err.Error()
	}
	p, err := s.st.AddPost(u.ID, title, markdown, html)
	if err != nil {
		return nil, err.Error()
	}
	return p, ""
}

func (s *Server) update(u *store.User, id int64, title, markdown string) (*store.Post, string) {
	if strings.TrimSpace(markdown) == "" {
		return nil, "Write something first."
	}
	html, err := md.Render(markdown)
	if err != nil {
		return nil, err.Error()
	}
	p, err := s.st.UpdatePost(u.ID, id, title, markdown, html)
	if err != nil {
		return nil, err.Error()
	}
	return p, ""
}
