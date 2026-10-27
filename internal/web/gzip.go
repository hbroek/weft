package web

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

// Text is sent compressed to a client that accepts it: pages, style sheets, scripts, feeds and JSON are a
// fifth of their size or less. Pictures are not (they are compressed already), and neither is a range
// request or an answer that has no body.

var gzipPool = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

// compressible reports whether a content type is text that shrinks.
func compressible(ct string) bool {
	ct = strings.ToLower(ct)
	for _, p := range []string{"text/", "application/json", "application/xml", "application/rss+xml", "application/atom+xml", "application/javascript", "application/xhtml+xml", "image/svg+xml"} {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	return false
}

// withGzip wraps w when the request allows a compressed answer.
func withGzip(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, func()) {
	if r.Method != http.MethodGet || r.Header.Get("Range") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) || strings.HasPrefix(r.URL.Path, "/media/") {
		return w, func() {}
	}
	w.Header().Add("Vary", "Accept-Encoding")
	g := &gzipWriter{ResponseWriter: w}
	return g, g.close
}

func acceptsGzip(v string) bool {
	for _, part := range strings.Split(v, ",") {
		name, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") {
			return strings.TrimSpace(strings.ReplaceAll(q, " ", "")) != "q=0"
		}
	}
	return false
}

type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	decided bool
}

// Unwrap lets http.ResponseController reach the real writer, to change a deadline.
func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipWriter) decide(status int) {
	if g.decided {
		return
	}
	g.decided = true
	h := g.Header()
	if status == http.StatusNoContent || status == http.StatusNotModified || h.Get("Content-Encoding") != "" || !compressible(h.Get("Content-Type")) {
		return
	}
	h.Set("Content-Encoding", "gzip")
	h.Del("Content-Length")
	if et := h.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
		h.Set("ETag", "W/"+et) // the bytes sent are not the bytes of the resource
	}
	g.gz = gzipPool.Get().(*gzip.Writer)
	g.gz.Reset(g.ResponseWriter)
}

func (g *gzipWriter) WriteHeader(status int) {
	g.decide(status)
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.decided {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(b))
		}
		g.decide(http.StatusOK)
	}
	if g.gz == nil {
		return g.ResponseWriter.Write(b)
	}
	return g.gz.Write(b)
}

func (g *gzipWriter) Flush() {
	if g.gz != nil {
		g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipWriter) close() {
	if g.gz != nil {
		g.gz.Close()
		gzipPool.Put(g.gz)
		g.gz = nil
	}
}
