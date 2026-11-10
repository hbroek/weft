package store

// The database layout before there was one file per user: every user, and the caches they share, in a
// single SQLite file. Nothing here serves requests any more. It remains so that `weft split` can read
// such a file (bringing a database of any age up to date first) and write the new layout.

import (
	"database/sql"
	"fmt"
	"strings"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  display TEXT NOT NULL DEFAULT '',
  bio TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL DEFAULT '',
  pw_salt BLOB,
  pw_hash BLOB,
  created INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS posts (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  title TEXT NOT NULL DEFAULT '',
  markdown TEXT NOT NULL,
  html TEXT NOT NULL,
  created INTEGER NOT NULL,
  updated INTEGER,
  deleted INTEGER NOT NULL DEFAULT 0,
  in_reply_to TEXT NOT NULL DEFAULT '',
  in_reply_to_feed TEXT NOT NULL DEFAULT '',
  root TEXT NOT NULL DEFAULT '',
  root_feed TEXT NOT NULL DEFAULT '',
  keywords TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS replies (
  id INTEGER PRIMARY KEY,
  post_id INTEGER NOT NULL REFERENCES posts(id),
  source TEXT NOT NULL,
  feed_url TEXT NOT NULL DEFAULT '',
  author TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '',
  markdown TEXT NOT NULL DEFAULT '',
  published INTEGER NOT NULL,
  updated INTEGER,
  status TEXT NOT NULL,
  received INTEGER NOT NULL,
  root TEXT NOT NULL DEFAULT '',
  replies_href TEXT NOT NULL DEFAULT '',
  UNIQUE(post_id, source)
);
CREATE TABLE IF NOT EXISTS subreplies (
  id INTEGER PRIMARY KEY,
  post_id INTEGER NOT NULL REFERENCES posts(id),
  via TEXT NOT NULL,
  parent TEXT NOT NULL,
  source TEXT NOT NULL,
  feed_url TEXT NOT NULL DEFAULT '',
  author TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '',
  published INTEGER NOT NULL,
  UNIQUE(post_id, source)
);
CREATE TABLE IF NOT EXISTS likes (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  target TEXT NOT NULL,
  target_feed TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL,
  updated INTEGER,
  deleted INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS likes_active ON likes(user_id, target) WHERE deleted=0;
CREATE TABLE IF NOT EXISTS likes_in (
  id INTEGER PRIMARY KEY,
  post_id INTEGER NOT NULL REFERENCES posts(id),
  source TEXT NOT NULL,
  feed_url TEXT NOT NULL DEFAULT '',
  author TEXT NOT NULL DEFAULT '',
  trusted INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL,
  UNIQUE(post_id, source)
);
CREATE TABLE IF NOT EXISTS wm_queue (
  id INTEGER PRIMARY KEY,
  dir TEXT NOT NULL,
  user_id INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL,
  target TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_try INTEGER NOT NULL,
  done INTEGER NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS follows (
  user_id INTEGER NOT NULL REFERENCES users(id),
  feed_url TEXT NOT NULL,
  added INTEGER NOT NULL,
  PRIMARY KEY (user_id, feed_url)
);
CREATE TABLE IF NOT EXISTS feeds (
  url TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  link TEXT NOT NULL DEFAULT '',
  fetched INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS items (
  feed_url TEXT NOT NULL,
  guid TEXT NOT NULL,
  link TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '',
  markdown TEXT NOT NULL DEFAULT '',
  published INTEGER NOT NULL,
  updated INTEGER,
  in_reply_to TEXT NOT NULL DEFAULT '',
  in_reply_to_feed TEXT NOT NULL DEFAULT '',
  root TEXT NOT NULL DEFAULT '',
  root_feed TEXT NOT NULL DEFAULT '',
  likes INTEGER NOT NULL DEFAULT 0,
  replies_href TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (feed_url, guid)
);
CREATE TABLE IF NOT EXISTS feed_likes (
  feed_url TEXT NOT NULL,
  guid TEXT NOT NULL,
  target TEXT NOT NULL,
  published INTEGER NOT NULL,
  PRIMARY KEY (feed_url, guid)
);
CREATE INDEX IF NOT EXISTS items_pub ON items(published DESC);
CREATE INDEX IF NOT EXISTS posts_user ON posts(user_id, created DESC);
`

func hasColumn(db *sql.DB, table, col string) bool {
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, col).Scan(&n)
	return n > 0
}

func backfillIdentity(db *sql.DB) error {
	rows, err := db.Query(`SELECT id,title,markdown FROM posts WHERE uid='' OR slug=''`)
	if err != nil {
		return err
	}
	type old struct {
		id        int64
		title, md string
	}
	var todo []old
	for rows.Next() {
		var o old
		if err := rows.Scan(&o.id, &o.title, &o.md); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, o)
	}
	rows.Close()
	for _, o := range todo {
		for try := 0; try < 8; try++ {
			_, err = db.Exec(`UPDATE posts SET uid=?, slug=? WHERE id=?`, newUID(), Slug(o.title, o.md), o.id)
			if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
				break
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// PostBySlug finds a user's post by the last segment of its permalink.
// schemaV1 brings any database made before schema versions existed to version 1. Those databases were
// built by this code adding columns as features arrived, so the statements are idempotent (an ALTER
// that fails means the column is already there) and it is safe to run on a database of any age.
func schemaV1(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	// Databases created by iteration 1 lack the password columns; errors mean they already exist.
	db.Exec(`ALTER TABLE users ADD COLUMN pw_salt BLOB`)
	db.Exec(`ALTER TABLE users ADD COLUMN pw_hash BLOB`)
	db.Exec(`ALTER TABLE users ADD COLUMN avatar TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE feeds ADD COLUMN image TEXT NOT NULL DEFAULT ''`)
	for _, t := range []string{"posts", "items", "likes", "feed_likes"} {
		for _, c := range []string{"parent_title", "parent_desc", "parent_image"} {
			db.Exec(`ALTER TABLE ` + t + ` ADD COLUMN ` + c + ` TEXT NOT NULL DEFAULT ''`)
		}
	}
	for _, c := range []string{"in_reply_to", "in_reply_to_feed", "root", "root_feed"} {
		db.Exec(`ALTER TABLE posts ADD COLUMN ` + c + ` TEXT NOT NULL DEFAULT ''`)
	}
	for _, c := range []string{"in_reply_to_feed", "root", "root_feed"} {
		db.Exec(`ALTER TABLE items ADD COLUMN ` + c + ` TEXT NOT NULL DEFAULT ''`)
	}
	db.Exec(`ALTER TABLE items ADD COLUMN replies_href TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE items ADD COLUMN replies_count INTEGER NOT NULL DEFAULT -1`) // -1: the feed did not say
	for _, c := range []string{"uid", "slug"} {
		db.Exec(`ALTER TABLE posts ADD COLUMN ` + c + ` TEXT NOT NULL DEFAULT ''`)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS posts_slug ON posts(user_id, slug) WHERE slug != ''`); err != nil {
		return err
	}
	if err := backfillIdentity(db); err != nil {
		return err
	}
	for _, c := range []string{"root", "replies_href"} {
		db.Exec(`ALTER TABLE replies ADD COLUMN ` + c + ` TEXT NOT NULL DEFAULT ''`)
	}
	db.Exec(`ALTER TABLE items ADD COLUMN likes INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE items ADD COLUMN keywords TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE posts ADD COLUMN keywords TEXT NOT NULL DEFAULT ''`)
	return nil
}

// directory adds the two tables that tell the instance, without opening any user's data, who follows
// a feed and which users have Webmention jobs waiting. They are a function of the user data and can be
// rebuilt from it.
func directory(tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS local_follows (
		  feed_url TEXT NOT NULL,
		  name TEXT NOT NULL,
		  PRIMARY KEY (feed_url, name)
		)`,
		`CREATE INDEX IF NOT EXISTS local_follows_name ON local_follows(name)`,
		`CREATE TABLE IF NOT EXISTS work (
		  name TEXT PRIMARY KEY,
		  next_due INTEGER NOT NULL
		)`,
		`INSERT OR IGNORE INTO local_follows(feed_url,name) SELECT f.feed_url, u.name FROM follows f JOIN users u ON u.id=f.user_id`,
		`INSERT OR REPLACE INTO work(name,next_due) SELECT u.name, MIN(q.next_try) FROM wm_queue q JOIN users u ON u.id=q.user_id
		   WHERE q.done=0 GROUP BY u.name`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// legacySteps are the migrations of the single-file layout, from version 1 on. Version 1 is schemaV1.
var legacySteps = []func(tx *sql.Tx) error{
	nil,       // 1: schemaV1
	directory, // 2: the directory tables
}

// migrateLegacy brings a single-file database of any age to the last legacy version.
func migrateLegacy(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(legacySteps) {
		return fmt.Errorf("database has schema version %d, but this Weft only knows up to %d: upgrade Weft", v, len(legacySteps))
	}
	if v < 1 {
		if err := schemaV1(db); err != nil {
			return fmt.Errorf("migrating to schema version 1: %w", err)
		}
		if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
			return err
		}
		v = 1
	}
	return runSteps(db, legacySteps, v)
}
