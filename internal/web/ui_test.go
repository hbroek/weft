package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

const host = "alice.localhost:8080"

func form(s *Server, method, path, cookie string, vals url.Values, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(vals.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func cookieOf(w *httptest.ResponseRecorder) string {
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			return c.Value
		}
	}
	return ""
}

func TestHumanFlow(t *testing.T) {
	s, tok := setup(t)
	if err := s.st.SetPassword("alice", "correct horse"); err != nil {
		t.Fatal(err)
	}

	// logged-out visitors are sent to login
	if w := do(s, "GET", host, "/compose", "", ""); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Fatalf("compose logged out: %d", w.Code)
	}
	if w := form(s, "POST", "/login", "", url.Values{"password": {"wrong"}}, ""); w.Code != 401 || cookieOf(w) != "" {
		t.Fatalf("bad login: %d", w.Code)
	}
	// cross-origin form posts are refused
	if w := form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, "http://evil.example"); w.Code != 403 {
		t.Fatalf("cross-origin: %d", w.Code)
	}
	w := form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, "http://"+host)
	ck := cookieOf(w)
	if w.Code != 303 || ck == "" {
		t.Fatalf("login: %d", w.Code)
	}

	// compose
	w = form(s, "POST", "/compose", ck, url.Values{"markdown": {"first *post*"}}, "")
	loc := w.Header().Get("Location")
	if w.Code != 303 || !regexp.MustCompile(`^/posts/first-post-[a-z0-9]{4}$`).MatchString(loc) {
		t.Fatalf("compose: %d %s", w.Code, loc)
	}
	if w := form(s, "POST", "/compose", ck, url.Values{"markdown": {" "}}, ""); w.Code != 400 {
		t.Fatalf("empty compose: %d", w.Code)
	}
	// compose without a session must not publish
	if w := form(s, "POST", "/compose", "", url.Values{"markdown": {"sneaky"}}, ""); w.Code != 303 {
		t.Fatalf("anon compose: %d", w.Code)
	}

	// edit sets the updated stamp and keeps the permalink
	w = form(s, "POST", loc+"/edit", ck, url.Values{"title": {"Now titled"}, "markdown": {"edited"}}, "")
	if g := form(s, "GET", loc+"/edit", ck, nil, ""); g.Code != 200 || !strings.Contains(g.Body.String(), "edited") {
		t.Fatalf("edit form: %d %s %s", g.Code, loc, g.Body.String())
	}
	if w.Code != 303 {
		t.Fatalf("edit: %d", w.Code)
	}
	feed := do(s, "GET", host, "/feed.xml", "", "").Body.String()
	if !strings.Contains(feed, "<tc:updated>") || !strings.Contains(feed, "<title>Now titled</title>") || strings.Contains(feed, "sneaky") {
		t.Fatalf("feed after edit:\n%s", feed)
	}
	// the title changed but the permalink must not: its slug is frozen when the post is made
	if w := do(s, "GET", host, loc, "", ""); w.Code != 200 || strings.Contains(w.Body.String(), "/edit") {
		t.Errorf("permalink after edit: %d, or edit link shown to anonymous visitor", w.Code)
	}
	if w := do(s, "GET", host, "/posts/1", "", ""); w.Code != 404 {
		t.Errorf("the numeric id must not be a second way to the post: %d", w.Code)
	}

	// API edit and delete with the bearer token
	if w := do(s, "PUT", host, "/api/posts/1", "", `{"markdown":"x"}`); w.Code != 401 {
		t.Fatalf("api put no token: %d", w.Code)
	}
	if w := do(s, "PUT", host, "/api/posts/1", tok, `{"markdown":"via api"}`); w.Code != 200 {
		t.Fatalf("api put: %d %s", w.Code, w.Body)
	}
	if w := do(s, "DELETE", host, "/api/posts/1", tok, ""); w.Code != 204 {
		t.Fatalf("api delete: %d", w.Code)
	}
	if w := do(s, "DELETE", host, "/api/posts/1", tok, ""); w.Code != 404 {
		t.Fatalf("double delete: %d", w.Code)
	}
	if w := do(s, "PUT", host, "/api/posts/1", tok, `{"markdown":"zombie"}`); w.Code != 404 {
		t.Fatalf("edit deleted: %d", w.Code)
	}

	// deleted permalink: 410 tombstone, gone from feed, content discarded
	w = do(s, "GET", host, loc, "", "")
	if w.Code != http.StatusGone || !strings.Contains(w.Body.String(), "removed") || strings.Contains(w.Body.String(), "via api") {
		t.Fatalf("tombstone: %d", w.Code)
	}
	if feed := do(s, "GET", host, "/feed.xml", "", "").Body.String(); strings.Contains(feed, loc+"<") {
		t.Errorf("deleted post still in feed")
	}

	// logout invalidates the session
	form(s, "POST", "/logout", ck, nil, "")
	if w := do2(s, "/compose", ck); w.Code != 303 {
		t.Errorf("session survived logout: %d", w.Code)
	}
}

func do2(s *Server, path, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://"+host+path, nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
