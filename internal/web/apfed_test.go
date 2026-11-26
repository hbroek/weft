package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/fetch"
)

// federated is an account with ActivityPub on and one follower, bob, whose Accept has been taken.
func federated(t *testing.T) (*Server, string, *remote) {
	t.Helper()
	a, _ := testKeys(t)
	s, tok := setup(t)
	s.cfg.ActivityPub = true
	s.web = fetch.New(true)
	cookie := ownerCookie(t, s, "alice")
	if w := send(s, "POST", alice, "/profile/activitypub", cookie, url.Values{"enable": {"1"}}); w.Code != 303 {
		t.Fatalf("switch on: %d", w.Code)
	}
	rem := newRemote(t, a)
	if w := rem.post(s, followBody(rem, "1"), a); w.Code != 202 {
		t.Fatalf("follow: %d", w.Code)
	}
	flush(s)
	if _, body := take(t, rem); !strings.Contains(string(body), `"Accept"`) {
		t.Fatalf("no Accept: %s", body)
	}
	return s, tok, rem
}

// take waits for the next request to the remote's inboxes.
func take(t *testing.T, rem *remote) (*http.Request, []byte) {
	t.Helper()
	select {
	case r := <-rem.got:
		return r, <-rem.bodies
	case <-time.After(3 * time.Second):
		t.Fatal("nothing was delivered")
		return nil, nil
	}
}

// nothing says that no more requests came.
func nothing(t *testing.T, rem *remote, what string) {
	t.Helper()
	select {
	case r := <-rem.got:
		b := <-rem.bodies
		t.Errorf("%s: something was delivered to %s: %s", what, r.URL.Path, b)
	default:
	}
}

type delivered struct {
	Type, ID, Actor string
	Object          struct {
		ID, Type, AttributedTo, Content, Name, URL, Published, Updated string
		To, CC                                                         []string
		Tag                                                            []struct{ Type, Href, Name string }
		Attachment                                                     []struct{ Type, MediaType, URL, Name string }
	}
}

func decode(t *testing.T, b []byte) delivered {
	t.Helper()
	var d delivered
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return d
}

func createPost(t *testing.T, s *Server, tok, body string) string {
	t.Helper()
	w := do(s, "POST", alice, "/api/posts", tok, body)
	if w.Code != 201 {
		t.Fatalf("post: %d %s", w.Code, w.Body)
	}
	return pathOf(w.Header().Get("Location"))
}

