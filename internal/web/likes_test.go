package web

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/store"
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
	if !strings.Contains(lf, `<weft:likeOf href="`+ua+`" title="RSS plus links already gives you a social web.">`) || !strings.Contains(lf, `<guid isPermaLink="true">`+bob.base+`/likes/1<`) {
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

	// Alice does not follow Bob: the like is verified and counted, and marked as from a stranger.
	got := alice.call(t, "GET", "/api/likes/received?post_id=1", "", 200)
	if !strings.Contains(got, `"source":"`+bob.base+`/likes/1"`) || !strings.Contains(got, `"trusted":false`) {
		t.Fatalf("received: %s", got)
	}
	if f := alice.get("/feed.xml"); !strings.Contains(f, `<weft:likes count="1"`) {
		t.Errorf("a like from a stranger was not counted:\n%s", f)
	}

	// Liking twice changes nothing: same permalink, still one item.
	if out := bob.call(t, "POST", "/api/likes", `{"target":"`+ua+`"}`, 200); !strings.Contains(out, "/likes/1") {
		t.Fatalf("liking again must keep the permalink: %s", out)
	}
	if strings.Count(bob.get("/likes.xml"), "<item>") != 1 {
		t.Errorf("a second like made a second item")
	}

	// Alice follows Bob; the count stays the same, since every like counts.
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	bob.call(t, "POST", "/api/likes", `{"target":"`+ua+`","target_feed":"`+alice.base+`/feed.xml"}`, 200)
	settle(ctx, alice, bob)
	if f := alice.get("/feed.xml"); !strings.Contains(f, `<weft:likes count="1"`) {
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
	bob.s.space("bob").SetPassword("correct horse")
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
	alice.s.space("alice").SetPassword("correct horse")
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
	bob.s.space("bob").SetPassword("correct horse")
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
	bob.s.space("bob").SetPassword("correct horse")
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
	bob.s.space("bob").SetPassword("correct horse")
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
	if w.Code != 303 || w.Header().Get("Location") != "/timeline?keyword=go#"+anchorID(ua) {
		t.Errorf("like should return to the page it came from, at the post: %d %q", w.Code, w.Header().Get("Location"))
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
	if home := do4(alice, "/", ""); !strings.Contains(home, `<a href="/liked-by?target=`) || !strings.Contains(home, "2 likes") {
		t.Errorf("count is not a link to the likers:\n%s", home)
	}

	// the owner sees everyone, counted or not, and can follow or unfollow each
	own := do4(alice, page, ack)
	for _, want := range []string{">Bob<", ">Carol<", `action="/following/remove"`, `action="/following"`, `value="` + bob.base + `/feed.xml"`, `value="` + carol.base + `/feed.xml"`} {
		if !strings.Contains(own, want) {
			t.Errorf("owner's list lacks %q:\n%s", want, own)
		}
	}
	// visitors see everyone too, with no buttons
	vis := do4(alice, page, "")
	if !strings.Contains(vis, ">Bob<") || !strings.Contains(vis, ">Carol<") || strings.Contains(vis, "not counted") || strings.Contains(vis, `action="/following`) {
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
	bob.s.space("bob").Follow(zed.ID, carol.base+"/feed.xml")
	bob.s.RefreshAll(ctx)
	formAs(bob, "/following/remove", bck, "http://"+bob.host+page, url.Values{"url": {carol.base + "/feed.xml"}})
	bl = do4(bob, page, bck)
	// she is no longer someone Bob follows, so she moves to the people he could follow
	if i := strings.Index(bl, "Also liked by"); i < 0 || !strings.Contains(bl[i:], "Carol") || strings.Contains(bl[:i], "Carol") || strings.Contains(bl, "not followed") || !strings.Contains(bl, ">You<") {
		t.Errorf("only people bob follows belong above \"Also liked by\":\n%s", bl)
	}
	if !strings.Contains(bl, `<form method="post" action="/following" style="display:inline"><input type="hidden" name="url" value="`+carol.base+`/feed.xml">`) {
		t.Errorf("a stranger on the list needs a Follow button:\n%s", bl)
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

// Every like received counts, whether or not you follow the sender; following changes only how it is listed.
func TestFollowingRecountsLikes(t *testing.T) {
	alice, carol := newInstance(t, "alice"), newInstance(t, "carol")
	ctx := t.Context()
	ua := alice.post(t, `{"markdown":"Count my likes"}`)
	carol.call(t, "POST", "/api/likes", `{"target":"`+ua+`"}`, 200)
	settle(ctx, alice, carol)
	if !strings.Contains(alice.get("/"), "1 like") {
		t.Fatalf("carol is not followed, but her like counts")
	}

	alice.call(t, "POST", "/api/following", `{"url":"`+carol.base+`/"}`, 200)
	if home := alice.get("/"); !strings.Contains(home, "1 like") {
		t.Errorf("following carol should count her earlier like:\n%s", home)
	}
	alice.call(t, "DELETE", "/api/following", `{"url":"`+carol.base+`/feed.xml"}`, 204)
	if !strings.Contains(alice.get("/"), "1 like") {
		t.Errorf("unfollowing carol must not drop her like from the count")
	}

	// likes recorded before they named their feed point at the likes feed; they recount too
	xs, _ := alice.s.space("alice").LikesInOf(1)
	if len(xs) != 1 {
		t.Fatalf("likes received: %d", len(xs))
	}
	old := xs[0]
	old.FeedURL = carol.base + "/likes.xml"
	alice.s.space("alice").UpsertLikeIn(old)
	alice.call(t, "POST", "/api/following", `{"url":"`+carol.base+`/"}`, 200)
	if !strings.Contains(alice.get("/"), "1 like") {
		t.Errorf("a like recorded from the likes feed should still count")
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
	bob.s.space("bob").UpdateProfile(1, "Bob", "", "https://img.example/bob.png")
	bob.post(t, `{"markdown":"from bob"}`)
	carol.post(t, `{"markdown":"from carol"}`)
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	alice.call(t, "POST", "/api/following", `{"url":"`+carol.base+`/"}`, 200)
	alice.s.space("alice").UpdateProfile(1, "Alice", "", "https://img.example/alice.png")
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
	if w.Code != 303 || w.Header().Get("Location") != "/timeline#"+anchorID(page) {
		t.Fatalf("publish reply: %d %q", w.Code, w.Header().Get("Location"))
	}
	sp := alice.s.space("alice")
	mine, err := sp.RecentPosts(sp.UID(), 1)
	if err != nil || len(mine) != 1 {
		t.Fatal(err)
	}
	postURL := alice.base + "/posts/" + mine[0].Slug

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

func getMD(i *instance, path, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://"+i.host+path, nil)
	r.Header.Set("Accept", "text/markdown")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	i.s.ServeHTTP(w, r)
	return w
}

// The pages a person navigates answer Accept: text/markdown with the content they show, as one
// Markdown document with absolute links, and nothing of the interface (menus, forms, buttons).
func TestPagesAsMarkdown(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	ctx := t.Context()
	bob.post(t, `{"title":"Bob essay","markdown":"Bob wrote **this**","keywords":["essays"]}`)
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	un := alice.post(t, `{"title":"Alice note","markdown":"Alice says *hi*","keywords":["go","open web"]}`)
	ur := alice.post(t, `{"markdown":"A reply with `+"`code`"+`","in_reply_to":"https://blog.example/x","context":{"title":"The web","description":"About links."}}`)
	bob.s.RefreshAll(ctx)
	ack := loginCookie(t, alice)
	bob.call(t, "POST", "/api/likes", `{"target":"`+un+`"}`, 200)
	alice.call(t, "POST", "/api/likes", `{"target":"https://blog.example/x","context":{"title":"The web","description":"About links."}}`, 200)
	settle(ctx, alice, bob)

	check := func(name string, w *httptest.ResponseRecorder, want ...string) string {
		t.Helper()
		body := w.Body.String()
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/markdown") || w.Header().Get("Vary") != "Accept" {
			t.Errorf("%s: %d %q vary=%q\n%s", name, w.Code, w.Header().Get("Content-Type"), w.Header().Get("Vary"), body)
		}
		for _, x := range want {
			if !strings.Contains(body, x) {
				t.Errorf("%s lacks %q:\n%s", name, x, body)
			}
		}
		for _, bad := range []string{"<nav", "<form", "<button", "<div", "<header", "Log out", "Published with Weft"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains interface markup %q:\n%s", name, bad, body)
			}
		}
		return body
	}

	// the home page, for a visitor
	home := check("home", getMD(alice, "/", ""), "# Alice", "## [Alice note]("+alice.base+"/posts/", "Alice says *hi*",
		"Keywords: ["+"go]("+alice.base+"/?keyword=go), [open web]("+alice.base+"/?keyword=open%20web)",
		"In reply to [blog.example](https://blog.example/x)", "> **[The web](https://blog.example/x)**: About links.", "A reply with `code`")
	if strings.Contains(home, "Bob wrote") {
		t.Errorf("home page holds only this user's posts")
	}
	check("filtered home", getMD(alice, "/?keyword=go", ""), "Posts tagged **go**", "Alice says *hi*")
	// the HTML stays the default, and a browser's Accept gets it
	if h := do(alice.s, "GET", alice.host, "/", "", "").Body.String(); !strings.Contains(h, "<article") {
		t.Errorf("html page changed")
	}

	// the owner's pages
	tl := check("timeline", getMD(alice, "/timeline", ack), "# Timeline", "## Bob essay", "Bob wrote **this**", "Keywords: [essays]("+alice.base+"/timeline?keyword=essays)",
		"**[You]("+alice.base+"/)**", "Alice says *hi*")
	if !strings.Contains(tl, "---") {
		t.Errorf("posts should be separated:\n%s", tl)
	}
	check("filtered timeline", getMD(alice, "/timeline?keyword=essays", ack), "Tagged **essays**", "Clear filter", "Bob wrote **this**")
	check("following", getMD(alice, "/following", ack), "# Following", "- ["+"Bob]("+alice.base+"/timeline?feed=", bob.base+"/feed.xml")
	check("replies", getMD(alice, "/replies", ack), "# Replies to your posts")
	th := check("thread", getMD(alice, "/thread?url="+url.QueryEscape(ur), ack), "# Thread", "This branch (2 posts)", "Views: ", "[Whole thread]("+alice.base+"/thread?url=",
		"**[The web](https://blog.example/x)**: About links. (blog.example)")
	if !strings.Contains(th, "> **You**") && !strings.Contains(th, "> **Alice**") {
		t.Errorf("the reply should be quoted under its parent:\n%s", th)
	}
	// who liked, a like, and a removed post
	check("likers", getMD(alice, "/liked-by?target="+url.QueryEscape(un), ack), "# Likes", "of ["+"Alice note]("+un+")", "- ["+"Bob](")
	check("like", getMD(alice, "/likes/1", ""), "# Alice liked", "[https://blog.example/x](https://blog.example/x)", "> **[The web](https://blog.example/x)**: About links.")
	alice.call(t, "DELETE", "/api/posts/1", ``, 204)
	if still := getMD(alice, pathOf(ur), ""); still.Code != 200 {
		t.Errorf("the other post should still answer 200: %d", still.Code)
	}
	slug := strings.TrimPrefix(un, alice.base)
	if gone := getMD(alice, slug, ""); gone.Code != 410 || !strings.HasPrefix(gone.Header().Get("Content-Type"), "text/markdown") || !strings.Contains(gone.Body.String(), "This post was removed by its author.") {
		t.Errorf("removed post: %d %q %s", gone.Code, gone.Header().Get("Content-Type"), gone.Body)
	}
	// pages with no content of their own fall back to HTML, and owner pages still need the login
	if lg := getMD(alice, "/login", ""); !strings.HasPrefix(lg.Header().Get("Content-Type"), "text/html") {
		t.Errorf("login form has no Markdown form: %q", lg.Header().Get("Content-Type"))
	}
	if w := getMD(alice, "/timeline", ""); w.Code != 303 {
		t.Errorf("a visitor must not read the timeline as Markdown: %d", w.Code)
	}
}

func getAccept(i *instance, path, accept string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://"+i.host+path, nil)
	if accept != "" {
		r.Header.Set("Accept", accept)
	}
	w := httptest.NewRecorder()
	i.s.ServeHTTP(w, r)
	return w
}

// The feeds answer Accept: text/markdown with their content as one Markdown document; feed readers,
// which accept anything, keep getting the XML.
func TestFeedsAsMarkdown(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	ctx := t.Context()
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	un := alice.post(t, `{"title":"Alice note","markdown":"Alice says *hi*","keywords":["go","open web"]}`)
	alice.post(t, `{"markdown":"A reply","in_reply_to":"https://blog.example/x","context":{"title":"The web","description":"About links."}}`)
	bob.post(t, `{"markdown":"Bob answers **Alice**","in_reply_to":"`+un+`","in_reply_to_feed":"`+alice.base+`/feed.xml"}`)
	alice.call(t, "POST", "/api/likes", `{"target":"https://blog.example/x","context":{"title":"The web","description":"About links."}}`, 200)
	settle(ctx, alice, bob)

	md := func(name, path string, want ...string) string {
		t.Helper()
		w := getAccept(alice, path, "text/markdown")
		body := w.Body.String()
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/markdown") || w.Header().Get("Vary") != "Accept" {
			t.Errorf("%s: %d %q vary=%q\n%s", name, w.Code, w.Header().Get("Content-Type"), w.Header().Get("Vary"), body)
		}
		for _, x := range want {
			if !strings.Contains(body, x) {
				t.Errorf("%s lacks %q:\n%s", name, x, body)
			}
		}
		if strings.Contains(body, "<item>") || strings.Contains(body, "<rss") {
			t.Errorf("%s is still XML:\n%s", name, body)
		}
		return body
	}
	main := md("feed.xml", "/feed.xml", "# Alice", "## ["+"Alice note]("+un+")", "Alice says *hi*", "Keywords: [go]("+alice.base+"/?keyword=go), [open web]("+alice.base+"/?keyword=open%20web)",
		"1 reply", "In reply to [blog.example](https://blog.example/x)", "> **[The web](https://blog.example/x)**: About links.", "Also: [Likes]("+alice.base+"/likes.xml) · [Following]("+alice.base+"/following.opml)")
	if strings.Contains(main, "Older posts") {
		t.Errorf("one page has no next link")
	}
	md("topic feed", "/feed.xml?category=go", "# Alice: go", "Alice says *hi*")
	md("replies.xml", pathOf(un)+"/replies.xml", "# Replies to: Alice note", "**Bob**", "Bob answers")
	md("likes.xml", "/likes.xml", "# Alice likes", "liked [The web](https://blog.example/x): About links.")
	md("following.opml", "/following.opml", "# Alice follows", "- [Bob]("+bob.base+"/feed.xml)")

	// paging: the older posts are one link away
	for i := 0; i < 101; i++ {
		if _, err := alice.s.space("alice").AddPost(store.NewPost{UserID: 1, Markdown: fmt.Sprintf("filler %d", i), HTML: "<p>f</p>"}); err != nil {
			t.Fatal(err)
		}
	}
	md("paged feed", "/feed.xml", "[Older posts]("+alice.base+"/feed.xml?page=2)")
	md("second page", "/feed.xml?page=2", "[Newer posts]("+alice.base+"/feed.xml)", "[First page]("+alice.base+"/feed.xml)")

	// feed readers and plain requests keep the XML
	for _, accept := range []string{"", "*/*", "application/rss+xml, application/xml;q=0.9, */*;q=0.8", "application/rss+xml"} {
		w := getAccept(alice, "/feed.xml", accept)
		if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/rss+xml") || !strings.Contains(w.Body.String(), "<rss") {
			t.Errorf("Accept %q should get the XML feed: %q", accept, w.Header().Get("Content-Type"))
		}
	}
	if w := getAccept(alice, "/following.opml", "*/*"); !strings.HasPrefix(w.Header().Get("Content-Type"), "text/x-opml") {
		t.Errorf("opml for */*: %q", w.Header().Get("Content-Type"))
	}
}

func TestTimelineBack(t *testing.T) {
	for in, want := range map[string]string{
		"/timeline":                             "/timeline",
		"/timeline?feed=http%3A%2F%2Fa.example": "/timeline?feed=http%3A%2F%2Fa.example",
		"/timeline?likes=1#p-abc":               "/timeline?likes=1",
		"":                                      "",
		"//evil.example/timeline":               "",
		"http://evil.example/timeline":          "",
		"/timeline/../login":                    "",
		"/following":                            "",
		"/timeline\r\nSet-Cookie: x=1":          "",
		"\\\\evil.example":                      "",
	} {
		if got := timelineBack(in); got != want {
			t.Errorf("timelineBack(%q) = %q, want %q", in, got, want)
		}
	}
}

// A reply goes back to the timeline view it was written from, at the post it answers; likes and unlikes stay at
// their post. The timeline rows carry the ids that make that work.
func TestReplyAndLikeReturnToThePost(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	ua := alice.post(t, `{"markdown":"Pickleball today?"}`)
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
	ck := loginCookie(t, bob)
	view := "/timeline?feed=" + url.QueryEscape(alice.base+"/feed.xml")

	tl := do4(bob, view, ck)
	id := anchorID(ua)
	if !strings.Contains(tl, `<article id="`+id+`">`) {
		t.Fatalf("the row has no id:\n%s", tl)
	}
	m := regexp.MustCompile(`href="(/compose\?reply_to=[^"]+)"`).FindStringSubmatch(tl)
	if m == nil || !strings.Contains(html.UnescapeString(m[1]), "&back="+url.QueryEscape(view)) {
		t.Fatalf("the reply link does not remember the view: %v", m)
	}
	// the form carries it
	cf := do4(bob, html.UnescapeString(m[1]), ck)
	if !strings.Contains(cf, `name="back" value="`+html.EscapeString(view)+`"`) {
		t.Errorf("the compose form lost the view:\n%s", cf)
	}

	reply := func(back string) string {
		v := url.Values{"markdown": {"Count me in."}, "reply_to": {ua}, "reply_feed": {alice.base + "/feed.xml"}}
		if back != "" {
			v.Set("back", back)
		}
		w := formAs(bob, "/compose", ck, "", v)
		if w.Code != 303 {
			t.Fatalf("reply: %d", w.Code)
		}
		return w.Header().Get("Location")
	}
	if got := reply(view); got != view+"#"+id {
		t.Errorf("reply from a view returned to %q, want %q", got, view+"#"+id)
	}
	if got := reply(""); got != "/timeline#"+id {
		t.Errorf("reply without a view returned to %q", got)
	}
	for _, evil := range []string{"//evil.example/", "http://evil.example/timeline", "/login"} {
		if got := reply(evil); got != "/timeline#"+id {
			t.Errorf("back=%q returned to %q", evil, got)
		}
	}
	// a post that is not a reply still opens its own page
	w := formAs(bob, "/compose", ck, "", url.Values{"markdown": {"Not a reply."}})
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/posts/") {
		t.Errorf("a plain post went to %q", loc)
	}

	// like and unlike keep the view and return to the post
	vals := url.Values{"target": {ua}, "feed": {alice.base + "/feed.xml"}}
	for _, path := range []string{"/like", "/unlike"} {
		w := formAs(bob, path, ck, bob.base+view, vals)
		if got := w.Header().Get("Location"); w.Code != 303 || got != view+"#"+id {
			t.Errorf("%s returned to %q", path, got)
		}
	}
}

// A post's liked-by feed lists everyone whose like arrived, counted or not, to anyone who asks.
func TestPostLikesFeedListsEveryLiker(t *testing.T) {
	alice, bob, carol := newInstance(t, "alice"), newInstance(t, "bob"), newInstance(t, "carol")
	ctx := t.Context()
	ua := alice.post(t, `{"markdown":"Dark Matter is good"}`)
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	bob.call(t, "POST", "/api/likes", `{"target":"`+ua+`"}`, 200)
	carol.call(t, "POST", "/api/likes", `{"target":"`+ua+`"}`, 200)
	settle(ctx, alice, bob, carol)

	if f := alice.get("/feed.xml"); !strings.Contains(f, `<weft:likes count="2" href="`+ua+`/likes.xml"`) {
		t.Errorf("the feed does not say where the likes are listed:\n%s", f)
	}
	list := do(alice.s, "GET", alice.host, pathOf(ua)+"/likes.xml", "", "")
	body := list.Body.String()
	if list.Code != 200 || !strings.Contains(body, carol.base+"/feed.xml") || !strings.Contains(body, bob.base+"/feed.xml") || !strings.Contains(body, ">Carol<") {
		t.Errorf("liked-by feed: %d\n%s", list.Code, body)
	}
}

// On a followed post the Likes page adds the people the owner does not follow, found through the post's liked-by feed.
func TestLikesPageListsStrangersFromTheLikedByFeed(t *testing.T) {
	alice, bob, carol := newInstance(t, "alice"), newInstance(t, "bob"), newInstance(t, "carol")
	ctx := t.Context()
	ub := bob.post(t, `{"markdown":"I love watching Dark Matter"}`)
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	carol.call(t, "POST", "/api/likes", `{"target":"`+ub+`"}`, 200)
	settle(ctx, alice, bob, carol)
	alice.s.RefreshAll(ctx)

	ack := loginCookie(t, alice)
	page := do4(alice, "/liked-by?target="+url.QueryEscape(ub), ack)
	i := strings.Index(page, "Also liked by")
	if i < 0 || !strings.Contains(page[i:], ">Carol<") || !strings.Contains(page[i:], `value="`+carol.base+`/feed.xml"`) {
		t.Fatalf("no stranger section:\n%s", page)
	}
	if strings.Contains(page, "can't be listed") {
		t.Errorf("the old apology is still shown:\n%s", page)
	}
}

// A page of the web that is not a post can be liked, and its "who liked it" page opens with the reader's own like.
func TestLikedByPageOfAPageThatIsNotAPost(t *testing.T) {
	bob := newInstance(t, "bob")
	page := "http://other.example/"
	bob.call(t, "POST", "/api/likes", `{"target":"`+page+`","context":{"title":"Other site"}}`, 200)
	bob.s.space("bob").SetPassword("correct horse")
	lr := httptest.NewRequest("POST", bob.base+"/login", strings.NewReader(url.Values{"password": {"correct horse"}}.Encode()))
	lr.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	lw := httptest.NewRecorder()
	bob.s.ServeHTTP(lw, lr)
	w := do3(bob.s, "GET", bob.host, "/liked-by?target="+url.QueryEscape(page), cookieOf(lw))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Other site") || !strings.Contains(w.Body.String(), "You") {
		t.Errorf("liked-by of a page: %d\n%s", w.Code, w.Body.String())
	}
}
