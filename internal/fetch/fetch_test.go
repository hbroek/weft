package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestMovedOnlyForPermanentRedirects(t *testing.T) {
	mux := http.NewServeMux()
	redirect := func(from string, code int, to string) {
		mux.HandleFunc(from, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, code) })
	}
	mux.HandleFunc("/end", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	redirect("/301", 301, "/end")
	redirect("/308", 308, "/end")
	redirect("/302", 302, "/end")
	redirect("/307", 307, "/end")
	redirect("/301-302", 301, "/302")
	redirect("/301-301", 301, "/301")
	redirect("/302-301", 302, "/301")
	ts := httptest.NewServer(mux)
	defer ts.Close()
	c := New(true)
	for path, want := range map[string]bool{"/end": false, "/301": true, "/308": true, "/302": false, "/307": false,
		"/301-302": false, "/301-301": true, "/302-301": false} {
		res, err := c.Get(context.Background(), ts.URL+path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if res.Moved != want {
			t.Errorf("%s: Moved = %v, want %v", path, res.Moved, want)
		}
		if want && res.URL != ts.URL+"/end" {
			t.Errorf("%s: URL = %s", path, res.URL)
		}
	}
}
