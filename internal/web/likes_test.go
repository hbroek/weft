package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
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
	if strings.Contains(off, " liked <a") || !strings.Contains(off, `href="/timeline?likes=1">Show likes<`) {
		t.Errorf("likes must be off by default, with a way to switch them on:\n%s", off)
	}
	on := do4(alice, "/timeline?likes=1", ck)
	if !strings.Contains(on, " liked <a") || !strings.Contains(on, "your post “Hello weft”") || !strings.Contains(on, `href="`+ua+`"`) || !strings.Contains(on, ">Hide likes<") {
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
	if strings.Contains(do4(alice, "/timeline?likes=1", ck), " liked <a") {
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
	if !strings.Contains(vis, ">Bob<") || strings.Contains(vis, "Carol") || strings.Contains(vis, `action="/following`) {
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

	// on a post in a feed Bob follows: himself, and the people he follows who liked it
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
	bob.call(t, "POST", "/api/following", `{"url":"`+carol.base+`/"}`, 200)
	bob.s.RefreshAll(ctx)
	bck := loginCookie(t, bob)
	bl := do4(bob, page, bck)
	if !strings.Contains(bl, ">You<") || !strings.Contains(bl, ">Carol<") || !strings.Contains(bl, `action="/following/remove"`) {
		t.Errorf("bob's list:\n%s", bl)
	}
	// another user on the same server follows Carol too: that must not put her on bob's list
	if _, err := bob.s.st.AddUser("zed", "Zed", ""); err != nil {
		t.Fatal(err)
	}
	zed, _ := bob.s.st.UserByName("zed")
	bob.s.st.Follow(zed.ID, carol.base+"/feed.xml")
	bob.s.RefreshAll(ctx)
	formAs(bob, "/following/remove", bck, "http://"+bob.host+page, url.Values{"url": {carol.base + "/feed.xml"}})
	bl = do4(bob, page, bck)
	if strings.Contains(bl, "Carol") || strings.Contains(bl, "not followed") || !strings.Contains(bl, ">You<") {
		t.Errorf("only people bob follows belong on his list:\n%s", bl)
	}
	if c := do(bob.s, "GET", bob.host, page, "", "").Code; c == 200 {
		t.Errorf("a list for a remote post is for the owner only")
	}
}

// Your own posts are in your timeline, marked as yours, and "Hide my posts" takes them out.
func TestOwnPostsInTimeline(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	alice.post(t, `{"markdown":"Alice says hello","keywords":["go"]}`)
	bob.post(t, `{"markdown":"Bob's *note*"}`)
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	ck := loginCookie(t, alice)

	tl := do4(alice, "/timeline", ck)
	if !strings.Contains(tl, "Alice says hello") || !strings.Contains(tl, "<strong>You</strong>") || !strings.Contains(tl, "Bob") {
		t.Fatalf("own post missing from the timeline:\n%s", tl)
	}
	if !strings.Contains(tl, `Hide my posts`) || !strings.Contains(tl, `href="/timeline?mine=0"`) {
		t.Errorf("toggle missing:\n%s", tl)
	}
	// your own post has Edit, and no like button
	if !strings.Contains(tl, "/edit") || strings.Count(tl, `class="likebtn"`) != 1 {
		t.Errorf("own post should have Edit and only Bob's post a like button:\n%s", tl)
	}

	hidden := do4(alice, "/timeline?mine=0", ck)
	if strings.Contains(hidden, "Alice says hello") || !strings.Contains(hidden, "Bob") || !strings.Contains(hidden, "Show my posts") {
		t.Errorf("hidden:\n%s", hidden)
	}
	// the choice carries through the likes toggle
	if !strings.Contains(hidden, `href="/timeline?likes=1&amp;mine=0"`) {
		t.Errorf("likes toggle should keep mine=0:\n%s", hidden)
	}

	// keyword filter applies to them too; one person's page has none
	if f := do4(alice, "/timeline?keyword=go", ck); !strings.Contains(f, "Alice says hello") || strings.Contains(f, "Bob's") {
		t.Errorf("keyword filter:\n%s", f)
	}
	one := do4(alice, "/timeline?feed="+url.QueryEscape(bob.base+"/feed.xml"), ck)
	if strings.Contains(one, "Alice says hello") || strings.Contains(one, "my posts") {
		t.Errorf("one person's page should hold no own posts or toggle:\n%s", one)
	}
}

// Following someone counts the likes they already gave your posts, and unfollowing drops them.
func TestFollowingRecountsLikes(t *testing.T) {
	alice, carol := newInstance(t, "alice"), newInstance(t, "carol")
	ctx := t.Context()
	ua := alice.post(t, `{"markdown":"Count my likes"}`)
	carol.call(t, "POST", "/api/likes", `{"target":"`+ua+`"}`, 200)
	settle(ctx, alice, carol)
	if strings.Contains(alice.get("/"), "1 like") {
		t.Fatalf("carol is not followed, so her like must not count yet")
	}

	alice.call(t, "POST", "/api/following", `{"url":"`+carol.base+`/"}`, 200)
	if home := alice.get("/"); !strings.Contains(home, "1 like") {
		t.Errorf("following carol should count her earlier like:\n%s", home)
	}
	alice.call(t, "DELETE", "/api/following", `{"url":"`+carol.base+`/feed.xml"}`, 204)
	if strings.Contains(alice.get("/"), "1 like") {
		t.Errorf("unfollowing carol should drop her like from the count")
	}

	// likes recorded before they named their feed point at the likes feed; they recount too
	xs, _ := alice.s.st.LikesInOf(1)
	if len(xs) != 1 {
		t.Fatalf("likes received: %d", len(xs))
	}
	old := xs[0]
	old.FeedURL = carol.base + "/likes.xml"
	alice.s.st.UpsertLikeIn(old)
	alice.call(t, "POST", "/api/following", `{"url":"`+carol.base+`/"}`, 200)
	if !strings.Contains(alice.get("/"), "1 like") {
		t.Errorf("a like recorded from the likes feed should count once the sender is followed")
	}
}

// A card's footer is one line, likes on the left and icon actions on the right, and its age
// ("3 hrs ago", exact time in the tooltip) is at the top right.
func TestCardFooterOneLine(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	alice.post(t, `{"markdown":"mine"}`)
	bob.post(t, `{"markdown":"theirs"}`)
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	ck := loginCookie(t, alice)
	foot := regexp.MustCompile(`class="meta"><div class="m-l">.*?</div>\s*<div class="m-r">`)
	top := regexp.MustCompile(`(?s)<div class="card-top">.*?<span class="when">.*?<time[^>]*title="\d{1,2} \w{3} \d{4} at \d\d:\d\d"[^>]*>just now</time>`)
	for name, page := range map[string]string{"timeline": do4(alice, "/timeline", ck), "home": do4(alice, "/", ck)} {
		if !foot.MatchString(page) || strings.Contains(page, `class="m-c"`) {
			t.Errorf("%s: footer should be likes left, actions right:\n%s", name, page)
		}
		if !top.MatchString(page) {
			t.Errorf("%s: age should be at the top right with the exact time as a tooltip:\n%s", name, page)
		}
		if !strings.Contains(page, `aria-label="Edit"`) || strings.Contains(page, ">Edit</a>") {
			t.Errorf("%s: actions should be icons with labels", name)
		}
	}
	tl := do4(alice, "/timeline", ck)
	if !strings.Contains(tl, `aria-label="Reply"`) || !strings.Contains(tl, `class="likebtn"`) {
		t.Errorf("timeline lacks the reply icon or like button")
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		-time.Hour: "just now", 0: "just now", 59 * time.Second: "just now",
		time.Minute: "1 min ago", 5 * time.Minute: "5 mins ago", 59 * time.Minute: "59 mins ago",
		time.Hour: "1 hr ago", 3*time.Hour + 20*time.Minute: "3 hrs ago", 23 * time.Hour: "23 hrs ago",
		24 * time.Hour: "1 day ago", 2*24*time.Hour + time.Hour: "2 days ago", 6 * 24 * time.Hour: "6 days ago",
		7 * 24 * time.Hour: "1 wk ago", 15 * 24 * time.Hour: "2 wks ago",
		30 * 24 * time.Hour: "1 mo ago", 100 * 24 * time.Hour: "3 mos ago",
		365 * 24 * time.Hour: "1 yr ago", 800 * 24 * time.Hour: "2 yrs ago",
	} {
		if got := agoAt(now, now.Add(-d)); got != want {
			t.Errorf("%v ago: %q, want %q", d, got, want)
		}
	}
}

// A followed author's avatar (their feed's channel image) shows on their timeline cards, and the
// first letter of their name does when they have none.
func TestTimelineAvatars(t *testing.T) {
	alice, bob, carol := newInstance(t, "alice"), newInstance(t, "bob"), newInstance(t, "carol")
	bob.s.st.UpdateProfile(1, "Bob", "", "https://img.example/bob.png")
	bob.post(t, `{"markdown":"from bob"}`)
	carol.post(t, `{"markdown":"from carol"}`)
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	alice.call(t, "POST", "/api/following", `{"url":"`+carol.base+`/"}`, 200)
	alice.s.st.UpdateProfile(1, "Alice", "", "https://img.example/alice.png")
	alice.post(t, `{"markdown":"from alice"}`)
	tl := do4(alice, "/timeline", loginCookie(t, alice))
	for _, want := range []string{
		`<span class="av"><img src="https://img.example/bob.png"`,   // bob's feed image
		`<span class="av"><span>C</span></span>`,                    // carol has none: her letter
		`<span class="av"><img src="https://img.example/alice.png"`, // your own row
	} {
		if !strings.Contains(tl, want) {
			t.Errorf("timeline lacks %q:\n%s", want, tl)
		}
	}
}

// A reply to an ordinary web page carries a snapshot of the page (weft:context title, description and
// image), so followers and the thread view can show a card for a parent they cannot look up.
func TestReplyToPageCarriesSnapshot(t *testing.T) {
	alice, bob, carol := newInstance(t, "alice"), newInstance(t, "bob"), newInstance(t, "carol")
	ctx := t.Context()
	page := carol.base + "/about-the-web" // not a Weft post: nothing to look up
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
	ack := loginCookie(t, alice)

	// compose as a reply, as the bookmarklet opens it
	q := url.Values{"page": {page}, "ptitle": {"About <the> web"}, "pdesc": {"A page about the web."}, "pimg": {"https://img.example/p.png"}, "wm": {"1"}}
	form1 := do4(alice, "/compose?"+q.Encode(), ack)
	for _, want := range []string{`name="reply_to" value="` + page + `"`, `name="ptitle" value="About &lt;the&gt; web"`, `class="pcard"`, `src="https://img.example/p.png"`, "<strong>Reply</strong>", "mode=bookmark"} {
		if !strings.Contains(form1, want) {
			t.Errorf("reply form lacks %q:\n%s", want, form1)
		}
	}
	w := formAs(alice, "/compose", ack, "", url.Values{"markdown": {"I disagree with the *premise*"}, "reply_to": {page},
		"ptitle": {"About <the> web"}, "pdesc": {"A page about the web."}, "pimg": {"https://img.example/p.png"}})
	if w.Code != 303 {
		t.Fatalf("publish reply: %d %s", w.Code, w.Body)
	}
	postURL := alice.base + w.Header().Get("Location")

	// the feed says what the parent looked like, as optional attributes
	fx := alice.get("/feed.xml")
	if !regexp.MustCompile(`<weft:context href="` + regexp.QuoteMeta(page) + `"[^>]*title="About &lt;the&gt; web"[^>]*description="A page about the web\."[^>]*image="https://img\.example/p\.png"`).MatchString(fx) {
		t.Errorf("weft:context lacks the snapshot:\n%s", fx)
	}
	// own site and a follower's timeline show the card
	if home := alice.get("/"); !strings.Contains(home, `class="pcard"`) || !strings.Contains(home, "A page about the web.") {
		t.Errorf("own post lacks the parent card:\n%s", home)
	}
	bob.s.RefreshAll(ctx)
	bck := loginCookie(t, bob)
	if tl := do4(bob, "/timeline", bck); !strings.Contains(tl, `class="pcard"`) || !strings.Contains(tl, "About &lt;the&gt; web") || strings.Contains(tl, "About <the>") {
		t.Errorf("follower's timeline lacks the parent card:\n%s", tl)
	}
	// the thread view shows the unreadable root as a card, not a bare address
	if th := threadPageOf(bob, bck, postURL, ""); !strings.Contains(th, `class="pcard"`) || !strings.Contains(th, "A page about the web.") || strings.Contains(th, "not known to your reader") {
		t.Errorf("thread root should be a card:\n%s", th)
	}

	// "Whole thread" shows the reply too: a plain page cannot approve replies, so the known ones stand.
	// The view links all keep the post you came from, so "This branch" comes back to it.
	cur := threadPageOf(bob, bck, postURL, "curated")
	if !strings.Contains(cur, "I disagree with the") || !strings.Contains(cur, `class="pcard"`) || !strings.Contains(cur, "(2 posts)") {
		t.Errorf("whole-thread view should show the page and the reply:\n%s", cur)
	}
	back := `href="/thread?url=` + url.QueryEscape(postURL) + `#focus">This branch</a>`
	for name, v := range map[string]string{"curated": "curated", "raw": "raw"} {
		if pg := threadPageOf(bob, bck, postURL, v); !strings.Contains(pg, back) || !strings.Contains(pg, "(2 posts)") {
			t.Errorf("%s view: This branch must return to the reply and show 2 posts:\n%s", name, pg)
		}
	}
	if br := threadPageOf(bob, bck, postURL, ""); !strings.Contains(br, "(2 posts)") || !strings.Contains(br, `url=`+url.QueryEscape(postURL)+`&amp;view=curated">Whole thread`) {
		t.Errorf("branch view links:\n%s", br)
	}

	// unsafe images and long text are cleaned; the API takes the same snapshot
	out := alice.call(t, "POST", "/api/posts", `{"markdown":"api reply","in_reply_to":"`+page+`","context":{"title":"`+strings.Repeat("t", 300)+`","description":"d","image":"javascript:alert(1)"}}`, 201)
	_ = out
	fx = alice.get("/feed.xml")
	if strings.Contains(fx, "javascript:") || !regexp.MustCompile(`title="t{150,199}…"`).MatchString(fx) {
		t.Errorf("snapshot not cleaned:\n%s", fx)
	}
}

// The bookmarklet's page opens the compose form as a reply when the page takes a Webmention, as a
// bookmark card when it does not, and either can be switched to the other.
func TestComposeBookmarkOrReply(t *testing.T) {
	alice := newInstance(t, "alice")
	ck := loginCookie(t, alice)
	q := func(extra ...string) string {
		v := url.Values{"page": {"https://blog.example/post?id=7"}, "ptitle": {"A [fine] post"}, "pdesc": {"Something worth reading."},
			"pimg": {"https://blog.example/i.jpg"}, "keywords": {"web, reading"}}
		for i := 0; i+1 < len(extra); i += 2 {
			v.Set(extra[i], extra[i+1])
		}
		return "/compose?" + v.Encode()
	}
	bm := do4(alice, q("wm", "0"), ck)
	for _, want := range []string{`value="A [fine] post"`, "![A fine post](https://blog.example/i.jpg)", "&gt; Something worth reading.", "[blog.example](https://blog.example/post?id=7)", `value="web, reading"`, "<strong>Bookmark</strong>", "mode=reply", "mode=like"} {
		if !strings.Contains(bm, want) {
			t.Errorf("bookmark mode lacks %q:\n%s", want, bm)
		}
	}
	if strings.Contains(bm, `name="reply_to"`) {
		t.Errorf("a bookmark is not a reply")
	}
	rp := do4(alice, q("wm", "1"), ck)
	if !strings.Contains(rp, `name="reply_to" value="https://blog.example/post?id=7"`) || !strings.Contains(rp, "<strong>Reply</strong>") || strings.Contains(rp, "![A fine post]") {
		t.Errorf("reply mode:\n%s", rp)
	}
	// the user's choice wins over the guess
	if sw := do4(alice, q("wm", "0", "mode", "reply"), ck); !strings.Contains(sw, `name="reply_to"`) {
		t.Errorf("mode=reply should override wm=0")
	}
	if sw := do4(alice, q("wm", "1", "mode", "bookmark"), ck); strings.Contains(sw, `name="reply_to"`) {
		t.Errorf("mode=bookmark should override wm=1")
	}
	// an unsafe image address is dropped
	if bad := do4(alice, q("wm", "1", "pimg", "javascript:alert(1)"), ck); strings.Contains(bad, "javascript:") {
		t.Errorf("unsafe image kept:\n%s", bad)
	}
}

// A like of an ordinary web page: the bookmarklet's Like mode, a like that carries what the page looked
// like, and Unlike from the likes lines.
func TestLikeANonWeftPage(t *testing.T) {
	alice, bob, carol := newInstance(t, "alice"), newInstance(t, "bob"), newInstance(t, "carol")
	ctx := t.Context()
	page := carol.base + "/about-the-web" // not a Weft post
	ack := loginCookie(t, alice)

	q := url.Values{"page": {page}, "ptitle": {"About the <web>"}, "pdesc": {"A page about the web."}, "pimg": {"https://img.example/p.png"}, "mode": {"like"}}
	lp := do4(alice, "/compose?"+q.Encode(), ack)
	for _, want := range []string{"Like this page", `class="pcard"`, `action="/like"`, `name="from" value="page"`, `name="target" value="` + page + `"`,
		"<strong>Like</strong>", "mode=bookmark", "mode=reply"} {
		if !strings.Contains(lp, want) {
			t.Errorf("like mode lacks %q:\n%s", want, lp)
		}
	}
	vals := url.Values{"target": {page}, "from": {"page"}, "ptitle": {"About the <web>"}, "pdesc": {"A page about the web."}, "pimg": {"https://img.example/p.png"}}
	w := formAs(alice, "/like", ack, "http://"+alice.host+"/compose", vals)
	if w.Code != 303 || w.Header().Get("Location") != "/timeline?likes=1" {
		t.Fatalf("like from the page: %d %q", w.Code, w.Header().Get("Location"))
	}

	// published with what the page looked like, and the like's own page shows it
	lx := alice.get("/likes.xml")
	if !regexp.MustCompile(`<weft:likeOf href="` + regexp.QuoteMeta(page) + `"[^>]*title="About the &lt;web&gt;"[^>]*description="A page about the web\."[^>]*image="https://img\.example/p\.png"`).MatchString(lx) {
		t.Errorf("likes.xml lacks the snapshot:\n%s", lx)
	}
	if lk := alice.get("/likes/1"); !strings.Contains(lk, `class="pcard"`) || !strings.Contains(lk, "A page about the web.") || !strings.Contains(lk, "u-like-of") {
		t.Errorf("like page lacks the card:\n%s", lk)
	}
	// the likes lines say what was liked, and offer Unlike
	mine := do4(alice, "/timeline?likes=1", ack)
	if !strings.Contains(mine, "<strong>You</strong> liked") || !strings.Contains(mine, "“About the &lt;web&gt;” (carol.localhost") || !strings.Contains(mine, `action="/unlike"`) {
		t.Errorf("own like line:\n%s", mine)
	}
	// a follower sees the title too, not just the address
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
	bob.s.RefreshAll(ctx)
	if fl := do4(bob, "/timeline?likes=1", loginCookie(t, bob)); !strings.Contains(fl, "“About the &lt;web&gt;”") {
		t.Errorf("follower's likes line lacks the title:\n%s", fl)
	}
	// liking again with nothing new keeps the one like; the API takes the snapshot and refuses unsafe images
	formAs(alice, "/like", ack, "", url.Values{"target": {page}})
	alice.call(t, "POST", "/api/likes", `{"target":"`+carol.base+`/other","context":{"title":"Other","image":"javascript:alert(1)"}}`, 200)
	lx = alice.get("/likes.xml")
	if strings.Count(lx, "<item>") != 2 || strings.Contains(lx, "javascript:") || !strings.Contains(lx, `title="Other"`) {
		t.Errorf("likes.xml after more likes:\n%s", lx)
	}

	// Unlike from the likes line withdraws it
	formAs(alice, "/unlike", ack, "http://"+alice.host+"/timeline?likes=1", url.Values{"target": {page}})
	if after := alice.get("/likes.xml"); strings.Contains(after, page) {
		t.Errorf("unliked page still in likes.xml:\n%s", after)
	}
	if again := do4(alice, "/timeline?likes=1", ack); strings.Contains(again, "About the &lt;web&gt;") {
		t.Errorf("unliked page still shown")
	}
}
