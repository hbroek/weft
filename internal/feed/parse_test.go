package feed

import (
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

func TestParseWeftFeed(t *testing.T) {
	x := `<?xml version="1.0"?>
<rss version="2.0" xmlns:weft="https://weftseed.com/ns/weft/0.1" xmlns:source="http://source.scripting.com/">
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

func TestParseLegacyNamespace(t *testing.T) {
	old := `<rss version="2.0" xmlns:tc="` + NSLegacy + `"><channel><title>B</title><link>https://bob.example/</link>
<tc:webmention>https://bob.example/wm</tc:webmention>
<item><guid>https://bob.example/n/2</guid><link>https://bob.example/n/2</link><description>hi</description>
<pubDate>Mon, 12 Oct 2026 22:40:00 -0400</pubDate>
<tc:inReplyTo href="https://alice.example/n/1"/></item></channel></rss>`
	p, err := Parse([]byte(old), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if p.Webmention != "https://bob.example/wm" || len(p.Items) != 1 || p.Items[0].InReplyTo != "https://alice.example/n/1" {
		t.Errorf("old namespace not read: %+v", p)
	}
}

// Feeds written when a reaction had a polarity and an emoji still read: a "+" is a like, a "-" is nothing.
func TestParseOldReactions(t *testing.T) {
	old := `<rss version="2.0" xmlns:weft="` + NSWeft + `"><channel><title>B</title><link>https://bob.example/</link>
<weft:reactionsFeed>https://bob.example/reactions.xml</weft:reactionsFeed>
<item><guid>https://bob.example/r/1</guid><link>https://bob.example/r/1</link><pubDate>Mon, 12 Oct 2026 22:40:00 -0400</pubDate>
<weft:reactionOf href="https://alice.example/n/1" polarity="+" emoji="🎉"/></item>
<item><guid>https://bob.example/r/2</guid><link>https://bob.example/r/2</link><pubDate>Mon, 12 Oct 2026 22:41:00 -0400</pubDate>
<weft:reactionOf href="https://alice.example/n/2" polarity="-"/></item>
<item><guid>https://bob.example/n/3</guid><link>https://bob.example/n/3</link><description>hi</description><pubDate>Mon, 12 Oct 2026 22:42:00 -0400</pubDate>
<weft:reactions plus="2" minus="1"/></item></channel></rss>`
	p, err := Parse([]byte(old), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if p.LikesFeed != "https://bob.example/reactions.xml" || len(p.Items) != 3 ||
		p.Items[0].LikeOf != "https://alice.example/n/1" || p.Items[1].LikeOf != "" || p.Items[2].Likes != 2 {
		t.Errorf("old reactions: %+v", p)
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
