package web

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
)

// The site icons are compiled in, so the single binary serves them.
//
//go:embed static
var staticFiles embed.FS

func init() {
	mime.AddExtensionType(".webmanifest", "application/manifest+json")
	mime.AddExtensionType(".ico", "image/x-icon")
}

// staticHandler serves /static/ and /favicon.ico on every host: they are the same for all users.
func (s *Server) staticHandler() http.Handler {
	sub, _ := fs.Sub(staticFiles, "static")
	files := http.StripPrefix("/static/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if r.URL.Path == "/favicon.ico" {
			r.URL.Path = "/static/favicon.ico"
		}
		files.ServeHTTP(w, r)
	})
}
