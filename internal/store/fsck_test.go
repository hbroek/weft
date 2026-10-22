package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fsckOf(t *testing.T, in *Instance, full bool, only ...string) *FsckReport {
	t.Helper()
	rep, err := in.Fsck(context.Background(), full, only...)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func has(rep *FsckReport, name, what string) bool {
	for _, p := range rep.Problems {
		if p.Name == name && strings.Contains(p.What, what) {
			return true
		}
	}
	return false
}

func TestFsckFindsWhatIsWrong(t *testing.T) {
	in := newInst(t, "alice", "bob", "carol", "dave")
	for _, n := range []string{"alice", "bob"} {
		sp, release, _ := in.Space(n)
		sp.Follow(sp.UID(), "http://x.example/"+n+".xml")
		release()
	}
	if rep := fsckOf(t, in, true); len(rep.Problems) != 0 || rep.Users != 4 {
		t.Fatalf("a clean instance: %+v", rep)
	}

	// the directory drifts: a follow it does not know of, and one it should not have
	in.db.Exec(`DELETE FROM local_follows WHERE name='alice'`)
	in.db.Exec(`INSERT INTO local_follows(feed_url,name) VALUES('http://ghost.example/',  'bob')`)
	rep := fsckOf(t, in, false)
	if !has(rep, "alice", "directory lists 0 followed feeds") || !has(rep, "bob", "directory lists 2 followed feeds") {
		t.Fatalf("drift not found: %v", rep.Problems)
	}
	for _, p := range rep.Problems {
		if !p.Fixes {
			t.Errorf("%v should be fixable by a reindex", p)
		}
	}
	if err := in.Reindex(); err != nil {
		t.Fatal(err)
	}
	if rep := fsckOf(t, in, false); len(rep.Problems) != 0 {
		t.Fatalf("after Reindex: %v", rep.Problems)
	}

	// files that do not match the directory
	in.Close() // the instance owns its files; edit them while nothing has them open
	if err := os.Remove(in.spacePath("carol")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in.spacePath("stray"), []byte("x"), 0o644); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(in.spacePath("stray")), 0o755)
	os.WriteFile(in.spacePath("stray"), []byte("x"), 0o644)
	wrong := filepath.Join(in.dir, "users", "zz")
	os.MkdirAll(wrong, 0o755)
	os.Rename(in.spacePath("dave"), filepath.Join(wrong, "dave.db"))
	// a damaged file: its first pages overwritten
	f, _ := os.OpenFile(in.spacePath("bob"), os.O_WRONLY, 0)
	f.WriteAt([]byte(strings.Repeat("garbage!", 600)), 100)
	f.Close()
	// an account that does not match its file name
	raw := openRaw(t, in.spacePath("alice"))
	raw.Exec(`UPDATE users SET name='mallory'`)
	raw.Exec(`PRAGMA user_version = 99`)
	raw.Close()

	in2, err := Open(in.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer in2.Close()
	rep = fsckOf(t, in2, true)
	for name, what := range map[string]string{
		"carol": "no file", "stray": "no user in the directory", "dave": "wrong folder",
		"alice": "newer than this Weft knows", "bob": "",
	} {
		if !has(rep, name, what) {
			t.Errorf("missing a problem for %s containing %q in %v", name, what, rep.Problems)
		}
	}
	// asking about one user reports only that user
	if rep := fsckOf(t, in2, false, "carol"); len(rep.Problems) != 1 || rep.Problems[0].Name != "carol" {
		t.Errorf("fsck carol: %v", rep.Problems)
	}
}

func TestFsckIgnoresMovedUsersWithoutFiles(t *testing.T) {
	in := newInst(t, "alice")
	in.MarkMoved("alice", "https://alice.example.org")
	in.TrashMoved(time.Now())
	if rep := fsckOf(t, in, true); len(rep.Problems) != 0 {
		t.Fatalf("a moved user with their file in the trash: %v", rep.Problems)
	}
}

// One missing file does not stop the directory from being rebuilt for everybody else.
func TestReindexSkipsAUserWithoutAFile(t *testing.T) {
	in := newInst(t, "alice", "bob")
	for _, n := range []string{"alice", "bob"} {
		sp, release, _ := in.Space(n)
		sp.Follow(sp.UID(), "http://x.example/"+n+".xml")
		release()
	}
	in.pool.forget("alice")
	os.Remove(in.spacePath("alice"))
	in.db.Exec(`DELETE FROM local_follows WHERE name='bob'`)
	if err := in.Reindex(); err != nil {
		t.Fatal(err)
	}
	if !in.Followed("http://x.example/bob.xml") || in.Followed("http://x.example/alice.xml") {
		t.Error("the directory was not rebuilt from the files that exist")
	}
	if rep := fsckOf(t, in, false); !has(rep, "alice", "no file") || len(rep.Problems) != 1 {
		t.Errorf("fsck after: %v", rep.Problems)
	}
}
