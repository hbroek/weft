package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestLikeRoundTripAcrossInstances(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	ctx := t.Context()
	ua := alice.post(t, `{"markdown":"RSS plus links already gives you a social web."}`)

	out := bob.call(t, "POST", "/api/likes", `{"target":"`+ua+`"}`, 200)
	if !strings.Contains(out, bob.base+"/likes/1") {
		t.Fatalf("like url: %s", out)
	}
	lf := bob.get("/likes.xml")
	if !strings.Contains(lf, `<weft:likeOf href="`+ua+`">`) || !strings.Contains(lf, `<guid isPermaLink="true">`+bob.base+`/likes/1<`) {
		t.Errorf("likes feed:\n%s", lf)
	}
	if strings.Contains(lf, "polarity") || strings.Contains(lf, "emoji") {
		t.Errorf("a like carries only its target:\n%s", lf)
	}
	if !strings.Contains(bob.get("/feed.xml"), "<weft:likesFeed>"+bob.base+"/likes.xml<") {
		t.Errorf("main feed does not advertise the likes feed")
	}
	page := bob.get("/likes/1")
	if !strings.Contains(page, `class="u-like-of" href="`+ua+`"`) || !strings.Contains(page, bob.base+"/likes.xml") {
		t.Errorf("like page lacks the like-of link or likes feed link:\n%s", page)
	}
	settle(ctx, alice, bob)

	// Alice does not follow Bob: the like is verified but not in the public count.
	got := alice.call(t, "GET", "/api/likes/received?post_id=1", "", 200)
	if !strings.Contains(got, `"source":"`+bob.base+`/likes/1"`) || !strings.Contains(got, `"trusted":false`) {
		t.Fatalf("received: %s", got)
	}
	if f := alice.get("/feed.xml"); !strings.Contains(f, `<weft:likes count="0">`) {
		t.Errorf("untrusted like was counted:\n%s", f)
	}

	// Liking twice changes nothing: same permalink, still one item.
	if out := bob.call(t, "POST", "/api/likes", `{"target":"`+ua+`"}`, 200); !strings.Contains(out, "/likes/1") {
		t.Fatalf("liking again must keep the permalink: %s", out)
	}
	if strings.Count(bob.get("/likes.xml"), "<item>") != 1 {
		t.Errorf("a second like made a second item")
	}

	// Alice follows Bob; the count follows.
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	bob.call(t, "POST", "/api/likes", `{"target":"`+ua+`","target_feed":"`+alice.base+`/feed.xml"}`, 200)
	settle(ctx, alice, bob)
	if f := alice.get("/feed.xml"); !strings.Contains(f, `<weft:likes count="1">`) {
		t.Errorf("count after following:\n%s", f)
	}
	if pg := alice.get(pathOf(ua)); !strings.Contains(pg, "1 like") || !strings.Contains(pg, `title="Liked by Bob"`) {
		t.Errorf("permalink page does not show the count with who liked:\n%s", pg)
	}

	// Bob's timeline shows Alice's count once he refreshes, and his own like.
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
	if tl := bob.call(t, "GET", "/api/timeline", "", 200); !strings.Contains(tl, `"likes":1`) {
		t.Errorf("remote count not in timeline: %s", tl)
	}
	bob.s.st.SetPassword("bob", "correct horse")
	lr := httptest.NewRequest("POST", bob.base+"/login", strings.NewReader(url.Values{"password": {"correct horse"}}.Encode()))
	lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lw := httptest.NewRecorder()
	bob.s.ServeHTTP(lw, lr)
	ck := cookieOf(lw)
	if ck == "" {
		t.Fatalf("bob could not log in: %d", lw.Code)
	}
	if tl := do4(bob, "/timeline", ck); !strings.Contains(tl, `title="Liked by You"`) {
		t.Errorf("hovering the count should name who liked:\n%s", tl)
	}
	if tl := do4(bob, "/timeline", ck); !strings.Contains(tl, `aria-pressed="true"`) || !strings.Contains(tl, `action="/unlike"`) {
		t.Errorf("own like not shown as pressed in timeline")
	}

	// Withdrawing the like: permalink answers 410, feed drops it, Alice forgets it.
	bob.call(t, "DELETE", "/api/likes", `{"target":"`+ua+`"}`, 204)
	bob.call(t, "DELETE", "/api/likes", `{"target":"`+ua+`"}`, 404)
	if c := do(bob.s, "GET", bob.host, "/likes/1", "", "").Code; c != 410 {
		t.Errorf("withdrawn like permalink: %d", c)
	}
	if strings.Contains(bob.get("/likes.xml"), "<item>") {
		t.Errorf("withdrawn like still in feed")
	}
	if tl := do4(bob, "/timeline", ck); !strings.Contains(tl, `aria-pressed="false"`) || !strings.Contains(tl, `action="/like"`) {
		t.Errorf("withdrawn like still shown as pressed in timeline")
	}
	settle(ctx, alice, bob)
	if got := alice.call(t, "GET", "/api/likes/received?post_id=1", "", 200); strings.TrimSpace(got) != "[]" {
		t.Errorf("withdrawn like still received: %s", got)
	}
	if strings.Contains(alice.get(pathOf(ua)), "1 like") {
		t.Errorf("count still shown")
	}

	// Liking again creates a new item.
	if out := bob.call(t, "POST", "/api/likes", `{"target":"`+ua+`"}`, 200); !strings.Contains(out, "/likes/2") {
		t.Errorf("new like after withdrawal: %s", out)
	}
}

