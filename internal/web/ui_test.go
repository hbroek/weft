package web

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/store"
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
	if err := s.space("alice").SetPassword("correct horse"); err != nil {
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
		if err := s.space("alice").Follow(u.ID, url); err != nil {
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
	if err := s.space("alice").SetPassword("correct horse"); err != nil {
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
	if err := s.space("alice").Follow(u.ID, fu); err != nil {
		t.Fatal(err)
	}
	item := func(n, extra string) string {
		return `<item><title>` + n + `</title><link>http://a.example/` + n + `</link><guid>http://a.example/` + n + `</guid><description>x</description>` + extra + `</item>`
	}
	xml := `<rss version="2.0" xmlns:weft="https://weftprotocol.org/ns/weft/0.1"><channel><title>A</title>` +
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
	if err := s.space("alice").SetPassword("correct horse"); err != nil {
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
	if err := s.space("alice").SetPassword("correct horse"); err != nil {
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
	if err := s.space("alice").SetPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))

	page := do3(s, "GET", host, "/compose", ck).Body.String()
	i, j := strings.Index(page, `name="markdown"`), strings.Index(page, `name="keywords"`)
	if i < 0 || j < i || !strings.Contains(page, `id="preview-kw"`) {
		t.Fatalf("the keywords box belongs under the Markdown:\n%s", page)
	}
	if kw := page[j:]; !strings.Contains(kw[:strings.Index(kw, ">")], `autocapitalize="off"`) || !strings.Contains(kw[:strings.Index(kw, ">")], `autocorrect="off"`) || !strings.Contains(kw[:strings.Index(kw, ">")], `spellcheck="false"`) {
		t.Errorf("keywords are tags: the box must not capitalize or correct them")
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
	if pg := do(s, "GET", host, loc, "", "").Body.String(); !strings.Contains(pg, `<a class="p-category" href="/?keyword=open%20web">open web</a>`) {
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
	s.space("alice").SetPassword("correct horse")
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
	if posts, _ := s.space("alice").RecentPosts(1, 10); len(posts) != 0 {
		t.Errorf("prefill must not publish: %d posts", len(posts))
	}
}

// The owner's home page offers the bookmarklet; visitors do not see it. It must be a real
// javascript: link (html/template rewrites unsafe ones) that targets this host's compose form.
func TestBookmarkletOnHome(t *testing.T) {
	s, _ := setup(t)
	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))

	if page := do(s, "GET", host, "/", "", "").Body.String(); strings.Contains(page, "javascript:") {
		t.Errorf("visitors must not see the bookmarklet")
	}
	page := do3(s, "GET", host, "/", ck).Body.String()
	m := regexp.MustCompile(`href="(javascript:[^"]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no javascript: link on the owner's home page:\n%s", page)
	}
	js, err := url.PathUnescape(html.UnescapeString(strings.TrimPrefix(m[1], "javascript:")))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"http://` + host + `/compose"`, "og:title", "og:image", "twitter:image", "og:description", "encodeURIComponent", "webmention", "ptitle", "&wm=", `rel~="alternate"`, `application\/(rss|atom)\+xml`, "&feed=", "&ftitle="} {
		if !strings.Contains(js, want) {
			t.Errorf("bookmarklet lacks %q:\n%s", want, js)
		}
	}
	if strings.Contains(js, "\n") || strings.Contains(js, "COMPOSE") {
		t.Errorf("bookmarklet should be one line with the address filled in:\n%s", js)
	}
}

// The site icons are served from the binary on any host and linked from every page.
func TestSiteIcons(t *testing.T) {
	s, _ := setup(t)
	for path, ctype := range map[string]string{
		"/favicon.ico":                 "image/x-icon",
		"/static/weft-icon.svg":        "image/svg+xml",
		"/static/apple-touch-icon.png": "image/png",
		"/static/weft-icon-512.png":    "image/png",
		"/static/site.webmanifest":     "application/manifest+json",
	} {
		w := do(s, "GET", "nobody.example", path, "", "") // no user lives here: the icons do not need one
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), ctype) || w.Body.Len() == 0 {
			t.Errorf("%s: %d %q (%d bytes)", path, w.Code, w.Header().Get("Content-Type"), w.Body.Len())
		}
	}
	if w := do(s, "GET", host, "/static/nope.png", "", ""); w.Code != 404 {
		t.Errorf("missing icon: %d", w.Code)
	}
	page := do(s, "GET", host, "/", "", "").Body.String()
	for _, want := range []string{`rel="icon" href="/static/weft-icon.svg"`, `rel="icon" href="/favicon.ico"`, `rel="apple-touch-icon"`, `rel="manifest"`, "Published with Weft"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

// Every page has the header: logo on the left; site name, theme button and Log in or Log out on the
// right. The styles carry the palette with light, dark and auto modes.
func TestHeaderAndThemes(t *testing.T) {
	s, _ := setup(t)
	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))

	vis := do(s, "GET", host, "/", "", "").Body.String()
	for _, want := range []string{`<header class="top">`, `class="brand" href="/"`, `<span>Weft</span>`, `<a class="site" href="/">`, `<span>Alice</span></a>`, `id="theme"`, `<a class="btn" href="/login">Log in</a>`, `href="/static/weft.css"`, `localStorage.getItem('weft-theme')`} {
		if !strings.Contains(vis, want) {
			t.Errorf("visitor's header lacks %q", want)
		}
	}
	if strings.Contains(vis, "Log out") || strings.Contains(vis, `class="menu"`) {
		t.Errorf("visitor must not see Log out or the owner's menu")
	}
	own := do3(s, "GET", host, "/", ck).Body.String()
	if !strings.Contains(own, `action="/logout"><button class="btn">Log out</button>`) || !strings.Contains(own, `<nav class="menu"><a href="/compose">Compose</a>`) || strings.Contains(own, ">Log in<") {
		t.Errorf("owner's header:\n%s", own)
	}

	css := do(s, "GET", host, "/static/weft.css", "", "")
	if css.Code != 200 || !strings.HasPrefix(css.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("stylesheet: %d %q", css.Code, css.Header().Get("Content-Type"))
	}
	for _, want := range []string{"#26337a", "#f08a4b", "#eef0fa", "prefers-color-scheme: dark", `:root[data-theme="dark"]`, `:root:not([data-theme="light"])`} {
		if !strings.Contains(css.Body.String(), want) {
			t.Errorf("stylesheet lacks %q", want)
		}
	}
}

