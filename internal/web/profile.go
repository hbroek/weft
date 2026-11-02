package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/hbroek/weft/internal/img"
	"github.com/hbroek/weft/internal/store"
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

// avatarSize is the side, in pixels, of an uploaded profile picture.
const avatarSize = 256

// localAvatar is the path of an uploaded profile picture ("/media/<name>"), or "" for any other avatar.
func localAvatar(a string) string {
	if name, ok := strings.CutPrefix(a, "/media/"); ok && mediaName.MatchString(name) {
		return a
	}
	return ""
}

// dropAvatar removes the picture an avatar used to be, unless a post shows it or it is still the avatar.
func (s *Server) dropAvatar(sp *store.Space, old, keep string) {
	p := localAvatar(old)
	if p == "" || p == keep {
		return
	}
	name := strings.TrimPrefix(p, "/media/")
	if n, err := sp.PostsUsingMedia(name); err == nil && n == 0 {
		sp.DeleteMedia(name)
	}
}

// setAvatar makes a profile picture of an uploaded picture and keeps it in the user's database.
func (s *Server) setAvatar(q *req, data []byte) (string, error) {
	res, err := img.Avatar(data, s.imageLimits(), avatarSize)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(res.Data)
	m := store.Media{Name: hex.EncodeToString(sum[:16]) + "." + res.Ext, Mime: res.Mime, Width: res.Width, Height: res.Height, Origin: "profile picture", Data: res.Data}
	if _, err := q.sp.PutMedia(m, s.mediaQuota()); err != nil {
		return "", err
	}
	path := "/media/" + m.Name
	if err := q.sp.UpdateProfile(q.host.ID, q.host.Display, q.host.Bio, path); err != nil {
		return "", err
	}
	s.dropAvatar(q.sp, q.host.Avatar, path)
	return path, nil
}

// avatarUpload is the profile page's upload of a picture.
func (s *Server) avatarUpload(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	data, _, err := s.readUpload(w, r)
	if err == nil {
		_, err = s.setAvatar(q, data)
	}
	if err != nil {
		// a form post: show the profile page again with the problem written on it, not a bare error
		code, msg := s.uploadProblem(err, s.limitFor(img.Compact))
		s.renderProfile(w, r, q, code, msg, q.host.Display, q.host.Bio, q.host.Avatar)
		return
	}
	http.Redirect(w, r, "/profile", http.StatusSeeOther)
}

// apiAvatar is the same for a program with a token.
func (s *Server) apiAvatar(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	data, _, err := s.readUpload(w, r)
	if err != nil {
		s.mediaError(w, err, s.limitFor(img.Compact))
		return
	}
	path, err := s.setAvatar(q, data)
	if err != nil {
		s.mediaError(w, err, s.limitFor(img.Compact))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path, "url": s.base(r) + path})
}

func (s *Server) avatarRemove(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	if err := q.sp.UpdateProfile(q.host.ID, q.host.Display, q.host.Bio, ""); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.dropAvatar(q.sp, q.host.Avatar, "")
	http.Redirect(w, r, "/profile", http.StatusSeeOther)
}

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
	if localAvatar(avatar) != "" { // an uploaded picture is not an address to edit
		d.Profile.Avatar, d.Profile.Local = "", true
	}
	d.UploadMax = s.limitFor(img.Compact)
	d.Domain = s.domainView(r, q)
	d.Move = s.moveView(r, q)
	s.show(w, "profile", status, d)
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	display, bio, avatar := strings.TrimSpace(r.FormValue("display")), strings.TrimSpace(r.FormValue("bio")), strings.TrimSpace(r.FormValue("avatar"))
	cur := localAvatar(q.host.Avatar)
	if avatar == "" && cur != "" && r.FormValue("keep_avatar") == "1" { // the uploaded picture stays unless a link replaces it
		avatar = cur
	}
	msg := ""
	switch {
	case len([]rune(display)) > maxDisplay:
		msg = "The name is too long."
	case len([]rune(bio)) > maxBio:
		msg = "The bio is too long."
	case avatar != "" && avatar != cur && httpURL(avatar) == "":
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
	s.dropAvatar(q.sp, q.host.Avatar, avatar)
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
