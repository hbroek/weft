package web

import (
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
