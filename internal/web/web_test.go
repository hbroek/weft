package web

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbroek/text-thread-casting/internal/store"
)

func setup(t *testing.T) (*Server, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	tok, err := st.AddUser("alice", "Alice", "Notes on the open web.")
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{BaseDomain: "localhost"}, st), tok
}

func do(s *Server, method, host, path, tok, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestPublishAndFeed(t *testing.T) {
	s, tok := setup(t)
	host := "alice.localhost:8080"

	if w := do(s, "POST", host, "/api/posts", "", `{"markdown":"x"}`); w.Code != 401 {
		t.Fatalf("no token: got %d", w.Code)
	}
	if w := do(s, "POST", "bob.localhost:8080", "/api/posts", tok, `{"markdown":"x"}`); w.Code != 404 {
		t.Fatalf("unknown host: got %d", w.Code)
	}
	if w := do(s, "POST", host, "/api/posts", tok, `{"markdown":"  "}`); w.Code != 400 {
		t.Fatalf("empty: got %d", w.Code)
	}
	if w := do(s, "POST", host, "/api/posts", tok, `{"markdown":"A short *note*. <script>x</script>"}`); w.Code != 201 {
		t.Fatalf("untitled: got %d %s", w.Code, w.Body)
	}
	w := do(s, "POST", host, "/api/posts", tok, `{"title":"Essay","markdown":"# hi\n\nbody"}`)
	if w.Code != 201 || !strings.Contains(w.Body.String(), "http://alice.localhost:8080/posts/2") {
		t.Fatalf("titled: %d %s", w.Code, w.Body)
	}

	fw := do(s, "GET", host, "/feed.xml", "", "")
	if ct := fw.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/rss+xml") {
		t.Fatalf("content-type %q", ct)
	}
	var doc struct {
		Version string `xml:"version,attr"`
		Channel struct {
			Webmention string `xml:"webmention"`
			Items      []struct {
				Title    *string `xml:"title"`
				GUID     string  `xml:"guid"`
				Desc     string  `xml:"description"`
				Markdown string  `xml:"markdown"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(fw.Body.Bytes(), &doc); err != nil {
		t.Fatalf("feed does not parse: %v\n%s", err, fw.Body)
	}
	if doc.Version != "2.0" || doc.Channel.Webmention == "" || len(doc.Channel.Items) != 2 {
		t.Fatalf("bad feed: %+v", doc)
	}
	essay, note := doc.Channel.Items[0], doc.Channel.Items[1]
	if essay.Title == nil || *essay.Title != "Essay" {
		t.Errorf("essay title missing")
	}
	if note.Title != nil {
		t.Errorf("untitled note must have no <title>")
	}
	if !strings.Contains(note.Desc, "<em>note</em>") || strings.Contains(note.Desc, "<script>") {
		t.Errorf("html not rendered safely: %s", note.Desc)
	}
	if !strings.HasPrefix(note.Markdown, "A short *note*.") {
		t.Errorf("source:markdown missing: %q", note.Markdown)
	}

	pw := do(s, "GET", host, "/posts/1", "", "")
	body, _ := io.ReadAll(pw.Body)
	if pw.Code != http.StatusOK || !strings.Contains(string(body), `rel="webmention"`) || !strings.Contains(string(body), `type="application/rss+xml"`) {
		t.Errorf("permalink page missing discovery links: %d", pw.Code)
	}
	if w := do(s, "GET", host, "/posts/99", "", ""); w.Code != 404 {
		t.Errorf("missing post: %d", w.Code)
	}
}