// The header menu marks the view you are on.
func TestMenuShowsCurrentView(t *testing.T) {
	s, _ := setup(t)
	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))
	for path, on := range map[string]string{"/compose": "compose", "/timeline": "timeline", "/following": "following", "/replies": "replies", "/": ""} {
		page := do3(s, "GET", host, path, ck).Body.String()
		got := regexp.MustCompile(`<a href="/(\w+)" class="on" aria-current="page">`).FindAllStringSubmatch(page, -1)
		if on == "" && len(got) != 0 || on != "" && (len(got) != 1 || got[0][1] != on) {
			t.Errorf("%s: marked %v, want %q", path, got, on)
		}
	}
}

// A user can set an avatar image address; without one the pages show the first letter of the name.
func TestAvatarAndInitialFallback(t *testing.T) {
	s, _ := setup(t)
	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))

	// only the owner reaches the profile page
	if w := do(s, "GET", host, "/profile", "", ""); w.Code == 200 {
		t.Errorf("a visitor must not see the profile form: %d", w.Code)
	}
	// no avatar yet: the letter, no image
	if page := do(s, "GET", host, "/", "", "").Body.String(); !strings.Contains(page, `<span class="av"><span>A</span></span>`) || strings.Contains(page, `class="av"><img`) {
		t.Errorf("without an avatar the header should show the first letter:\n%s", page)
	}

	// unsafe or malformed addresses are refused
	for _, bad := range []string{"javascript:alert(1)", "data:image/png;base64,AAAA", "me.png", "ftp://x.example/me.png"} {
		w := form(s, "POST", "/profile", ck, url.Values{"display": {"Alice"}, "avatar": {bad}}, "")
		if w.Code != 400 || !strings.Contains(w.Body.String(), "full http:// or https:// image address") {
			t.Errorf("%q: %d", bad, w.Code)
		}
	}
	if u, _ := s.st.UserByName("alice"); u.Avatar != "" {
		t.Errorf("a refused address was saved: %q", u.Avatar)
	}

	// a good one is saved and shown, escaped, with the letter ready as the fallback
	w := form(s, "POST", "/profile", ck, url.Values{"display": {"Alice A"}, "bio": {"hi"}, "avatar": {`https://img.example/a.png?x="y"`}}, "")
	if w.Code != 303 {
		t.Fatalf("save profile: %d %s", w.Code, w.Body)
	}
	page := do(s, "GET", host, "/", "", "").Body.String()
	if !strings.Contains(page, `<img src="https://img.example/a.png?x=%22y%22"`) || !strings.Contains(page, `onerror="this.hidden=true;this.nextElementSibling.hidden=false"><span hidden>A</span>`) || !strings.Contains(page, ">Alice A<") {
		t.Errorf("avatar not shown with its fallback:\n%s", page)
	}
	if feedXML := do(s, "GET", host, "/feed.xml", "", "").Body.String(); !strings.Contains(feedXML, "<url>https://img.example/a.png?x=%22y%22</url>") && !strings.Contains(feedXML, "<url>https://img.example/a.png?x=&#34;y&#34;</url>") {
		t.Errorf("the feed should carry the avatar as its channel image:\n%s", feedXML)
	}
	// clearing it brings the letter back
	form(s, "POST", "/profile", ck, url.Values{"display": {"Alice A"}, "avatar": {""}}, "")
	if page := do(s, "GET", host, "/", "", "").Body.String(); strings.Contains(page, "<img src=\"https://img.example") {
		t.Errorf("a cleared avatar still shows")
	}
}

