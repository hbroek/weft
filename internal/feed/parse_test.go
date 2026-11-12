package feed

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func TestParseWeftFeed(t *testing.T) {
	x := `<?xml version="1.0"?>
<rss version="2.0" xmlns:weft="https://weftprotocol.org/ns/weft/0.1" xmlns:source="http://source.scripting.com/">
<channel><title>Bob</title><link>https://bob.example/</link><description>d</description>
<weft:webmention>https://bob.example/wm</weft:webmention>
<item><guid isPermaLink="true">https://bob.example/p/1</guid><link>https://bob.example/p/1</link>
<description>&lt;p&gt;Hi &lt;em&gt;there&lt;/em&gt;&lt;script&gt;x()&lt;/script&gt;&lt;/p&gt;</description>
<source:markdown>Hi *there*</source:markdown>
<pubDate>Mon, 12 Oct 2026 22:15:00 -0400</pubDate>
<weft:updated>Mon, 12 Oct 2026 22:40:00 -0400</weft:updated>
<weft:inReplyTo href="https://alice.example/n/1"/></item>
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

// An empty Atom feed is a feed with nothing in it; anything that is not a feed at all is refused.
func TestParseEmptyAtomAndNonFeeds(t *testing.T) {
	p, err := Parse([]byte(`<feed xmlns="http://www.w3.org/2005/Atom"></feed>`), now)
	if err != nil || len(p.Items) != 0 {
		t.Fatalf("empty atom: %v %v", err, p)
	}
	for _, doc := range []string{`<html><body>x</body></html>`, `{"json":true}`, `<opml version="2.0"></opml>`} {
		if _, err := Parse([]byte(doc), now); err == nil {
			t.Errorf("%q accepted as a feed", doc)
		}
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

func TestParseKeysItemsByPermalinkAndKeepsGUIDAsIdentity(t *testing.T) {
	const doc = `<rss version="2.0"><channel><title>x</title>
<item><guid isPermaLink="false">urn:uuid:0194f3a0-1111-7222-8333-444455556666</guid><link>https://a.example/posts/hello-k3x9</link><description>one</description></item>
<item><guid>https://b.example/p/2</guid><description>two</description></item>
</channel></rss>`
	p, err := Parse([]byte(doc), time.Now())
	if err != nil || len(p.Items) != 2 {
		t.Fatalf("%v %+v", err, p)
	}
	if a := p.Items[0]; a.GUID != "https://a.example/posts/hello-k3x9" || a.UID != "urn:uuid:0194f3a0-1111-7222-8333-444455556666" {
		t.Errorf("an item is filed under its permalink and keeps its guid as identity: %+v", a)
	}
	if b := p.Items[1]; b.GUID != "https://b.example/p/2" {
		t.Errorf("without a link the guid is the key: %+v", b)
	}
}

// Feeds that declare an older encoding (Slashdot's says ISO-8859-1) are read, not refused.
func TestParseLegacyEncodings(t *testing.T) {
	doc := func(enc string, body []byte) []byte {
		return append([]byte(`<?xml version="1.0" encoding="`+enc+`"?><rss version="2.0"><channel><title>`), append(body, []byte(`</title><link>http://a.example/</link><item><title>t</title><guid>g1</guid><link>http://a.example/1</link><pubDate>Mon, 12 Oct 2026 22:30:00 -0400</pubDate><description>d</description></item></channel></rss>`)...)...)
	}
	// 0xE9 é, 0x93 and 0x94 curly quotes, 0x80 euro, 0x97 em dash
	latin1 := []byte("Caf\xe9 \x93quoted\x94 \x80 5 \x97 ok")
	want := "Café “quoted” € 5 — ok"
	for _, enc := range []string{"ISO-8859-1", "iso-8859-1", "windows-1252", "Latin1"} {
		p, err := Parse(doc(enc, latin1), time.Now())
		if err != nil || p.Title != want || len(p.Items) != 1 {
			t.Errorf("%s: %v %q", enc, err, func() string {
				if p != nil {
					return p.Title
				}
				return ""
			}())
		}
	}
	// Latin-9 has the euro where Latin-1 has the currency sign
	if p, err := Parse(doc("ISO-8859-15", []byte("5\xa4")), time.Now()); err != nil || p.Title != "5€" {
		t.Errorf("iso-8859-15: %v %v", err, p)
	}
	// UTF-8 and ASCII declarations still work, and an encoding we do not read is said so plainly
	if p, err := Parse(doc("UTF-8", []byte("Café")), time.Now()); err != nil || p.Title != "Café" {
		t.Errorf("utf-8: %v", err)
	}
	if _, err := Parse(doc("us-ascii", []byte("plain")), time.Now()); err != nil {
		t.Errorf("us-ascii: %v", err)
	}
	if _, err := Parse(doc("Shift_JIS", []byte("x")), time.Now()); err == nil || !strings.Contains(err.Error(), `unsupported encoding "Shift_JIS"`) {
		t.Errorf("shift_jis: %v", err)
	}
}