func TestLikeValidation(t *testing.T) {
	bob := newInstance(t, "bob")
	bob.call(t, "POST", "/api/likes", `{"target":"javascript:alert(1)"}`, 400)
	bob.call(t, "POST", "/api/likes", `{}`, 400)
	bob.call(t, "POST", "/api/likes", `not json`, 400)
	if c := do(bob.s, "POST", bob.host, "/api/likes", "", `{"target":"http://x.example/p"}`).Code; c != 401 {
		t.Errorf("no token: %d", c)
	}
}

// The routes from before there were only likes still work for one release: a "+" is a like,
// a "-" is refused, and the old paths serve the new pages.
func TestOldReactionRoutes(t *testing.T) {
	bob := newInstance(t, "bob")
	bob.call(t, "POST", "/api/reactions", `{"target":"http://x.example/p","polarity":"-"}`, 400)
	if out := bob.call(t, "POST", "/api/reactions", `{"target":"http://x.example/p","polarity":"+","emoji":"🎉"}`, 200); !strings.Contains(out, "/likes/1") {
		t.Fatalf("old route: %s", out)
	}
	if !strings.Contains(bob.get("/reactions.xml"), `<weft:likeOf href="http://x.example/p">`) {
		t.Errorf("old feed path does not serve the likes feed")
	}
	if c := do(bob.s, "GET", bob.host, "/reactions/1", "", "").Code; c != 200 {
		t.Errorf("old permalink path: %d", c)
	}
	bob.call(t, "DELETE", "/api/reactions", `{"target":"http://x.example/p"}`, 204)
}

func do4(i *instance, path, cookie string) string {
	return do3(i.s, "GET", i.host, path, cookie).Body.String()
}

// Likes by people you follow show up in the timeline when "Show likes" is on, and only then.
func TestTimelineShowsLikesOfFollowed(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	ctx := t.Context()
	ua := alice.post(t, `{"title":"Hello weft","markdown":"RSS plus links already gives you a social web."}`)
	bob.call(t, "POST", "/api/likes", `{"target":"`+ua+`"}`, 200)
	settle(ctx, alice, bob)

	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	alice.s.st.SetPassword("alice", "correct horse")
	lr := httptest.NewRequest("POST", alice.base+"/login", strings.NewReader(url.Values{"password": {"correct horse"}}.Encode()))
	lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lw := httptest.NewRecorder()
	alice.s.ServeHTTP(lw, lr)
	ck := cookieOf(lw)
	if ck == "" {
		t.Fatalf("alice could not log in: %d", lw.Code)
	}

	off := do4(alice, "/timeline", ck)
	if strings.Contains(off, "liked") || !strings.Contains(off, `href="/timeline?likes=1">Show likes<`) {
		t.Errorf("likes must be off by default, with a way to switch them on:\n%s", off)
	}
	on := do4(alice, "/timeline?likes=1", ck)
	if !strings.Contains(on, "liked") || !strings.Contains(on, "your post “Hello weft”") || !strings.Contains(on, `href="`+ua+`"`) || !strings.Contains(on, ">Hide likes<") {
		t.Errorf("likes missing from the timeline:\n%s", on)
	}

	// Your own likes show too, as "You liked …".
	alice.call(t, "POST", "/api/likes", `{"target":"`+ua+`"}`, 200)
	if mine := do4(alice, "/timeline?likes=1", ck); !strings.Contains(mine, "<strong>You</strong> liked") {
		t.Errorf("own like missing from the timeline:\n%s", mine)
	}
	alice.call(t, "DELETE", "/api/likes", `{"target":"`+ua+`"}`, 204)

	// Withdrawn likes disappear after the next refresh.
	bob.call(t, "DELETE", "/api/likes", `{"target":"`+ua+`"}`, 204)
	alice.s.RefreshAll(ctx)
	if strings.Contains(do4(alice, "/timeline?likes=1", ck), "liked") {
		t.Errorf("withdrawn like still shown")
	}
}

