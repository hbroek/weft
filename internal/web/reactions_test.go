package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestReactionRoundTripAcrossInstances(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	ctx := t.Context()
	ua := alice.post(t, `{"markdown":"RSS plus links already gives you a social web."}`)

	// Bob reacts with a bare "+": the default emoji applies on display.
	out := bob.call(t, "POST", "/api/reactions", `{"target":"`+ua+`","polarity":"+"}`, 200)
	if !strings.Contains(out, bob.base+"/reactions/1") {
		t.Fatalf("reaction url: %s", out)
	}
	rf := bob.get("/reactions.xml")
	if !strings.Contains(rf, `<tc:reactionOf href="`+ua+`" polarity="+">`) || !strings.Contains(rf, `<guid isPermaLink="true">`+bob.base+`/reactions/1<`) {
		t.Errorf("reactions feed:\n%s", rf)
	}
	if !strings.Contains(bob.get("/feed.xml"), "<tc:reactionsFeed>"+bob.base+"/reactions.xml<") {
		t.Errorf("main feed does not advertise the reactions feed")
	}
	page := bob.get("/reactions/1")
	if !strings.Contains(page, `class="u-like-of" href="`+ua+`"`) || !strings.Contains(page, bob.base+"/reactions.xml") {
		t.Errorf("reaction page lacks the like-of link or reactions feed link:\n%s", page)
	}
	bob.s.ProcessQueues(ctx)
	alice.s.ProcessQueues(ctx)

	// Alice does not follow Bob: the reaction is verified but not in the public tally.
	got := alice.call(t, "GET", "/api/reactions/received?post_id=1", "", 200)
	if !strings.Contains(got, `"polarity":"+"`) || !strings.Contains(got, `"emoji":"👍"`) || !strings.Contains(got, `"trusted":false`) {
		t.Fatalf("received: %s", got)
	}
	if f := alice.get("/feed.xml"); !strings.Contains(f, `<tc:reactions plus="0" minus="0">`) {
		t.Errorf("untrusted reaction was tallied:\n%s", f)
	}

	// Alice follows Bob; Bob changes his reaction in place; the tally follows.
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
	out = bob.call(t, "POST", "/api/reactions", `{"target":"`+ua+`","polarity":"-","emoji":"😕"}`, 200)
	if !strings.Contains(out, "/reactions/1") {
		t.Fatalf("changing a reaction must keep its permalink: %s", out)
	}
	if rf := bob.get("/reactions.xml"); !strings.Contains(rf, `polarity="-" emoji="😕"`) || !strings.Contains(rf, "<tc:updated>") || strings.Count(rf, "<item>") != 1 {
		t.Errorf("edited reaction:\n%s", rf)
	}
	bob.s.ProcessQueues(ctx)
	alice.s.ProcessQueues(ctx)
	f := alice.get("/feed.xml")
	if !strings.Contains(f, `<tc:reactions plus="0" minus="1">`) || !strings.Contains(f, `<tc:emoji value="😕" count="1">`) {
		t.Errorf("tally after change:\n%s", f)
	}
	if !strings.Contains(alice.get("/posts/1"), "😕 1") {
		t.Errorf("permalink page does not show the tally")
	}

	// Bob's timeline shows Alice's tally once he refreshes, and his own reaction.
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
	if tl := bob.call(t, "GET", "/api/timeline", "", 200); !strings.Contains(tl, `"reactions_minus":1`) {
		t.Errorf("remote tally not in timeline: %s", tl)
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
	r := func() string { return do4(bob, "/timeline", ck) }
	if !strings.Contains(r(), "You reacted 😕 (-)") {
		t.Errorf("own reaction not shown in timeline")
	}

	// Removing the reaction: permalink answers 410, feed drops it, Alice forgets it.
	bob.call(t, "DELETE", "/api/reactions", `{"target":"`+ua+`"}`, 204)
	bob.call(t, "DELETE", "/api/reactions", `{"target":"`+ua+`"}`, 404)
	if c := do(bob.s, "GET", bob.host, "/reactions/1", "", "").Code; c != 410 {
		t.Errorf("removed reaction permalink: %d", c)
	}
	if strings.Contains(bob.get("/reactions.xml"), "<item>") {
		t.Errorf("removed reaction still in feed")
	}
	bob.s.ProcessQueues(ctx)
	alice.s.ProcessQueues(ctx)
	if got := alice.call(t, "GET", "/api/reactions/received?post_id=1", "", 200); strings.TrimSpace(got) != "[]" {
		t.Errorf("removed reaction still received: %s", got)
	}
	if strings.Contains(alice.get("/posts/1"), "😕") {
		t.Errorf("tally still shown")
	}

	// Reacting again creates a new item.
	if out := bob.call(t, "POST", "/api/reactions", `{"target":"`+ua+`","polarity":"+","emoji":"🎉"}`, 200); !strings.Contains(out, "/reactions/2") {
		t.Errorf("new reaction after removal: %s", out)
	}
}

func TestReactionValidation(t *testing.T) {
	bob := newInstance(t, "bob")
	cases := map[string]string{
		"bad polarity":   `{"target":"http://x.example/p","polarity":"x"}`,
		"empty polarity": `{"target":"http://x.example/p"}`,
		"letters":        `{"target":"http://x.example/p","polarity":"+","emoji":"abc"}`,
		"markup":         `{"target":"http://x.example/p","polarity":"+","emoji":"<b>"}`,
		"too long":       `{"target":"http://x.example/p","polarity":"+","emoji":"😀😀😀😀😀😀😀😀😀"}`,
		"bad target":     `{"target":"javascript:alert(1)","polarity":"+"}`,
		"no target":      `{"polarity":"+"}`,
	}
	for name, body := range cases {
		bob.call(t, "POST", "/api/reactions", body, 400)
		_ = name
	}
	// skin-tone modifiers and ZWJ sequences are fine
	bob.call(t, "POST", "/api/reactions", `{"target":"http://x.example/a","polarity":"+","emoji":"👍🏽"}`, 200)
	bob.call(t, "POST", "/api/reactions", `{"target":"http://x.example/b","polarity":"+","emoji":"👨‍👩‍👧"}`, 200)
	// unauthenticated
	if c := do(bob.s, "POST", bob.host, "/api/reactions", "", `{"target":"http://x.example/p","polarity":"+"}`).Code; c != 401 {
		t.Errorf("no token: %d", c)
	}
}

func do4(i *instance, path, cookie string) string {
	return do3(i.s, "GET", i.host, path, cookie).Body.String()
}
