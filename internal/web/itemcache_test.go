package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/feed"
)

func TestItemCacheKeepsForItsTimeAndNoLonger(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newItemCache(60*time.Second, 3)
	c.now = func() time.Time { return now }

	c.put("a", []byte("A"))
	if b, ok := c.get("a"); !ok || string(b) != "A" {
		t.Fatalf("a fresh entry: %q %v", b, ok)
	}
	now = now.Add(59 * time.Second)
	if _, ok := c.get("a"); !ok {
		t.Error("gone before its time")
	}
	now = now.Add(2 * time.Second)
	if _, ok := c.get("a"); ok {
		t.Error("kept past its time")
	}

	c.put("a", []byte("A"))
	c.drop("a")
	if _, ok := c.get("a"); ok {
		t.Error("kept after drop")
	}

	// full: stale entries go first, and the new one is always kept
	c.put("x", []byte("1"))
	now = now.Add(61 * time.Second)
	c.put("y", []byte("2"))
	c.put("z", []byte("3"))
	c.put("w", []byte("4")) // full of fresh ones: starts over
	if _, ok := c.get("w"); !ok {
		t.Error("the newest entry is missing")
	}
	if len(c.m) > 3 {
		t.Errorf("%d entries, the limit is 3", len(c.m))
	}
}

// A click on a reply makes the page again from the root. The posts a page read are kept, so the next page fetches
// what is new, the author's own edit shows at once, and a server that failed is asked again.
func TestThreadPostsAreKeptEditsShowAndFailuresAreNot(t *testing.T) {
	s, port, _, bobTok := twoUsers(t)
	bob := "bob.localhost:" + port
	w := do(s, "POST", bob, "/api/posts", bobTok, `{"markdown":"first words"}`)
	var made struct {
		ID  int64
		URL string
	}
	json.Unmarshal(w.Body.Bytes(), &made)
	if made.URL == "" || !strings.Contains(made.URL, "/posts/") {
		t.Fatalf("the post: %d %s", w.Code, w.Body)
	}

	// a page, as the thread view makes it: its own builder, its own request
	page := func(u string) (*feed.Parsed, int) {
		b := &threadBuilder{s: s, ctx: context.Background(), feeds: map[string]*feed.Parsed{}}
		p := b.fetchItem(u)
		return p, b.fetches
	}
	text := func(p *feed.Parsed) string {
		if p == nil || len(p.Items) == 0 {
			return ""
		}
		return p.Items[0].Text
	}

	p, n := page(made.URL)
	if n != 1 || !strings.Contains(text(p), "first words") {
		t.Fatalf("the first page: %d fetches, %q", n, text(p))
	}
	p, n = page(made.URL)
	if n != 0 || !strings.Contains(text(p), "first words") {
		t.Fatalf("the second page must not fetch: %d fetches, %q", n, text(p))
	}

	// the author edits here: the next page shows it at once
	if w := do(s, "PUT", bob, "/api/posts/"+itoa(made.ID), bobTok, `{"markdown":"second words"}`); w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	p, n = page(made.URL)
	if n != 1 || !strings.Contains(text(p), "second words") {
		t.Fatalf("after an edit here: %d fetches, %q", n, text(p))
	}

	// a change this server was not told about shows when the entry runs out
	sp, release, err := s.st.Space("bob")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := sp.User()
	if _, err := sp.UpdatePost(u.ID, made.ID, "", "third words", "<p>third words</p>", nil, ""); err != nil {
		t.Fatal(err)
	}
	release()
	if p, n = page(made.URL); n != 0 || strings.Contains(text(p), "third words") {
		t.Fatalf("within the time the old text stays: %d fetches, %q", n, text(p))
	}
	s.items.now = func() time.Time { return time.Now().Add(2 * itemTTL) }
	if p, n = page(made.URL); n != 1 || !strings.Contains(text(p), "third words") {
		t.Fatalf("after the time: %d fetches, %q", n, text(p))
	}
	s.items.now = time.Now

	// a failure is not kept
	missing := "http://bob.localhost:" + port + "/posts/not-there"
	if p, n = page(missing); p != nil || n != 1 {
		t.Fatalf("a missing post: %v, %d fetches", p, n)
	}
	if _, n = page(missing); n != 1 {
		t.Errorf("a failure was kept: %d fetches", n)
	}

	// a deleted post is gone at once for readers on this server
	if w := do(s, "DELETE", bob, "/api/posts/"+itoa(made.ID), bobTok, ""); w.Code != 200 && w.Code != 204 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if _, n = page(made.URL); n != 1 {
		t.Errorf("after a delete the page must ask again: %d fetches", n)
	}
}
