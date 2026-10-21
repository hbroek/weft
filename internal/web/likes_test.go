package web

import (
	"net/http/httptest"
	"net/url"
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
	if !strings.Contains(alice.get(pathOf(ua)), "1 like") {
		t.Errorf("permalink page does not show the count")
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
