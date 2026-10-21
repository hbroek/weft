package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/feed"
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
	if w.Code != 303 || !regexp.MustCompile(`^/posts/first-post\.[a-z0-9]{4}$`).MatchString(loc) {
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
	if !strings.Contains(feed, "<weft:updated>") || !strings.Contains(feed, "<title>Now titled</title>") || strings.Contains(feed, "sneaky") {
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

func TestTimelineCanShowOneFeed(t *testing.T) {
	s, _ := setup(t)
	u, _ := s.st.UserByName("alice")
	for _, n := range []string{"a", "b"} {
		url := "http://" + n + ".example/feed.xml"
		if err := s.st.Follow(u.ID, url); err != nil {
			t.Fatal(err)
		}
		xml := `<rss version="2.0"><channel><title>Feed ` + n + `</title><item><title>Post from ` + n + `</title><link>http://` + n + `.example/p</link><guid>http://` + n + `.example/p</guid><description>hello</description></item></channel></rss>`
		p, err := feed.Parse([]byte(xml), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.st.SaveFeed(url, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.st.SetPassword("alice", "correct horse"); err != nil {
		t.Fatal(err)
	}
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))
	one := form(s, "GET", "/timeline?feed="+url.QueryEscape("http://a.example/feed.xml"), ck, nil, "").Body.String()
	if !strings.Contains(one, "Post from a") || strings.Contains(one, "Post from b") || !strings.Contains(one, "<h2>Feed a</h2>") {
		t.Errorf("single-feed view wrong:\n%s", one)
	}
	all := form(s, "GET", "/timeline", ck, nil, "").Body.String()
	if !strings.Contains(all, "Post from a") || !strings.Contains(all, "Post from b") {
		t.Errorf("full timeline lost items")
	}
	fol := form(s, "GET", "/following", ck, nil, "").Body.String()
	if !strings.Contains(fol, `href="/timeline?feed=http%3A%2F%2Fa.example%2Ffeed.xml"`) {
		t.Errorf("following page does not link to the feed view:\n%s", fol)
	}
}

// A post that is not a reply and has no replies has no thread to open, so it gets no Thread link.
func TestThreadLinkOnlyWhereThereIsAThread(t *testing.T) {
	s, _ := setup(t)
	u, _ := s.st.UserByName("alice")
	const fu = "http://a.example/feed.xml"
	if err := s.st.Follow(u.ID, fu); err != nil {
		t.Fatal(err)
	}
	item := func(n, extra string) string {
		return `<item><title>` + n + `</title><link>http://a.example/` + n + `</link><guid>http://a.example/` + n + `</guid><description>x</description>` + extra + `</item>`
	}
	xml := `<rss version="2.0" xmlns:weft="https://weftseed.com/ns/weft/0.1"><channel><title>A</title>` +
		item("lonely", `<weft:replies href="http://a.example/lonely/replies.xml" count="0"/>`) +
		item("popular", `<weft:replies href="http://a.example/popular/replies.xml" count="2"/>`) +
		item("answer", `<weft:inReplyTo href="http://a.example/popular"/><weft:replies href="http://a.example/answer/replies.xml" count="0"/>`) +
		item("unknown", ``) + `</channel></rss>`
	p, err := feed.Parse([]byte(xml), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.SaveFeed(fu, p); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetPassword("alice", "correct horse"); err != nil {
		t.Fatal(err)
	}
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))
	page := form(s, "GET", "/timeline", ck, nil, "").Body.String()
	for name, want := range map[string]bool{"lonely": false, "popular": true, "answer": true, "unknown": true} {
		got := strings.Contains(page, "/thread?url="+url.QueryEscape("http://a.example/"+name)+"#focus")
		if got != want {
			t.Errorf("%s: Thread link present=%v, want %v", name, got, want)
		}
	}
}

// The editor shows a preview under the Publish button, rendered by the same Markdown renderer that
// publishes, and a /preview endpoint feeds it as you type.
func TestEditorPreview(t *testing.T) {
	s, _ := setup(t)
	if err := s.st.SetPassword("alice", "correct horse"); err != nil {
		t.Fatal(err)
	}
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))

	page := do3(s, "GET", host, "/compose", ck).Body.String()
	i, j := strings.Index(page, "<button>Publish</button>"), strings.Index(page, `id="preview-body"`)
	if i < 0 || j < i || !strings.Contains(page, "Nothing to preview yet.") {
		t.Errorf("preview must come after the Publish button and start empty:\n%s", page)
	}

	pv := form(s, "POST", "/preview", ck, url.Values{"markdown": {"first *post* with <script>alert(1)</script>"}}, "")
	if pv.Code != 200 || !strings.Contains(pv.Body.String(), "<em>post</em>") || strings.Contains(pv.Body.String(), "<script>") {
		t.Errorf("preview: %d %s", pv.Code, pv.Body)
	}
	if pv.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("preview type: %s", pv.Header().Get("Content-Type"))
	}
	if w := form(s, "POST", "/preview", "", url.Values{"markdown": {"x"}}, ""); w.Code == 200 {
		t.Errorf("logged-out preview must not render: %d", w.Code)
	}
	if w := form(s, "POST", "/preview", ck, url.Values{"markdown": {"x"}}, "http://evil.example"); w.Code != 403 {
		t.Errorf("cross-origin preview: %d", w.Code)
	}

	// editing an existing post starts with its preview already rendered
	loc := form(s, "POST", "/compose", ck, url.Values{"markdown": {"a *draft* post"}}, "").Header().Get("Location")
	if p := do3(s, "GET", host, loc+"/edit", ck).Body.String(); !strings.Contains(p, "<em>draft</em>") {
		t.Errorf("edit page lacks the preview:\n%s", p)
	}
}