func TestAPostGoesToFollowersAndItsEditsAndRemovalFollow(t *testing.T) {
	s, tok, rem := federated(t)
	id := "http://" + alice + "/"

	path := createPost(t, s, tok, `{"title":"Hello","markdown":"Short *post* with ![a pic](/media/p.jpg).","keywords":["Indie Web","go"]}`)
	flush(s)
	req, body := take(t, rem)
	if req.URL.Path != "/inbox" { // the shared inbox, not bob's own
		t.Errorf("delivered to %s", req.URL.Path)
	}
	// signed by the account, properly
	sig, err := checkSignedRequest(req, body, time.Now())
	if err != nil || sig.keyID != id+"#main-key" || sig.verify(req, apPublicPEM(t, s)) != nil {
		t.Errorf("the delivery is not properly signed: %v", err)
	}
	d := decode(t, body)
	permalink := strings.TrimSuffix(id, "/") + path
	if d.Type != "Create" || d.Actor != id || d.ID != permalink+"#create" || d.Object.Type != "Note" || d.Object.ID != permalink || d.Object.AttributedTo != id || d.Object.URL != permalink {
		t.Fatalf("create: %s", body)
	}
	if strings.Contains(d.Object.Content, permalink) { // no link back: it would give the post a card that repeats it
		t.Errorf("the content links to the post: %s", d.Object.Content)
	}
	if d.Object.Name != "Hello" || !strings.Contains(d.Object.Content, "<strong>Hello</strong>") || !strings.Contains(d.Object.Content, "Short <em>post</em>") || strings.Contains(d.Object.Content, "<img") ||
		!strings.Contains(d.Object.Content, `class="mention hashtag"`) {
		t.Errorf("content: %s", d.Object.Content)
	}
	if len(d.Object.To) != 1 || d.Object.To[0] != publicAudience || len(d.Object.CC) != 1 || d.Object.CC[0] != id+"followers" {
		t.Errorf("audience: %v %v", d.Object.To, d.Object.CC)
	}
	if len(d.Object.Tag) != 2 || d.Object.Tag[0].Name != "#IndieWeb" || d.Object.Tag[0].Type != "Hashtag" || d.Object.Tag[1].Name != "#go" {
		t.Errorf("tags: %+v", d.Object.Tag)
	}
	if len(d.Object.Attachment) != 1 || d.Object.Attachment[0].URL != strings.TrimSuffix(id, "/")+"/media/p.jpg" || d.Object.Attachment[0].MediaType != "image/jpeg" || d.Object.Attachment[0].Name != "a pic" {
		t.Errorf("attachment: %+v", d.Object.Attachment)
	}
	nothing(t, rem, "one follower, one delivery")

	// the post's address answers as the Note, for those who look it up, and the outbox lists it
	n := apGet(s, alice, path, activityJSON)
	if n.Code != 200 || !strings.HasPrefix(n.Header().Get("Content-Type"), "application/activity+json") || !strings.Contains(n.Body.String(), `"type":"Note"`) || !strings.Contains(n.Body.String(), `"@context"`) {
		t.Errorf("note: %d %s", n.Code, n.Body)
	}
	if h := apGet(s, alice, path, "text/html"); !strings.HasPrefix(h.Header().Get("Content-Type"), "text/html") {
		t.Errorf("the page: %q", h.Header().Get("Content-Type"))
	}
	if o := apGet(s, alice, "/outbox", activityJSON); !strings.Contains(o.Body.String(), `"totalItems":1`) {
		t.Errorf("outbox: %s", o.Body)
	}
	if o := apGet(s, alice, "/outbox?page=true", activityJSON); !strings.Contains(o.Body.String(), `"OrderedCollectionPage"`) || !strings.Contains(o.Body.String(), permalink+"#create") {
		t.Errorf("outbox page: %s", o.Body)
	}

	// an edit is an Update, with the new text and a time
	if w := do(s, "PUT", alice, "/api/posts/1", tok, `{"title":"Hello","markdown":"Changed words.","keywords":["go"]}`); w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	flush(s)
	_, body = take(t, rem)
	u := decode(t, body)
	if u.Type != "Update" || !strings.HasPrefix(u.ID, permalink+"#update-") || u.Object.ID != permalink || !strings.Contains(u.Object.Content, "Changed words.") || u.Object.Updated == "" {
		t.Errorf("update: %s", body)
	}

	// the outbox shows the post as it is now, and the words that were edited out stay nowhere in the account's file
	if o := apGet(s, alice, "/outbox?page=true", activityJSON).Body.String(); !strings.Contains(o, "Changed words.") || strings.Contains(o, "Short <em>post</em>") {
		t.Errorf("the outbox after an edit: %s", o)
	}
	if b := s.space("alice"); b.APActivityBody(b.UID(), permalink+"#create") == "" || strings.Contains(b.APActivityBody(b.UID(), permalink+"#create"), "Short <em>post</em>") {
		t.Errorf("the stored Create still has the old words")
	}

	// removing it is a Delete of a Tombstone, and nothing of the post stays
	if w := do(s, "DELETE", alice, "/api/posts/1", tok, ""); w.Code != 204 && w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	flush(s)
	_, body = take(t, rem)
	x := decode(t, body)
	if x.Type != "Delete" || x.ID != permalink+"#delete" || x.Object.ID != permalink || x.Object.Type != "Tombstone" || strings.Contains(string(body), "Changed words") {
		t.Errorf("delete: %s", body)
	}
	if g := apGet(s, alice, path, activityJSON); g.Code != 410 || !strings.Contains(g.Body.String(), `"Tombstone"`) {
		t.Errorf("a removed post: %d %s", g.Code, g.Body)
	}
	if o := apGet(s, alice, "/outbox", activityJSON); !strings.Contains(o.Body.String(), `"totalItems":0`) {
		t.Errorf("outbox after removal: %s", o.Body)
	}
	sp := s.space("alice")
	if b := sp.APActivityBody(sp.UID(), permalink+"#create"); b != "" {
		t.Errorf("the Create of a removed post is kept: %s", b)
	}
}

