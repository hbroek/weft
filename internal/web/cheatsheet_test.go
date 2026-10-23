package web

import (
	"html"
	"net/url"
	"strings"
	"testing"

	"github.com/hbroek/weft/internal/md"
)

// The cheatsheet may only say what the renderer does.
func TestCheatsheetShowsWhatTheRendererDoes(t *testing.T) {
	rows := append(append([]cheatRow{}, cheatRowsLeft...), cheatRowsRight...)
	for _, r := range rows {
		h, err := md.Render(r.Sample)
		if err != nil || !strings.Contains(h, r.Want) {
			t.Errorf("%q renders as %q, which lacks %q (%v)", r.Syntax, h, r.Want, err)
		}
	}
	for _, r := range append(append([]cheatRow{}, cheats.Left...), cheats.Right...) {
		if r.Shown == "" || strings.Contains(string(r.Shown), "<img") || strings.Contains(string(r.Shown), "<script") {
			t.Errorf("%q is shown as %q", r.Syntax, r.Shown)
		}
	}
}

// The sheet is on the pages where a post is written or edited, outside the form, after the editor and before the
// preview; the page that only likes something does not have it.
func TestCheatsheetBesideTheEditor(t *testing.T) {
	s, tok := setup(t)
	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))
	resp := do(s, "POST", host, "/api/posts", tok, `{"markdown":"To edit."}`).Body.String()
	edit := resp[strings.Index(resp, "/posts/"):strings.LastIndex(resp, `"`)] + "/edit"

	for _, path := range []string{"/compose", edit} {
		page := do3(s, "GET", host, path, ck).Body.String()
		ed, left, right, preview := strings.Index(page, `<div class="ed">`), strings.Index(page, `cheat cheat-l`), strings.Index(page, `cheat cheat-r`), strings.Index(page, "<h3>Preview</h3>")
		formEnd := -1
		if left > 0 {
			formEnd = strings.LastIndex(page[:left], "</form>") // the editor's form closes before the sheet
		}
		if ed < 0 || !(ed < formEnd && formEnd < left && left < right && right < preview) || strings.Contains(page[ed:formEnd], "cheat") {
			t.Errorf("%s: order wrong (ed %d, form end %d, left %d, right %d, preview %d)", path, ed, formEnd, left, right, preview)
		}
		for _, r := range append(append([]cheatRow{}, cheatRowsLeft...), cheatRowsRight...) {
			if !strings.Contains(page, "<code>"+html.EscapeString(r.Syntax)+"</code>") {
				t.Errorf("%s lacks %q", path, r.Syntax)
			}
		}
	}
	like := do3(s, "GET", host, "/compose?"+url.Values{"page": {"https://blog.example/p"}, "mode": {"like"}}.Encode(), ck).Body.String()
	if strings.Contains(like, "cheat") {
		t.Errorf("the like page has the cheatsheet")
	}
	// the sheet is for the owner's editor; a visitor sees no editor at all
	if v := do(s, "GET", host, "/compose", "", ""); strings.Contains(v.Body.String(), "cheat") {
		t.Errorf("a visitor sees the cheatsheet")
	}
}

// Code in a post scrolls inside its card and is set in a monospace face: the stylesheet says so for the places
// that show a post, a reply and the preview.
func TestCodeBlocksAreStyled(t *testing.T) {
	s, _ := setup(t)
	css := do(s, "GET", host, "/static/weft.css", "", "").Body.String()
	for _, want := range []string{"article pre,", ".comment pre,", "#preview pre", "overflow-x: auto", "ui-monospace", "pre code"} {
		if !strings.Contains(css, want) {
			t.Errorf("stylesheet lacks %q", want)
		}
	}
}

// The editor opens with the Markdown box alone. "Advanced" brings the title, keywords, address and Markdown help;
// it is on from the start only when one of those holds something.
func TestAdvancedButton(t *testing.T) {
	s, tok := setup(t)
	s.space("alice").SetPassword("correct horse")
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))
	adv := func(page string) (present, checked bool) {
		i := strings.Index(page, `id="adv"`)
		if i < 0 {
			return false, false
		}
		tag := page[strings.LastIndex(page[:i], "<"):]
		tag = tag[:strings.Index(tag, ">")]
		return true, strings.Contains(tag, " checked") && !strings.Contains(tag, `name=`)
	}
	get := func(path string) string { return do3(s, "GET", host, path, ck).Body.String() }

	page := get("/compose")
	if present, checked := adv(page); !present || checked {
		t.Errorf("a new post: present %v, checked %v; want the box present and off", present, checked)
	}
	// the three fields and the note are the ones that fold away; the Markdown box is not
	for _, want := range []string{`<label class="adv">Title`, `<label class="adv">Keywords`, `<label class="adv">Address`, `<p class="adv">`} {
		if !strings.Contains(page, want) {
			t.Errorf("compose lacks %q", want)
		}
	}
	// the word "Markdown" is shown only in advanced mode; the box itself always, and it still has a name for screen readers
	if !strings.Contains(page, `<span class="adv">Markdown</span> <textarea name="markdown" aria-label="Markdown"`) || strings.Contains(page, `<label class="adv">Markdown`) {
		t.Errorf("the Markdown label is not folded away on its own, or the box is")
	}
	// the choice is not sent with the post
	if strings.Contains(page, `name="advanced"`) {
		t.Errorf("the checkbox is part of the form")
	}
	// something in an advanced field opens it
	for name, path := range map[string]string{
		"a title from the bookmarklet": "/compose?title=Pickleball",
		"keywords":                     "/compose?keywords=web",
	} {
		if _, checked := adv(get(path)); !checked {
			t.Errorf("%s: Advanced is off", name)
		}
	}
	// a form sent back with a message keeps what was typed, and opens when that was an address
	w := form(s, "POST", "/compose", ck, url.Values{"markdown": {"  "}, "slug": {"my-own-words"}}, "")
	if _, checked := adv(w.Body.String()); w.Code != 400 || !checked {
		t.Errorf("a refused form with an address: %d, checked %v", w.Code, checked)
	}
	w = form(s, "POST", "/compose", ck, url.Values{"markdown": {"  "}}, "")
	if _, checked := adv(w.Body.String()); w.Code != 400 || checked {
		t.Errorf("a refused form with nothing advanced: %d, checked %v", w.Code, checked)
	}
	// editing: open for a post with a title or keywords, closed for a plain one
	post := func(body string) string {
		resp := do(s, "POST", host, "/api/posts", tok, body).Body.String()
		return resp[strings.Index(resp, "/posts/"):strings.LastIndex(resp, `"`)] + "/edit"
	}
	if _, checked := adv(get(post(`{"title":"Titled","markdown":"x"}`))); !checked {
		t.Errorf("editing a titled post: Advanced is off")
	}
	if _, checked := adv(get(post(`{"markdown":"x","keywords":["go"]}`))); !checked {
		t.Errorf("editing a post with keywords: Advanced is off")
	}
	if _, checked := adv(get(post(`{"markdown":"just words"}`))); checked {
		t.Errorf("editing a plain post: Advanced is on")
	}
	// the stylesheet folds the fields and the sheet away unless the box is on
	css := do(s, "GET", host, "/static/weft.css", "", "").Body.String()
	if !strings.Contains(css, ".ed:not(:has(#adv:checked)) .adv, .ed:not(:has(#adv:checked)) .cheats { display: none; }") {
		t.Errorf("the stylesheet does not fold the advanced fields away")
	}
}
