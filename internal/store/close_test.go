package store

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sidecars lists the -wal and -shm files under dir.
func sidecars(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && (strings.HasSuffix(p, "-wal") || strings.HasSuffix(p, "-shm")) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// A data folder that is closed properly is single files again: the write-ahead logs are written back and
// the -wal and -shm files deleted, in instance.db and in every user's file. The log is also capped, so it
// does not stay at its biggest size.
func TestCloseLeavesOneFilePerDatabase(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var limit int64
	if err := st.db.QueryRow(`PRAGMA journal_size_limit`).Scan(&limit); err != nil || limit != 1<<20 {
		t.Errorf("journal_size_limit = %d (%v), want 1 MiB", limit, err)
	}
	for _, n := range []string{"alice", "bob"} {
		if _, err := st.AddUser(n, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	sp := st.SpaceForTests(t, "alice")
	st.SpaceForTests(t, "bob")
	for i := 0; i < 50; i++ {
		if _, err := sp.AddPost(NewPost{UserID: sp.UID(), Markdown: fmt.Sprintf("post %d", i), HTML: "<p>x</p>"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(sidecars(t, dir)) == 0 {
		t.Fatal("while open there should be -wal and -shm files")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if left := sidecars(t, dir); len(left) != 0 {
		t.Errorf("left after Close: %v", left)
	}
	if _, err := os.Stat(st.spacePath("alice")); err != nil {
		t.Error(err)
	}
	// nothing was lost: it was all written back into the files
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if ps, err := again.SpaceForTests(t, "alice").RecentPosts(1, 100); err != nil || len(ps) != 50 {
		t.Errorf("after reopening: %d posts, %v", len(ps), err)
	}
}
