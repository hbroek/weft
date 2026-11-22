package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/store"
)

func TestParseDocument(t *testing.T) {
	in, id, err := parseDoc("---\nid: 42\nurl: https://x/posts/a\ntitle: \"Say: \\\"hi\\\"\"  # a comment\nkeywords: [weft, 'two words', \"a, b\"]\ncanonical: https://example.org/o\nin_reply_to: https://o/p\nin_reply_to_feed: https://o/feed.xml\ncontext:\n  title: A page\n  description: It says: things\n  image: https://o/i.png\nmine: ignored\nlater:\n  - a\n  - b\npublished: 2026-11-21T09:30:00Z\n---\nBody *here*.\n\n---\nstays\n")
	if err != nil {
		t.Fatal(err)
	}
	if id != 42 || in.Title != `Say: "hi"` || in.Canonical != "https://example.org/o" || in.InReplyTo != "https://o/p" || in.InReplyToFeed != "https://o/feed.xml" {
		t.Errorf("fields: %d %+v", id, in)
	}
	if !reflect.DeepEqual(in.Keywords, []string{"weft", "two words", "a b"}) { // a keyword never holds a comma
		t.Errorf("keywords: %q", in.Keywords)
	}
	if in.Context == nil || *in.Context != (postContext{"A page", "It says: things", "https://o/i.png"}) {
		t.Errorf("context: %+v", in.Context)
	}
	if in.Markdown != "Body *here*.\n\n---\nstays\n" {
		t.Errorf("body: %q", in.Markdown)
	}

	// no frontmatter: all body, and the body is kept as it is
	if in, id, err := parseDoc("Just text.\n---\nnot front\n"); err != nil || id != 0 || in.Markdown != "Just text.\n---\nnot front\n" {
		t.Errorf("no frontmatter: %+v %d %v", in, id, err)
	}
	// an empty frontmatter lets a body begin with a rule
	if in, _, err := parseDoc("---\n---\n---\nrule first\n"); err != nil || in.Markdown != "---\nrule first\n" {
		t.Errorf("empty frontmatter: %q %v", in.Markdown, err)
	}
	// CRLF files, a byte order mark, keywords as a block list and as one line
	in, _, err = parseDoc("\xEF\xBB\xBF---\r\ntitle: Win\r\nkeywords:\r\n  - a\r\n  - b c\r\n---\r\nText\r\n")
	if err != nil || in.Title != "Win" || !reflect.DeepEqual(in.Keywords, []string{"a", "b c"}) || in.Markdown != "Text\r\n" {
		t.Errorf("crlf: %+v %v", in, err)
	}
	if in, _, err := parseDoc("---\nkeywords: one, two words\n---\nx"); err != nil || !reflect.DeepEqual(in.Keywords, []string{"one", "two words"}) {
		t.Errorf("keyword line: %+v %v", in, err)
	}
}

func TestParseDocumentErrors(t *testing.T) {
	for doc, want := range map[string]string{
		"---\ntitle: x\nbody":                                "not closed",
		"---\ntitle: x\ntitle: y\n---\nb":                    "line 3: title appears twice",
		"---\ntitle: >\n  folded\n---\nb":                    "line 2: title: multi-line",
		"---\ntitle:\n  - a\n---\nb":                         "line 2: title must be a single value",
		"---\nnot a key\n---\nb":                             "line 2: expected key: value",
		"---\n  stray\n---\nb":                               "line 2: expected key: value",
		"---\ntitle: \"open\n---\nb":                         "line 2: title: a quote is not closed",
		"---\ntitle: \"a\\qb\"\n---\nb":                      "escape that is not supported",
		"---\nid: abc\n---\nb":                               "line 2: id must be a number",
		"---\nkeywords: [a, b\n---\nb":                       "line 2: keywords:",
		"---\ncontext: yes\n---\nb":                          "line 2: context must list",
		"---\n" + strings.Repeat("a: b\n", 20000) + "---\nb": "too long",
	} {
		_, _, err := parseDoc(doc)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.40q: got %v, want %q", doc, err, want)
		}
	}
}

