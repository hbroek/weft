package store

import "database/sql"

// A user's file holds everything that is theirs. Ids are kept in the user's own counters. user_id is
// always the id of the one row in users: the column is a leftover of the single-file layout, kept so
// that no query or row had to change, and can go in a later migration.
//
// A new file gets only the tables every account uses. The rest (lazySchema) is created by the first
// write that needs it, see lazy.go: an empty table costs a page and each of its indexes another, which
// for an account that never receives a reply is most of its file.
const spaceSchema = `
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  display TEXT NOT NULL DEFAULT '',
  bio TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL DEFAULT '',
  pw_salt BLOB,
  pw_hash BLOB,
  created INTEGER NOT NULL,
  avatar TEXT NOT NULL DEFAULT ''
);
CREATE TABLE posts (
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
  uid TEXT NOT NULL DEFAULT '',
  slug TEXT NOT NULL DEFAULT '',
  keywords TEXT NOT NULL DEFAULT '',
  parent_title TEXT NOT NULL DEFAULT '',
  parent_desc TEXT NOT NULL DEFAULT '',
  parent_image TEXT NOT NULL DEFAULT ''
);
CREATE INDEX posts_user ON posts(user_id, created DESC);
CREATE UNIQUE INDEX posts_slug ON posts(user_id, slug) WHERE slug != '';
CREATE TABLE follows (
  user_id INTEGER NOT NULL REFERENCES users(id),
  feed_url TEXT NOT NULL,
  added INTEGER NOT NULL,
  PRIMARY KEY (user_id, feed_url)
);
`

// lazySchema is created on first use, all at once, and may already exist (a file made by weft split or
// import has it).
const lazySchema = `
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  expires INTEGER NOT NULL
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
  deleted INTEGER NOT NULL DEFAULT 0,
  parent_title TEXT NOT NULL DEFAULT '',
  parent_desc TEXT NOT NULL DEFAULT '',
  parent_image TEXT NOT NULL DEFAULT ''
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
`

// The instance's file holds what is shared or is the directory. Nothing in it is a user's content.
const instanceSchema = `
CREATE TABLE feeds (
  url TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  link TEXT NOT NULL DEFAULT '',
  fetched INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  image TEXT NOT NULL DEFAULT ''
);
CREATE TABLE items (
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
  replies_count INTEGER NOT NULL DEFAULT -1,
  keywords TEXT NOT NULL DEFAULT '',
  parent_title TEXT NOT NULL DEFAULT '',
  parent_desc TEXT NOT NULL DEFAULT '',
  parent_image TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (feed_url, guid)
);
CREATE INDEX items_pub ON items(published DESC);
CREATE TABLE feed_likes (
  feed_url TEXT NOT NULL,
  guid TEXT NOT NULL,
  target TEXT NOT NULL,
  published INTEGER NOT NULL,
  parent_title TEXT NOT NULL DEFAULT '',
  parent_desc TEXT NOT NULL DEFAULT '',
  parent_image TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (feed_url, guid)
);
CREATE TABLE users (
  name TEXT PRIMARY KEY,
  status TEXT NOT NULL DEFAULT 'active',
  created INTEGER NOT NULL,
  moved_to TEXT NOT NULL DEFAULT '',
  moved_until INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE local_follows (
  feed_url TEXT NOT NULL,
  name TEXT NOT NULL,
  PRIMARY KEY (feed_url, name)
);
CREATE INDEX local_follows_name ON local_follows(name);
CREATE TABLE work (
  name TEXT PRIMARY KEY,
  next_due INTEGER NOT NULL
);
CREATE TABLE meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
INSERT INTO meta(key,value) VALUES ('shard_depth','2');
`

func spaceV1(tx *sql.Tx) error    { _, err := tx.Exec(spaceSchema); return err }
func instanceV1(tx *sql.Tx) error { _, err := tx.Exec(instanceSchema); return err }
