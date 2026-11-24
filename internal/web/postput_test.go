package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hbroek/weft/internal/store"
)

func getDoc(s *Server, path, tok, enc string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://"+alice+path, nil)
	r.Header.Set("Accept", "text/markdown")
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	if enc != "" {
		r.Header.Set("Accept-Encoding", enc)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func putDoc(s *Server, path, tok, ifMatch, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("PUT", "http://"+alice+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "text/markdown; charset=utf-8")
	if tok != "" {
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	if ifMatch != "" {
		r.Header.Set("If-Match", ifMatch)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// An editor reads a post's own address and saves to it: Allow says who may, the ETag guards the save.
func TestAPostIsEditedAtItsOwnAddress(t *testing.T) {
	s, own := setup(t)
	s.cfg.OAuth = true
	if _, err := s.st.AddUser("bob", "Bob", ""); err != nil {
		t.Fatal(err)
	}
	mk := func(name, scope, user string) string {
		c, _ := s.st.AddOAuthClient(store.OAuthClient{Name: name, RedirectURIs: []string{"https://app.example/cb"}})
		return tokenWith(t, s, c, user, scope)
	}
	writer, reader, other := mk("w", "read post", "alice"), mk("r", "read", "alice"), mk("o", "read post", "bob")

	w := doWith(s, "POST", alice, "/api/posts", own, "text/markdown", "", "---\ntitle: Notes\nkeywords: [a, b]\n---\nHello ![p](/media/abc.jpg)\n")
	var made postResp
	json.Unmarshal(w.Body.Bytes(), &made)
	path := pathOf(made.URL)

	// signed out, a reader without the post scope, and another account's token: read only
	for name, tok := range map[string]string{"signed out": "", "read scope": reader, "other account": other} {
		w := getDoc(s, path, tok, "")
		if w.Code != 200 || w.Header().Get("Allow") != "GET, HEAD" || w.Header().Get("Link") != "" || w.Header().Get("ETag") == "" {
			t.Errorf("%s: %d %v", name, w.Code, w.Header())
		}
	}
	// the author's tokens: PUT is allowed, the tag is strong, and the document carries the frontmatter
	for name, tok := range map[string]string{"oauth": writer, "account token": own} {
		w := getDoc(s, path, tok, "gzip")
		et := w.Header().Get("ETag")
		if w.Code != 200 || w.Header().Get("Allow") != "GET, HEAD, PUT" || w.Header().Get("Link") != "<"+path+`>; rel="edit"` ||
			et == "" || strings.HasPrefix(et, "W/") || w.Header().Get("Content-Encoding") != "" || !strings.Contains(strings.Join(w.Header().Values("Vary"), ","), "Authorization") {
			t.Errorf("%s: %d %v", name, w.Code, w.Header())
		}
		if !strings.HasPrefix(w.Body.String(), "---\nid: ") || !strings.Contains(w.Body.String(), "title: Notes") || !strings.Contains(w.Body.String(), "](http://"+alice+"/media/abc.jpg)") {
			t.Errorf("%s: document %q", name, w.Body)
		}
	}

	first := getDoc(s, path, writer, "")
	et := first.Header().Get("ETag")
	// saving the document back keeps the title and keywords, stores paths for the pictures, and changes the tag
	edited := strings.Replace(first.Body.String(), "Hello", "Hello again", 1)
	if w := putDoc(s, path, writer, et, edited); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	var got postJSON
	json.Unmarshal(doWith(s, "GET", alice, "/api/posts/"+itoa(made.ID), own, "", "", "").Body.Bytes(), &got)
	if got.Title != "Notes" || len(got.Keywords) != 2 || got.Markdown != "Hello again ![p](/media/abc.jpg)\n" {
		t.Errorf("stored: %+v", got)
	}
	second := getDoc(s, path, writer, "")
	if !strings.Contains(second.Body.String(), "Hello again") || second.Header().Get("ETag") == et {
		t.Errorf("after the save: %q %q", second.Header().Get("ETag"), second.Body)
	}
	// the old tag no longer matches; neither does a made-up one; the new one and none at all do
	for _, tag := range []string{et, `"nope"`, "W/" + second.Header().Get("ETag")} {
		if w := putDoc(s, path, writer, tag, edited); w.Code != 412 {
			t.Errorf("If-Match %s: %d", tag, w.Code)
		}
	}
	if w := putDoc(s, path, writer, second.Header().Get("ETag")+`, "x"`, edited); w.Code != 200 {
		t.Errorf("a list with the right tag: %d", w.Code)
	}
	if w := putDoc(s, path, own, "", edited); w.Code != 200 {
		t.Errorf("the account's own token without If-Match: %d", w.Code)
	}
	// not allowed: no token, a token that may only read, another account's token; nothing is redirected
	if w := putDoc(s, path, "", "", edited); w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "resource_metadata") {
		t.Errorf("no token: %d %v", w.Code, w.Header())
	}
	if w := putDoc(s, path, reader, "", edited); w.Code != 403 || !strings.Contains(w.Header().Get("WWW-Authenticate"), `scope="post"`) {
		t.Errorf("read scope: %d %v", w.Code, w.Header())
	}
	if w := putDoc(s, path, other, "", edited); w.Code != 401 {
		t.Errorf("other account: %d", w.Code)
	}
	if w := putDoc(s, "/posts/nothing.zzzz", writer, "", edited); w.Code != 404 {
		t.Errorf("unknown post: %d", w.Code)
	}
	// with OAuth off an app's token is nothing, and the page says read only
	s.cfg.OAuth = false
	if w := getDoc(s, path, writer, ""); w.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("oauth off: %v", w.Header())
	}
	if w := putDoc(s, path, writer, "", edited); w.Code != 401 {
		t.Errorf("oauth off, put: %d", w.Code)
	}
}

// The page a person reads is not affected by any of this.
func TestThePostPageStaysHTML(t *testing.T) {
	s, own := setup(t)
	w := doWith(s, "POST", alice, "/api/posts", own, "application/json", "", `{"title":"T","markdown":"Body"}`)
	var made postResp
	json.Unmarshal(w.Body.Bytes(), &made)
	r := httptest.NewRequest("GET", "http://"+alice+pathOf(made.URL), nil)
	r.Header.Set("Accept", "text/html")
	r.Header.Set("Authorization", "Bearer "+own)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || rec.Header().Get("Allow") != "" || strings.Contains(rec.Body.String(), "id: ") {
		t.Errorf("%d %v", rec.Code, rec.Header())
	}
}
