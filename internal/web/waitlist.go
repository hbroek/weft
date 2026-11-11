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
	case "/signup", "/signup/check", "/signup/finish":
		s.signup(w, r)
		return
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
		if s.cfg.SiteDir == "" {
			http.NotFound(w, r)
			return
		}
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
	out := map[string]any{"accounts": accounts, "waiting": waiting, "signups": s.cfg.Signups}
	if s.cfg.MaxUsers > 0 {
		out["cap"] = s.cfg.MaxUsers
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	json.NewEncoder(w).Encode(out)
}

// shell writes a small page, in the colours of the landing page. body is HTML the caller has made safe.
func shell(w http.ResponseWriter, code int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="robots" content="noindex"><title>%s</title>
<style>body{margin:0;font:17px/1.6 system-ui,sans-serif;background:#eef0fa;color:#1a1f3d}main{max-width:36rem;margin:0 auto;padding:2rem 16px 3rem}a{color:#26337a}
h1{font-size:1.7rem;line-height:1.25}label{display:block;font-weight:600;margin-top:.9rem}small{display:block;font-weight:400;color:#5b6188}
input{width:100%%;box-sizing:border-box;font:inherit;padding:.5rem .6rem;margin-top:.25rem;border:1px solid #26337a33;border-radius:6px;background:#fff;color:inherit}
button{margin-top:1.1rem;border:0;cursor:pointer;font:inherit;font-weight:600;background:#26337a;color:#eef0fa;padding:.6rem 1.1rem;border-radius:8px}
code{background:#26337a12;padding:.1em .35em;border-radius:4px;overflow-wrap:anywhere}button.copy{margin:0 0 0 .4rem;padding:.1rem .55rem;font-size:.8rem;font-weight:600}.err{background:#fbe9e7;color:#8c1d18;padding:.6rem .8rem;border-radius:8px}
table{border-collapse:collapse;margin:.5rem 0}td,th{text-align:left;padding:.3rem .8rem .3rem 0;vertical-align:top}th{color:#5b6188;font-weight:600}
@media(prefers-color-scheme:dark){body{background:#11142b;color:#eef0fa}a{color:#f08a4b}small,th{color:#a4abd8}input{background:#141937;border-color:#eef0fa40}button{background:#f08a4b;color:#11142b}code{background:#eef0fa14}.err{background:#4a1d1a;color:#ffb4ab}}</style></head><body><main><h1>%s</h1>%s</main>%s</body></html>`,
		html.EscapeString(title), html.EscapeString(title), body, copyScript)
}

// reply writes a message page with a way back to the sign-up section.
func reply(w http.ResponseWriter, code int, title, msg string) {
	shell(w, code, title, "<p>"+html.EscapeString(msg)+`</p><p><a href="/#signup">Back to the sign-up page</a></p>`)
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
