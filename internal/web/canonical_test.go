package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbroek/weft/internal/store"
)

const origin = "https://orig.example/the-essay"

// A repost names the page it first appeared at: the form keeps it only while "This is a repost" is checked, the
// post's page and feed point at it, and an original carries nothing.
func TestRepostCarriesACanonicalLink(t *testing.T) {
	s, _ := setup(t)
	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))
	sp := s.space("alice")

	// an original: no field, no tag
	form(s, "POST", "/compose", ck, url.Values{"markdown": {"my own words"}}, "")
	// a repost
	w := form(s, "POST", "/compose", ck, url.Values{"markdown": {"words from elsewhere"}, "repost": {"1"}, "canonical": {"  " + origin + "  "}}, "")
	if w.Code != 303 {
		t.Fatalf("compose: %d\n%s", w.Code, w.Body.String())
	}
	posts, _ := sp.RecentPosts(1, 10)
	var orig, repost = posts[1], posts[0]
	if orig.Canonical != "" || repost.Canonical != origin {
		t.Fatalf("canonical: original %q, repost %q", orig.Canonical, repost.Canonical)
	}

	page := do(s, "GET", host, "/posts/"+repost.Slug, "", "").Body.String()
	for _, want := range []string{`<link rel="canonical" href="` + origin + `">`, `Originally published at <a class="u-repost-of" href="` + origin + `">orig.example</a>`} {
		if !strings.Contains(page, want) {
			t.Errorf("repost page lacks %q", want)
		}
	}
	if p := do(s, "GET", host, "/posts/"+orig.Slug, "", "").Body.String(); strings.Contains(p, `rel="canonical"`) || strings.Contains(p, "Originally published") {
		t.Errorf("an original must not name a canonical address")
	}
	feed := do(s, "GET", host, "/feed.xml", "", "").Body.String()
	if strings.Count(feed, "<weft:canonical") != 1 || !strings.Contains(feed, `<weft:canonical href="`+origin+`">`) {
		t.Errorf("feed:\n%s", feed)
	}

	// the editor opens a repost with the box checked and the address in it; an original with neither
	if e := do3(s, "GET", host, "/posts/"+repost.Slug+"/edit", ck).Body.String(); !strings.Contains(e, `id="repost" name="repost" value="1" checked`) || !strings.Contains(e, `value="`+origin+`"`) {
		t.Errorf("edit form of a repost:\n%s", e)
	}
	if e := do3(s, "GET", host, "/posts/"+orig.Slug+"/edit", ck).Body.String(); strings.Contains(e, `value="1" checked`) {
		t.Errorf("edit form of an original has the repost box checked")
	}

	// a bad address is refused and keeps what was typed
	w = form(s, "POST", "/posts/"+repost.Slug+"/edit", ck, url.Values{"markdown": {"words from elsewhere"}, "repost": {"1"}, "canonical": {"ftp://x.example/a"}}, "")
	if w.Code != 400 || !strings.Contains(w.Body.String(), "must be a web address") {
		t.Errorf("bad address: %d", w.Code)
	}
	if p, _ := sp.Post(1, repost.ID); p.Canonical != origin {
		t.Errorf("a refused edit changed the address: %q", p.Canonical)
	}

	// unchecking the box removes it, even though the text is still in the field
	w = form(s, "POST", "/posts/"+repost.Slug+"/edit", ck, url.Values{"markdown": {"words, now my own"}, "canonical": {origin}}, "")
	if w.Code != 303 {
		t.Fatalf("edit: %d", w.Code)
	}
	if p, _ := sp.Post(1, repost.ID); p.Canonical != "" {
		t.Errorf("unchecked box kept %q", p.Canonical)
	}
}

func TestAPICanonical(t *testing.T) {
	s, tok := setup(t)
	w := do(s, "POST", host, "/api/posts", tok, `{"markdown":"x","canonical":"`+origin+`"}`)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if posts, _ := s.space("alice").RecentPosts(1, 1); posts[0].Canonical != origin {
		t.Errorf("canonical %q", posts[0].Canonical)
	}
	if w := do(s, "POST", host, "/api/posts", tok, `{"markdown":"y","canonical":"not a link"}`); w.Code != 400 {
		t.Errorf("bad canonical accepted: %d", w.Code)
	}
}

