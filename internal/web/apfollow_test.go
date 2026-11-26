package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// bobPosts makes the fake remote server answer WebFinger and show an outbox with a post, a post for followers only,
// a boost, and a post by somebody else.
func bobPosts(t *testing.T, rem *remote) {
	t.Helper()
	apScheme = "http"
	t.Cleanup(func() { apScheme = "https" })
	pub := `["https://www.w3.org/ns/activitystreams#Public"]`
	note := func(n int, to, content, extra string) string {
		return fmt.Sprintf(`{"id":%q,"type":"Note","attributedTo":%q,"published":"2026-11-20T10:00:0%dZ","content":%q,"to":%s%s}`,
			rem.srv.URL+fmt.Sprintf("/users/bob/statuses/%d", n), rem.actor(), n, content, to, extra)
	}
	rem.extra = func(w http.ResponseWriter, q *http.Request) bool {
		switch q.URL.Path {
		case "/.well-known/webfinger":
			fmt.Fprintf(w, `{"subject":"acct:bob@x","links":[{"rel":"self","type":"application/activity+json","href":%q}]}`, rem.actor())
		case "/users/bob/outbox":
			w.Header().Set("Content-Type", "application/activity+json")
			if q.URL.Query().Get("page") == "" {
				fmt.Fprintf(w, `{"type":"OrderedCollection","totalItems":4,"first":%q}`, rem.actor()+"/outbox?page=true")
				return true
			}
			create := func(n string) string {
				return fmt.Sprintf(`{"type":"Create","actor":%q,"published":"2026-11-20T10:00:01Z","to":%s,"object":%s}`, rem.actor(), pub, n)
			}
			hello := note(1, pub, `<p>Hello <b>world</b> &lt;script&gt;alert(1)&lt;/script&gt; #go</p>`,
				`,"url":"`+rem.srv.URL+`/@bob/1","tag":[{"type":"Hashtag","name":"#go"}],"attachment":[{"type":"Document","mediaType":"image/png","url":"`+rem.srv.URL+`/pic.png","name":"a pic"}]`)
			private := note(2, `["`+rem.actor()+`/followers"]`, `<p>for followers only</p>`, ``)
			stranger := strings.Replace(note(3, pub, `<p>by carol</p>`, ``), `"attributedTo":"`+rem.actor()+`"`, `"attributedTo":"`+rem.srv.URL+`/users/carol"`, 1)
			boost := fmt.Sprintf(`{"type":"Announce","actor":%q,"published":"2026-11-21T10:00:00Z","to":%s,"object":%q}`, rem.actor(), pub, rem.srv.URL+"/users/bob/statuses/9")
			fmt.Fprintf(w, `{"type":"OrderedCollectionPage","orderedItems":[%s,%s,%s,%s]}`, boost, create(hello), create(private), create(stranger))
		default:
			return false
		}
		return true
	}
}

func timelineOf(s *Server, cookie string) string {
	return send(s, "GET", alice, "/timeline", cookie, nil).Body.String()
}

