package feed

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func TestParseTextcastFeed(t *testing.T) {
	x := `<?xml version="1.0"?>
<rss version="2.0" xmlns:tc="https://example.org/ns/textcast/0.1" xmlns:source="http://source.scripting.com/">
<channel><title>Bob</title><link>https://bob.example/</link><description>d</description>
<tc:webmention>https://bob.example/wm</tc:webmention>
<item><guid isPermaLink="true">https://bob.example/p/1</guid><link>https://bob.example/p/1</link>
<description>&lt;p&gt;Hi &lt;em&gt;there&lt;/em&gt;&lt;script&gt;x()&lt;/script&gt;&lt;/p&gt;</description>
<source:markdown>Hi *there*</source:markdown>
<pubDate>Mon, 12 Oct 2026 22:15:00 -0400</pubDate>
<tc:updated>Mon, 12 Oct 2026 22:40:00 -0400</tc:updated>
<tc:inReplyTo href="https://alice.example/n/1"/></item>
</channel></rss>`
	p, err := Parse([]byte(x), now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Bob" || p.Webmention != "https://bob.example/wm" || len(p.Items) != 1 {
		t.Fatalf("%+v", p)
	}
	it := p.Items[0]
	if it.Title != "" || it.Markdown != "Hi *there*" || it.InReplyTo != "https://alice.example/n/1" || it.Updated.IsZero() {
		t.Errorf("%+v", it)
	}
	if strings.Contains(it.Text, "<") || it.Published.UTC().Hour() != 2 {
		t.Errorf("text %q published %v", it.Text, it.Published)
	}
}

func TestParsePlainRSS(t *testing.T) {
	x := `<rss version="2.0"><channel><title>Blog</title>
<item><title>Post</title><link>https://b.example/1</link><description>&lt;p&gt;one&lt;/p&gt;&lt;p&gt;two &amp;amp; three&lt;/p&gt;</description></item>
<item><description>no id at all</description></item></channel></rss>`
	p, err := Parse([]byte(x), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 { // the item with no guid/link is skipped
		t.Fatalf("items: %d", len(p.Items))
	}
	it := p.Items[0]
	if it.GUID != "https://b.example/1" || it.Text != "one\n\ntwo & three" || !it.Published.Equal(now) {
		t.Errorf("%+v", it)
	}
}

func TestParseRejectsAtom(t *testing.T) {
	if _, err := Parse([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"></feed>`), now); err == nil {
		t.Fatal("atom accepted")
	}
}

// atom:link rel="self" comes after <link> in our own feeds; it must not clobber the homepage.
func TestParseIgnoresNamespacedLink(t *testing.T) {
	x := `<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom"><channel><title>T</title>
<link>https://a.example/</link><atom:link href="https://a.example/feed.xml" rel="self" type="application/rss+xml"/>
<item><guid>g1</guid><link>https://a.example/p/1</link><atom:link rel="replies" href="https://a.example/p/1/r"/></item>
</channel></rss>`
	p, err := Parse([]byte(x), now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Link != "https://a.example/" || p.Items[0].Link != "https://a.example/p/1" {
		t.Errorf("channel link %q, item link %q", p.Link, p.Items[0].Link)
	}
}
