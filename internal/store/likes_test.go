package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// A database from before there were only likes keeps its positive reactions as likes and loses
// the negative ones.
func TestMigrateReactionsToLikes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, display TEXT NOT NULL DEFAULT '', bio TEXT NOT NULL DEFAULT '', email TEXT NOT NULL DEFAULT '', token_hash TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL)`,
		`CREATE TABLE reactions (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, target TEXT NOT NULL, target_feed TEXT NOT NULL DEFAULT '', polarity TEXT NOT NULL, emoji TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, updated INTEGER, deleted INTEGER NOT NULL DEFAULT 0)`,
		`CREATE UNIQUE INDEX reactions_active ON reactions(user_id, target) WHERE deleted=0`,
		`INSERT INTO users(id,name,created) VALUES (1,'alice',1)`,
		`CREATE TABLE posts (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, title TEXT NOT NULL DEFAULT '', markdown TEXT NOT NULL, html TEXT NOT NULL, created INTEGER NOT NULL, updated INTEGER, deleted INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO posts(id,user_id,markdown,html,created) VALUES (1,1,'x','x',1)`,
		`INSERT INTO reactions(user_id,target,polarity,emoji,created) VALUES (1,'http://a.example/1','+','🎉',1), (1,'http://a.example/2','-','😕',2)`,
		`CREATE TABLE reactions_in (id INTEGER PRIMARY KEY, post_id INTEGER NOT NULL, source TEXT NOT NULL, feed_url TEXT NOT NULL DEFAULT '', author TEXT NOT NULL DEFAULT '', polarity TEXT NOT NULL, emoji TEXT NOT NULL, trusted INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL, UNIQUE(post_id, source))`,
		`INSERT INTO reactions_in(post_id,source,author,polarity,emoji,trusted,created) VALUES (1,'s1','dana reactions','+','👍',1,1), (1,'s2','Eric','+','👍',1,2)`,
		`CREATE TABLE items (feed_url TEXT NOT NULL, guid TEXT NOT NULL, published INTEGER NOT NULL, plus INTEGER NOT NULL DEFAULT 0, minus INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (feed_url, guid))`,
		`INSERT INTO items(feed_url,guid,published,plus,minus) VALUES ('f','g',1,3,1)`,
	} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	old.Close()

	data := filepath.Join(t.TempDir(), "data")
	if _, err := Split(path, data); err != nil {
		t.Fatal(err)
	}
	st, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sp := st.SpaceForTests(t, "alice")
	xs, err := sp.ActiveLikes(1, 10)
	if err != nil || len(xs) != 1 || xs[0].Target != "http://a.example/1" {
		t.Fatalf("likes after migration: %v %v", xs, err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT likes FROM items WHERE guid='g'`).Scan(&n); err != nil || n != 3 {
		t.Errorf("item likes: %d %v", n, err)
	}
	var a string
	if err := sp.db.QueryRow(`SELECT author FROM likes_in WHERE source='s1'`).Scan(&a); err != nil || a != "dana" {
		t.Errorf("sender name after migration: %q %v", a, err)
	}
	if err := sp.db.QueryRow(`SELECT author FROM likes_in WHERE source='s2'`).Scan(&a); err != nil || a != "Eric" {
		t.Errorf("a plain name must stay: %q %v", a, err)
	}
}
