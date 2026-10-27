package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/fetch"
	"github.com/hbroek/weft/internal/img"
	"github.com/hbroek/weft/internal/store"
)

// Pictures live in the author's own database (see store/media.go). A post's Markdown refers to one by its path,
// /media/<hash>.<ext>, without a host, so a post that moves to another address keeps its pictures; feeds and
// other documents that leave the site make the path a full address (absMedia).

// Defaults for what a server allows; each can be set in Config.
const (
	defaultMaxImageBytes = 5 << 20
	defaultMaxUpload     = 25 << 20
	defaultMediaQuota    = 200 << 20
	defaultImagePixels   = 24_000_000
	defaultCompactSide   = 1600
)

func (s *Server) imageLimits() img.Limits {
	l := img.Limits{MaxBytes: defaultMaxImageBytes, MaxInput: defaultMaxUpload, MaxPixels: defaultImagePixels, MaxSide: defaultCompactSide}
	if s.cfg.MaxUploadBytes > 0 {
		l.MaxInput = s.cfg.MaxUploadBytes
	}
	if s.cfg.MaxImageBytes > 0 {
		l.MaxBytes = s.cfg.MaxImageBytes
	}
	if s.cfg.MaxImagePixels > 0 {
		l.MaxPixels = s.cfg.MaxImagePixels
	}
	if s.cfg.CompactSide > 0 {
		l.MaxSide = s.cfg.CompactSide
	}
	return l
}

// mediaQuota is how much each user may store: the default, the configured amount, or no limit (negative).
func (s *Server) mediaQuota() int64 {
	switch {
	case s.cfg.MediaQuota < 0:
		return 0
	case s.cfg.MediaQuota == 0:
		return defaultMediaQuota
	}
	return s.cfg.MediaQuota
}

var mediaName = regexp.MustCompile(`^[0-9a-f]{32}\.(jpg|png|gif)$`)

// absMedia turns the paths of a user's pictures in Markdown or HTML into full addresses on base.
func absMedia(s, base string) string {
	if !strings.Contains(s, "/media/") {
		return s
	}
	base = strings.TrimSuffix(base, "/")
	return strings.NewReplacer(`](/media/`, `](`+base+`/media/`, `src="/media/`, `src="`+base+`/media/`,
		`href="/media/`, `href="`+base+`/media/`, `]: /media/`, `]: `+base+`/media/`).Replace(s)
}

// originOf is the scheme and host of an address, "" if it is not one.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// mediaGet serves a picture. Anyone may read it: it is part of a published post.
func (s *Server) mediaGet(w http.ResponseWriter, r *http.Request, q *req) {
	name := r.PathValue("name")
	if !mediaName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	m, err := q.sp.Media(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", m.Mime)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "public, max-age=31536000, immutable") // the name is the hash of the bytes
	h.Set("ETag", `"`+name+`"`)
	if r.Header.Get("If-None-Match") == `"`+name+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(m.Data)))
	w.Write(m.Data)
}

// storeImage cleans a picture and keeps it in the user's database.
func (s *Server) storeImage(sp *store.Space, data []byte, mode img.Mode, origin string) (*store.Media, bool, error) {
	res, err := img.Process(data, mode, s.imageLimits())
	if err != nil {
		return nil, false, err
	}
	sum := sha256.Sum256(res.Data)
	m := store.Media{Name: hex.EncodeToString(sum[:16]) + "." + res.Ext, Mime: res.Mime, Width: res.Width, Height: res.Height, Origin: origin, Data: res.Data}
	added, err := sp.PutMedia(m, s.mediaQuota())
	if err != nil {
		return nil, false, err
	}
	m.Size = int64(len(res.Data))
	return &m, added, nil
}

func modeOf(v string) img.Mode {
	if v == "compact" {
		return img.Compact
	}
	return img.Original
}

// mediaError answers a failed upload.
func mediaError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	switch {
	case errors.Is(err, img.ErrUnsupported):
		http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
	case errors.Is(err, img.ErrTooLarge), errors.Is(err, img.ErrTooManyPx), errors.As(err, &tooBig):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	case errors.Is(err, img.ErrCorrupt):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, store.ErrQuota):
		http.Error(w, "Your storage is full. Delete pictures you no longer use.", http.StatusInsufficientStorage)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// readUpload reads the picture a request carries: the "file" part of a form, or else the whole body.
func (s *Server) readUpload(w http.ResponseWriter, r *http.Request) (data []byte, mode img.Mode, err error) {
	lim := s.imageLimits()
	max := int64(max(lim.MaxBytes, lim.MaxInput))
	r.Body = http.MaxBytesReader(w, r.Body, max+(1<<20))
	// a photo from a phone on a mobile connection takes longer than the 30 seconds a form gets
	http.NewResponseController(w).SetReadDeadline(time.Now().Add(2 * time.Minute))
	size := r.URL.Query().Get("size")
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			return nil, 0, err
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			return nil, 0, errors.New("send the picture as the form field \"file\"")
		}
		defer f.Close()
		if v := r.FormValue("size"); v != "" {
			size = v
		}
		data, err = io.ReadAll(io.LimitReader(f, max+1))
		return data, modeOf(size), err
	}
	data, err = io.ReadAll(r.Body)
	return data, modeOf(size), err
}

