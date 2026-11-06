package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/hbroek/weft/internal/store"
)

// The landing page (served at www, see Config.SiteDir) has two small dynamic parts: a count of places and
// people waiting, and the form of the waiting list. Everything else on it is a static file.

const (
	maxContact = 254
	maxLinks   = 2000
	maxNote    = 1000
)

// landing serves the landing page's files, the counts at /status.json and the waiting list's form at /waitlist.
func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/status.json":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.status(w)
	case "/waitlist":
		if r.Method != http.MethodPost {
			http.Redirect(w, r, "/#signup", http.StatusSeeOther)
			return
		}
		s.joinWaitlist(w, r)
	default:
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.FileServer(http.Dir(s.cfg.SiteDir)).ServeHTTP(w, r)
	}
}

// status answers with the numbers the landing page shows.
func (s *Server) status(w http.ResponseWriter) {
	accounts, waiting, err := s.st.Counts()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	out := map[string]int{"accounts": accounts, "waiting": waiting}
	if s.cfg.MaxUsers > 0 {
		out["cap"] = s.cfg.MaxUsers
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	json.NewEncoder(w).Encode(out)
}

// reply writes a small page, in the colours of the landing page, with a way back.
func reply(w http.ResponseWriter, code int, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex"><title>%s</title>
<style>body{margin:0;font:17px/1.6 system-ui,sans-serif;background:#eef0fa;color:#1a1f3d}main{max-width:36rem;margin:0 auto;padding:3rem 16px}a{color:#26337a}
@media(prefers-color-scheme:dark){body{background:#11142b;color:#eef0fa}a{color:#f08a4b}}</style></head><body><main><h1>%s</h1><p>%s</p><p><a href="/#signup">Back to the sign-up page</a></p></main></body></html>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(msg))
}

// joinWaitlist takes the form of the waiting list.
func (s *Server) joinWaitlist(w http.ResponseWriter, r *http.Request) {
	if s.lim != nil {
		if ok, wait := s.lim.waitlist.take(s.clientIP(r)); !ok {
			tooMany(w, wait)
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		reply(w, http.StatusBadRequest, "That did not work", "The form could not be read.")
		return
	}
	if r.PostFormValue("website") != "" { // a field people cannot see: only a program fills it in
		reply(w, http.StatusOK, "You are on the list", "Thank you.")
		return
	}
	name := strings.ToLower(strings.TrimSpace(r.PostFormValue("name")))
	contact := strings.TrimSpace(r.PostFormValue("contact"))
	links := strings.TrimSpace(r.PostFormValue("links"))
	note := strings.TrimSpace(r.PostFormValue("note"))
	bad := func(msg string) { reply(w, http.StatusBadRequest, "Please check the form", msg) }
	switch a, err := mail.ParseAddress(contact); {
	case err != nil || a.Address != contact || len(contact) > maxContact:
		bad("The email address does not look right. It is only used to answer you.")
		return
	case links == "":
		bad("Please say where you already write: a blog, your posts, your profiles.")
		return
	case utf8.RuneCountInString(links) > maxLinks || utf8.RuneCountInString(note) > maxNote:
		bad("That is too long. Please shorten it.")
		return
	}
	switch err := s.st.JoinWaitlist(name, contact, links, note); {
	case err == nil:
		reply(w, http.StatusOK, "You are on the list", "Thank you. We read each request by hand and write to you at the address you gave when there is a place.")
	case errors.Is(err, store.ErrInvalid):
		bad(strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": "))
	case errors.Is(err, store.ErrNameTaken):
		bad("That name is taken. Please choose another.")
	case errors.Is(err, store.ErrOnList):
		bad("That name or that email address is already on the list.")
	case errors.Is(err, store.ErrWaitlistFull):
		reply(w, http.StatusServiceUnavailable, "The list is full", "The waiting list is closed for now. Please try again later.")
	default:
		log.Printf("waiting list: %v", err)
		reply(w, http.StatusInternalServerError, "That did not work", "Something went wrong on our side. Please try again later.")
	}
}