// Keywords are typed in a box under the Markdown, published as <category> and shown as chips.
func TestKeywordsEndToEnd(t *testing.T) {
	s, tok := setup(t)
	if err := s.st.SetPassword("alice", "correct horse"); err != nil {
		t.Fatal(err)
	}
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))

	page := do3(s, "GET", host, "/compose", ck).Body.String()
	i, j := strings.Index(page, `name="markdown"`), strings.Index(page, `name="keywords"`)
	if i < 0 || j < i || !strings.Contains(page, `id="preview-kw"`) {
		t.Fatalf("the keywords box belongs under the Markdown:\n%s", page)
	}

	w := form(s, "POST", "/compose", ck, url.Values{"markdown": {"Heading out for pickleball"}, "keywords": {"pickleball, open web, , Pickleball"}}, "")
	loc := w.Header().Get("Location")
	if w.Code != 303 {
		t.Fatalf("compose: %d", w.Code)
	}
	feedXML := do(s, "GET", host, "/feed.xml", "", "").Body.String()
	if !strings.Contains(feedXML, "<category>pickleball</category>") || !strings.Contains(feedXML, "<category>open web</category>") || strings.Count(feedXML, "<category>") != 2 {
		t.Errorf("feed categories:\n%s", feedXML)
	}
	if pg := do(s, "GET", host, loc, "", "").Body.String(); !strings.Contains(pg, `<span class="p-category">open web</span>`) {
		t.Errorf("chips missing on the post page:\n%s", pg)
	}

	// editing shows them in the box and replaces them
	edit := do3(s, "GET", host, loc+"/edit", ck).Body.String()
	if !strings.Contains(edit, `value="pickleball, open web"`) {
		t.Errorf("edit form lacks the keywords:\n%s", edit)
	}
	form(s, "POST", loc+"/edit", ck, url.Values{"markdown": {"Heading out"}, "keywords": {"tennis"}}, "")
	if f := do(s, "GET", host, "/feed.xml", "", "").Body.String(); !strings.Contains(f, "<category>tennis</category>") || strings.Contains(f, "<category>pickleball</category>") {
		t.Errorf("edited keywords:\n%s", f)
	}

	// the API takes a list
	out := do(s, "POST", host, "/api/posts", tok, `{"markdown":"api post","keywords":["a b","c","A B"]}`)
	if out.Code != 201 {
		t.Fatalf("api: %d %s", out.Code, out.Body)
	}
	if f := do(s, "GET", host, "/feed.xml", "", "").Body.String(); strings.Count(f, "<category>a b</category>") != 1 || !strings.Contains(f, "<category>c</category>") {
		t.Errorf("api keywords:\n%s", f)
	}
}

// /compose?title=&body=&keywords= prefills the form; it publishes nothing and escapes what it is given.
func TestComposePrefill(t *testing.T) {
	s, _ := setup(t)
	s.st.SetPassword("alice", "correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))

	q := url.Values{"title": {"Hello \"world\""}, "body": {"Some *text* <script>x</script>"}, "keywords": {"go, open web"}}
	w := do3(s, "GET", host, "/compose?"+q.Encode(), ck)
	page := w.Body.String()
	for _, want := range []string{`value="Hello &#34;world&#34;"`, "Some *text* &lt;script&gt;x&lt;/script&gt;", `value="go, open web"`, "<em>text</em>"} {
		if !strings.Contains(page, want) {
			t.Errorf("prefilled page lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "<script>x") {
		t.Errorf("prefill not escaped")
	}
	if posts, _ := s.st.RecentPosts(1, 10); len(posts) != 0 {
		t.Errorf("prefill must not publish: %d posts", len(posts))
	}
}

// The owner's home page offers the bookmarklet; visitors do not see it. It must be a real
// javascript: link (html/template rewrites unsafe ones) that targets this host's compose form.
func TestBookmarkletOnHome(t *testing.T) {
	s, _ := setup(t)
	s.st.SetPassword("alice", "correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))

	if page := do(s, "GET", host, "/", "", "").Body.String(); strings.Contains(page, "javascript:") {
		t.Errorf("visitors must not see the bookmarklet")
	}
	page := do3(s, "GET", host, "/", ck).Body.String()
	m := regexp.MustCompile(`href="(javascript:[^"]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no javascript: link on the owner's home page:\n%s", page)
	}
	js, err := url.PathUnescape(strings.TrimPrefix(m[1], "javascript:"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"http://` + host + `/compose"`, "og:title", "og:image", "twitter:image", "og:description", "encodeURIComponent"} {
		if !strings.Contains(js, want) {
			t.Errorf("bookmarklet lacks %q:\n%s", want, js)
		}
	}
	if strings.Contains(js, "\n") || strings.Contains(js, "COMPOSE") {
		t.Errorf("bookmarklet should be one line with the address filled in:\n%s", js)
	}
}
