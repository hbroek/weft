package feed

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestKeywords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{" , ,, ", nil},
		{"pickleball, open web ,Dave  Winer", []string{"pickleball", "open web", "Dave Winer"}},
		{"Go, go, GO", []string{"Go"}},
		{"tab\tseparated, new\nline", []string{"tab separated", "new line"}},
		{"bell\x07char", []string{"bellchar"}},
		{strings.Repeat("x", 80), []string{strings.Repeat("x", MaxKeywordLen)}},
		{"1,2,3,4,5,6,7,8,9,10,11,12", []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}},
	}
	for _, c := range cases {
		if got := SplitKeywords(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitKeywords(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := JoinKeywords([]string{"a b", "c"}); got != "a b, c" {
		t.Errorf("JoinKeywords = %q", got)
	}
}

func TestKeywordsRoundTripThroughAFeed(t *testing.T) {
	ch := Channel{Title: "A", Link: "http://a.example/", Items: []Item{{GUID: GUID{Value: "urn:uuid:1"}, Link: "http://a.example/p", Description: "x",
		PubDate: RFC822(time.Now()), Categories: []string{"open web", "Dave Winer"}}}}
	b, err := New(ch).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "<category>open web</category>") {
		t.Fatalf("category not written:\n%s", b)
	}
	p, err := Parse(b, time.Now())
	if err != nil || len(p.Items) != 1 || !reflect.DeepEqual(p.Items[0].Keywords, []string{"open web", "Dave Winer"}) {
		t.Errorf("keywords after parse: %+v %v", p, err)
	}
	// other hosts' categories are cleaned like ours
	p, _ = Parse([]byte(`<rss version="2.0"><channel><title>B</title><item><guid>g</guid><link>http://b.example/p</link><description>x</description>
<category> Spam </category><category>spam</category><category>`+strings.Repeat("y", 90)+`</category></item></channel></rss>`), time.Now())
	if k := p.Items[0].Keywords; len(k) != 2 || k[0] != "Spam" || len(k[1]) != MaxKeywordLen {
		t.Errorf("remote categories: %q", k)
	}
}

func TestKeywordFilterHelpers(t *testing.T) {
	if got := Keyword("  open   web, "); got != "open web" {
		t.Errorf("Keyword = %q", got)
	}
	if Keyword(" , ") != "" {
		t.Error("empty keyword")
	}
	if !HasKeyword([]string{"Go", "open web"}, "OPEN WEB") || HasKeyword([]string{"Go"}, "gopher") {
		t.Error("HasKeyword")
	}
}

func TestChannelImageIsTheAvatar(t *testing.T) {
	ch := Channel{Title: "Alice", Link: "http://a.example/", Image: &ChannelImage{URL: "https://a.example/me.png", Title: "Alice", Link: "http://a.example/"}}
	b, err := New(ch).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?s)<image>\s*<url>https://a.example/me.png</url>`).Match(b) {
		t.Errorf("channel image not written:\n%s", b)
	}
	p, err := Parse(b, time.Now())
	if err != nil || p.Image != "https://a.example/me.png" {
		t.Errorf("channel image not read: %v %q", err, p.Image)
	}
	for _, bad := range []string{"javascript:alert(1)", "data:image/png;base64,AAAA", "/relative.png", ""} {
		ch.Image = &ChannelImage{URL: bad}
		b, _ := New(ch).Marshal()
		if p, err := Parse(b, time.Now()); err != nil || p.Image != "" {
			t.Errorf("%q must not become an avatar: %v %q", bad, err, p.Image)
		}
	}
}

func TestContextSnapshotRoundTrip(t *testing.T) {
	ch := Channel{Title: "A", Link: "http://a.example/", Items: []Item{{GUID: GUID{Value: "urn:uuid:1"}, Link: "http://a.example/p", PubDate: RFC822(time.Now()),
		InReplyTo: &Ref{Href: "https://blog.example/x"}, Root: &Ref{Href: "https://blog.example/x"},
		Context: &Context{Href: "https://blog.example/x", Title: "  The   web\nof links ", Description: strings.Repeat("d", 600), Image: "javascript:alert(1)"}}}}
	b, err := New(ch).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(b, time.Now())
	if err != nil || len(p.Items) != 1 {
		t.Fatalf("%v %v", err, p)
	}
	got := p.Items[0].Parent
	if got.Title != "The web of links" || len([]rune(got.Description)) != MaxParentDesc || got.Image != "" {
		t.Errorf("snapshot not cleaned on the way in: %+v", got)
	}
	if !(ParentInfo{}).Empty() || got.Empty() {
		t.Errorf("Empty()")
	}
}
