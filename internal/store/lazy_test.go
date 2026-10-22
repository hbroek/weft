package store

import (
	"testing"
)

func tablesOf(t *testing.T, sp *Space) map[string]bool {
	t.Helper()
	rows, err := sp.RawDBForTests().Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		got[n] = true
	}
	return got
}

// A new account has only the core tables; reading never creates the others, and the first write that
// needs them does.
func TestLazyTables(t *testing.T) {
	in, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if _, err := in.AddUser("alice", "Alice", ""); err != nil {
		t.Fatal(err)
	}
	sp, release, err := in.Space("alice")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, lazy := range lazyTables {
		if tablesOf(t, sp)[lazy] {
			t.Fatalf("a new file has %s", lazy)
		}
	}
	u, _ := sp.User()
	p, err := sp.AddPost(NewPost{UserID: u.ID, Title: "T", Markdown: "hello", HTML: "<p>hello</p>"})
	if err != nil {
		t.Fatal(err)
	}

	// reads of what is not there find nothing, and make nothing
	if rs, err := sp.RepliesFor(p.ID, "approved"); err != nil || len(rs) != 0 {
		t.Fatalf("replies: %v %v", rs, err)
	}
	if xs, err := sp.ActiveLikes(u.ID, 10); err != nil || len(xs) != 0 {
		t.Fatalf("likes: %v %v", xs, err)
	}
	if _, err := sp.UserBySession("nope"); err == nil {
		t.Fatal("a session that never existed was found")
	}
	if q, err := sp.DueQueue("in", 10); err != nil || len(q) != 0 {
		t.Fatalf("queue: %v %v", q, err)
	}
	if got := tablesOf(t, sp); got["replies"] || got["likes"] || got["sessions"] || got["wm_queue"] {
		t.Fatalf("reading created tables: %v", got)
	}

	// deleting and updating what is not there is fine
	sp.DeleteReply(p.ID, "http://x/y")
	if got := tablesOf(t, sp); got["replies"] {
		t.Fatal("a delete created tables")
	}

	// the first write creates them and works
	tok, err := sp.CreateSession(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := sp.UserBySession(tok); err != nil || got.ID != u.ID {
		t.Fatalf("session: %v %v", got, err)
	}
	if err := sp.Enqueue("out", u.ID, "http://a/", "http://b/"); err != nil {
		t.Fatal(err)
	}
	if q, err := sp.DueQueue("out", 10); err != nil || len(q) != 1 {
		t.Fatalf("queue: %v %v", q, err)
	}
	for _, lazy := range lazyTables {
		if !tablesOf(t, sp)[lazy] {
			t.Fatalf("no %s after a write", lazy)
		}
	}
}