func TestLikedByText(t *testing.T) {
	cases := []struct {
		names []string
		total int
		want  string
	}{
		{nil, 3, "Likes from people the author follows"},
		{[]string{"Bob"}, 1, "Liked by Bob"},
		{[]string{"Bob", "Carol"}, 2, "Liked by Bob and Carol"},
		{[]string{"Bob", "Carol", "Dan"}, 3, "Liked by Bob, Carol and Dan"},
		{[]string{"Bob", "Carol"}, 5, "Liked by Bob, Carol and 3 others"},
		{[]string{"Bob"}, 2, "Liked by Bob and 1 other"},
		{[]string{"Bob", "Bob", " "}, 1, "Liked by Bob"},
		{[]string{"You"}, 0, "Liked by You"},
	}
	for _, c := range cases {
		if got := likedBy(c.names, c.total); got != c.want {
			t.Errorf("likedBy(%v, %d) = %q, want %q", c.names, c.total, got, c.want)
		}
	}
}

// Keywords travel in the feed as <category> and show as chips in a follower's timeline.
func TestKeywordsReachFollowers(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	alice.post(t, `{"markdown":"Heading out for pickleball","keywords":["pickleball","open web"]}`)
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
	if tl := bob.call(t, "GET", "/api/timeline", "", 200); !strings.Contains(tl, `"keywords":["pickleball","open web"]`) {
		t.Errorf("keywords missing from the timeline JSON: %s", tl)
	}
	bob.s.st.SetPassword("bob", "correct horse")
	lr := httptest.NewRequest("POST", bob.base+"/login", strings.NewReader(url.Values{"password": {"correct horse"}}.Encode()))
	lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lw := httptest.NewRecorder()
	bob.s.ServeHTTP(lw, lr)
	if tl := do4(bob, "/timeline", cookieOf(lw)); !strings.Contains(tl, `<a href="/timeline?keyword=pickleball" title="Posts with this keyword from everyone you follow">pickleball</a>`) {
		t.Errorf("chips missing from the timeline:\n%s", tl)
	}
}

