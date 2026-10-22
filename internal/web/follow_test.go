package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/store"
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
	if feed := do(s, "GET", alice, "/feed.xml", "", "").Body.String(); !strings.Contains(feed, "<weft:following>http://"+alice+"/following.opml") {
		t.Errorf("weft:following missing from feed")
	}

	// HTML timeline: remote markup is neutralised
	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", map[string][]string{"password": {"correct horse"}}, ""))
	page := do3(s, "GET", alice, "/timeline", ck).Body.String()
	for _, bad := range []string{"<img src=x", "onerror", "javascript:", "<script>alert"} {
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

// A poll interval of zero (or less) turns the background refresh off instead of crashing the ticker.
func TestPollerWithoutInterval(t *testing.T) {
	for _, every := range []time.Duration{0, -time.Second} {
		i := newInstance(t, "alice")
		ctx, cancel := context.WithCancel(context.Background())
		i.s.StartPoller(ctx, every)
		time.Sleep(50 * time.Millisecond) // a bad interval panics in the goroutine right away
		cancel()
	}
}

// Following a feed that declares ISO-8859-1 and is RSS 1.0 (Slashdot's) works end to end.
func TestFollowLatin1RSS1Feed(t *testing.T) {
	doc := []byte("<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?>\n" + `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/" xmlns:dc="http://purl.org/dc/elements/1.1/">
<channel rdf:about="https://old.example/"><title>Old news</title><link>https://old.example/</link><description>d</description></channel>
<item rdf:about="https://old.example/1"><title>Caf` + "\xe9" + ` news</title><link>https://old.example/1</link><description>Body</description><dc:date>2026-10-21T14:10:00+00:00</dc:date></item>
</rdf:RDF>`)
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write(doc)
	}))
	defer old.Close()
	alice := newInstance(t, "alice")
	alice.call(t, "POST", "/api/following", `{"url":"`+old.URL+`/rss"}`, 200)
	if tl := alice.call(t, "GET", "/api/timeline", "", 200); !strings.Contains(tl, "Café news") || !strings.Contains(tl, `"feed_title":"Old news"`) {
		t.Errorf("timeline: %s", tl)
	}
}

// Following an Atom feed works, by its own address and from a page that only advertises it.
func TestFollowAtomFeed(t *testing.T) {
	atom := `<?xml version="1.0" encoding="utf-8"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Atom blog</title><link href="https://atom.example/"/>
<entry><title>First atom post</title><id>tag:atom.example,2026:1</id><link href="https://atom.example/1"/><published>2026-10-21T14:10:00Z</published><content type="html">&lt;p&gt;Hello &lt;b&gt;atom&lt;/b&gt;&lt;/p&gt;</content></entry></feed>`
	mux := http.NewServeMux()
	mux.HandleFunc("/atom.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		w.Write([]byte(atom))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><link rel="alternate" type="application/atom+xml" href="/atom.xml"></head><body>blog</body></html>`))
	})
	site := httptest.NewServer(mux)
	defer site.Close()
	alice := newInstance(t, "alice")
	for _, target := range []string{site.URL + "/atom.xml", site.URL + "/"} {
		out := alice.call(t, "POST", "/api/following", `{"url":"`+target+`"}`, 200)
		if !strings.Contains(out, site.URL+"/atom.xml") {
			t.Errorf("following %s: %s", target, out)
		}
	}
	tl := alice.call(t, "GET", "/api/timeline", "", 200)
	if !strings.Contains(tl, "First atom post") || !strings.Contains(tl, `"feed_title":"Atom blog"`) || !strings.Contains(tl, "Hello atom") {
		t.Errorf("timeline: %s", tl)
	}
	if strings.Count(alice.call(t, "GET", "/api/following", "", 200), "atom.xml") != 1 {
		t.Errorf("both addresses are the same feed")
	}
}