// Post and like pages name their author with an h-card (name, address and avatar), so a Webmention
// receiver such as a Ghost blog can show who replied instead of falling back to the domain.
func TestPostsHaveAuthorHCard(t *testing.T) {
	s, tok := setup(t)
	do(s, "POST", host, "/api/posts", tok, `{"markdown":"A reply","in_reply_to":"http://other.example/p"}`)
	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))
	form(s, "POST", "/profile", ck, url.Values{"display": {"Alice <A>"}, "avatar": {"https://img.example/a.png"}}, "")
	card := regexp.MustCompile(`(?s)<article class="h-entry">\s*<span class="p-author h-card" hidden><a class="u-url p-name" href="http://alice\.localhost:8080/">Alice &lt;A&gt;</a><img class="u-photo" src="https://img\.example/a\.png"`)

	home := do(s, "GET", host, "/", "", "").Body.String()
	if !card.MatchString(home) {
		t.Errorf("home page entry lacks the author h-card:\n%s", home)
	}
	posts, _ := s.space("alice").RecentPosts(1, 1)
	if pg := do(s, "GET", host, "/posts/"+posts[0].Slug, "", "").Body.String(); !card.MatchString(pg) || !strings.Contains(pg, `class="u-in-reply-to"`) {
		t.Errorf("permalink lacks the author h-card or the reply link:\n%s", pg)
	}
	s.space("alice").SetLike(1, "http://other.example/p", "")
	if lk := do(s, "GET", host, "/likes/1", "", "").Body.String(); !card.MatchString(lk) || !strings.Contains(lk, `u-like-of`) {
		t.Errorf("like page lacks the author h-card:\n%s", lk)
	}
	// without an avatar the card still names the author, with no photo
	form(s, "POST", "/profile", ck, url.Values{"display": {"Alice"}, "avatar": {""}}, "")
	if pg := do(s, "GET", host, "/", "", "").Body.String(); !strings.Contains(pg, `>Alice</a></span>`) || strings.Contains(pg, "u-photo") {
		t.Errorf("author card without an avatar:\n%s", pg)
	}
}

