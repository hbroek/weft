package fetch

import "testing"

func TestFindFeedLink(t *testing.T) {
	base := "https://blog.example/posts/"
	cases := []struct{ name, page, want string }{
		{"rss", `<link rel="alternate" type="application/rss+xml" href="/rss.xml">`, "https://blog.example/rss.xml"},
		{"atom only", `<link rel="alternate" type="application/atom+xml" href="feed.atom">`, "https://blog.example/posts/feed.atom"},
		{"rss wins over atom", `<link rel="alternate" type="application/atom+xml" href="/a.atom"><link rel="alternate" type="application/rss+xml" href="/r.xml">`, "https://blog.example/r.xml"},
		{"escaped ampersand", `<link rel='alternate' type='application/atom+xml' href='/f?a=1&amp;b=2'>`, "https://blog.example/f?a=1&b=2"},
		{"none", `<link rel="stylesheet" href="/s.css"><link rel="alternate" type="text/html" href="/x">`, ""},
	}
	for _, c := range cases {
		got, ok := FindFeedLink([]byte(c.page), base)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: %q %v, want %q", c.name, got, ok, c.want)
		}
	}
}
