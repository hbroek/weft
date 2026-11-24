package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
)

// A post's own address serves its source as a document (the one the API gives, with frontmatter) and takes the
// document back with PUT, so an editor that reads /posts/<slug> can save to it. What the reader may do is said in
// Allow; the ETag of the document is what a save names in If-Match.

// docETag is the strong ETag of a document: a hash of the bytes served.
func docETag(doc string) string {
	sum := sha256.Sum256([]byte(doc))
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// etagMatches reports whether an If-Match header names etag. Only strong tags match, so a weak one never does.
func etagMatches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		if t = strings.TrimSpace(t); t == "*" || t == etag {
			return true
		}
	}
	return false
}

// mayEdit reports whether this request carries a bearer token that may change the host's posts: the account's own
// token, or an OAuth token with the post scope. It never answers the request, because the page is public.
func (s *Server) mayEdit(r *http.Request, q *req) bool {
	need := q.need
	q.need = "post"
	defer func() { q.need = need }()
	return s.bearer(r, q) == nil
}

// postDocument serves a post as a document, with the headers an editor looks for.
func (s *Server) postDocument(w http.ResponseWriter, r *http.Request, q *req, slug string, doc string) {
	h := w.Header()
	h.Add("Vary", "Authorization")
	h.Set("Content-Type", "text/markdown; charset=utf-8")
	h.Set("ETag", docETag(doc))
	h.Set("Cache-Control", "no-transform") // a changed encoding would make the tag weak
	allow := "GET, HEAD"
	if s.mayEdit(r, q) {
		allow += ", PUT"
		h.Set("Link", "</posts/"+slug+`>; rel="edit"`)
		h.Set("Cache-Control", "private, no-transform")
	}
	h.Set("Allow", allow)
	io.WriteString(w, doc)
}

// putPost replaces a post with the document in the body (PUT /posts/<slug>), as PUT /api/posts/<slug> does. A
// token without the post scope is refused, and If-Match, when sent, has to name the document as it is now.
func (s *Server) putPost(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	p, err := q.sp.PostBySlug(q.host.ID, r.PathValue("slug"))
	if err != nil || p.Deleted {
		http.NotFound(w, r)
		return
	}
	base := s.base(r)
	if im := r.Header.Get("If-Match"); im != "" && !etagMatches(im, docETag(absMedia(postDoc(p, base+"/posts/"+p.Slug), base))) {
		http.Error(w, "the post has changed since you read it", http.StatusPreconditionFailed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "the body is larger than 1 MB", http.StatusRequestEntityTooLarge)
		return
	}
	// The document was served with full addresses for the pictures; they are stored as paths.
	body = bytes.ReplaceAll(body, []byte(strings.TrimSuffix(base, "/")+"/media/"), []byte("/media/"))
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.SetPathValue("ref", p.Slug)
	s.apiUpdate(w, r, q)
}
