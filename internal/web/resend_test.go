package web

import (
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/store"
)

func portOf(i *instance) string { return i.host[strings.LastIndex(i.host, ":"):] }

// A removed account answers 410, and the replies and likes it sent are sent again, so that the servers it wrote to
// take them down.
func TestRemovedAccountRetractsItsRepliesAndLikes(t *testing.T) {
	alice, bob := newInstance(t, "alice"), newInstance(t, "bob")
	bob.s.cfg.Port = portOf(bob)
	ctx := t.Context()

	ua := alice.post(t, `{"markdown":"A post to answer."}`)
	alice.call(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200) // so that his reply is approved at once
	ub := bob.post(t, `{"markdown":"A reply that will go.","in_reply_to":"`+ua+`","in_reply_to_feed":"`+alice.base+`/feed.xml"}`)
	bob.call(t, "POST", "/api/likes", `{"target":"`+ua+`"}`, 200)
	settle(ctx, alice, bob)
	if !strings.Contains(alice.get(pathOf(ua)+"/replies.xml"), "A reply that will go.") {
		t.Fatal("the reply did not arrive")
	}
	likes := func() int {
		sp, release, _ := alice.s.st.Space("alice")
		defer release()
		l, _ := sp.LikesInFor(1)
		return len(l)
	}
	if likes() != 1 {
		t.Fatalf("the like did not arrive: %d", likes())
	}

	if err := bob.s.st.Remove("bob"); err != nil {
		t.Fatal(err)
	}
	if w := do(bob.s, "GET", bob.host, pathOf(ub), "", ""); w.Code != 410 {
		t.Fatalf("the removed account's post answers %d, want 410", w.Code)
	}
	bob.s.retractRemoved()
	if got := bob.s.st.NeedingRetraction(); len(got) != 0 {
		t.Errorf("still needs retraction: %v", got)
	}
	// the file stays until the queue is empty
	if n, _ := bob.s.st.TrashMoved(time.Now()); n != 0 {
		t.Errorf("the file was trashed with the Webmentions still waiting")
	}
	settle(ctx, alice, bob)
	if strings.Contains(alice.get(pathOf(ua)+"/replies.xml"), "A reply that will go.") {
		t.Errorf("the reply is still listed")
	}
	if likes() != 0 {
		t.Errorf("the like is still counted")
	}
	// with nothing left to send the file goes to the trash
	if n, _ := bob.s.st.TrashMoved(time.Now()); n != 1 {
		t.Errorf("the file was not trashed once the queue was empty: %d", n)
	}
	// it is done once, and a restored account can be retracted again
	bob.s.retractRemoved()
}

// After a move the new server sends the Webmentions of every reply and like again, from the new addresses.
func TestMovedAccountSendsItsMentionsAgain(t *testing.T) {
	ts := setupTransfer(t)
	bob := newInstance(t, "bob")
	target := bob.post(t, `{"markdown":"Something to answer."}`)
	ts.old.post(t, `{"markdown":"A reply.","in_reply_to":"`+target+`"}`)
	ts.old.call(t, "POST", "/api/likes", `{"target":"`+target+`"}`, 200)
	ts.old.post(t, `{"markdown":"Not a reply."}`)

	ts.receive(t, ts.code(t))
	if v := ts.wait(t); v.Err != "" || !v.Done {
		t.Fatalf("move: %+v", v)
	}
	sp, release, _ := ts.nw.s.st.Space("alice")
	defer release()
	items, err := sp.DueQueue(store.QueueOut, 50)
	if err != nil {
		t.Fatal(err)
	}
	// the file arrives with whatever the old server had queued (these tests never send), so look at the new addresses
	var sources []string
	for _, it := range items {
		if strings.HasPrefix(it.Source, ts.nw.base+"/") {
			sources = append(sources, it.Source)
			if it.Target != target {
				t.Errorf("a Webmention to %s", it.Target)
			}
		}
	}
	if len(sources) != 2 {
		t.Fatalf("queued %v from the new server, want the reply and the like", sources)
	}
	for _, s := range sources {
		if !strings.HasPrefix(s, ts.nw.base+"/posts/") && !strings.HasPrefix(s, ts.nw.base+"/likes/") {
			t.Errorf("a source that is not a reply or a like: %s", s)
		}
	}
}
