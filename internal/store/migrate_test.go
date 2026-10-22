package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// fixture builds a database file from a schema dump in testdata, as an older Weft left it: version 0,
// with a user and a post inserted the way that Weft would have.
func fixture(t *testing.T, name string) string {
	t.Helper()
	sqlText, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		string(sqlText),
		`INSERT INTO users(id,name,created) VALUES (1,'alice',1700000000)`,
		`INSERT INTO posts(user_id,markdown,html,created) VALUES (1,'# Old post','<h1>Old post</h1>',1700000000)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func version(t *testing.T, st *Store) int {
	t.Helper()
	var v int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// Every schema Weft has had upgrades to the latest version and works: the old rows are still there
// and new ones can be added.
func TestOldSchemasUpgrade(t *testing.T) {
	for _, name := range []string{"v0-reactions.sql", "v0-current.sql"} {
		t.Run(name, func(t *testing.T) {
			path := fixture(t, name)
			st, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if v := version(t, st); v != SchemaVersion {
				t.Errorf("user_version = %d, want %d", v, SchemaVersion)
			}
			posts, err := st.PostsPage(1, 10, 0)
			if err != nil || len(posts) != 1 || posts[0].Slug == "" || posts[0].UID == "" {
				t.Fatalf("old post after upgrade: %v, %v", posts, err)
			}
			if _, err := st.AddPost(NewPost{UserID: 1, Markdown: "new", HTML: "<p>new</p>", Keywords: []string{"go"}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Opening again changes nothing, and a new database starts at the latest version.
func TestOpenIsRepeatable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	for i := 0; i < 3; i++ {
		st, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if v := version(t, st); v != SchemaVersion {
			t.Errorf("open %d: user_version = %d, want %d", i, v, SchemaVersion)
		}
		st.Close()
	}
}

// A database written by a newer Weft is refused and left alone, rather than read with a schema that
// may no longer match.
func TestNewerDatabaseRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "99") {
		t.Fatalf("Open of a newer database = %v, want a refusal naming version 99", err)
	}
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	var v int
	db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 99 {
		t.Errorf("version after refusal = %d, want 99 (untouched)", v)
	}
}

// Later migrations run once, in order, each with its version bump; one that fails leaves the file at
// the version before it, with none of its changes.
func TestMigrationsApplyInOrder(t *testing.T) {
	saved := migrations
	defer func() { migrations = saved; SchemaVersion = len(saved) }()
	path := filepath.Join(t.TempDir(), "w.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	var ran []int
	migrations = append(append([]func(*sql.Tx) error{}, saved...),
		func(tx *sql.Tx) error { ran = append(ran, 2); _, err := tx.Exec(`CREATE TABLE t2(x)`); return err },
		func(tx *sql.Tx) error {
			ran = append(ran, 3)
			if _, err := tx.Exec(`CREATE TABLE t3(x)`); err != nil {
				return err
			}
			return sql.ErrConnDone // fails after changing something
		})
	SchemaVersion = len(migrations)
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "version 3") {
		t.Fatalf("Open = %v, want the version 3 migration to fail", err)
	}
	SchemaVersion = len(saved)
	migrations = migrations[:len(saved)+1]
	SchemaVersion = len(migrations)
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if v := version(t, st); v != 2 {
		t.Errorf("user_version = %d, want 2", v)
	}
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('t2','t3')`).Scan(&n)
	if n != 1 {
		t.Errorf("tables t2/t3 present: %d, want only t2", n)
	}
	if len(ran) != 2 || ran[0] != 2 || ran[1] != 3 {
		t.Errorf("migrations ran %v, want [2 3] (version 2 once, then the failing 3)", ran)
	}
}
