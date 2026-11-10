package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// fixture builds a single-file database from a schema dump in testdata, as an older Weft left it: version
// 0, with a user and a post inserted the way that Weft would have.
func fixture(t *testing.T, name string) string {
	t.Helper()
	sqlText, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "old.db")
	db := openRaw(t, path)
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

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func userVersion(t *testing.T, path string) int {
	t.Helper()
	db := openRaw(t, path)
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// Every schema Weft has had in the single-file layout converts to the per-user layout and works: the old
// rows are still there, with identities for the posts that had none, and new ones can be added.
func TestOldSchemasSplitAndWork(t *testing.T) {
	for _, name := range []string{"v0-current.sql"} {
		t.Run(name, func(t *testing.T) {
			old := fixture(t, name)
			data := filepath.Join(t.TempDir(), "data")
			rep, err := Split(old, data)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Users != 1 || rep.Rows["posts"] != 1 {
				t.Errorf("report: %+v", rep)
			}
			st, err := Open(data)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			sp := st.SpaceForTests(t, "alice")
			posts, err := sp.PostsPage(sp.UID(), 10, 0)
			if err != nil || len(posts) != 1 || posts[0].Slug == "" || posts[0].UID == "" {
				t.Fatalf("old post after split: %v, %v", posts, err)
			}
			if _, err := sp.AddPost(NewPost{UserID: sp.UID(), Markdown: "new", HTML: "<p>new</p>", Keywords: []string{"go"}}); err != nil {
				t.Fatal(err)
			}
			if v := userVersion(t, old); v != 0 {
				t.Errorf("the old file was changed: user_version %d", v)
			}
		})
	}
}

// Opening again changes nothing, and new files start at the latest version.
func TestOpenIsRepeatable(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		st, err := Open(dir)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if i == 0 {
			if _, err := st.AddUser("alice", "", ""); err != nil {
				t.Fatal(err)
			}
		}
		st.SpaceForTests(t, "alice")
		st.Close()
		if v := userVersion(t, filepath.Join(dir, "instance.db")); v != InstanceSchemaVersion {
			t.Errorf("open %d: instance.db at version %d, want %d", i, v, InstanceSchemaVersion)
		}
		if v := userVersion(t, st.spacePath("alice")); v != SpaceSchemaVersion {
			t.Errorf("open %d: alice.db at version %d, want %d", i, v, SpaceSchemaVersion)
		}
	}
}

// A file written by a newer Weft is refused and left alone, rather than read with a schema that may no
// longer match: the shared file at Open, a user's file when it is first used.
func TestNewerFilesRefused(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser("alice", "", ""); err != nil {
		t.Fatal(err)
	}
	st.Close()
	for _, path := range []string{st.spacePath("alice"), filepath.Join(dir, "instance.db")} {
		db := openRaw(t, path)
		db.Exec(`PRAGMA user_version = 99`)
		db.Close()
	}
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "99") {
		t.Fatalf("Open with a newer instance.db = %v, want a refusal naming version 99", err)
	}
	if v := userVersion(t, filepath.Join(dir, "instance.db")); v != 99 {
		t.Errorf("instance.db version after refusal = %d, want 99 (untouched)", v)
	}
	db := openRaw(t, filepath.Join(dir, "instance.db"))
	db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, InstanceSchemaVersion))
	db.Close()
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, err := st.Space("alice"); err == nil || !strings.Contains(err.Error(), "99") {
		t.Fatalf("Space with a newer user file = %v, want a refusal naming version 99", err)
	}
}

// Later migrations run once, in order, each with its version bump; one that fails leaves the file at the
// version before it, with none of its changes.
func TestMigrationsApplyInOrder(t *testing.T) {
	db := openRaw(t, filepath.Join(t.TempDir(), "m.db"))
	defer db.Close()
	var ran []int
	steps := []func(*sql.Tx) error{
		func(tx *sql.Tx) error { ran = append(ran, 1); _, err := tx.Exec(`CREATE TABLE t1(x)`); return err },
		func(tx *sql.Tx) error {
			ran = append(ran, 2)
			if _, err := tx.Exec(`CREATE TABLE t2(x)`); err != nil {
				return err
			}
			return sql.ErrConnDone // fails after changing something
		},
	}
	if err := migrate(db, steps); err == nil || !strings.Contains(err.Error(), "version 2") {
		t.Fatalf("migrate = %v, want the version 2 step to fail", err)
	}
	var v, n int
	db.QueryRow(`PRAGMA user_version`).Scan(&v)
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('t1','t2')`).Scan(&n)
	if v != 1 || n != 1 {
		t.Errorf("after the failure: version %d, tables %d; want version 1 with only t1", v, n)
	}
	steps[1] = func(tx *sql.Tx) error { ran = append(ran, 2); _, err := tx.Exec(`CREATE TABLE t2(x)`); return err }
	steps = append(steps, func(tx *sql.Tx) error { ran = append(ran, 3); _, err := tx.Exec(`CREATE TABLE t3(x)`); return err })
	if err := migrate(db, steps); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 3 || fmt.Sprint(ran) != "[1 2 2 3]" {
		t.Errorf("version %d, steps run %v; want 3 and [1 2 2 3] (step 1 once, 2 twice, 3 once)", v, ran)
	}
}
