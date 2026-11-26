package web

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/store"
)

type sentReply struct {
	Type, ID, Actor string
	CC              []string
	Object          struct {
		ID, Type, InReplyTo, Content string
		CC                           []string
		Tag                          []struct{ Type, Href, Name string }
	}
}

func decodeReply(t *testing.T, b []byte) sentReply {
	t.Helper()
	var d sentReply
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return d
}

func TestAReplyToAPostOnTheFediverseGoesToItsAuthorAndYourFollowers(t *testing.T) {
	s, tok, rem := federated(t)
	status := rem.srv.URL + "/users/bob/statuses/7"
	path := createPost(t, s, tok, fmt.Sprintf(`{"markdown":"Thanks **bob**.","in_reply_to":%q}`, status))
	s.WaitFederation()
	flush(s)
	req, body := take(t, rem)
	if req.URL.Path != "/inbox" { // bob's server has a shared inbox, which is both bob's and his server's
		t.Errorf("delivered to %s", req.URL.Path)
	}
	d := decodeReply(t, body)
	id := "http://" + alice + "/"
	permalink := strings.TrimSuffix(id, "/") + path
	if d.Type != "Create" || d.Object.Type != "Note" || d.Object.ID != permalink || d.Object.InReplyTo != status {
		t.Fatalf("create: %s", body)
	}
	if len(d.Object.CC) != 2 || d.Object.CC[0] != id+"followers" || d.Object.CC[1] != rem.actor() || len(d.CC) != 2 {
		t.Errorf("cc: %v / %v", d.Object.CC, d.CC)
	}
	handle := "@bob@" + strings.TrimPrefix(rem.srv.URL, "http://")
	if len(d.Object.Tag) < 1 || d.Object.Tag[0].Type != "Mention" || d.Object.Tag[0].Href != rem.actor() || d.Object.Tag[0].Name != handle {
		t.Errorf("tags: %+v", d.Object.Tag)
	}
	if !strings.HasPrefix(d.Object.Content, `<p><span class="h-card"><a href="`+rem.actor()+`" class="u-url mention"`) || !strings.Contains(d.Object.Content, "Thanks <strong>bob</strong>.") {
		t.Errorf("content: %s", d.Object.Content)
	}
	nothing(t, rem, "one delivery to a server that is the author's and a follower's")
	// the post's own address says what it answers
	if n := apGet(s, alice, path, activityJSON).Body.String(); !strings.Contains(n, `"inReplyTo":"`+status+`"`) {
		t.Errorf("the Note: %s", n)
	}
	// an edit and a removal follow it, to the same people
	if w := do(s, "PUT", alice, "/api/posts/1", tok, fmt.Sprintf(`{"markdown":"Thanks again.","in_reply_to":%q}`, status)); w.Code != 200 {
		t.Fatalf("edit: %d %s", w.Code, w.Body)
	}
	flush(s)
	_, body = take(t, rem)
	if u := decodeReply(t, body); u.Type != "Update" || u.Object.InReplyTo != status || len(u.Object.CC) != 2 || !strings.Contains(u.Object.Content, "Thanks again.") || !strings.Contains(u.Object.Content, `class="u-url mention"`) {
		t.Errorf("update: %s", body)
	}
	do(s, "DELETE", alice, "/api/posts/1", tok, "")
	flush(s)
	_, body = take(t, rem)
	if x := decodeReply(t, body); x.Type != "Delete" || len(x.CC) != 2 {
		t.Errorf("delete: %s", body)
	}
}

func TestAReplyToAPageThatIsNotAFediversePostStaysOnTheSite(t *testing.T) {
	s, tok, rem := federated(t)
	for _, target := range []string{rem.srv.URL + "/page", rem.srv.URL + "/nothing", "http://127.0.0.1:1/x"} {
		createPost(t, s, tok, fmt.Sprintf(`{"markdown":"a reply","in_reply_to":%q}`, target))
	}
	s.WaitFederation()
	flush(s)
	nothing(t, rem, "replies to pages that are not posts")
	sp := s.space("alice")
	if ps, _ := sp.RecentPosts(sp.UID(), 10); len(ps) != 3 {
		t.Errorf("%d posts kept", len(ps))
	}
	// and a repost never goes
	createPost(t, s, tok, `{"markdown":"a repost","canonical":"http://other.example/q"}`)
	s.WaitFederation()
	flush(s)
	nothing(t, rem, "a repost")
}

