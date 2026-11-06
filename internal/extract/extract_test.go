package extract

import (
	"strings"
	"testing"
)

const sample = `<!doctype html><html><head><title>The Essay - Some Blog</title>
<link rel="canonical" href="/2026/the-essay?ref=home">
<script>var junk = "not text"</script></head><body>
<header><nav><a href="/">Home</a> <a href="/about">About</a></nav></header>
<article>
<h1>The Essay</h1>
<p>First paragraph with a <a href="/other">relative link</a>, some <em>emphasis</em> and <strong>weight</strong>, and a
snake_case word with *stars*. It is long enough that the article counts as the post and not as page furniture around it.</p>
<h2>A section</h2>
<ul><li>one</li><li>two <a href="https://x.example/a(b)">linked</a></li></ul>
<ol><li>first</li><li>second</li></ol>
<blockquote><p>Quoted words.</p></blockquote>
<p><img src="/pics/a.png" alt="A [picture]"> and <img src="data:image/png;base64,AAAA" alt="inline"> and <img src="javascript:alert(1)"></p>
<pre><code>line one
line two</code></pre>
<p>Last paragraph, with enough words to be a paragraph of its own in the converted text.</p>
</article>
<aside>Related posts: do not copy me</aside>
<footer>Copyright</footer></body></html>`

func TestPageBecomesMarkdown(t *testing.T) {
	a := Page([]byte(sample), "https://blog.example/2026/the-essay")
	if a.Title != "The Essay" {
		t.Errorf("title %q", a.Title)
	}
	if a.Canonical != "https://blog.example/2026/the-essay?ref=home" {
		t.Errorf("canonical %q", a.Canonical)
	}
	for _, want := range []string{
		"First paragraph with a [relative link](https://blog.example/other), some *emphasis* and **weight**",
		`snake\_case word with \*stars\*`,
		"## A section",
		"- one\n- two [linked](https://x.example/a(b%29)",
		"1. first\n2. second",
		"> Quoted words.",
		"![A picture](https://blog.example/pics/a.png)",
		"```\nline one\nline two\n```",
		"Last paragraph",
	} {
		if !strings.Contains(a.Markdown, want) {
			t.Errorf("missing %q in:\n%s", want, a.Markdown)
		}
	}
	for _, bad := range []string{"Home", "Related posts", "Copyright", "not text", "data:image", "javascript:", "# The Essay"} {
		if strings.Contains(a.Markdown, bad) {
			t.Errorf("should not contain %q:\n%s", bad, a.Markdown)
		}
	}
}

func TestPageWithoutArticleGivesNothing(t *testing.T) {
	a := Page([]byte(`<html><head><title>App</title></head><body><div id="root"></div><script>render()</script></body></html>`), "https://app.example/")
	if a.Markdown != "" || a.Title != "App" {
		t.Errorf("%+v", a)
	}
}

func TestLargestBlockIsUsedWhenThereIsNoArticleElement(t *testing.T) {
	p := strings.Repeat("A long sentence about a thing that matters to the author. ", 6)
	a := Page([]byte(`<html><body><div class="menu"><p>Menu</p></div><div class="post"><p>`+p+`</p><p>`+p+`</p></div></body></html>`), "https://x.example/")
	if !strings.Contains(a.Markdown, "A long sentence") || strings.Contains(a.Markdown, "Menu") {
		t.Errorf("%q", a.Markdown)
	}
}
