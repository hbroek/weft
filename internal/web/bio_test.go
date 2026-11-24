package web

import (
	"net/url"
	"strings"
	"testing"
)

// A bio is Markdown, clamped to two lines on the home page with a More that a script shows when the text is longer;
// the field is a larger text area.
func TestBioIsMarkdownWithAMoreForTheRest(t *testing.T) {
	s, _ := setup(t)
	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))

	if page := do3(s, "GET", host, "/profile", ck).Body.String(); !strings.Contains(page, `<textarea name="bio"`) || strings.Contains(page, `<input type="text" name="bio"`) {
		t.Errorf("the bio is not a text area:\n%s", page)
	}
	long := "I write about *the open web*.\nFind me on [Mastodon](https://example.social/@a).\r\n\r\nA third line, **bold**.\r\nAnd a fourth [x](javascript:alert(1)) <script>bad()</script>"
	if w := form(s, "POST", "/profile", ck, url.Values{"display": {"Alice"}, "bio": {long}, "avatar": {""}}, ""); w.Code != 303 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	page := do(s, "GET", host, "/", "", "").Body.String()
	i := strings.Index(page, `<div class="bio-text" id="bio-text">`)
	if i < 0 {
		t.Fatalf("no bio on the page:\n%s", page)
	}
	bio := page[i:]
	bio = bio[:strings.Index(bio, `<button`)]
	for _, want := range []string{"<em>the open web</em>", `<a href="https://example.social/@a">Mastodon</a>`, "<strong>bold</strong>", "And a fourth"} {
		if !strings.Contains(bio, want) {
			t.Errorf("the bio lacks %q:\n%s", want, bio)
		}
	}
	if strings.Contains(bio, "javascript:") || strings.Contains(bio, "<script") {
		t.Errorf("unsafe markup in the bio:\n%s", bio)
	}
	// the More is there for the script to show; the page works without it
	if !strings.Contains(page, `id="bio-more"`) || !strings.Contains(page, ` hidden>More</button>`) || !strings.Contains(page, "getElementById('bio-text')") {
		t.Errorf("the More toggle is missing:\n%s", page)
	}
	form(s, "POST", "/profile", ck, url.Values{"display": {"Alice"}, "bio": {""}, "avatar": {""}}, "")
	if page := do(s, "GET", host, "/", "", "").Body.String(); strings.Contains(page, "bio-text") {
		t.Errorf("an empty bio leaves a box:\n%s", page)
	}

	// the meta description, the feed's description and the Markdown page
	form(s, "POST", "/profile", ck, url.Values{"display": {"Alice"}, "bio": {"I write about *the open web*.\n[Mastodon](https://example.social/@a)"}, "avatar": {""}}, "")
	home := do(s, "GET", host, "/", "", "").Body.String()
	if !strings.Contains(home, `content="I write about the open web. Mastodon"`) {
		t.Errorf("the meta description is not plain text:\n%s", home)
	}
	if x := do(s, "GET", host, "/feed.xml", "", "").Body.String(); !strings.Contains(x, "<description>I write about the open web. Mastodon</description>") {
		t.Errorf("the feed description is not plain text:\n%s", x)
	}
	if md := doWith(s, "GET", host, "/", "", "", "text/markdown", "").Body.String(); !strings.Contains(md, "I write about *the open web*.\n[Mastodon](https://example.social/@a)") {
		t.Errorf("the Markdown page does not carry the bio as written:\n%s", md)
	}

	// 2,000 characters at most, however the lines end
	if w := form(s, "POST", "/profile", ck, url.Values{"display": {"Alice"}, "bio": {strings.Repeat("a", 2001)}, "avatar": {""}}, ""); w.Code != 400 || !strings.Contains(w.Body.String(), "too long") {
		t.Errorf("a bio of 2001: %d", w.Code)
	}
	if w := form(s, "POST", "/profile", ck, url.Values{"display": {"Alice"}, "bio": {strings.Repeat("a\r\n", 666) + "ab"}, "avatar": {""}}, ""); w.Code != 303 {
		t.Errorf("a bio of 1999 with CRLF line ends: %d", w.Code)
	}
}
