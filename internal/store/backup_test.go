package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The first backup copies everybody, the next only those who changed, and -full copies everybody again.
func TestBackupCopiesOnlyWhoChanged(t *testing.T) {
	in, alice, _ := twoUsers(t)
	dest := t.TempDir()
	rep, err := in.Backup(dest, false)
	if err != nil || rep.Users != 2 || rep.Copied != 2 || rep.Unchanged != 0 {
		t.Fatalf("first backup: %+v %v", rep, err)
	}
	for _, f := range []string{"instance.db", spaceRel("alice"), spaceRel("bob"), backupState} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("backup lacks %s: %v", f, err)
		}
	}
	if rep, _ := in.Backup(dest, false); rep.Copied != 0 || rep.Unchanged != 2 {
		t.Errorf("nothing changed, but: %+v", rep)
	}
	alice.AddPost(NewPost{UserID: alice.UID(), Markdown: "new", HTML: "<p>new</p>"})
	if rep, _ := in.Backup(dest, false); rep.Copied != 1 || rep.Unchanged != 1 {
		t.Errorf("only alice changed, but: %+v", rep)
	}
	if rep, _ := in.Backup(dest, false); rep.Copied != 0 {
		t.Errorf("alice's change was copied twice: %+v", rep)
	}
	if rep, _ := in.Backup(dest, true); rep.Copied != 2 {
		t.Errorf("full backup: %+v", rep)
	}
	if _, err := in.Backup(in.dir, false); err == nil {
		t.Error("a backup into the data folder itself was allowed")
	}
}

// A backup taken while the server is writing restores to a data folder that works: every file intact, the
// directory rebuilt, and a user nobody was writing to identical to the live one.
func TestBackupWhileWritingThenRestore(t *testing.T) {
	in, alice, bob := twoUsers(t)
	bob.AddPost(NewPost{UserID: bob.UID(), Markdown: "quiet", HTML: "<p>quiet</p>"})
	bob.Follow(bob.UID(), "http://x.example/feed")
	dest := t.TempDir()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	written := 0
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				alice.AddPost(NewPost{UserID: alice.UID(), Markdown: fmt.Sprintf("p%d", written), HTML: "<p>x</p>"})
				written++
			}
		}
	}()
	for i := 0; i < 5; i++ {
		if _, err := in.Backup(dest, true); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()

	restored := filepath.Join(t.TempDir(), "data")
	rep, err := Restore(dest, restored)
	if err != nil || rep.Users != 2 {
		t.Fatalf("Restore: %+v %v", rep, err)
	}
	r, err := Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ra := r.SpaceForTests(t, "alice")
	if ps, _ := ra.PostsPage(ra.UID(), 100000, 0); len(ps) == 0 || len(ps) > written {
		t.Errorf("alice has %d posts in the restore, %d were written", len(ps), written)
	}
	rb := r.SpaceForTests(t, "bob")
	if ps, _ := rb.PostsPage(rb.UID(), 10, 0); len(ps) != 1 || ps[0].Markdown != "quiet" {
		t.Errorf("bob after restore: %+v", ps)
	}
	if !r.Followed("http://x.example/feed") {
		t.Error("the directory was not rebuilt")
	}
	if _, err := Restore(dest, restored); err == nil {
		t.Error("restore into a folder that is not empty was allowed")
	}
	os.Remove(filepath.Join(dest, spaceRel("bob")))
	if _, err := Restore(dest, filepath.Join(t.TempDir(), "again")); err == nil {
		t.Error("restore of a backup that lacks a user's file succeeded")
	}
}