func TestAReplyToYourOwnPostAndToAReplyUnderItGoOut(t *testing.T) {
	a, _ := testKeys(t)
	s, tok, rem := federated(t)
	path := createPost(t, s, tok, `{"markdown":"The first post."}`)
	flush(s)
	take(t, rem) // its Create
	permalink := "http://" + alice + path

	// to your own post: only the followers, no mention
	createPost(t, s, tok, fmt.Sprintf(`{"markdown":"A second thought.","in_reply_to":%q}`, permalink))
	s.WaitFederation()
	flush(s)
	_, body := take(t, rem)
	d := decodeReply(t, body)
	if d.Type != "Create" || d.Object.InReplyTo != permalink || len(d.Object.CC) != 1 || len(d.Object.Tag) != 0 || strings.Contains(d.Object.Content, "mention") {
		t.Errorf("to own post: %s", body)
	}

	// a reply that came from the fediverse under the post: answering it needs no lookup of the post, only of its author
	note := rem.srv.URL + "/users/bob/statuses/1"
	rem.post(s, replyActivity(rem, "Create", note, permalink, "<p>Nice</p>", publicAudience), a)
	before := rem.noteGets
	createPost(t, s, tok, fmt.Sprintf(`{"markdown":"Glad you liked it.","in_reply_to":%q}`, note))
	s.WaitFederation()
	flush(s)
	_, body = take(t, rem)
	if e := decodeReply(t, body); e.Object.InReplyTo != note || len(e.Object.Tag) < 1 || e.Object.Tag[0].Href != rem.actor() {
		t.Errorf("to a reply: %s", body)
	}
	if rem.noteGets != before {
		t.Errorf("the reply's post was fetched again")
	}
	nothing(t, rem, "to a reply")
}

func TestALikeOfAPostOnTheFediverseGoesToItsAuthorAndIsTakenBack(t *testing.T) {
	s, tok, rem := federated(t)
	status := rem.srv.URL + "/users/bob/statuses/7"
	like := func(method string) int {
		return do(s, method, alice, "/api/likes", tok, fmt.Sprintf(`{"target":%q}`, status)).Code
	}
	if c := like("POST"); c != 200 {
		t.Fatalf("like: %d", c)
	}
	s.WaitFederation()
	flush(s)
	req, body := take(t, rem)
	var l struct {
		Type, ID, Actor, Object string
		To                      []string
	}
	json.Unmarshal(body, &l)
	id := "http://" + alice + "/"
	if l.Type != "Like" || l.Actor != id || l.Object != status || len(l.To) != 1 || l.To[0] != rem.actor() || !strings.HasPrefix(l.ID, id+"likes#") || req.URL.Path != "/inbox" {
		t.Errorf("like: %s to %s", body, req.URL.Path)
	}
	like("POST") // twice is once
	s.WaitFederation()
	flush(s)
	nothing(t, rem, "liking again")
	if c := like("DELETE"); c != 204 {
		t.Fatalf("unlike: %d", c)
	}
	s.WaitFederation()
	flush(s)
	_, body = take(t, rem)
	var u struct {
		Type   string
		Object struct{ Type, ID, Object string }
	}
	json.Unmarshal(body, &u)
	if u.Type != "Undo" || u.Object.Type != "Like" || u.Object.ID != l.ID || u.Object.Object != status {
		t.Errorf("undo: %s", body)
	}
	// a page that is not a post, and one's own page, are no business of the fediverse
	for _, target := range []string{rem.srv.URL + "/page", id + "posts/x"} {
		do(s, "POST", alice, "/api/likes", tok, fmt.Sprintf(`{"target":%q}`, target))
		do(s, "DELETE", alice, "/api/likes", tok, fmt.Sprintf(`{"target":%q}`, target))
	}
	s.WaitFederation()
	flush(s)
	nothing(t, rem, "likes of pages that are not posts")
}

func TestNothingGoesOutWhenActivityPubIsOff(t *testing.T) {
	a, _ := testKeys(t)
	s, tok := setup(t)
	s.cfg.ActivityPub = true // the operator allows it, the account has not switched it on
	rem := newRemote(t, a)
	status := rem.srv.URL + "/users/bob/statuses/7"
	createPost(t, s, tok, fmt.Sprintf(`{"markdown":"a reply","in_reply_to":%q}`, status))
	do(s, "POST", alice, "/api/likes", tok, fmt.Sprintf(`{"target":%q}`, status))
	s.WaitFederation()
	flush(s)
	if rem.noteGets != 0 {
		t.Errorf("the server asked about a post")
	}
	nothing(t, rem, "ActivityPub off")
	_ = url.Values{}
	_ = time.Second
	_ = store.StatusPending
}
