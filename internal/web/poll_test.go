package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/store"
)

const pollFeed = `<?xml version="1.0"?><rss version="2.0"><channel><title>T</title><link>http://x/</link>
<item><guid>g1</guid><title>One</title><link>http://x/1</link><pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate></item></channel></rss>`

func pollServer(t *testing.T) (*Server, *store.Instance) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddUser("alice", "Alice", ""); err != nil {
		t.Fatal(err)
	}
	return New(Config{BaseDomain: "localhost", AllowPrivate: true}, st), st
}

// A feed that has not changed is asked for with its validators and answers 304, so nothing is parsed or rewritten.
func TestRefreshAsksWhetherAFeedChanged(t *testing.T) {
	var full, notModified int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` && r.Header.Get("If-Modified-Since") == "Mon, 02 Jan 2006 15:04:05 GMT" {
			atomic.AddInt32(&notModified, 1)
			w.WriteHeader(304)
			return
		}
		atomic.AddInt32(&full, 1)
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprint(w, pollFeed)
	}))
	defer ts.Close()
	s, in := pollServer(t)
	ctx := context.Background()
	s.Refresh(ctx, ts.URL)
	s.Refresh(ctx, ts.URL)
	s.Refresh(ctx, ts.URL)
	if full != 1 || notModified != 2 {
		t.Fatalf("full %d, not modified %d; want 1 and 2", full, notModified)
	}
	if !in.HasItems(ts.URL) {
		t.Error("a 304 must keep the cached items")
	}
}

// A failing feed is left alone for longer each time, and "Refresh now" still goes through.
func TestFailingFeedsBackOff(t *testing.T) {
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "down", 500)
	}))
	defer ts.Close()
	s, in := pollServer(t)
	sp := s.space("alice")
	if err := sp.Follow(sp.UID(), ts.URL); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s.RefreshAll(ctx)
	s.RefreshAll(ctx) // waiting out the backoff
	if hits != 1 {
		t.Fatalf("a failed feed was fetched again at once: %d fetches", hits)
	}
	_ = in
	s.Refresh(ctx, ts.URL) // the user asked
	if hits != 2 {
		t.Fatalf("Refresh now did not fetch: %d", hits)
	}
	if got := store.Backoff(1); got != 15*time.Minute {
		t.Errorf("first backoff %v", got)
	}
	if store.Backoff(2) != 30*time.Minute || store.Backoff(50) != store.MaxBackoff {
		t.Errorf("backoff does not double up to the cap: %v %v", store.Backoff(2), store.Backoff(50))
	}
}

// Retry-After longer than our own backoff is honoured.
func TestRetryAfterIsHonoured(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7200")
		http.Error(w, "slow down", 429)
	}))
	defer ts.Close()
	s, in := pollServer(t)
	sp := s.space("alice")
	if err := sp.Follow(sp.UID(), ts.URL); err != nil {
		t.Fatal(err)
	}
	s.Refresh(context.Background(), ts.URL)
	due, _ := in.DueFeedURLs(time.Now().Add(time.Hour))
	for _, u := range due {
		if u == ts.URL {
			t.Fatal("due again within the Retry-After")
		}
	}
}