func TestOnlyPostsOfYourOwnGoOut(t *testing.T) {
	s, tok, rem := federated(t)
	// a reply and a repost are not sent: the fediverse cannot reply to a page that is not one of its posts
	createPost(t, s, tok, `{"markdown":"a reply","in_reply_to":"http://other.example/p"}`)
	createPost(t, s, tok, `{"markdown":"a repost","canonical":"http://other.example/q"}`)
	flush(s)
	nothing(t, rem, "reply and repost")
	if g := apGet(s, alice, "/posts/", activityJSON); g.Code == 200 {
		t.Errorf("the posts address")
	}
	// ...nor is their edit or removal
	do(s, "DELETE", alice, "/api/posts/1", tok, "")
	flush(s)
	nothing(t, rem, "removal of a reply")
}

func TestAPostMadeBeforeActivityPubWasOnIsNotSentOrEdited(t *testing.T) {
	a, _ := testKeys(t)
	s, tok := setup(t)
	s.cfg.ActivityPub = true
	s.web = fetch.New(true)
	path := createPost(t, s, tok, `{"markdown":"old news"}`) // before the account is on the fediverse
	cookie := ownerCookie(t, s, "alice")
	send(s, "POST", alice, "/profile/activitypub", cookie, url.Values{"enable": {"1"}})
	rem := newRemote(t, a)
	rem.post(s, followBody(rem, "1"), a)
	flush(s)
	take(t, rem) // the Accept
	if w := do(s, "PUT", alice, "/api/posts/1", tok, `{"markdown":"older news"}`); w.Code != 200 {
		t.Fatalf("edit: %d", w.Code)
	}
	flush(s)
	nothing(t, rem, "edit of an old post")
	if n := apGet(s, alice, path, activityJSON); !strings.HasPrefix(n.Header().Get("Content-Type"), "text/html") {
		t.Errorf("an old post answers as a Note: %q", n.Header().Get("Content-Type"))
	}
	// and an account that is not on the fediverse sends nothing
	s2, tok2 := setup(t)
	s2.cfg.ActivityPub = true
	createPost(t, s2, tok2, `{"markdown":"hi"}`)
	flush(s2)
}

func TestFollowersOnOneServerGetOneDelivery(t *testing.T) {
	a, _ := testKeys(t)
	s, tok, rem := federated(t)
	// carol is on the same server and shares bob's shared inbox
	carol := func() []byte {
		b, _ := json.Marshal(map[string]any{"id": rem.srv.URL + "/follows/c", "type": "Follow", "actor": rem.srv.URL + "/users/carol", "object": "http://" + alice + "/"})
		return b
	}()
	w := deliver(s, signedPost(alice, "/inbox", carol, rem.srv.URL+"/users/carol#main-key", a, time.Now(), allSigned))
	if w.Code != 202 || followerCount(s) != 2 {
		t.Fatalf("carol: %d, %d followers", w.Code, followerCount(s))
	}
	flush(s)
	take(t, rem) // carol's Accept, to her own inbox
	createPost(t, s, tok, `{"markdown":"to both"}`)
	flush(s)
	r, _ := take(t, rem)
	if r.URL.Path != "/inbox" {
		t.Errorf("delivered to %s", r.URL.Path)
	}
	nothing(t, rem, "two followers on one server")
}

func TestADeliveryThatFailsIsTriedAgainLaterAndAGoneInboxEndsTheFollow(t *testing.T) {
	s, tok, rem := federated(t)
	rem.mu.Lock()
	rem.answer = 503
	rem.mu.Unlock()
	createPost(t, s, tok, `{"markdown":"will wait"}`)
	flush(s)
	take(t, rem) // tried, and refused with a server error
	flush(s)
	nothing(t, rem, "a retry is not at once") // it waits a minute at least
	if apRetryDelay(0) != time.Minute || apRetryDelay(3) != 8*time.Minute || apRetryDelay(9) != 6*time.Hour || apRetryDelay(20) != 6*time.Hour {
		t.Errorf("delays: %v %v %v", apRetryDelay(0), apRetryDelay(3), apRetryDelay(20))
	}
	// a server that says the inbox is gone loses its followers
	rem.mu.Lock()
	rem.answer = 410
	rem.mu.Unlock()
	createPost(t, s, tok, `{"markdown":"to nobody"}`)
	flush(s)
	take(t, rem)
	if followerCount(s) != 0 {
		t.Errorf("a gone inbox left %d followers", followerCount(s))
	}
}