func TestFollowingAnAccountOnTheFediverse(t *testing.T) {
	a, _ := testKeys(t)
	s, cookie := inboxServer(t)
	rem := newRemote(t, a)
	bobPosts(t, rem)
	handle := "@bob@" + strings.TrimPrefix(rem.srv.URL, "http://")

	if w := send(s, "POST", alice, "/following", cookie, url.Values{"url": {handle}}); w.Code != 303 {
		t.Fatalf("follow: %d %s", w.Code, w.Body)
	}
	// a Follow goes to bob, signed by the account, naming bob
	flush(s)
	_, body := take(t, rem)
	var f struct{ Type, ID, Actor, Object string }
	json.Unmarshal(body, &f)
	id := "http://" + alice + "/"
	if f.Type != "Follow" || f.Actor != id || f.Object != rem.actor() || !strings.HasPrefix(f.ID, id+"#follows/") {
		t.Fatalf("follow: %s", body)
	}
	if c := apGet(s, alice, "/following", activityJSON); !strings.Contains(c.Body.String(), `"totalItems":1`) {
		t.Errorf("following: %s", c.Body)
	}

	// the timeline has bob's public posts and his boost, and not what was for followers, or what was somebody else's
	tl := timelineOf(s, cookie)
	for _, want := range []string{"Hello", "<strong>world</strong>", "&lt;script&gt;", "Bob B", "Boosted from", "A post of bob", `src="` + rem.srv.URL + `/pic.png"`} {
		if !strings.Contains(tl, want) {
			t.Errorf("the timeline lacks %q", want)
		}
	}
	for _, not := range []string{"for followers only", "by carol", "<script>alert"} {
		if strings.Contains(tl, not) {
			t.Errorf("the timeline has %q", not)
		}
	}

	// his server accepts, and then sends what he posts
	accept, _ := json.Marshal(map[string]any{"type": "Accept", "actor": rem.actor(), "object": map[string]any{"id": f.ID, "type": "Follow"}})
	if w := rem.post(s, accept, a); w.Code != 202 {
		t.Fatalf("accept: %d", w.Code)
	}
	sp := s.space("alice")
	if got, err := sp.APFollowingGet(sp.UID(), rem.actor()); err != nil || !got.Accepted {
		t.Errorf("following: %+v %v", got, err)
	}
	push := func(act map[string]any) int {
		act["actor"] = rem.actor()
		b, _ := json.Marshal(act)
		return rem.post(s, b, a).Code
	}
	pub := []string{publicAudience}
	post := func(n int, text string, to []string) map[string]any {
		return map[string]any{"id": fmt.Sprintf("%s/users/bob/statuses/%d", rem.srv.URL, 100+n), "type": "Note", "attributedTo": rem.actor(), "content": "<p>" + text + "</p>", "to": to, "published": time.Now().Format(time.RFC3339)}
	}
	if c := push(map[string]any{"type": "Create", "to": pub, "object": post(1, "pushed news", pub)}); c != 202 {
		t.Fatalf("create: %d", c)
	}
	push(map[string]any{"type": "Create", "to": []string{}, "object": post(2, "pushed secret", []string{rem.actor() + "/followers"})})
	if tl := timelineOf(s, cookie); !strings.Contains(tl, "pushed news") || strings.Contains(tl, "pushed secret") {
		t.Errorf("pushed posts: news=%v secret=%v", strings.Contains(tl, "pushed news"), strings.Contains(tl, "pushed secret"))
	}
	push(map[string]any{"type": "Delete", "object": rem.srv.URL + "/users/bob/statuses/101"})
	if tl := timelineOf(s, cookie); strings.Contains(tl, "pushed news") {
		t.Error("a removed post is still in the timeline")
	}

	// unfollowing sends an Undo of that Follow and leaves the timeline
	if w := send(s, "POST", alice, "/following/remove", cookie, url.Values{"url": {rem.actor()}}); w.Code != 303 {
		t.Fatalf("unfollow: %d", w.Code)
	}
	flush(s)
	_, body = take(t, rem)
	var u struct {
		Type   string
		Object struct{ ID, Type, Object string }
	}
	json.Unmarshal(body, &u)
	if u.Type != "Undo" || u.Object.ID != f.ID || u.Object.Type != "Follow" || u.Object.Object != rem.actor() {
		t.Errorf("undo: %s", body)
	}
	if tl := timelineOf(s, cookie); strings.Contains(tl, "Hello") {
		t.Error("an account that is not followed is in the timeline")
	}
	if c := apGet(s, alice, "/following", activityJSON); !strings.Contains(c.Body.String(), `"totalItems":0`) {
		t.Errorf("following: %s", c.Body)
	}
	// a post pushed by somebody who is not followed is not shown
	if c := push(map[string]any{"type": "Create", "to": pub, "object": post(3, "unwanted", pub)}); c != 202 || strings.Contains(timelineOf(s, cookie), "unwanted") {
		t.Errorf("a post from an account that is not followed: %d", c)
	}
}

func TestFollowingNeedsActivityPubOn(t *testing.T) {
	s, cookie := inboxServer(t)
	send(s, "POST", alice, "/profile/activitypub", cookie, url.Values{"enable": {"0"}})
	w := send(s, "POST", alice, "/following", cookie, url.Values{"url": {"@bob@example.org"}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Switch ActivityPub on") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}

func TestFediHandle(t *testing.T) {
	for in, ok := range map[string]bool{"@bob@mastodon.social": true, "bob@mastodon.social": true, "@bob@127.0.0.1:8080": true,
		"https://mastodon.social/@bob": false, "bob": false, "@bob@": false, "http://a.b/x@y.z": false} {
		if _, _, got := fediHandle(in); got != ok {
			t.Errorf("%q: %v", in, got)
		}
	}
}