// What the server writes, it reads back unchanged, whatever the text is.
func TestDocumentRoundTrip(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for _, p := range []*store.Post{
		{ID: 1, Markdown: "plain", Created: now},
		{ID: 2, Title: "Why: a #hashtag, [brackets] and 'quotes'", Markdown: "---\nstarts with a rule\n\n![i](/media/abc.jpg)\n", Created: now, Updated: &now,
			Keywords:  []string{"weft", "two words", "a, b", "true", "2026-01-01", "12", "- x", "", "tab\tin"},
			Canonical: "https://example.org/a?b=c#d", InReplyTo: "https://o/p", InReplyToFeed: "https://o/f.xml",
			Parent: feed.ParentInfo{Title: "null", Description: "multi\nline: \"text\" # not a comment", Image: "https://o/i.png"}},
		{ID: 3, Title: "  padded ", Markdown: "x", Created: now},
		{ID: 4, Title: "~", Markdown: "x\r\ny", Created: now},
		{ID: 5, Title: "é – 日本語 \u2028", Markdown: "y", Created: now},
	} {
		doc := postDoc(p, "https://alice.example/posts/p")
		in, id, err := parseDoc(doc)
		if err != nil {
			t.Fatalf("post %d: %v\n%s", p.ID, err, doc)
		}
		want := postReq{Title: strings.TrimSpace(p.Title), Markdown: p.Markdown, Canonical: p.Canonical, InReplyTo: p.InReplyTo, InReplyToFeed: p.InReplyToFeed, Keywords: feed.Keywords(p.Keywords)}
		if !p.Parent.Empty() {
			want.Context = &postContext{p.Parent.Title, p.Parent.Description, p.Parent.Image}
		}
		if id != p.ID || !reflect.DeepEqual(in, want) {
			t.Errorf("post %d:\n got %+v\nwant %+v\n%s", p.ID, in, want, doc)
		}
	}
}

func FuzzParseDocument(f *testing.F) {
	f.Add("---\ntitle: x\n---\nb")
	f.Add("---\nkeywords: [a, \"b\n---\n")
	f.Fuzz(func(t *testing.T, s string) { parseDoc(s) })
}