// "Weft it" offers Repost: the form starts with the text of the page, and its canonical link is the page's own.
func TestComposeRepostCopiesThePage(t *testing.T) {
	long := strings.Repeat("A sentence that is part of the essay. ", 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/essay", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<html><head><title>The Essay</title><link rel="canonical" href="/essay"></head><body><nav><a href="/">Home</a></nav>
<article><h1>The Essay</h1><p>`+long+`<a href="/more">more</a></p><p><img src="/p.png" alt="pic"></p></article></body></html>`)
	})
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><head><title>App</title></head><body><div id="root"></div></body></html>`)
	})
	mux.HandleFunc("/md", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept"), "text/markdown") {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html><body>html</body></html>")
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		io.WriteString(w, "# Already markdown\n\nWords **here**.\n")
	})
	origin := httptest.NewServer(mux)
	defer origin.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.AddUser("alice", "Alice", "")
	s := New(Config{BaseDomain: "localhost", AllowPrivate: true}, st)
	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))

	compose := func(page string) string {
		return do3(s, "GET", host, "/compose?mode=repost&ptitle=Card&page="+url.QueryEscape(page), ck).Body.String()
	}

	// a page with an article: its text, its title, and its own canonical address (utm tracking dropped)
	page := compose(origin.URL + "/essay?utm_source=x#top")
	line := "&gt; Repost from [" + strings.TrimPrefix(origin.URL, "http://") + "](" + origin.URL + "/essay)\n\nA sentence"
	if !strings.Contains(page, line) {
		t.Errorf("the text does not start with the repost line %q:\n%s", line, page)
	}
	for _, want := range []string{"<strong>Repost</strong>", `value="The Essay"`, "A sentence that is part of the essay.",
		"[more](" + origin.URL + "/more)", "![pic](" + origin.URL + "/p.png)",
		`id="repost" name="repost" value="1" checked`, `name="canonical" value="` + origin.URL + `/essay"`} {
		if !strings.Contains(page, want) {
			t.Errorf("repost form lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "Home") {
		t.Errorf("the page's navigation was copied")
	}

	// a page that answers with Markdown gives it as it is, canonical = the page
	page = compose(origin.URL + "/md")
	if !strings.Contains(page, "Words **here**.") || !strings.Contains(page, `name="canonical" value="`+origin.URL+`/md"`) {
		t.Errorf("markdown page:\n%s", page)
	}

	// a page with no text: a card with the link, a sentence saying so, and the canonical link still set
	page = compose(origin.URL + "/app")
	if !strings.Contains(page, "could not be read") || !strings.Contains(page, `name="canonical" value="`+origin.URL+`/app"`) {
		t.Errorf("empty page:\n%s", page)
	}

	// the other modes are unchanged: no canonical link, no checked box
	page = do3(s, "GET", host, "/compose?mode=bookmark&page="+url.QueryEscape(origin.URL+"/essay"), ck).Body.String()
	if strings.Contains(page, `value="1" checked`) || strings.Contains(page, "Originally") {
		t.Errorf("bookmark mode set a canonical link")
	}
	if !strings.Contains(page, `mode=repost`) {
		t.Errorf("no Repost choice among the modes")
	}
}

func TestUntrackedDropsCampaignParameters(t *testing.T) {
	for in, want := range map[string]string{
		"https://a.example/p?utm_source=x&id=7#frag": "https://a.example/p?id=7",
		"https://a.example/p?fbclid=1":               "https://a.example/p",
		"https://a.example/p":                        "https://a.example/p",
	} {
		if got := untracked(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

// Reposting a Weft post copies its Markdown, and when that post is itself a repost the canonical link is the
// first original, not the repost.
func TestRepostOfAWeftPostFollowsItsCanonicalLink(t *testing.T) {
	s, port, aliceTok, bobTok := twoUsers(t)
	bob := "bob.localhost:" + port
	w := do(s, "POST", bob, "/api/posts", bobTok, `{"title":"Bob copy","markdown":"Copied *words*.","canonical":"https://orig.example/first"}`)
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	posts, _ := s.space("bob").RecentPosts(1, 1)
	postURL := "http://" + bob + "/posts/" + posts[0].Slug

	s.space("alice").SetPassword("correct horse")
	_ = aliceTok
	r := httptest.NewRequest("POST", "http://alice.localhost:"+port+"/login", strings.NewReader(url.Values{"password": {"correct horse"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	ck := cookieOf(rec)
	req := httptest.NewRequest("GET", "http://alice.localhost:"+port+"/compose?mode=repost&page="+url.QueryEscape(postURL), nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: ck})
	out := httptest.NewRecorder()
	s.ServeHTTP(out, req)
	page := out.Body.String()
	for _, want := range []string{"&gt; Repost from [orig.example](https://orig.example/first)\n\nCopied *words*.", `value="Bob copy"`, `name="canonical" value="https://orig.example/first"`} {
		if !strings.Contains(page, want) {
			t.Errorf("lacks %q:\n%s", want, page)
		}
	}
}