// The owner downloads their account as a zip, which another instance can import: the new server then
// publishes byte for byte the same feed. Others cannot download it, and a form from another site cannot
// trigger it.
func TestExportThenImportElsewhere(t *testing.T) {
	s, tok := setup(t)
	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, "http://"+host))
	for _, md := range []string{"first *post*", "second post"} {
		if w := form(s, "POST", "/compose", ck, url.Values{"markdown": {md}, "keywords": {"go"}}, ""); w.Code != 303 {
			t.Fatalf("compose: %d", w.Code)
		}
	}
	feedBefore := do(s, "GET", host, "/feed.xml", "", "").Body.String()

	if w := form(s, "POST", "/export", "", nil, "http://"+host); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Errorf("export logged out: %d", w.Code)
	}
	if w := form(s, "POST", "/export", ck, nil, "http://evil.example"); w.Code != 403 {
		t.Errorf("cross-origin export: %d", w.Code)
	}
	if w := do(s, "GET", host, "/export", tok, ""); w.Code == 200 {
		t.Errorf("GET /export answered 200; only a form post may download")
	}
	w := form(s, "POST", "/export", ck, nil, "http://"+host)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment; filename=\"alice-") {
		t.Fatalf("export: %d %v", w.Code, w.Header())
	}
	zipPath := filepath.Join(t.TempDir(), "alice.zip")
	os.WriteFile(zipPath, w.Body.Bytes(), 0o600)

	// another instance, with nobody on it
	st2, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })
	name, newTok, err := st2.Import(zipPath)
	if err != nil || name != "alice" {
		t.Fatalf("import: %q %v", name, err)
	}
	s2 := New(Config{BaseDomain: "localhost"}, st2)
	if after := do(s2, "GET", host, "/feed.xml", "", "").Body.String(); after != feedBefore {
		t.Errorf("the imported feed differs from the original:\n--- before\n%s\n--- after\n%s", feedBefore, after)
	}
	if w := do(s2, "GET", host, "/api/replies", tok, ""); w.Code != 401 {
		t.Errorf("the old token still works on the new instance: %d", w.Code)
	}
	if w := do(s2, "GET", host, "/api/replies", newTok, ""); w.Code != 200 {
		t.Errorf("the new token does not work: %d", w.Code)
	}
	if w := form(s2, "POST", "/login", "", url.Values{"password": {"correct horse"}}, "http://"+host); w.Code != 303 {
		t.Errorf("the password did not travel: %d", w.Code)
	}
}

