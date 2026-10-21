package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hbroek/weft/internal/store"
)

type instance struct {
	ts   *httptest.Server
	s    *Server
	host string // name.localhost:port
	base string
	tok  string
	hits *atomic.Int64 // requests received from other servers, for cost assertions
}

// newInstance starts an independent server (own database, own port) with one user.
func newInstance(t *testing.T, name string) *instance {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), name+".db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(Config{BaseDomain: "localhost", AllowPrivate: true}, st)
	hits := &atomic.Int64{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		s.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	port := ts.URL[strings.LastIndex(ts.URL, ":")+1:]
	tok, err := st.AddUser(name, strings.ToUpper(name[:1])+name[1:], "")
	if err != nil {
		t.Fatal(err)
	}
	host := name + ".localhost:" + port
	return &instance{ts: ts, s: s, host: host, base: "http://" + host, tok: tok, hits: hits}
}

// settle runs every instance's Webmention queues until nothing is left to do, so tests do not
// depend on how many rounds a chain of notifications (including bubble-up) takes. It fails
// if the queues never drain, which would mean a notification loop.
func settle(ctx context.Context, insts ...*instance) {
	for round := 0; round < 20; round++ {
		for _, i := range insts {
			i.s.ProcessQueues(ctx)
		}
		pending := 0
		for _, i := range insts {
			for _, dir := range []string{"out", "in"} {
				q, _ := i.s.st.DueQueue(dir, 100)
				pending += len(q)
			}
		}
		if pending == 0 {
			return
		}
	}
	panic("webmention queues did not settle: notification loop?")
}

func (i *instance) call(t *testing.T, method, path, body string, want int) string {
	t.Helper()
	w := do(i.s, method, i.host, path, i.tok, body)
	if w.Code != want {
		t.Fatalf("%s %s%s: got %d, want %d: %s", method, i.host, path, w.Code, want, w.Body)
	}
	return w.Body.String()
}

func (i *instance) post(t *testing.T, body string) string {
	t.Helper()
	var r struct{ URL string }
	if err := json.Unmarshal([]byte(i.call(t, "POST", "/api/posts", body, 201)), &r); err != nil {
		t.Fatal(err)
	}
	return r.URL
}

func (i *instance) get(path string) string {
	return do(i.s, "GET", i.host, path, "", "").Body.String()
}

