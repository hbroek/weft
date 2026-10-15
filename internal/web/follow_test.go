package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbroek/text-thread-casting/internal/store"
)

// twoUsers starts a real listener so the server can fetch its own users' feeds,
// as it would fetch a remote instance's.
func twoUsers(t *testing.T) (s *Server, port string, aliceTok, bobTok string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s = New(Config{BaseDomain: "localhost", AllowPrivate: true}, st)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	port = ts.URL[strings.LastIndex(ts.URL, ":")+1:]
	if aliceTok, err = st.AddUser("alice", "Alice", ""); err != nil {
		t.Fatal(err)
	}
	if bobTok, err = st.AddUser("bob", "Bob", ""); err != nil {
		t.Fatal(err)
	}
	return
}

func TestFollowAndTimeline(t *testing.T) {
	s, port, aliceTok, bobTok := twoUsers(t)
	alice, bob := "alice.localhost:"+port, "bob.localhost:"+port

	do(s, "POST", bob, "/api/posts", bobTok, `{"markdown":"bob's *note* <img src=x onerror=alert(1)>"}`)
	do(s, "POST", bob, "/api/posts", bobTok, `{"title":"Bob essay","markdown":"[click](javascript:alert(1)) text"}`)

	// Alice follows by site address; the feed is discovered from the page
	w := do(s, "POST", alice, "/api/following", aliceTok, `{"url":"http://`+bob+`/"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "http://"+bob+"/feed.xml") {
		t.Fatalf("follow: %d %s", w.Code, w.Body)
	}
	// following twice is a no-op
	do(s, "POST", alice, "/api/following", aliceTok, `{"url":"http://`+bob+`/feed.xml"}`)
	if w := do(s, "GET", alice, "/api/following", aliceTok, ""); strings.Count(w.Body.String(), "feed.xml") != 1 {
		t.Fatalf("following: %s", w.Body)
	}

	w = do(s, "GET", alice, "/api/timeline", aliceTok, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"feed_title":"Bob"`) || !strings.Contains(w.Body.String(), "Bob essay") {
		t.Fatalf("timeline: %d %s", w.Code, w.Body)
	}
	// bob has no follows, so his timeline is empty
	if w := do(s, "GET", bob, "/api/timeline", bobTok, ""); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("bob timeline: %s", w.Body)
	}

	// public follow graph
	opml := do(s, "GET", alice, "/following.opml", "", "").Body.String()
	if !strings.Contains(opml, `xmlUrl="http://`+bob+`/feed.xml"`) || !strings.Contains(opml, `text="Bob"`) {
		t.Errorf("opml: %s", opml)
	}
	if feed := do(s, "GET", alice, "/feed.xml", "", "").Body.String(); !strings.Contains(feed, "<tc:following>http://"+alice+"/following.opml") {
		t.Errorf("tc:following missing from feed")
	}

	// HTML timeline: remote markup is neutralised
	s.st.SetPassword("alice", "correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", map[string][]string{"password": {"correct horse"}}, ""))
	page := do3(s, "GET", alice, "/timeline", ck).Body.String()
	for _, bad := range []string{"<img", "onerror", "javascript:", "<script"} {
		if strings.Contains(page, bad) {
			t.Errorf("timeline page contains %q:\n%s", bad, page)
		}
	}
	if !strings.Contains(page, "<em>note</em>") || !strings.Contains(page, "Bob essay") {
		t.Errorf("timeline page missing content:\n%s", page)
	}

	// edits and deletes at the source show up after a refresh
	do(s, "DELETE", bob, "/api/posts/2", bobTok, "")
	s.RefreshAll(t.Context())
	if w := do(s, "GET", alice, "/api/timeline", aliceTok, ""); strings.Contains(w.Body.String(), "Bob essay") {
		t.Errorf("deleted post still in timeline")
	}

	// unfollow
	do(s, "DELETE", alice, "/api/following", aliceTok, `{"url":"http://`+bob+`/feed.xml"}`)
	if w := do(s, "GET", alice, "/api/timeline", aliceTok, ""); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("after unfollow: %s", w.Body)
	}
}

func do3(s *Server, method, host, path, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+host+path, nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestFollowRefusesPrivateByDefault(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer st.Close()
	s := New(Config{BaseDomain: "localhost"}, st) // AllowPrivate off
	ts := httptest.NewServer(s)
	defer ts.Close()
	port := ts.URL[strings.LastIndex(ts.URL, ":")+1:]
	tok, _ := st.AddUser("alice", "", "")
	for _, target := range []string{"http://alice.localhost:" + port + "/", "http://127.0.0.1:" + port + "/", "http://169.254.169.254/"} {
		w := do(s, "POST", "alice.localhost:"+port, "/api/following", tok, `{"url":"`+target+`"}`)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "private address") {
			t.Errorf("%s: %d %s", target, w.Code, w.Body)
		}
	}
	if w := do(s, "POST", "alice.localhost:"+port, "/api/following", tok, `{"url":"file:///etc/passwd"}`); w.Code != 400 {
		t.Errorf("file url: %d", w.Code)
	}
}

// On one instance, a follower sees a new post at once; no waiting for the poll.
func TestLocalFollowerSeesPostImmediately(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(Config{BaseDomain: "localhost", AllowPrivate: true}, st)
	ts := httptest.NewServer(s)
	defer ts.Close()
	port := ts.URL[strings.LastIndex(ts.URL, ":")+1:]
	atok, _ := st.AddUser("alice", "Alice", "")
	btok, _ := st.AddUser("bob", "Bob", "")
	ah, bh := "alice.localhost:"+port, "bob.localhost:"+port

	if w := do(s, "POST", bh, "/api/following", btok, `{"url":"http://`+ah+`/"}`); w.Code != 200 {
		t.Fatalf("follow: %d %s", w.Code, w.Body)
	}
	// do() calls the handler directly, so make the post over HTTP like a real client
	req, _ := http.NewRequest("POST", ts.URL+"/api/posts", strings.NewReader(`{"markdown":"fresh news"}`))
	req.Host = ah
	req.Header.Set("Authorization", "Bearer "+atok)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 201 {
		t.Fatalf("post: %v %v", err, res)
	}
	res.Body.Close()

	w := do(s, "GET", bh, "/api/timeline", btok, "")
	if !strings.Contains(w.Body.String(), "fresh news") {
		t.Fatalf("bob's timeline should show alice's new post right away:\n%s", w.Body)
	}
}
