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

func indexNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	db := openRaw(t, path)
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='index'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		out[n] = true
	}
	return out
}

// Files made before the thread indexes gain them when they are opened, and nothing else changes.
func TestThreadIndexesAreAddedToOlderFiles(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser("alice", "", ""); err != nil {
		t.Fatal(err)
	}
	st.Close()
	inst, spool := filepath.Join(dir, "instance.db"), st.spacePath("alice")
	for _, n := range []string{"items_root"} {
		if !indexNames(t, inst)[n] {
			t.Fatalf("a new instance.db lacks %s", n)
		}
	}
	if !indexNames(t, spool)["posts_root"] {
		t.Fatal("a new spool lacks posts_root")
	}

	// give both files something to plan with: many threads, each of a few posts or items
	db := openRaw(t, inst)
	for i := 0; i < 3000; i++ {
		db.Exec(`INSERT INTO items(feed_url,guid,published,root) VALUES(?,?,?,?)`, fmt.Sprintf("http://f%d/feed.xml", i%30), fmt.Sprintf("http://f%d/p%d", i%30, i), i, fmt.Sprintf("http://f%d/r%d", i%30, i%500))
	}
	db.Close()
	db = openRaw(t, spool)
	for i := 0; i < 3000; i++ {
		db.Exec(`INSERT INTO posts(user_id,markdown,html,created,root) VALUES(1,'x','x',?,?)`, i, fmt.Sprintf("http://alice/r%d", i%500))
	}
	db.Close()

	// take the indexes away and put the files back to the versions before
	db = openRaw(t, inst)
	db.Exec(`DROP INDEX items_root`)
	db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, InstanceSchemaVersion-1))
	db.Close()
	db = openRaw(t, spool)
	db.Exec(`DROP INDEX posts_root`)
	db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, SpaceSchemaVersion-1))
	db.Close()

	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.SpaceForTests(t, "alice")
	st.Close()
	for _, n := range []string{"items_root"} {
		if !indexNames(t, inst)[n] {
			t.Errorf("opening an older instance.db did not add %s", n)
		}
	}
	if !indexNames(t, spool)["posts_root"] {
		t.Error("opening an older spool did not add posts_root")
	}
	if v := userVersion(t, inst); v != InstanceSchemaVersion {
		t.Errorf("instance.db at version %d, want %d", v, InstanceSchemaVersion)
	}
	if v := userVersion(t, spool); v != SpaceSchemaVersion {
		t.Errorf("spool at version %d, want %d", v, SpaceSchemaVersion)
	}

	// the queries the indexes are for use them at once, on the migrated files, without waiting for statistics
	plan := func(path, q string, args ...any) string {
		db := openRaw(t, path)
		defer db.Close()
		rows, err := db.Query(`EXPLAIN QUERY PLAN `+q, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var all []string
		for rows.Next() {
			var a, b, c int
			var d string
			rows.Scan(&a, &b, &c, &d)
			all = append(all, d)
		}
		return strings.Join(all, " | ")
	}
	got := plan(inst, `SELECT 1 FROM items i WHERE i.feed_url IN ('http://f1/feed.xml','http://f2/feed.xml') AND (i.root=? OR i.guid=?) ORDER BY i.published, i.guid LIMIT 300`, "r", "g")
	if !strings.Contains(got, "items_root") {
		t.Errorf("thread items by root do not use the index: %s", got)
	}
	got = plan(spool, `SELECT 1 FROM posts WHERE user_id=? AND deleted=0 AND root=? ORDER BY created, id`, 1, "r")
	if !strings.Contains(got, "posts_root") {
		t.Errorf("posts by root do not use the index: %s", got)
	}
}
