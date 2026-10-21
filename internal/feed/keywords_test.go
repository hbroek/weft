package feed

import (
	"reflect"
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