// A reply in the timeline says whose post it answers and shows the first lines of it, from what this server
// already has: the reader's own post, another user of the server, or a followed feed.
func TestReplyHeaderNamesTheParent(t *testing.T) {
	s, port, aliceTok, bobTok := twoUsers(t)
	alice, bob := "alice.localhost:"+port, "bob.localhost:"+port
	urlOf := func(body string) string {
		i := strings.Index(body, `"url":"`)
		if i < 0 {
			t.Fatalf("no url in %s", body)
		}
		rest := body[i+len(`"url":"`):]
		return rest[:strings.Index(rest, `"`)]
	}
	ua := urlOf(do(s, "POST", alice, "/api/posts", aliceTok, `{"title":"Pickleball at the Rally House","markdown":"Played **three** sets, see [the club](http://club.example)."}`).Body.String())
	ub := urlOf(do(s, "POST", bob, "/api/posts", bobTok, `{"title":"Bob on trains","markdown":"Late again."}`).Body.String())
	do(s, "POST", bob, "/api/posts", bobTok, `{"markdown":"Count me in.","in_reply_to":"`+ua+`"}`)
	do(s, "POST", alice, "/api/posts", aliceTok, `{"markdown":"Same here.","in_reply_to":"`+ub+`"}`)
	do(s, "POST", alice, "/api/posts", aliceTok, `{"markdown":"Interesting.","in_reply_to":"http://elsewhere.example/a/b"}`)
	do(s, "POST", alice, "/api/following", aliceTok, `{"url":"http://`+bob+`/"}`)

	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", map[string][]string{"password": {"correct horse"}}, ""))
	page := do3(s, "GET", alice, "/timeline", ck).Body.String()
	for _, want := range []string{
		`replying to <a href="` + ua + `">your post</a>`, // bob answering alice
		"Pickleball at the Rally House",                  // the card
		"Played three sets, see the club.",
		`replying to <a href="` + ub + `">Bob’s post</a>`, // alice answering bob
		"Bob on trains",
		`replying to <a href="http://elsewhere.example/a/b">elsewhere.example</a>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("timeline lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "replying to <a href=\""+ua+"\">a post") {
		t.Errorf("still says \"a post\"")
	}
}

// The card of a post without a title is named by its first words, the alt text of its first image, or "a
// photo"; its first image is the thumbnail; a post with nothing to show gets no card.
func TestReplyCardFallbacks(t *testing.T) {
	s, port, aliceTok, bobTok := twoUsers(t)
	alice, bob := "alice.localhost:"+port, "bob.localhost:"+port
	urlOf := func(body string) string {
		rest := body[strings.Index(body, `"url":"`)+len(`"url":"`):]
		return rest[:strings.Index(rest, `"`)]
	}
	cases := []struct {
		name, post string
		want       []string
	}{
		{"words", `{"markdown":"First words of an untitled post."}`, []string{`<strong>First words of an untitled post.</strong>`}},
		{"alt", `{"markdown":"![Pickleball court](http://img.example/c.jpg)"}`, []string{`<strong>Pickleball court</strong>`, `src="http://img.example/c.jpg"`}},
		{"photo", `{"markdown":"![](http://img.example/d.jpg)"}`, []string{`<strong>a photo</strong>`, `src="http://img.example/d.jpg"`}},
		{"titled", `{"title":"Titled","markdown":"Some *words* ![x](http://img.example/e.jpg)"}`, []string{`<strong>Titled</strong>`, `Some words`, `src="http://img.example/e.jpg"`}},
	}
	for _, c := range cases {
		u := urlOf(do(s, "POST", alice, "/api/posts", aliceTok, c.post).Body.String())
		do(s, "POST", bob, "/api/posts", bobTok, `{"markdown":"re `+c.name+`","in_reply_to":"`+u+`"}`)
	}
	do(s, "POST", alice, "/api/following", aliceTok, `{"url":"http://`+bob+`/"}`)
	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", map[string][]string{"password": {"correct horse"}}, ""))
	page := do3(s, "GET", alice, "/timeline", ck).Body.String()
	for _, c := range cases {
		for _, w := range c.want {
			if !strings.Contains(page, w) {
				t.Errorf("%s: timeline lacks %q:\n%s", c.name, w, page)
			}
		}
	}
	if strings.Contains(page, "![") || strings.Contains(page, "<strong>alice.localhost") {
		t.Errorf("a card shows raw Markdown or the host as its title")
	}
}
