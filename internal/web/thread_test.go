package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Alice posts A; Bob replies B; Carol replies C to B. Carol's reply is approved by Bob, whose
// server bubbles the change up to Alice, whose replies feed then caches C one level down.
func TestSubtreeCachingAndBubbleUp(t *testing.T) {
	alice, bob, carol := newInstance(t, "alice"), newInstance(t, "bob"), newInstance(t, "carol")
	ctx := t.Context()
	all := []*instance{alice, bob, carol}

	ua := alice.post(t, `{"markdown":"Who builds the reader half?"}`)
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200) // Bob's replies auto-approve
	bob.call(t, "POST", "/api/following", `{"url":"`+carol.base+`/"}`, 200)

	ub := bob.post(t, `{"markdown":"I am.","in_reply_to":"`+ua+`","in_reply_to_feed":"`+alice.base+`/feed.xml"}`)
	settle(ctx, all...)
	uc := carol.post(t, `{"markdown":"Count me in.","in_reply_to":"`+ub+`","in_reply_to_feed":"`+bob.base+`/feed.xml"}`)

	// Carol learned the thread root from Bob's feed, not just her parent
	if cf := carol.get("/feed.xml"); !strings.Contains(cf, `<tc:root href="`+ua+`"`) || !strings.Contains(cf, `<tc:inReplyTo href="`+ub+`"`) {
		t.Fatalf("carol's reply links:\n%s", cf)
	}
	settle(ctx, all...)

	// Bob approved Carol, so his replies feed carries her
	if rf := bob.get("/posts/1/replies.xml"); !strings.Contains(rf, uc) {
		t.Fatalf("bob's replies feed lacks carol's reply:\n%s", rf)
	}
	// Bob's server bubbled the change; Alice's feed now caches Carol one level down
	rf := alice.get("/posts/1/replies.xml")
	if !strings.Contains(rf, "<tc:depth>2</tc:depth>") || strings.Count(rf, "<item>") != 2 ||
		!strings.Contains(rf, `<guid isPermaLink="true">`+uc+`<`) || !strings.Contains(rf, `<tc:inReplyTo href="`+ub+`">`) ||
		!strings.Contains(rf, `<tc:feed href="`+carol.base+`/feed.xml">Carol<`) {
		t.Fatalf("alice's replies feed should cache carol's reply under bob's:\n%s", rf)
	}

	// Carol edits: the change travels carol -> bob -> alice with no manual step
	carol.call(t, "PUT", "/api/posts/1", `{"markdown":"Count me in, edited.","in_reply_to":"`+ub+`"}`, 200)
	settle(ctx, all...)
	if rf := alice.get("/posts/1/replies.xml"); !strings.Contains(rf, "edited.") {
		t.Errorf("edit did not bubble up to alice:\n%s", rf)
	}

	// Bob un-approves Carol: the cached subtree under him shrinks, and Alice hears about it
	bob.call(t, "POST", "/api/replies/1/status", `{"status":"rejected"}`, 204)
	settle(ctx, all...)
	if rf := alice.get("/posts/1/replies.xml"); strings.Contains(rf, uc) || strings.Count(rf, "<item>") != 1 {
		t.Errorf("rejection did not bubble up:\n%s", rf)
	}

	// Carol deletes: nothing left to bubble, and nothing loops
	carol.call(t, "DELETE", "/api/posts/1", "", 204)
	settle(ctx, all...)

	// Bob's reply is rejected by Alice: its (empty) subtree vanishes with it, and Alice's
	// own post is a root, so there is nothing further to bubble to
	alice.call(t, "POST", "/api/replies/1/status", `{"status":"rejected"}`, 204)
	settle(ctx, all...)
}

// Replies that point at each other must not make Webmentions circulate forever.
func TestBubbleUpTerminatesOnCycles(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
	// two posts that claim to reply to each other (a cycle no honest app produces)
	ua := alice.base + "/posts/1"
	ub := bob.base + "/posts/1"
	alice.post(t, `{"markdown":"a","in_reply_to":"`+ub+`"}`)
	bob.post(t, `{"markdown":"b","in_reply_to":"`+ua+`"}`)
	settle(t.Context(), alice, bob) // panics if the queues never drain
}

