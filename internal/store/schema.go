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
CREATE TABLE IF NOT EXISTS media (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  mime TEXT NOT NULL,
  size INTEGER NOT NULL,
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  origin TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL,
  data BLOB NOT NULL
);
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
  moved_to TEXT NOT NULL DEFAULT ''
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
`

func spaceV1(tx *sql.Tx) error { _, err := tx.Exec(spaceSchema); return err }

// spaceV2SQL adds the flag for a temporary password: an administrator set it, and the user must replace it at
// the first sign-in. It is separate from spaceSchema because a file made before it has to get it too.
const spaceV2SQL = `ALTER TABLE users ADD COLUMN must_change INTEGER NOT NULL DEFAULT 0;`

func spaceV2(tx *sql.Tx) error { _, err := tx.Exec(spaceV2SQL); return err }

// spaceV3SQL adds a post's canonical address: where the content first appeared, for a post that republishes it.
const spaceV3SQL = `ALTER TABLE posts ADD COLUMN canonical TEXT NOT NULL DEFAULT '';`

func spaceV3(tx *sql.Tx) error    { _, err := tx.Exec(spaceV3SQL); return err }
func instanceV1(tx *sql.Tx) error { _, err := tx.Exec(instanceSchema); return err }

// instanceV6 adds moving an account to another server: the state an account is in while it moves, the move
// itself (the code's hash, the address the account will have, the copy on offer), and the Webmentions that
// arrive meanwhile.
func instanceV6(tx *sql.Tx) error {
	_, err := tx.Exec(`
ALTER TABLE users ADD COLUMN move_state TEXT NOT NULL DEFAULT '';
CREATE TABLE moves (
  name TEXT PRIMARY KEY,
  dest TEXT NOT NULL,
  code_hash TEXT NOT NULL DEFAULT '',
  attempts INTEGER NOT NULL DEFAULT 0,
  xfer_hash TEXT NOT NULL DEFAULT '',
  expires INTEGER NOT NULL,
  file TEXT NOT NULL DEFAULT '',
  sha256 TEXT NOT NULL DEFAULT '',
  size INTEGER NOT NULL DEFAULT 0,
  schema INTEGER NOT NULL DEFAULT 0,
  fingerprint TEXT NOT NULL DEFAULT ''
);
CREATE TABLE held (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  source TEXT NOT NULL,
  target TEXT NOT NULL,
  created INTEGER NOT NULL
);
CREATE INDEX held_name ON held(name);`)
	return err
}

// instanceV8 is the waiting list for accounts: who asked for one, how to answer, and where they write.
func instanceV8(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE waitlist (
	id      INTEGER PRIMARY KEY,
	name    TEXT NOT NULL UNIQUE,
	contact TEXT NOT NULL UNIQUE,
	links   TEXT NOT NULL,
	note    TEXT NOT NULL DEFAULT '',
	created INTEGER NOT NULL
)`)
	return err
}

// instanceV7 remembers where a followed post, a repost, says it first appeared.
func instanceV7(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE items ADD COLUMN canonical TEXT NOT NULL DEFAULT ''`)
	return err
}

// instanceV5 remembers where a followed post lists everyone who liked it.
func instanceV5(tx *sql.Tx) error {
	_, err := tx.Exec(`ALTER TABLE items ADD COLUMN likes_href TEXT NOT NULL DEFAULT ''`)
	return err
}

// instanceV4 remembers what a feed's server said about the copy fetched (to ask "has it changed?" next
// time) and how many fetches in a row failed (to wait longer before trying again).
func instanceV4(tx *sql.Tx) error {
	_, err := tx.Exec(`
ALTER TABLE feeds ADD COLUMN etag TEXT NOT NULL DEFAULT '';
ALTER TABLE feeds ADD COLUMN last_modified TEXT NOT NULL DEFAULT '';
ALTER TABLE feeds ADD COLUMN fails INTEGER NOT NULL DEFAULT 0;
ALTER TABLE feeds ADD COLUMN next_try INTEGER NOT NULL DEFAULT 0;`)
	return err
}

// instanceV3 separates a domain that is verified (the TXT record proves it is the user's) from one that is
// active (it reaches this server, so it can be the user's address). A domain that was verified before this
// existed was already the address, so it is active.
func instanceV3(tx *sql.Tx) error {
	_, err := tx.Exec(`
ALTER TABLE domains ADD COLUMN active INTEGER NOT NULL DEFAULT 0;
UPDATE domains SET active=verified;`)
	return err
}

// instanceV2 adds custom domains.
func instanceV2(tx *sql.Tx) error {
	_, err := tx.Exec(`
-- anyone may claim a domain; the claim becomes theirs when their own token is found in the domain's DNS,
-- and only one claim per domain can get that far
CREATE TABLE domains (
  domain TEXT NOT NULL,
  name TEXT NOT NULL UNIQUE,
  token TEXT NOT NULL,
  verified INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL,
  PRIMARY KEY (domain, name)
);
CREATE UNIQUE INDEX domains_verified ON domains(domain) WHERE verified=1;`)
	return err
}
