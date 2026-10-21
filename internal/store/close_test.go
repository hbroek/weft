package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A database that is closed properly is a single file again: the write-ahead log is written back and
// the -wal and -shm files are deleted. The log is also capped, so it does not stay at its biggest size.
func TestCloseLeavesOneFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var limit int64
	if err := st.db.QueryRow(`PRAGMA journal_size_limit`).Scan(&limit); err != nil || limit != 1<<20 {
		t.Errorf("journal_size_limit = %d (%v), want 1 MiB", limit, err)
	}
	if _, err := st.AddUser("alice", "Alice", ""); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if _, err := st.AddPost(NewPost{UserID: 1, Markdown: fmt.Sprintf("post %d", i), HTML: "<p>x</p>"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, side := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + side); err != nil {
			t.Fatalf("while open there should be a %s file: %v", side, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for _, side := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + side); !os.IsNotExist(err) {
			t.Errorf("%s should be gone after Close: %v", side, err)
		}
	}
	// nothing was lost: it was all written back into the one file
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if ps, err := again.RecentPosts(1, 100); err != nil || len(ps) != 50 {
		t.Errorf("after reopening: %d posts, %v", len(ps), err)
	}
}