// RSS 1.0 (an rdf:RDF root with the items beside the channel), as Slashdot publishes it, in Latin-1.
func TestParseRSS1(t *testing.T) {
	doc := []byte("<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?>\n" + `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:slash="http://purl.org/rss/1.0/modules/slash/">
<channel rdf:about="https://slashdot.org/"><title>Slashdot</title><link>https://slashdot.org/</link><description>News for nerds</description>
<items><rdf:Seq><rdf:li rdf:resource="https://tech.slashdot.org/story/1"/></rdf:Seq></items></channel>
<item rdf:about="https://tech.slashdot.org/story/1"><title>Caf` + "\xe9" + ` merger</title><link>https://tech.slashdot.org/story/1</link>
<description>&lt;p&gt;It says &lt;b&gt;&amp;ldquo;hi&amp;rdquo;&lt;/b&gt;.&lt;/p&gt;</description><dc:date>2026-10-21T14:10:00+00:00</dc:date><slash:department>x</slash:department></item>
<item rdf:about="https://tech.slashdot.org/story/2"><title>No link element</title><description>d</description></item>
</rdf:RDF>`)
	p, err := Parse(doc, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Slashdot" || p.Link != "https://slashdot.org/" || len(p.Items) != 2 {
		t.Fatalf("channel: %+v", p)
	}
	a, b := p.Items[0], p.Items[1]
	if a.GUID != "https://tech.slashdot.org/story/1" || a.Title != "Café merger" || a.Published.UTC().Format(time.RFC3339) != "2026-10-21T14:10:00Z" || !strings.Contains(a.Text, "hi") || strings.Contains(a.Text, "<") {
		t.Errorf("item 1: %+v", a)
	}
	if b.GUID != "https://tech.slashdot.org/story/2" || b.Published.Year() != 2026 || b.RepliesCount != -1 {
		t.Errorf("item 2 (identified by rdf:about, dated by the time it was read): %+v", b)
	}
	if _, err := Parse([]byte("<html><body>not a feed</body></html>"), time.Now()); err == nil {
		t.Errorf("an HTML page is not a feed")
	}
}

const atomSample = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:thr="http://purl.org/syndication/thread/1.0">
  <title type="html">Blog &lt;b&gt;of&lt;/b&gt; things</title>
  <subtitle>Notes</subtitle>
  <link rel="self" href="https://blog.example/atom.xml"/>
  <link rel="alternate" type="text/html" href="https://blog.example/"/>
  <link rel="next" href="https://blog.example/atom.xml?page=2"/>
  <logo>https://blog.example/me.png</logo>
  <id>tag:blog.example,2026:feed</id>
  <updated>2026-10-21T14:10:00Z</updated>
  <entry>
    <title>Plain title</title>
    <id>tag:blog.example,2026:1</id>
    <link rel="alternate" type="text/html" href="https://blog.example/one"/>
    <link rel="enclosure" href="https://blog.example/one.mp3"/>
    <published>2026-10-20T10:00:00.5Z</published>
    <updated>2026-10-21T11:00:00Z</updated>
    <summary>Only a summary</summary>
    <content type="html">&lt;p&gt;Hello &lt;b&gt;html&lt;/b&gt; &amp;amp; more&lt;/p&gt;</content>
    <category term="go"/><category term="open-web" label="Open web"/>
    <thr:in-reply-to ref="tag:x" href="https://other.example/post"/>
  </entry>
  <entry>
    <title type="xhtml"><div xmlns="http://www.w3.org/1999/xhtml">An <em>xhtml</em> title</div></title>
    <id>tag:blog.example,2026:2</id>
    <summary type="text">Just text &lt;kept&gt;</summary>
    <updated>2026-10-19T09:00:00Z</updated>
  </entry>
  <entry><title>No link, identified by its id</title><id>urn:uuid:abc</id><content>c</content></entry>
  <entry><title>Nothing to identify it</title></entry>
