package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"strings"
)

// The site icons are compiled in, so the single binary serves them.
//
//go:embed static
var staticFiles embed.FS

// cssVersion changes whenever the style sheet does. It is part of the address the pages ask for, because a CDN
// or a browser may keep /static/weft.css for hours whatever the server says (Cloudflare rewrites the header), and
// a page that is newer than its styles looks broken.
func cssVersion() string {
	b, err := staticFiles.ReadFile("static/weft.css")
	if err != nil {
		return "0"
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:4])
}

var cssV = cssVersion()

func init() {
	mime.AddExtensionType(".webmanifest", "application/manifest+json")
	mime.AddExtensionType(".ico", "image/x-icon")
}

// staticHandler serves /static/ and /favicon.ico on every host: they are the same for all users.
func (s *Server) staticHandler() http.Handler {
	sub, _ := fs.Sub(staticFiles, "static")
	files := http.StripPrefix("/static/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".css") {
			w.Header().Set("Cache-Control", "no-cache") // styles change with the build
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		if r.URL.Path == "/favicon.ico" {
			r.URL.Path = "/static/favicon.ico"
		}
		files.ServeHTTP(w, r)
	})
}