func TestReplyRoundTripAcrossInstances(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	ctx := t.Context()

	ua := alice.post(t, `{"markdown":"RSS plus links already gives you a social web."}`)
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)

	// Bob replies; his instance queues a Webmention for Alice's post.
	ub1 := bob.post(t, `{"title":"The reader half","markdown":"Search and threads are missing.","in_reply_to":"`+ua+`","in_reply_to_feed":"`+alice.base+`/feed.xml"}`)
	bf := bob.get("/feed.xml")
	for _, want := range []string{`<weft:inReplyTo href="` + ua + `"`, `<weft:root href="` + ua + `"`, `<weft:context href="` + ua + `"`} {
		if !strings.Contains(bf, want) {
			t.Errorf("bob's feed lacks %s:\n%s", want, bf)
		}
	}
	if !strings.Contains(bob.get(pathOf(ub1)), `class="u-in-reply-to" href="`+ua+`"`) {
		t.Errorf("bob's permalink page must link to the parent")
	}
	settle(ctx, alice, bob) // Bob's queue sends the Webmention; Alice's verifies his page and feed

	// Alice does not follow Bob, so the reply waits for approval.
	if r := alice.call(t, "GET", "/api/replies?status=pending", "", 200); !strings.Contains(r, ub1) {
		t.Fatalf("pending replies: %s", r)
	}
	if strings.Contains(alice.get(pathOf(ua)+"/replies.xml"), "<item>") || strings.Contains(alice.get(pathOf(ua)), "Search and threads") {
		t.Errorf("unapproved reply is visible")
	}
	if !strings.Contains(alice.get("/feed.xml"), `<weft:replies href="`+ua+`/replies.xml" count="0">`) {
		t.Errorf("expected count 0")
	}

	alice.call(t, "POST", "/api/replies/1/status", `{"status":"approved"}`, 204)
	rf := alice.get(pathOf(ua) + "/replies.xml")
	for _, want := range []string{`<weft:repliesTo href="` + ua + `">`, "<weft:depth>2</weft:depth>", "<title>The reader half</title>", `<weft:feed href="` + bob.base + `/feed.xml">`, `<weft:inReplyTo href="` + ua + `">`} {
		if !strings.Contains(rf, want) {
			t.Errorf("replies feed lacks %s:\n%s", want, rf)
		}
	}
	if !strings.Contains(alice.get("/feed.xml"), `count="1"`) || !strings.Contains(alice.get(pathOf(ua)), "Search and threads are missing.") {
		t.Errorf("approved reply not shown")
	}

	// Alice follows Bob, then replies to his reply: the root must still be her own post.
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	alice.post(t, `{"markdown":"Agreed.","in_reply_to":"`+ub1+`"}`)
	af := alice.get("/feed.xml")
	if !strings.Contains(af, `<weft:inReplyTo href="`+ub1+`" feed="`+bob.base+`/feed.xml">`) || !strings.Contains(af, `<weft:root href="`+ua+`"`) {
		t.Errorf("thread links wrong in alice's feed:\n%s", af)
	}

	// A reply from a followed author is approved automatically.
	ub2 := bob.post(t, `{"markdown":"Second thought.","in_reply_to":"`+ua+`"}`)
	settle(ctx, alice, bob)
	if !strings.Contains(alice.get(pathOf(ua)+"/replies.xml"), "Second thought.") {
		t.Fatalf("followed author's reply should be auto-approved")
	}

	// Editing re-notifies; the reply text updates in place.
	bob.call(t, "PUT", "/api/posts/2", `{"markdown":"Second thought, edited.","in_reply_to":"`+ua+`"}`, 200)
	settle(ctx, alice, bob)
	if rf := alice.get(pathOf(ua) + "/replies.xml"); !strings.Contains(rf, "edited.") || strings.Count(rf, "<item>") != 3 {
		t.Errorf("edit not reflected:\n%s", rf)
	}

	// Deleting sends a Webmention; the source now answers 410 and the reply is removed.
	bob.call(t, "DELETE", "/api/posts/2", "", 204)
	settle(ctx, alice, bob)
	// two items remain: Bob's first reply, and Alice's own reply to it, cached one level down
	if rf := alice.get(pathOf(ua) + "/replies.xml"); strings.Contains(rf, ub2) || strings.Count(rf, "<item>") != 2 {
		t.Errorf("deleted reply still listed:\n%s", rf)
	}

	// Un-approving hides a reply again without deleting it.
	alice.call(t, "POST", "/api/replies/1/status", `{"status":"rejected"}`, 204)
	if rf := alice.get(pathOf(ua) + "/replies.xml"); strings.Contains(rf, "<item>") {
		t.Errorf("rejected reply (and the subtree under it) still listed:\n%s", rf)
	}
}

func TestWebmentionEndpointValidation(t *testing.T) {
	alice := newInstance(t, "alice")
	ua := alice.post(t, `{"markdown":"hello"}`)
	send := func(source, target string) int {
		r := httptest.NewRequest("POST", alice.base+"/webmention", strings.NewReader("source="+source+"&target="+target))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		alice.s.ServeHTTP(w, r)
		return w.Code
	}
	if c := send("http://bob.example/r/1", ua); c != 202 {
		t.Errorf("valid: %d", c)
	}
	for name, c := range map[string]int{
		"not a post":   send("http://bob.example/r/1", alice.base+"/feed.xml"),
		"missing post": send("http://bob.example/r/1", alice.base+"/posts/99"),
		"same url":     send(ua, ua),
		"bad source":   send("javascript:alert(1)", ua),
		"other host":   send("http://bob.example/r/1", "http://carol.localhost:1/posts/1"),
	} {
		if c != 400 {
			t.Errorf("%s: got %d, want 400", name, c)
		}
	}
}

// A page that merely links to a post is a plain mention, not a reply.
func TestPlainMentionIsNotAReply(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	ua := alice.post(t, `{"markdown":"hello"}`)
	ub := bob.post(t, `{"markdown":"I disagree with [this](`+ua+`)."}`) // links, but no in_reply_to

	r := httptest.NewRequest("POST", alice.base+"/webmention", strings.NewReader("source="+ub+"&target="+ua))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	alice.s.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("webmention: %d", w.Code)
	}
	alice.s.ProcessQueues(t.Context())
	if got := alice.call(t, "GET", "/api/replies", "", 200); strings.TrimSpace(got) != "[]" {
		t.Errorf("plain mention stored as a reply: %s", got)
	}

	// A source that does not link to the target at all is rejected too.
	uc := bob.post(t, `{"markdown":"unrelated"}`)
	r = httptest.NewRequest("POST", alice.base+"/webmention", strings.NewReader("source="+uc+"&target="+ua))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	alice.s.ServeHTTP(httptest.NewRecorder(), r)
	alice.s.ProcessQueues(t.Context())
	if got := alice.call(t, "GET", "/api/replies", "", 200); strings.TrimSpace(got) != "[]" {
		t.Errorf("unlinked source stored: %s", got)
	}
}