func doWith(s *Server, method, host, path, tok, contentType, accept, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if accept != "" {
		r.Header.Set("Accept", accept)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// An editor's loop: make a post from a document, open it, change it and save it back, list, delete.
func TestPostsAsDocumentsThroughTheAPI(t *testing.T) {
	s, tok := setup(t)
	host := "alice.localhost:8080"
	const md, js = "text/markdown", "application/json"

	w := doWith(s, "POST", host, "/api/posts", tok, md, "", "---\ntitle: Notes\nslug: my-notes\nkeywords: [a, b c]\ncanonical: https://example.org/n\n---\nHello ![p](/media/abc.jpg)\n")
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var made postResp
	json.Unmarshal(w.Body.Bytes(), &made)
	if !strings.HasPrefix(made.URL, "http://alice.localhost:8080/posts/my-notes") {
		t.Fatalf("slug words not used: %+v", made)
	}
	id := strconv.FormatInt(made.ID, 10)
	slug := made.URL[strings.LastIndex(made.URL, "/")+1:]

	// the document: JSON by default, the editor's file with Accept: text/markdown; media paths as stored
	got := doWith(s, "GET", host, "/api/posts/"+id, tok, "", md, "")
	if got.Code != 200 || !strings.HasPrefix(got.Header().Get("Content-Type"), md) || got.Header().Get("Vary") != "Accept" {
		t.Fatalf("document: %d %q", got.Code, got.Header())
	}
	doc := got.Body.String()
	for _, want := range []string{"id: " + id + "\n", "url: " + made.URL + "\n", "title: Notes\n", "keywords: [a, b c]\n", "canonical: https://example.org/n\n", "\n---\nHello ![p](/media/abc.jpg)\n"} {
		if !strings.Contains(doc, want) {
			t.Errorf("document lacks %q:\n%s", want, doc)
		}
	}
	// by slug too, and as JSON
	var one postJSON
	jw := doWith(s, "GET", host, "/api/posts/"+slug, tok, "", js, "")
	if jw.Code != 200 || json.Unmarshal(jw.Body.Bytes(), &one) != nil || one.ID != made.ID || one.Markdown != "Hello ![p](/media/abc.jpg)\n" || one.Title != "Notes" {
		t.Fatalf("json by slug: %d %s", jw.Code, jw.Body)
	}
	if w := doWith(s, "GET", host, "/api/posts/"+id, tok, "", "*/*", ""); !strings.HasPrefix(w.Header().Get("Content-Type"), js) {
		t.Errorf("*/* should give JSON: %q", w.Header().Get("Content-Type"))
	}

	// save the document back with a change (what the editor does); keys it does not know are fine
	edited := strings.Replace(doc, "Hello", "Hello again", 1)
	edited = strings.Replace(edited, "---\nid:", "---\neditor_state: x\nid:", 1)
	if w := doWith(s, "PUT", host, "/api/posts/"+slug, tok, md, "", edited); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if again := doWith(s, "GET", host, "/api/posts/"+id, tok, "", md, "").Body.String(); !strings.Contains(again, "\nHello again ![p]") || !strings.Contains(again, "updated: ") || strings.Contains(again, "editor_state") {
		t.Errorf("after save:\n%s", again)
	}
	// a document is the whole post: leaving out the canonical link and the keywords removes them
	if w := doWith(s, "PUT", host, "/api/posts/"+id, tok, md, "", "---\ntitle: Notes\n---\nBare\n"); w.Code != 200 {
		t.Fatalf("bare save: %d %s", w.Code, w.Body)
	}
	if bare := doWith(s, "GET", host, "/api/posts/"+id, tok, "", md, "").Body.String(); strings.Contains(bare, "canonical") || strings.Contains(bare, "keywords") {
		t.Errorf("bare document kept fields:\n%s", bare)
	}
	// a document for another post is refused, and the post is not touched
	if w := doWith(s, "PUT", host, "/api/posts/"+id, tok, md, "", "---\nid: 9999\n---\nWrong post\n"); w.Code != http.StatusConflict {
		t.Errorf("wrong id: %d %s", w.Code, w.Body)
	}
	if w := doWith(s, "PUT", host, "/api/posts/"+id, tok, md, "", "---\nkeywords: [oops\n---\nx"); w.Code != 400 || !strings.Contains(w.Body.String(), "line 2") {
		t.Errorf("bad document: %d %s", w.Code, w.Body)
	}
	// JSON keeps working, with and without a content type, and the old edit by number
	if w := doWith(s, "PUT", host, "/api/posts/"+id, tok, "", "", `{"title":"Json","markdown":"by json"}`); w.Code != 200 {
		t.Errorf("json edit: %d %s", w.Code, w.Body)
	}

	// the list: own posts, newest first, no bodies, paged with a Link header
	for i := 0; i < 3; i++ {
		doWith(s, "POST", host, "/api/posts", tok, "", "", `{"markdown":"note `+strconv.Itoa(i)+`"}`)
	}
	var list []postJSON
	lw := doWith(s, "GET", host, "/api/posts?limit=2", tok, "", "", "")
	if lw.Code != 200 || json.Unmarshal(lw.Body.Bytes(), &list) != nil || len(list) != 2 {
		t.Fatalf("list: %d %s", lw.Code, lw.Body)
	}
	for _, p := range list {
		if p.Markdown != "" || p.URL == "" || p.ID == 0 {
			t.Errorf("list entry: %+v", p)
		}
	}
	if doWith(s, "GET", host, "/api/posts?limit=0", tok, "", "", "").Code != 400 || doWith(s, "GET", host, "/api/posts?before=soon", tok, "", "", "").Code != 400 {
		t.Error("bad limit or before accepted")
	}
	all := doWith(s, "GET", host, "/api/posts", tok, "", "", "")
	var every []postJSON
	json.Unmarshal(all.Body.Bytes(), &every)
	if len(every) != 4 || all.Header().Get("Link") != "" {
		t.Errorf("all posts: %d, link %q", len(every), all.Header().Get("Link"))
	}

	// delete by slug; it is then gone from the API, and the permalink says 410
	if w := doWith(s, "DELETE", host, "/api/posts/"+slug, tok, "", "", ""); w.Code != 204 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	for _, ref := range []string{id, slug} {
		if w := doWith(s, "GET", host, "/api/posts/"+ref, tok, "", "", ""); w.Code != 404 {
			t.Errorf("deleted post %s: %d", ref, w.Code)
		}
	}
	if w := doWith(s, "GET", host, "/posts/"+slug, "", "", "", ""); w.Code != http.StatusGone {
		t.Errorf("permalink of a deleted post: %d", w.Code)
	}

	// every read and write needs the token
	for _, path := range []string{"/api/posts", "/api/posts/" + id} {
		if w := doWith(s, "GET", host, path, "", "", "", ""); w.Code != 401 {
			t.Errorf("GET %s without a token: %d", path, w.Code)
		}
	}
	// a body over 1 MB is refused as too large
	if w := doWith(s, "POST", host, "/api/posts", tok, md, "", "---\n---\n"+strings.Repeat("x", 1<<20)); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("large body: %d", w.Code)
	}
}

// A reply made with a document keeps its links and context, and shows them again.
func TestReplyDocumentKeepsItsContext(t *testing.T) {
	s, tok := setup(t)
	host := "alice.localhost:8080"
	w := doWith(s, "POST", host, "/api/posts", tok, "text/markdown", "", "---\nin_reply_to: https://other.example/page\ncontext:\n  title: The page\n  description: What it says\n---\nI disagree.\n")
	if w.Code != 201 {
		t.Fatalf("reply: %d %s", w.Code, w.Body)
	}
	var made postResp
	json.Unmarshal(w.Body.Bytes(), &made)
	doc := doWith(s, "GET", host, "/api/posts/"+strconv.FormatInt(made.ID, 10), tok, "", "text/markdown", "").Body.String()
	for _, want := range []string{"in_reply_to: https://other.example/page\n", "context:\n  title: The page\n  description: What it says\n"} {
		if !strings.Contains(doc, want) {
			t.Errorf("reply document lacks %q:\n%s", want, doc)
		}
	}
}