// loginCookie logs the instance's user in through the real form and returns the session cookie.
func loginCookie(t *testing.T, i *instance) string {
	t.Helper()
	name := strings.SplitN(i.host, ".", 2)[0]
	if err := i.s.st.SetPassword(name, "correct horse"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", i.base+"/login", strings.NewReader(url.Values{"password": {"correct horse"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	i.s.ServeHTTP(w, r)
	if ck := cookieOf(w); ck != "" {
		return ck
	}
	t.Fatalf("login failed: %d", w.Code)
	return ""
}

func threadPageOf(i *instance, cookie, target, view string) string {
	return do3(i.s, "GET", i.host, "/thread?url="+url.QueryEscape(target)+"&view="+view, cookie).Body.String()
}

func inOrder(t *testing.T, page string, parts ...string) {
	t.Helper()
	at := 0
	for _, p := range parts {
		i := strings.Index(page[at:], p)
		if i < 0 {
			t.Errorf("%q missing or out of order in page:\n%s", p, page)
			return
		}
		at += i + len(p)
	}
}

func TestThreadCuratedAndRawViews(t *testing.T) {
	alice, bob, carol, dana := newInstance(t, "alice"), newInstance(t, "bob"), newInstance(t, "carol"), newInstance(t, "dana")
	ctx := t.Context()
	all := []*instance{alice, bob, carol, dana}
	ck := loginCookie(t, alice)

	ua := alice.post(t, `{"markdown":"ROOT-A: who builds the reader half?"}`)
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	bob.call(t, "POST", "/api/following", `{"url":"`+carol.base+`/"}`, 200)
	ub := bob.post(t, `{"markdown":"REPLY-B: I am.","in_reply_to":"`+ua+`","in_reply_to_feed":"`+alice.base+`/feed.xml"}`)
	settle(ctx, all...)
	carol.post(t, `{"markdown":"REPLY-C: count me in.","in_reply_to":"`+ub+`","in_reply_to_feed":"`+bob.base+`/feed.xml"}`)
	dana.post(t, `{"markdown":"REPLY-D: spam spam.","in_reply_to":"`+ua+`","in_reply_to_feed":"`+alice.base+`/feed.xml"}`) // alice does not follow dana
	settle(ctx, all...)

	// asking about any post in the thread gives the same thread, rooted at A
	for _, from := range []string{ua, ub} {
		page := threadPageOf(alice, ck, from, "curated")
		inOrder(t, page, "ROOT-A", "REPLY-B", "REPLY-C")
		if strings.Contains(page, "REPLY-D") {
			t.Errorf("curated view shows an unapproved reply:\n%s", page)
		}
		if n := strings.Count(page, "<ul"); n != 3 { // thread list, replies to A, replies to B
			t.Errorf("curated nesting: %d lists\n%s", n, page)
		}
	}

	raw := threadPageOf(alice, ck, ua, "raw")
	inOrder(t, raw, "ROOT-A", "REPLY-B", "REPLY-C")
	if !strings.Contains(raw, "REPLY-D") || !strings.Contains(raw, "not approved") {
		t.Errorf("raw view should show the unapproved reply, flagged:\n%s", raw)
	}

	// approving Dana moves her into the curated view
	alice.call(t, "GET", "/api/replies?status=pending", "", 200)
	alice.call(t, "POST", "/api/replies/2/status", `{"status":"approved"}`, 204)
	if page := threadPageOf(alice, ck, ua, "curated"); !strings.Contains(page, "REPLY-D") {
		t.Errorf("approved reply missing from curated view")
	}

	// the thread view is for the logged-in owner only
	if w := do(alice.s, "GET", alice.host, "/thread?url="+url.QueryEscape(ua), "", ""); w.Code != 303 {
		t.Errorf("anonymous thread view: %d", w.Code)
	}
}

// orphanSetup builds: A (alice) <- B (bob, never announced to alice) <- C (carol, followed by alice).
// Alice therefore sees C but not its parent B.
func orphanSetup(t *testing.T) (alice, bob, carol *instance, ua, ub string) {
	alice, bob, carol = newInstance(t, "alice"), newInstance(t, "bob"), newInstance(t, "carol")
	ctx := t.Context()
	ua = alice.post(t, `{"markdown":"ROOT-A"}`)
	ub = bob.post(t, `{"markdown":"REPLY-B","in_reply_to":"`+ua+`","in_reply_to_feed":"`+alice.base+`/feed.xml"}`)
	// bob's queue is deliberately not run, so alice never hears about B
	carol.post(t, `{"markdown":"REPLY-C","in_reply_to":"`+ub+`","in_reply_to_feed":"`+bob.base+`/feed.xml"}`)
	settle(ctx, carol)
	alice.call(t, "POST", "/api/following", `{"url":"`+carol.base+`/"}`, 200)
	return
}

func TestThreadOrphanParentExistsButUnknown(t *testing.T) {
	alice, _, _, ua, ub := orphanSetup(t)
	page := threadPageOf(alice, loginCookie(t, alice), ua, "raw")
	inOrder(t, page, "ROOT-A", "exists but is not known", ub, "REPLY-C")
	if !strings.Contains(page, `href="/compose?reply_to=`) {
		t.Errorf("no reply links in thread view")
	}
}

func TestThreadOrphanParentDeletedLeavesTombstone(t *testing.T) {
	alice, bob, _, ua, ub := orphanSetup(t)
	bob.call(t, "DELETE", "/api/posts/1", "", 204)
	page := threadPageOf(alice, loginCookie(t, alice), ua, "raw")
	inOrder(t, page, "ROOT-A", "original post removed", "REPLY-C")
	if strings.Contains(page, "REPLY-B") {
		t.Errorf("deleted post's content still shown")
	}
	_ = ub
}

func TestThreadOrphanParentOfflineFallsBackToRoot(t *testing.T) {
	alice, bob, _, ua, ub := orphanSetup(t)
	bob.ts.Close()
	page := threadPageOf(alice, loginCookie(t, alice), ua, "raw")
	inOrder(t, page, "ROOT-A", "REPLY-C", "parent unavailable: "+ub)
	if strings.Contains(page, "original post removed") {
		t.Errorf("an offline parent must not be shown as deleted")
	}
}

func TestThreadReplyLoopDoesNotVanish(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	ua, ub := alice.base+"/posts/1", bob.base+"/posts/1"
	alice.post(t, `{"markdown":"LOOP-A","in_reply_to":"`+ub+`","in_reply_to_feed":"`+bob.base+`/feed.xml"}`)
	bob.post(t, `{"markdown":"LOOP-B","in_reply_to":"`+ua+`","in_reply_to_feed":"`+alice.base+`/feed.xml"}`)
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	page := threadPageOf(alice, loginCookie(t, alice), ua, "raw")
	if !strings.Contains(page, "LOOP-A") || !strings.Contains(page, "LOOP-B") {
		t.Errorf("posts in a reply loop disappeared:\n%s", page)
	}
}