type mediaJSON struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	URL      string `json:"url"`
	Markdown string `json:"markdown"`
	Mime     string `json:"mime"`
	Size     int64  `json:"size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Added    bool   `json:"added"`
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request, q *req) {
	data, mode, err := s.readUpload(w, r)
	if err != nil {
		mediaError(w, err)
		return
	}
	m, added, err := s.storeImage(q.sp, data, mode, "")
	if err != nil {
		mediaError(w, err)
		return
	}
	alt := strings.NewReplacer("[", "", "]", "", "\n", " ").Replace(r.URL.Query().Get("alt"))
	if alt == "" {
		alt = r.FormValue("alt")
	}
	writeJSON(w, http.StatusCreated, mediaJSON{Name: m.Name, Path: "/media/" + m.Name, URL: s.base(r) + "/media/" + m.Name,
		Markdown: "![" + alt + "](/media/" + m.Name + ")", Mime: m.Mime, Size: m.Size, Width: m.Width, Height: m.Height, Added: added})
}

// mediaUpload is the editor's upload, for the signed-in owner.
func (s *Server) mediaUpload(w http.ResponseWriter, r *http.Request, q *req) {
	if !q.owner {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	s.upload(w, r, q)
}

// apiMediaUpload is the same for a program with a token.
func (s *Server) apiMediaUpload(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	s.upload(w, r, q)
}

var imageRef = regexp.MustCompile(`!\[([^\]]*)\]\((https?://[^)\s]+)((?:\s+"[^"]*")?)\)`)

const maxKeepPerRequest = 10

type keepFailure struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

// mediaKeep makes copies of the pictures from other sites that a post's Markdown shows, keeps them in the
// user's database and returns the Markdown pointing at the copies. It runs when the author asks for it.
func (s *Server) mediaKeep(w http.ResponseWriter, r *http.Request, q *req) {
	if !q.owner {
		http.Error(w, "sign in first", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	markdown, mode := r.FormValue("markdown"), modeOf(r.FormValue("size"))
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	copies := map[string]string{} // address -> path of the copy
	var failed []keepFailure
	own := s.base(r) + "/media/"
	for _, m := range imageRef.FindAllStringSubmatch(markdown, -1) {
		u := html.UnescapeString(m[2])
		if _, done := copies[u]; done {
			continue
		}
		if strings.HasPrefix(u, own) { // our own address, written in full
			copies[u] = strings.TrimPrefix(u, strings.TrimSuffix(own, "/media/"))
			continue
		}
		if len(copies) >= maxKeepPerRequest {
			failed = append(failed, keepFailure{u, "left for the next time (at most " + strconv.Itoa(maxKeepPerRequest) + " at once)"})
			continue
		}
		res, err := s.web.GetAs(ctx, u, "image/jpeg,image/png,image/gif;q=0.9,*/*;q=0.1")
		if err != nil {
			failed = append(failed, keepFailure{u, keepReason(err)})
			continue
		}
		med, _, err := s.storeImage(q.sp, res.Body, mode, u)
		if err != nil {
			failed = append(failed, keepFailure{u, keepReason(err)})
			if errors.Is(err, store.ErrQuota) {
				break
			}
			continue
		}
		copies[u] = "/media/" + med.Name
	}
	out := imageRef.ReplaceAllStringFunc(markdown, func(ref string) string {
		m := imageRef.FindStringSubmatch(ref)
		if p, ok := copies[html.UnescapeString(m[2])]; ok {
			return "![" + m[1] + "](" + p + m[3] + ")"
		}
		return ref
	})
	kept := 0
	for u, p := range copies {
		if !strings.HasPrefix(u, own) && p != "" {
			kept++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"markdown": out, "kept": kept, "failed": failed})
}

func keepReason(err error) string {
	var se *fetch.StatusError
	switch {
	case errors.As(err, &se):
		return "the site answered " + strconv.Itoa(se.Code)
	case errors.Is(err, img.ErrUnsupported):
		return "not a JPEG, PNG or GIF picture"
	case errors.Is(err, img.ErrTooLarge), errors.Is(err, img.ErrTooManyPx):
		return "too large"
	case errors.Is(err, img.ErrCorrupt):
		return "not a valid picture"
	case errors.Is(err, store.ErrQuota):
		return "your storage is full"
	}
	return "could not be fetched"
}

// ---- the owner's list of pictures ----

type imageRow struct {
	Name, Path, Size, Dims, Origin, When string
	Used                                 int
}

func byteSize(n int64) string {
	switch {
	case n >= 10<<20:
		return fmt.Sprintf("%d MB", n>>20)
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

func (s *Server) imagesPage(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	d := s.data(r, q, "Images")
	list, err := q.sp.MediaList()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var used int64
	for _, m := range list {
		n, _ := q.sp.PostsUsingMedia(m.Name)
		used += m.Size
		d.Images = append(d.Images, imageRow{Name: m.Name, Path: "/media/" + m.Name, Size: byteSize(m.Size), Dims: fmt.Sprintf("%d × %d", m.Width, m.Height),
			Origin: m.Origin, When: stampAt(m.Created), Used: n})
	}
	d.MediaUsed = byteSize(used)
	if quota := s.mediaQuota(); quota > 0 {
		d.MediaQuota = byteSize(quota)
	}
	if m := r.FormValue("msg"); m == "in-use" {
		d.Error = "That picture is in use by a post. Delete it anyway to remove it from the post too."
	}
	s.show(w, "images", http.StatusOK, d)
}

func (s *Server) imageDelete(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	name := r.FormValue("name")
	if !mediaName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	if n, _ := q.sp.PostsUsingMedia(name); n > 0 && r.FormValue("force") != "1" {
		http.Redirect(w, r, "/images?msg=in-use", http.StatusSeeOther)
		return
	}
	if err := q.sp.DeleteMedia(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/images", http.StatusSeeOther)
}
