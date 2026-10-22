package web

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
)

// initial is the letter an avatar shows when there is no image: the first letter or digit of the
// name, in capitals.
func initial(name string) string {
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return string(unicode.ToUpper(r))
		}
	}
	return "?"
}

const (
	maxDisplay = 80
	maxBio     = 500
)

func (s *Server) profileForm(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	s.renderProfile(w, r, q, http.StatusOK, "", q.host.Display, q.host.Bio, q.host.Avatar)
}

func (s *Server) renderProfile(w http.ResponseWriter, r *http.Request, q *req, status int, msg, display, bio, avatar string) {
	d := s.data(r, q, "Profile")
	d.Error = msg
	d.Profile.Display, d.Profile.Bio, d.Profile.Avatar = display, bio, avatar
	d.Domain = s.domainView(r, q)
	s.show(w, "profile", status, d)
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	display, bio, avatar := strings.TrimSpace(r.FormValue("display")), strings.TrimSpace(r.FormValue("bio")), strings.TrimSpace(r.FormValue("avatar"))
	msg := ""
	switch {
	case len([]rune(display)) > maxDisplay:
		msg = "The name is too long."
	case len([]rune(bio)) > maxBio:
		msg = "The bio is too long."
	case avatar != "" && httpURL(avatar) == "":
		msg = "The avatar must be a full http:// or https:// image address."
	case len(avatar) > 2048:
		msg = "The avatar address is too long."
	}
	if msg != "" {
		s.renderProfile(w, r, q, http.StatusBadRequest, msg, display, bio, avatar)
		return
	}
	if err := q.sp.UpdateProfile(q.host.ID, display, bio, avatar); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// export sends the owner their whole account as a zip: posts, replies, likes, follows and login, without
// sessions or API token. It is a POST so that a link, a prefetch or another site cannot trigger it.
func (s *Server) export(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	tmp, err := os.CreateTemp("", "weft-export-*.zip") // on disk, not in memory: an account may be large
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := q.sp.Export(tmp, s.cfg.BaseDomain); err != nil {
		http.Error(w, "export failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.weft.zip"`, q.host.Name, time.Now().Format("2006-01-02")))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", time.Time{}, tmp)
}
