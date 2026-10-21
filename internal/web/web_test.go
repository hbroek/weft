package web

import (
	"bytes"
	"encoding/xml"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hbroek/weft/internal/store"
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
	if w.Code != 201 || !regexp.MustCompile(`http://alice\.localhost:8080/posts/essay-[a-z0-9]{4}"`).MatchString(w.Body.String()) {
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
				Link     string  `xml:"link"`
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

	// identity and address are separate: the guid is a UUID, the link a readable slug
	if !regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(note.GUID) ||
		!regexp.MustCompile(`^http://alice\.localhost:8080/posts/a-short-note-[a-z0-9]{4}$`).MatchString(note.Link) {
		t.Errorf("guid %q link %q", note.GUID, note.Link)
	}
	pw := do(s, "GET", host, pathOf(note.Link), "", "")
	body, _ := io.ReadAll(pw.Body)
	if pw.Code != http.StatusOK || !strings.Contains(string(body), `rel="webmention"`) || !strings.Contains(string(body), `type="application/rss+xml"`) {
		t.Errorf("permalink page missing discovery links: %d", pw.Code)
	}
	// content negotiation: text/markdown gets the source, browsers keep the page
	mr := httptest.NewRequest("GET", "http://"+host+pathOf(note.Link), nil)
	mr.Header.Set("Accept", "text/markdown")
	mw := httptest.NewRecorder()
	s.ServeHTTP(mw, mr)
	if ct := mw.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") || !strings.HasPrefix(mw.Body.String(), "A short *note*.") || mw.Header().Get("Vary") != "Accept" {
		t.Errorf("markdown request: %q %q", ct, mw.Body)
	}
	br := httptest.NewRequest("GET", "http://"+host+pathOf(note.Link), nil)
	br.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	bw := httptest.NewRecorder()
	s.ServeHTTP(bw, br)
	if !strings.HasPrefix(bw.Header().Get("Content-Type"), "text/html") {
		t.Errorf("browser accept got %q", bw.Header().Get("Content-Type"))
	}
	if w := do(s, "GET", host, "/posts/99", "", ""); w.Code != 404 {
		t.Errorf("missing post: %d", w.Code)
	}
}

func TestVerboseLogsAPICallsWithoutTokens(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	st, err := store.Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tok, _ := st.AddUser("alice", "Alice", "")
	s := New(Config{BaseDomain: "localhost", Verbose: true}, st)
	if w := do(s, "POST", "alice.localhost:8080", "/api/posts", tok, `{"markdown":"hello logs"}`); w.Code != 201 {
		t.Fatalf("post: %d", w.Code)
	}
	do(s, "GET", "alice.localhost:8080", "/api/timeline", "bad-token", "")
	do(s, "GET", "alice.localhost:8080", "/feed.xml", "", "")
	out := buf.String()
	for _, want := range []string{"api alice.localhost:8080 POST /api/posts -> 201", `{"markdown":"hello logs"}`, "GET /api/timeline -> 401"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, tok) || strings.Contains(out, "bad-token") || strings.Contains(out, "/feed.xml") {
		t.Errorf("log leaks a token or logs a non-API request:\n%s", out)
	}
	buf.Reset()
	s2 := New(Config{BaseDomain: "localhost"}, st)
	do(s2, "GET", "alice.localhost:8080", "/api/timeline", tok, "")
	if buf.Len() != 0 {
		t.Errorf("logging without -v: %s", buf.String())
	}
}

// pathOf returns the path part of a URL, for requests sent straight to a handler.
func pathOf(u string) string {
	x, _ := url.Parse(u)
	return x.Path
}