// An address can be chosen when a post is written, in the form and in the API, and never afterwards.
func TestChoosingAnAddress(t *testing.T) {
	s, tok := setup(t)
	if err := s.space("alice").SetPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))

	if p := do3(s, "GET", host, "/compose?title=Pickleball+at+the+Rally+House", ck).Body.String(); !regexp.MustCompile(`name="slug"[^>]*value="pickleball-at-the-rally-house"[^>]*data-auto="1"`).MatchString(p) {
		t.Errorf("the new post form does not suggest an address:\n%s", p)
	}
	// the page the form talks to answers with the words of the address for what is typed so far
	if got := form(s, "POST", "/preview", ck, url.Values{"title": {"Pickleball at the Rally House"}, "markdown": {"x"}}, "").Header().Get("X-Slug-Base"); got != "pickleball-at-the-rally-house" {
		t.Errorf("X-Slug-Base = %q", got)
	}
	if got := form(s, "POST", "/preview", ck, url.Values{"markdown": {"Heading out for pickleball. More later."}}, "").Header().Get("X-Slug-Base"); got != "heading-out-for-pickleball" {
		t.Errorf("X-Slug-Base from the first sentence = %q", got)
	}
	// the words typed are cleaned and the short code is always added
	code := regexp.MustCompile(`^/posts/my-trip-to-lisbon\.[a-z0-9]{4}$`)
	w := form(s, "POST", "/compose", ck, url.Values{"markdown": {"Lisbon was warm."}, "slug": {"My Trip to Lisbon!"}}, "")
	loc := w.Header().Get("Location")
	if w.Code != 303 || !code.MatchString(loc) {
		t.Fatalf("compose with an address: %d %s", w.Code, loc)
	}
	if p := do3(s, "GET", host, loc, ck).Body.String(); !strings.Contains(p, "Lisbon was warm.") {
		t.Errorf("the post is not at its address")
	}
	if feedXML := do(s, "GET", host, "/feed.xml", "", "").Body.String(); !strings.Contains(feedXML, loc+"</link>") {
		t.Errorf("the feed does not link the chosen address:\n%s", feedXML)
	}
	// a dot in the words is a hyphen
	if w = form(s, "POST", "/compose", ck, url.Values{"markdown": {"x"}, "slug": {"v1.2 notes"}}, ""); !strings.HasPrefix(w.Header().Get("Location"), "/posts/v1-2-notes.") {
		t.Errorf("a dot in the address: %s", w.Header().Get("Location"))
	}
	// the same words again: another code, nobody is refused
	w = form(s, "POST", "/compose", ck, url.Values{"markdown": {"Another trip."}, "slug": {"my-trip-to-lisbon"}}, "")
	if loc2 := w.Header().Get("Location"); w.Code != 303 || !code.MatchString(loc2) || loc2 == loc {
		t.Errorf("same words: %d %s (first %s)", w.Code, loc2, loc)
	}
	// an address with nothing usable in it: the form comes back with what was typed
	if w = form(s, "POST", "/compose", ck, url.Values{"markdown": {"Keep this text."}, "slug": {"!!!"}}, ""); w.Code != 400 ||
		!strings.Contains(w.Body.String(), "needs letters or digits") || !strings.Contains(w.Body.String(), "Keep this text.") || !strings.Contains(w.Body.String(), `value="!!!"`) {
		t.Errorf("bad address: %d", w.Code)
	}
	// empty means made from the post, as before
	if w = form(s, "POST", "/compose", ck, url.Values{"markdown": {"Plain note about boats."}}, ""); !strings.HasPrefix(w.Header().Get("Location"), "/posts/plain-note-about-boats.") {
		t.Errorf("no address chosen: %s", w.Header().Get("Location"))
	}

	// the API takes the words too
	w = do(s, "POST", host, "/api/posts", tok, `{"markdown":"By API.","slug":"api-post"}`)
	if w.Code != 201 || !regexp.MustCompile(`/posts/api-post\.[a-z0-9]{4}"`).MatchString(w.Body.String()) {
		t.Errorf("API with a slug: %d %s", w.Code, w.Body)
	}
	if w = do(s, "POST", host, "/api/posts", tok, `{"markdown":"Bad.","slug":"!!!"}`); w.Code != 400 {
		t.Errorf("API with an unusable slug: %d", w.Code)
	}

	// editing offers no address box, and an edit sends none
	if p := do3(s, "GET", host, loc+"/edit", ck).Body.String(); strings.Contains(p, `name="slug"`) {
		t.Errorf("the edit form offers to change the address")
	}
	form(s, "POST", loc+"/edit", ck, url.Values{"markdown": {"Edited."}, "slug": {"somewhere-else"}}, "")
	if w := do3(s, "GET", host, loc, ck); w.Code != 200 || !strings.Contains(w.Body.String(), "Edited.") {
		t.Errorf("an edit moved the post: %d", w.Code)
	}
	if w := do3(s, "GET", host, "/posts/somewhere-else", ck); w.Code != 404 {
		t.Errorf("an edit created an address: %d", w.Code)
	}
}

// The buttons of the profile page's address section sit in a paragraph of their own, so there is space between
// an input and its button.
func TestDomainButtonsHaveSpace(t *testing.T) {
	s, _ := setup(t)
	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))
	page := do3(s, "GET", host, "/profile", ck).Body.String()
	if !strings.Contains(page, `name="domain"`) || !strings.Contains(page, "<p><button>Add</button></p>") {
		t.Errorf("the Add button is not in a paragraph of its own:\n%s", page)
	}
	if w := form(s, "POST", "/domain", ck, url.Values{"domain": {"weft.example.org"}}, ""); w.Code != 200 && w.Code != 303 {
		t.Fatalf("claim: %d", w.Code)
	}
	page = do3(s, "GET", host, "/profile", ck).Body.String()
	for _, want := range []string{"<p><button>Check now</button></p>", "<p><button>Remove this domain</button></p>"} {
		if !strings.Contains(page, want) {
			t.Errorf("profile lacks %q", want)
		}
	}
}

