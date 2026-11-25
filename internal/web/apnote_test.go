package web

import (
	"strings"
	"testing"
)

func TestHashtags(t *testing.T) {
	for kw, want := range map[string]string{"go": "go", "Indie Web": "IndieWeb", "c++": "c", "2026": "", "web_dev": "web_dev", "été": "été", "a-b": "ab", "": ""} {
		if got := hashtagOf(kw); got != want {
			t.Errorf("%q: %q, want %q", kw, got, want)
		}
	}
	tags := noteTags([]string{"Go", "go", "Indie Web", "2026"}, "https://x.example")
	if len(tags) != 2 || tags[0].Name != "#Go" || tags[1].Name != "#IndieWeb" || tags[1].Href != "https://x.example/?keyword=Indie+Web" {
		t.Errorf("%+v", tags)
	}
}

func TestPostHTMLIsRewrittenForMastodon(t *testing.T) {
	in := `<h2>Big</h2><p>Text with <a href="/posts/x">a link</a>, <a href="javascript:alert(1)">a bad one</a>, <em>em</em> and <code>code</code>.</p>` +
		`<p><img src="/media/a.jpg" alt="A picture"></p><hr><script>x()</script><ul><li>one</li><li>two</li></ul>` +
		`<table><tr><th>a</th><td>b</td></tr></table><div class="x"><span>plain</span></div>`
	blocks, images := federateHTML(in, "https://me.example")
	got := strings.Join(blocks, "")
	for _, want := range []string{"<p><strong>Big</strong></p>", `<a href="https://me.example/posts/x" rel="nofollow noopener noreferrer">a link</a>`, "a bad one,", "<em>em</em>", "<code>code</code>", "<ul><li>one</li><li>two</li></ul>", "<p>a b </p>", "plain"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q in %s", want, got)
		}
	}
	for _, bad := range []string{"<h2", "<img", "javascript", "<script", "<hr", "<div", "<table", "<td"} {
		if strings.Contains(got, bad) {
			t.Errorf("still has %q in %s", bad, got)
		}
	}
	if len(images) != 1 || images[0].URL != "https://me.example/media/a.jpg" || images[0].Alt != "A picture" {
		t.Errorf("images: %+v", images)
	}
	if strings.Contains(got, "<p></p>") {
		t.Errorf("an empty paragraph is left: %s", got)
	}
}

func TestAPostGoesWholeWithoutALinkBackAndOnlyAHugeOneAsAnExcerpt(t *testing.T) {
	site, link := "https://me.example", "https://me.example/posts/t.abcd"
	tags := noteTags([]string{"go"}, site)
	short, _ := noteContent("Hello", "<p>Short body.</p>", link, tags, site)
	if short != `<p><strong>Hello</strong></p><p>Short body.</p><p><a href="https://me.example/?keyword=go" class="mention hashtag" rel="tag">#<span>go</span></a></p>` {
		t.Errorf("short: %s", short)
	}
	if strings.Contains(short, link) || strings.Contains(short, "Read more") {
		t.Errorf("a link back to the post: %s", short)
	}
	// untitled: only the text
	if got, _ := noteContent("", "<p>Just this.</p>", link, nil, site); got != "<p>Just this.</p>" {
		t.Errorf("untitled: %s", got)
	}
	// a long post, a few thousand characters in paragraphs, goes whole and without a link
	p := func(c string, n int) string { return "<p>" + strings.Repeat(c, n) + "</p>" }
	essay := p("a", 1500) + p("b", 1500) + p("c", 1500) + p("d", 1500)
	if got, _ := noteContent("Essay", essay, link, nil, site); !strings.Contains(got, strings.Repeat("d", 1500)) || strings.Contains(got, "Read more") || strings.Contains(got, link) {
		t.Errorf("a long post was cut or linked: %d characters", len(got))
	}
	// the limit itself: whole at the limit, an excerpt with a link over it
	if got, _ := noteContent("", p("a", noteWholeLimit), link, nil, site); strings.Contains(got, "Read more") {
		t.Errorf("a post at the limit was cut")
	}
	small := ""
	for i := 0; i < noteWholeLimit/100+2; i++ {
		small += p("z", 100)
	}
	huge, _ := noteContent("Huge", p("a", 150)+p("b", 150)+p("c", 150)+small, link, nil, site)
	if !strings.Contains(huge, strings.Repeat("a", 150)) || !strings.Contains(huge, strings.Repeat("b", 150)) || strings.Contains(huge, "ccc") || !strings.HasSuffix(huge, `Read more</a></p>`) {
		t.Errorf("huge: %s", huge)
	}
	// one very long paragraph in a huge post is cut at a word, never past 400 characters
	words := strings.TrimSpace(strings.Repeat("word ", noteWholeLimit/4))
	one, _ := noteContent("", "<p>"+words+"</p>", link, nil, site)
	text := plainText(one)
	if n := runeLen(strings.TrimSuffix(text, " Read more")); n > excerptMax+3 || !strings.Contains(text, "word…") {
		t.Errorf("one paragraph: %d runes", n)
	}
	// at most four pictures
	var imgs string
	for i := 0; i < 6; i++ {
		imgs += `<img src="/m/` + string(rune('a'+i)) + `.png">`
	}
	if _, images := noteContent("", "<p>x</p>"+imgs, link, nil, site); len(images) != maxAttachments {
		t.Errorf("%d pictures", len(images))
	}
	if imageType("https://x.example/a.PNG") != "image/png" || imageType("https://x.example/a.svg") != "" || imageType("https://x.example/a") != "" {
		t.Errorf("image types")
	}
}