</feed>`

// Atom feeds are read like RSS ones.
func TestParseAtom(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p, err := Parse([]byte(atomSample), now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Blog of things" || p.Link != "https://blog.example/" || p.Description != "Notes" || p.Image != "https://blog.example/me.png" || p.Next != "https://blog.example/atom.xml?page=2" {
		t.Errorf("feed: %+v", p)
	}
	if len(p.Items) != 3 {
		t.Fatalf("items: %d (an entry that cannot be identified is skipped)", len(p.Items))
	}
	a, b, c := p.Items[0], p.Items[1], p.Items[2]
	if a.GUID != "https://blog.example/one" || a.Link != "https://blog.example/one" || a.UID != "tag:blog.example,2026:1" || a.Title != "Plain title" {
		t.Errorf("entry 1: %+v", a)
	}
	if a.Text != "Hello html & more" {
		t.Errorf("html content is shown as text, and wins over the summary: %q", a.Text)
	}
	if a.Published.UTC().Format(time.RFC3339) != "2026-10-20T10:00:00Z" || a.Updated.UTC().Format(time.RFC3339) != "2026-10-21T11:00:00Z" {
		t.Errorf("dates: %v %v", a.Published, a.Updated)
	}
	if len(a.Keywords) != 2 || a.Keywords[0] != "go" || a.Keywords[1] != "Open web" || a.InReplyTo != "https://other.example/post" {
		t.Errorf("categories and reply: %v %q", a.Keywords, a.InReplyTo)
	}
	if b.Title != "An xhtml title" || b.Text != "Just text <kept>" || b.Published.Year() != 2026 || b.GUID != "tag:blog.example,2026:2" {
		t.Errorf("entry 2: %+v", b)
	}
	if c.GUID != "urn:uuid:abc" || c.Text != "c" || !c.Published.Equal(now) || c.RepliesCount != -1 {
		t.Errorf("entry 3: %+v", c)
	}
	// a <feed> that is not in the Atom namespace is not Atom
	if _, err := Parse([]byte(`<feed><title>x</title></feed>`), now); err == nil {
		t.Errorf("a feed element outside the Atom namespace should not parse")
	}
}

func TestParseReadsSourceInReplyTo(t *testing.T) {
	data := `<rss version="2.0" xmlns:source="http://source.scripting.com/" xmlns:weft="` + NSWeft + `"><channel><title>t</title>