// An account whose password an administrator set must replace it before doing anything else: the sign-in leads
// to the page that changes it, every other page leads back to it, and afterwards the account works as usual.
func TestTemporaryPasswordMustBeChanged(t *testing.T) {
	s, tok := setup(t)
	if err := s.space("alice").SetTemporaryPassword("temporary one"); err != nil {
		t.Fatal(err)
	}
	w := form(s, "POST", "/login", "", url.Values{"password": {"temporary one"}}, "")
	ck := cookieOf(w)
	if w.Code != 303 || w.Header().Get("Location") != "/password" || ck == "" {
		t.Fatalf("sign in with a temporary password: %d %q", w.Code, w.Header().Get("Location"))
	}
	for _, path := range []string{"/", "/compose", "/timeline", "/following", "/profile", "/replies"} {
		if w := do3(s, "GET", host, path, ck); w.Code != 303 || w.Header().Get("Location") != "/password" {
			t.Errorf("GET %s with a temporary password: %d %q", path, w.Code, w.Header().Get("Location"))
		}
	}
	if w := form(s, "POST", "/compose", ck, url.Values{"markdown": {"sneaking past"}}, ""); w.Code != 303 || w.Header().Get("Location") != "/password" {
		t.Errorf("POST /compose: %d %q", w.Code, w.Header().Get("Location"))
	}
	if c := do(s, "GET", host, "/api/timeline", tok, "").Code; c != 200 { // the API has its own token and is not held back
		t.Errorf("the API is held back")
	}
	if w := do(s, "GET", host, "/static/weft.css", "", ""); w.Code != 200 {
		t.Errorf("the stylesheet: %d", w.Code)
	}
	page := do3(s, "GET", host, "/password", ck)
	body := page.Body.String()
	if page.Code != 200 || !strings.Contains(body, "Choose a new password") || !strings.Contains(body, "Temporary password") || strings.Contains(body, `class="menu"`) {
		t.Fatalf("the page that changes it: %d\n%s", page.Code, body)
	}

	post := func(cur, next, again string) *httptest.ResponseRecorder {
		return form(s, "POST", "/password", ck, url.Values{"current": {cur}, "new": {next}, "again": {again}}, "")
	}
	for _, c := range []struct{ cur, next, again, want string }{
		{"not it at all", "a brand new one", "a brand new one", "temporary password is not right"},
		{"temporary one", "a brand new one", "something else", "not the same"},
		{"temporary one", "short", "short", "at least 8 characters"},
		{"temporary one", "temporary one", "temporary one", "different from the old one"},
	} {
		if w := post(c.cur, c.next, c.again); w.Code != 400 || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%+v: %d\n%s", c, w.Code, w.Body)
		}
	}
	if w := do3(s, "GET", host, "/compose", ck); w.Code != 303 {
		t.Errorf("a refused change opened the account: %d", w.Code)
	}
	if w := post("temporary one", "a brand new one", "a brand new one"); w.Code != 303 || w.Header().Get("Location") != "/" {
		t.Fatalf("the change: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := do3(s, "GET", host, "/compose", ck); w.Code != 200 {
		t.Errorf("the account after the change: %d", w.Code)
	}
	if w := form(s, "POST", "/login", "", url.Values{"password": {"temporary one"}}, ""); w.Code != 401 {
		t.Errorf("the temporary password still signs in: %d", w.Code)
	}
	w = form(s, "POST", "/login", "", url.Values{"password": {"a brand new one"}}, "")
	if w.Code != 303 || w.Header().Get("Location") != "/" {
		t.Errorf("sign in with the new password: %d %q", w.Code, w.Header().Get("Location"))
	}

	// anyone can change it later from the profile page, and a visitor cannot
	ck = cookieOf(w)
	if p := do3(s, "GET", host, "/profile", ck).Body.String(); !strings.Contains(p, `href="/password"`) {
		t.Errorf("the profile page does not offer to change the password")
	}
	if p := do3(s, "GET", host, "/password", ck).Body.String(); !strings.Contains(p, "<h2>Change password</h2>") || !strings.Contains(p, "Current password") {
		t.Errorf("the plain form:\n%s", p)
	}
	if w := form(s, "POST", "/password", ck, url.Values{"current": {"a brand new one"}, "new": {"yet another one"}, "again": {"yet another one"}}, ""); w.Code != 303 || w.Header().Get("Location") != "/profile" {
		t.Errorf("a voluntary change: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := do(s, "GET", host, "/password", "", ""); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Errorf("a visitor at /password: %d %q", w.Code, w.Header().Get("Location"))
	}
}
