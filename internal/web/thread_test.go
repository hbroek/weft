package web

import (
	"html"
	"net/http/httptest"
	"net/url"
	"regexp"
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
	if cf := carol.get("/feed.xml"); !strings.Contains(cf, `<weft:root href="`+ua+`"`) || !strings.Contains(cf, `<weft:inReplyTo href="`+ub+`"`) {
		t.Fatalf("carol's reply links:\n%s", cf)
	}
	settle(ctx, all...)

	// Bob approved Carol, so his replies feed carries her
	if rf := bob.get(pathOf(ub) + "/replies.xml"); !strings.Contains(rf, uc) {
		t.Fatalf("bob's replies feed lacks carol's reply:\n%s", rf)
	}
	// Bob's server bubbled the change; Alice's feed now caches Carol one level down
	rf := alice.get(pathOf(ua) + "/replies.xml")
	if !strings.Contains(rf, "<weft:depth>2</weft:depth>") || strings.Count(rf, "<item>") != 2 ||
		!strings.Contains(rf, `<guid isPermaLink="true">`+uc+`<`) || !strings.Contains(rf, `<weft:inReplyTo href="`+ub+`">`) ||
		!strings.Contains(rf, `<weft:feed href="`+carol.base+`/feed.xml">Carol<`) {
		t.Fatalf("alice's replies feed should cache carol's reply under bob's:\n%s", rf)
	}

	// Carol edits: the change travels carol -> bob -> alice with no manual step
	carol.call(t, "PUT", "/api/posts/1", `{"markdown":"Count me in, edited.","in_reply_to":"`+ub+`"}`, 200)
	settle(ctx, all...)
	if rf := alice.get(pathOf(ua) + "/replies.xml"); !strings.Contains(rf, "edited.") {
		t.Errorf("edit did not bubble up to alice:\n%s", rf)
	}

	// Bob un-approves Carol: the cached subtree under him shrinks, and Alice hears about it
	bob.call(t, "POST", "/api/replies/1/status", `{"status":"rejected"}`, 204)
	settle(ctx, all...)
	if rf := alice.get(pathOf(ua) + "/replies.xml"); strings.Contains(rf, uc) || strings.Count(rf, "<item>") != 1 {
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
	ua, ub := replyCycle(t, alice, bob)
	settle(t.Context(), alice, bob)
	// an edit of either post notifies the other; the notifications must still die out
	alice.call(t, "PUT", "/api/posts/1", `{"markdown":"a, edited"}`, 200)
	bob.call(t, "PUT", "/api/posts/1", `{"markdown":"b, edited"}`, 200)
	settle(t.Context(), alice, bob) // panics if the queues never drain
	_, _ = ua, ub
}

// replyCycle makes two posts that claim to reply to each other, which the API cannot produce
// (a reply's parent must exist first) but a hostile feed can. Alice's post is first, then
// Bob replies to it, then Alice's row is rewritten to reply to Bob's.
func replyCycle(t *testing.T, alice, bob *instance) (ua, ub string) {
	t.Helper()
	ua = alice.post(t, `{"markdown":"LOOP-A"}`)
	ub = bob.post(t, `{"markdown":"LOOP-B","in_reply_to":"`+ua+`","in_reply_to_feed":"`+alice.base+`/feed.xml"}`)
	_, err := alice.s.space("alice").RawDBForTests().Exec(`UPDATE posts SET in_reply_to=?, in_reply_to_feed=?, root=?, root_feed=? WHERE id=1`,
		ub, bob.base+"/feed.xml", ua, alice.base+"/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	return
}

// loginCookie logs the instance's user in through the real form and returns the session cookie.
func loginCookie(t *testing.T, i *instance) string {
	t.Helper()
	name := strings.SplitN(i.host, ".", 2)[0]
	if err := i.s.space(name).SetPassword("correct horse"); err != nil {
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
	// the reply carries what its parent said (fetched when the reply was written), so the gap is not blank
	inOrder(t, page, "ROOT-A", ub, "REPLY-B", "REPLY-C")
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
	ua, _ := replyCycle(t, alice, bob)
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	page := threadPageOf(alice, loginCookie(t, alice), ua, "raw")
	if !strings.Contains(page, "LOOP-A") || !strings.Contains(page, "LOOP-B") {
		t.Errorf("posts in a reply loop disappeared:\n%s", page)
	}
}

// A reply to a reply sent without a feed hint (as an API client that only knows the post URL does)
// must still learn the thread root from the parent's own feed, not make the parent the root.
func TestReplyToReplyWithoutFeedHintKeepsRoot(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	ctx := t.Context()
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	ua := alice.post(t, `{"markdown":"Root question"}`)
	ub := bob.post(t, `{"markdown":"First answer","in_reply_to":"`+ua+`","in_reply_to_feed":"`+alice.base+`/feed.xml"}`)
	settle(ctx, alice, bob)

	// Alice answers Bob's reply knowing only its URL
	uf := alice.post(t, `{"markdown":"Follow-up","in_reply_to":"`+ub+`"}`)
	if f := alice.get("/feed.xml"); !strings.Contains(f, `<weft:root href="`+ua+`"`) {
		t.Fatalf("root should stay Alice's first post:\n%s", f)
	}
	settle(ctx, alice, bob)

	// Bob answers Alice's follow-up, again with no feed hint: he must find the root through her page
	bob.post(t, `{"markdown":"Third","in_reply_to":"`+uf+`"}`)
	if f := bob.get("/feed.xml"); strings.Count(f, `<weft:root href="`+ua+`"`) != 2 {
		t.Fatalf("both of Bob's replies should have Alice's first post as root:\n%s", f)
	}
}

// The focused view reads the path to one post (about one fetch per level) and shows nothing beside
// it; the rest of the thread is a click away, however big the rest of the thread is.
func TestThreadFocusReadsOnlyTheBranch(t *testing.T) {
	a, b, c, d, e := newInstance(t, "alice"), newInstance(t, "bob"), newInstance(t, "carol"), newInstance(t, "dana"), newInstance(t, "eric")
	s1, s2, k, viewer := newInstance(t, "sib1"), newInstance(t, "sib2"), newInstance(t, "kid"), newInstance(t, "viewer")
	all := []*instance{a, b, c, d, e, s1, s2, k, viewer}
	ctx := t.Context()
	// every parent follows its repliers, so the replies are approved
	for _, f := range [][2]*instance{{a, b}, {a, s2}, {b, c}, {b, s1}, {c, d}, {d, e}, {s1, k}} {
		f[0].call(t, "POST", "/api/following", `{"url":"`+f[1].base+`/"}`, 200)
	}
	reply := func(from *instance, text, to string, toInst *instance) string {
		return from.post(t, `{"markdown":"`+text+`","in_reply_to":"`+to+`","in_reply_to_feed":"`+toInst.base+`/feed.xml"}`)
	}
	ua := a.post(t, `{"markdown":"ROOT-A is the start"}`)
	settle(ctx, all...)
	ub := reply(b, "CHAIN-B first", ua, a)
	us2 := reply(s2, "SIDE-S2 beside b", ua, a)
	settle(ctx, all...)
	uc := reply(c, "CHAIN-C second", ub, b)
	us1 := reply(s1, "SIDE-S1 beside c", ub, b)
	settle(ctx, all...)
	ud := reply(d, "TARGET-D third", uc, c)
	settle(ctx, all...)
	ue := reply(e, "KIDOFD-E answers d", ud, d)
	reply(k, "DEEP-K under s1", us1, s1)
	settle(ctx, all...)
	_, _ = us2, ue

	ck := loginCookie(t, viewer)
	for _, x := range all {
		x.hits.Store(0)
	}
	page := threadPageOf(viewer, ck, ud, "")
	cost := int64(0)
	for _, x := range all {
		cost += x.hits.Load()
	}
	inOrder(t, page, "ROOT-A", "CHAIN-B", "CHAIN-C", "TARGET-D", "KIDOFD-E")
	for _, no := range []string{"SIDE-S2", "SIDE-S1", "DEEP-K"} {
		if strings.Contains(page, no) {
			t.Errorf("%s is beside the path and must not be shown:\n%s", no, page)
		}
	}
	if !strings.Contains(page, "/thread?url="+url.QueryEscape(ub)) {
		t.Errorf("an ancestor lacks a link that opens its branch:\n%s", page)
	}
	// reply and the other-replies link are icons with labels, not words and an arrow
	if !strings.Contains(page, `class="ico" href="/compose?reply_to=`) || !strings.Contains(page, `aria-label="Reply"`) ||
		!regexp.MustCompile(`aria-label="(\d+ (other )?repl(y|ies)|Other replies|Replies)"`).MatchString(page) ||
		strings.Contains(page, ">Reply</a>") || strings.Contains(page, "&rarr;") || strings.Contains(page, "→") {
		t.Errorf("thread actions should be icons:\n%s", page)
	}
	if cost > 6 {
		t.Errorf("focused view cost %d fetches for a path of 4 posts", cost)
	}
	t.Logf("focused view: %d fetches across %d servers", cost, len(all))

	// opening the closed branch makes it the focus
	if p2 := threadPageOf(viewer, ck, us1, ""); !strings.Contains(p2, "DEEP-K") || !strings.Contains(p2, "ROOT-A") {
		t.Errorf("focus on the sibling should show its reply and the path to the root:\n%s", p2)
	}
}

// A post that has scrolled out of its author's feed (which holds the newest 100) must still be
// readable in a thread: the post's own URL answers as a one-item feed.
func TestThreadFocusFindsPostsOlderThanTheFeedWindow(t *testing.T) {
	alice, bob, viewer := newInstance(t, "alice"), newInstance(t, "bob"), newInstance(t, "viewer")
	ua := alice.post(t, `{"markdown":"OLD-ROOT question"}`)
	ub := bob.post(t, `{"markdown":"OLD-REPLY answer","in_reply_to":"`+ua+`","in_reply_to_feed":"`+alice.base+`/feed.xml"}`)
	for i := 0; i < 101; i++ {
		bob.post(t, `{"markdown":"filler `+strings.Repeat("x", i%7)+`"}`)
		alice.post(t, `{"markdown":"filler"}`)
	}
	if feed := bob.get("/feed.xml"); strings.Contains(feed, ub) {
		t.Fatal("test setup: the reply should have left the feed window")
	}

	// the post as a one-item feed, with its links; browsers and plain fetches still get HTML
	r := httptest.NewRequest("GET", ub, nil)
	r.Header.Set("Accept", "application/rss+xml")
	w := httptest.NewRecorder()
	bob.s.ServeHTTP(w, r)
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/rss+xml") || strings.Count(w.Body.String(), "<item>") != 1 ||
		!strings.Contains(w.Body.String(), `<weft:inReplyTo href="`+ua+`"`) {
		t.Fatalf("one-item feed: %s\n%s", w.Header().Get("Content-Type"), w.Body)
	}
	for _, accept := range []string{"", "text/html,application/xhtml+xml,*/*;q=0.8", "application/rss+xml, application/xml;q=0.9, text/html;q=0.8, */*;q=0.5"} {
		r := httptest.NewRequest("GET", ub, nil)
		r.Header.Set("Accept", accept)
		w := httptest.NewRecorder()
		bob.s.ServeHTTP(w, r)
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
			t.Errorf("Accept %q got %s", accept, w.Header().Get("Content-Type"))
		}
	}

	ck := loginCookie(t, viewer)
	page := threadPageOf(viewer, ck, ub, "")
	if strings.Contains(page, "not known to your reader") {
		t.Fatalf("old post not found:\n%s", page)
	}
	inOrder(t, page, "OLD-ROOT", "OLD-REPLY")
}

// A feed shows the newest 100 posts and links to older pages (RFC 5005), and a new follower reads
// them all. Later polls see only the first page and must not lose the older posts.
func TestPagedFeedGivesNewFollowersTheHistory(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	for i := 0; i < 105; i++ {
		alice.post(t, `{"markdown":"note number `+strings.Repeat("x", i%5)+` `+url.QueryEscape(strings.Repeat("n", i%11))+`"}`)
	}
	// spread the posts over time: the reader tells them apart by their publication second
	if _, err := alice.s.space("alice").RawDBForTests().Exec(`UPDATE posts SET created = 1700000000 + id*60`); err != nil {
		t.Fatal(err)
	}
	first := alice.get("/feed.xml")
	if strings.Count(first, "<item>") != 100 || !strings.Contains(first, `rel="next"`) || strings.Contains(first, `rel="previous"`) {
		t.Fatalf("first page: %d items\n%.600s", strings.Count(first, "<item>"), first)
	}
	second := alice.get("/feed.xml?page=2")
	if strings.Count(second, "<item>") != 5 || !strings.Contains(second, `rel="previous"`) || !strings.Contains(second, `rel="first"`) || strings.Contains(second, `rel="next"`) {
		t.Fatalf("second page:\n%.800s", second)
	}
	if w := do(alice.s, "GET", alice.host, "/feed.xml?page=3", "", ""); w.Code != 404 {
		t.Errorf("page past the end: %d", w.Code)
	}

	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
	count := func() int {
		var n int
		bob.s.st.RawDBForTests().QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n)
		return n
	}
	if n := count(); n != 105 {
		t.Fatalf("new follower cached %d posts, want the whole history of 105", n)
	}
	bob.s.RefreshAll(t.Context()) // a later poll sees page 1 only
	if n := count(); n != 105 {
		t.Errorf("a later poll lost history: %d", n)
	}
	// deleting a post that is still on page 1 removes it from the follower's cache
	alice.call(t, "DELETE", "/api/posts/105", "", 204)
	bob.s.RefreshAll(t.Context())
	if n := count(); n != 104 {
		t.Errorf("deleted post not removed: %d", n)
	}
}

func TestPermalinkAdvertisesItsThreadLinks(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	ua := alice.post(t, `{"markdown":"parent"}`)
	ub := bob.post(t, `{"markdown":"child","in_reply_to":"`+ua+`","in_reply_to_feed":"`+alice.base+`/feed.xml"}`)
	page := html.UnescapeString(bob.get(pathOf(ub)))
	for _, want := range []string{`rel="in-reply-to" href="` + ua + `"`, `rel="replies" type="application/rss+xml" href="` + ub + `/replies.xml"`,
		`rel="alternate" type="application/rss+xml" href="` + ub + `"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %s\n%s", want, page)
		}
	}
	// the user's feed is still the first alternate, which is the one feed discovery finds
	if i, j := strings.Index(page, `href="`+bob.base+`/feed.xml"`), strings.Index(page, `href="`+ub+`"`); i < 0 || j < 0 || i > j {
		t.Errorf("feed discovery link must come first")
	}
}