// A keyword chip filters three ways: an author's site, the owner's timeline, and a topic feed
// that can be followed on its own.
func TestKeywordFilters(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	alice.post(t, `{"markdown":"First game","keywords":["pickleball","open web"]}`)
	alice.post(t, `{"markdown":"Second game","keywords":["Pickleball"]}`)
	alice.post(t, `{"markdown":"Unrelated gophers","keywords":["go"]}`)

	// 1. the author's site
	home := alice.call(t, "GET", "/", "", 200)
	if !strings.Contains(home, `<a class="p-category" href="/?keyword=open%20web">open web</a>`) {
		t.Errorf("chips on the site are not links:\n%s", home)
	}
	page := alice.call(t, "GET", "/?keyword=PICKLEBALL", "", 200)
	if !strings.Contains(page, "First game") || !strings.Contains(page, "Second game") || strings.Contains(page, "Unrelated gophers") {
		t.Errorf("site filter:\n%s", page)
	}
	if !strings.Contains(page, `Posts tagged <strong>PICKLEBALL</strong>`) || !strings.Contains(page, `<link rel="alternate" type="application/rss+xml" href="`+alice.base+`/feed.xml?category=PICKLEBALL">`) {
		t.Errorf("filter heading or topic feed link missing:\n%s", page)
	}
	if none := alice.call(t, "GET", "/?keyword=nothing", "", 200); strings.Contains(none, "First game") {
		t.Errorf("unknown keyword shows posts")
	}

	// 3. the topic feed
	xml := alice.call(t, "GET", "/feed.xml?category=pickleball", "", 200)
	if strings.Count(xml, "<item>") != 2 || strings.Contains(xml, "gophers") || !strings.Contains(xml, "<title>Alice: pickleball</title>") ||
		!strings.Contains(xml, `href="`+alice.base+`/feed.xml?category=pickleball"`) {
		t.Errorf("topic feed:\n%s", xml)
	}
	if all := alice.call(t, "GET", "/feed.xml", "", 200); strings.Count(all, "<item>") != 3 {
		t.Errorf("main feed changed:\n%s", all)
	}

	// following the filtered address follows only the topic
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/?keyword=pickleball"}`, 200)
	if tl := bob.call(t, "GET", "/api/timeline", "", 200); strings.Contains(tl, "gophers") || !strings.Contains(tl, "Second game") {
		t.Errorf("topic follow: %s", tl)
	}

	// 2. bob's timeline: follow the whole feed too, then filter
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/feed.xml"}`, 200)
	bob.s.st.SetPassword("bob", "correct horse")
	lr := httptest.NewRequest("POST", bob.base+"/login", strings.NewReader(url.Values{"password": {"correct horse"}}.Encode()))
	lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lw := httptest.NewRecorder()
	bob.s.ServeHTTP(lw, lr)
	ck := cookieOf(lw)
	all := do4(bob, "/timeline", ck)
	if !strings.Contains(all, "Unrelated gophers") || strings.Contains(all, "Only from") {
		t.Errorf("unfiltered timeline:\n%s", all)
	}
	f := do4(bob, "/timeline?keyword=go&likes=1", ck)
	if !strings.Contains(f, "Unrelated gophers") || strings.Contains(f, "First game") || !strings.Contains(f, "Clear filter") || strings.Contains(f, "Show likes") {
		t.Errorf("timeline filter:\n%s", f)
	}
	// a chip filters the view it is in: the whole timeline, or the one feed being looked at
	one := do4(bob, "/timeline?feed="+url.QueryEscape(alice.base+"/feed.xml"), ck)
	if !regexp.MustCompile(`href="/timeline\?feed=[^"&]+&amp;keyword=go"`).MatchString(one) || strings.Contains(one, `href="/timeline?keyword=go"`) {
		t.Errorf("chip in a single-feed view should keep the feed:\n%s", one)
	}
	if !strings.Contains(all, `href="/timeline?keyword=go"`) {
		t.Errorf("chip in the full timeline should filter everything")
	}
	if o := do4(bob, "/timeline?keyword=pickleball&feed="+url.QueryEscape(alice.base+"/feed.xml"), ck); !strings.Contains(o, "First game") || strings.Contains(o, "gophers") {
		t.Errorf("timeline filter with feed:\n%s", o)
	}
}

func formAs(i *instance, path, cookie, referer string, vals url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://"+i.host+path, strings.NewReader(vals.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	if referer != "" {
		r.Header.Set("Referer", referer)
	}
	w := httptest.NewRecorder()
	i.s.ServeHTTP(w, r)
	return w
}