<item><link>https://a.example/1</link><source:inReplyTo feedUrl="https://demo.rss.chat/users/dave/rss.xml" feedTitle="Dave">https://demo.rss.chat/?id=798</source:inReplyTo></item>
<item><link>https://a.example/2</link><source:inReplyTo isPermaLink="false">did:plc:iwl32vekohccji6khfdt3clw</source:inReplyTo></item>
<item><link>https://a.example/3</link><weft:inReplyTo href="https://w.example/p"/><source:inReplyTo>https://other.example/q</source:inReplyTo></item>
</channel></rss>`
	p, err := Parse([]byte(data), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Items[0]; got.InReplyTo != "https://demo.rss.chat/?id=798" || got.InReplyToFeed != "https://demo.rss.chat/users/dave/rss.xml" {
		t.Errorf("source:inReplyTo with feedUrl: %q, %q", got.InReplyTo, got.InReplyToFeed)
	}
	if got := p.Items[1].InReplyTo; got != "" {
		t.Errorf("an identifier that is not an address was taken as one: %q", got)
	}
	if got := p.Items[2].InReplyTo; got != "https://w.example/p" {
		t.Errorf("weft:inReplyTo should win, got %q", got)
	}
}

func TestMarshalWritesSourceInReplyToAndComments(t *testing.T) {
	r := New(Channel{Title: "t", Items: []Item{{
		GUID: GUID{Value: "g"}, Link: "https://a.example/1",
		InReplyTo: &Ref{Href: "https://b.example/p", Feed: "https://b.example/rss.xml"},
		Replies:   &Replies{Href: "https://a.example/1/replies.xml", Count: 0},
	}, {GUID: GUID{Value: "h"}, Link: "https://a.example/2"}}})
	b, err := r.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`<source:inReplyTo feedUrl="https://b.example/rss.xml">https://b.example/p</source:inReplyTo>`,
		`<source:comments count="0" feedUrl="https://a.example/1/replies.xml"></source:comments>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in\n%s", want, s)
		}
	}
	if strings.Count(s, "<source:inReplyTo") != 1 || strings.Count(s, "<source:comments") != 1 {
		t.Errorf("an item without a reply target or replies feed got source elements:\n%s", s)
	}
	p, err := Parse(b, time.Now())
	if err != nil || p.Items[0].InReplyTo != "https://b.example/p" {
		t.Errorf("round trip: %v %+v", err, p)
	}
}

func TestParseReadsCanonical(t *testing.T) {
	rss := `<rss version="2.0" xmlns:weft="` + NSWeft + `"><channel><title>t</title>
<item><link>https://a.example/1</link><weft:canonical href="https://orig.example/essay"/></item>
<item><link>https://a.example/2</link><weft:canonical href="javascript:alert(1)"/></item>
<item><link>https://a.example/3</link></item></channel></rss>`
	p, err := Parse([]byte(rss), now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Items[0].Canonical != "https://orig.example/essay" || p.Items[1].Canonical != "" || p.Items[2].Canonical != "" {
		t.Errorf("rss: %q %q %q", p.Items[0].Canonical, p.Items[1].Canonical, p.Items[2].Canonical)
	}
	atom := `<feed xmlns="http://www.w3.org/2005/Atom"><title>t</title><entry><id>1</id><title>e</title>
<link rel="alternate" href="https://a.example/1"/><link rel="canonical" href="https://orig.example/essay"/></entry></feed>`
	p, err = Parse([]byte(atom), now)
	if err != nil || p.Items[0].Canonical != "https://orig.example/essay" || p.Items[0].Link != "https://a.example/1" {
		t.Errorf("atom: %v %+v", err, p.Items)
	}
}

// The author of the source namespace writes it with https in his own feeds. It reads the same as the http form,
// and a link in the text that happens to be that address is left alone.
func TestParseSourceNamespaceWithHTTPS(t *testing.T) {
	for _, ns := range []string{"https://source.scripting.com/", "http://source.scripting.com/"} {
		x := `<rss version="2.0" xmlns:source="` + ns + `"><channel><title>t</title><link>http://a/</link><description>d</description>
<item><guid>g</guid><description>see https://source.scripting.com/ here</description><source:markdown>**hi** ![i](http://a/i.png)</source:markdown></item></channel></rss>`
		p, err := Parse([]byte(x), time.Now())
		if err != nil || len(p.Items) != 1 {
			t.Fatalf("%s: %v", ns, err)
		}
		if p.Items[0].Markdown != "**hi** ![i](http://a/i.png)" {
			t.Errorf("%s: markdown not read: %q", ns, p.Items[0].Markdown)
		}
		if !strings.Contains(p.Items[0].Text, "https://source.scripting.com/") {
			t.Errorf("%s: text changed: %q", ns, p.Items[0].Text)
		}
	}
}