// Clicking like or unlike in the timeline changes the count at once, before the author has
// counted it, stays right once the author has, and returns to the page it was clicked on.
func TestLikeButtonUpdatesCount(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	ctx := t.Context()
	ua := alice.post(t, `{"markdown":"count me","keywords":["go"]}`)
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
	bob.s.st.SetPassword("bob", "correct horse")
	lr := httptest.NewRequest("POST", bob.base+"/login", strings.NewReader(url.Values{"password": {"correct horse"}}.Encode()))
	lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lw := httptest.NewRecorder()
	bob.s.ServeHTTP(lw, lr)
	ck := cookieOf(lw)
	vals := url.Values{"target": {ua}, "feed": {alice.base + "/feed.xml"}}

	if tl := do4(bob, "/timeline", ck); strings.Contains(tl, `class="tally"`) {
		t.Fatalf("no likes yet:\n%s", tl)
	}
	w := formAs(bob, "/like", ck, "http://"+bob.host+"/timeline?keyword=go", vals)
	if w.Code != 303 || w.Header().Get("Location") != "/timeline?keyword=go" {
		t.Errorf("like should return to the page it came from: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := formAs(bob, "/like", ck, "http://evil.example/x", vals); w.Code != 403 {
		t.Errorf("a like sent from another site must be refused: %d", w.Code)
	}
	formAs(bob, "/like", ck, "", vals) // liking again changes nothing
	if tl := do4(bob, "/timeline", ck); !strings.Contains(tl, "1 like") {
		t.Errorf("count after like (liking twice counts once):\n%s", tl)
	}

	// the author counts it: no double count
	settle(ctx, alice, bob)
	bob.s.RefreshAll(ctx)
	if tl := do4(bob, "/timeline", ck); !strings.Contains(tl, "1 like") || strings.Contains(tl, "2 likes") {
		t.Errorf("count once the author has counted:\n%s", tl)
	}

	formAs(bob, "/unlike", ck, "", vals)
	if tl := do4(bob, "/timeline", ck); strings.Contains(tl, "1 like") {
		t.Errorf("count after unlike:\n%s", tl)
	}
	settle(ctx, alice, bob)
	bob.s.RefreshAll(ctx)
	if tl := do4(bob, "/timeline", ck); strings.Contains(tl, `class="tally"`) {
		t.Errorf("count once the author has dropped it:\n%s", tl)
	}
}

// Clicking a like count lists who liked, with Follow and Unfollow behind each name.
func TestWhoLikedPage(t *testing.T) {
	alice, bob, carol := newInstance(t, "alice"), newInstance(t, "bob"), newInstance(t, "carol")
	ctx := t.Context()
	ua := alice.post(t, `{"markdown":"Heading out for pickleball"}`)
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	bob.call(t, "POST", "/api/likes", `{"target":"`+ua+`"}`, 200)
	carol.call(t, "POST", "/api/likes", `{"target":"`+ua+`"}`, 200)
	settle(ctx, alice, bob, carol)
	ack := loginCookie(t, alice)
	page := "/liked-by?target=" + url.QueryEscape(ua)

	// the count links to the list
	if home := do4(alice, "/", ""); !strings.Contains(home, `<a href="/liked-by?target=`) || !strings.Contains(home, "1 like") {
		t.Errorf("count is not a link to the likers:\n%s", home)
	}

	// the owner sees everyone, counted or not, and can follow or unfollow each
	own := do4(alice, page, ack)
	for _, want := range []string{">Bob<", ">Carol<", "not counted", `action="/following/remove"`, `action="/following"`, `value="` + bob.base + `/feed.xml"`, `value="` + carol.base + `/feed.xml"`} {
		if !strings.Contains(own, want) {
			t.Errorf("owner's list lacks %q:\n%s", want, own)
		}
	}
	// visitors see only the likes that count, with no buttons
	vis := do4(alice, page, "")
	if !strings.Contains(vis, ">Bob<") || strings.Contains(vis, "Carol") || strings.Contains(vis, "<button") {
		t.Errorf("visitor's list:\n%s", vis)
	}

	// following Carol from the list returns to the list, where she now has Unfollow
	w := formAs(alice, "/following", ack, "http://"+alice.host+page, url.Values{"url": {carol.base + "/feed.xml"}})
	if w.Code != 303 || w.Header().Get("Location") != page {
		t.Fatalf("follow from the list: %d %q", w.Code, w.Header().Get("Location"))
	}
	if own := do4(alice, page, ack); strings.Count(own, `action="/following/remove"`) != 2 {
		t.Errorf("both likers should now offer Unfollow:\n%s", own)
	}

	// on a post in a feed Bob follows: himself, and the followed who liked it
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
	bob.call(t, "POST", "/api/following", `{"url":"`+carol.base+`/"}`, 200)
	bob.s.RefreshAll(ctx)
	bck := loginCookie(t, bob)
	bl := do4(bob, page, bck)
	if !strings.Contains(bl, ">You<") || !strings.Contains(bl, ">Carol<") || !strings.Contains(bl, `action="/following/remove"`) {
		t.Errorf("bob's list:\n%s", bl)
	}
	formAs(bob, "/following/remove", bck, "http://"+bob.host+page, url.Values{"url": {carol.base + "/feed.xml"}})
	if bl := do4(bob, page, bck); !strings.Contains(bl, ">Carol<") || !strings.Contains(bl, `action="/following"`) || !strings.Contains(bl, "not followed") || strings.Contains(bl, "not counted") {
		t.Errorf("an unfollowed liker should offer Follow again:\n%s", bl)
	}
	if c := do(bob.s, "GET", bob.host, page, "", "").Code; c == 200 {
		t.Errorf("a list for a remote post is for the owner only")
	}
}
