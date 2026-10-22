CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  display TEXT NOT NULL DEFAULT '',
  bio TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL DEFAULT '',
  pw_salt BLOB,
  pw_hash BLOB,
  created INTEGER NOT NULL
, avatar TEXT NOT NULL DEFAULT '');
CREATE TABLE sessions (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  expires INTEGER NOT NULL
);
CREATE TABLE posts (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  title TEXT NOT NULL DEFAULT '',
  markdown TEXT NOT NULL,
  html TEXT NOT NULL,
  created INTEGER NOT NULL,
  updated INTEGER,
  deleted INTEGER NOT NULL DEFAULT 0
, in_reply_to TEXT NOT NULL DEFAULT '', in_reply_to_feed TEXT NOT NULL DEFAULT '', root TEXT NOT NULL DEFAULT '', root_feed TEXT NOT NULL DEFAULT '', uid TEXT NOT NULL DEFAULT '', slug TEXT NOT NULL DEFAULT '', keywords TEXT NOT NULL DEFAULT '', parent_title TEXT NOT NULL DEFAULT '', parent_desc TEXT NOT NULL DEFAULT '', parent_image TEXT NOT NULL DEFAULT '');
CREATE INDEX posts_user ON posts(user_id, created DESC);
CREATE TABLE follows (
  user_id INTEGER NOT NULL REFERENCES users(id),
  feed_url TEXT NOT NULL,
  added INTEGER NOT NULL,
  PRIMARY KEY (user_id, feed_url)
);
CREATE TABLE feeds (
  url TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  link TEXT NOT NULL DEFAULT '',
  fetched INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT ''
, image TEXT NOT NULL DEFAULT '');
CREATE TABLE items (
  feed_url TEXT NOT NULL,
  guid TEXT NOT NULL,
  link TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT '',
  markdown TEXT NOT NULL DEFAULT '',
  published INTEGER NOT NULL,
  updated INTEGER,
  in_reply_to TEXT NOT NULL DEFAULT '', in_reply_to_feed TEXT NOT NULL DEFAULT '', root TEXT NOT NULL DEFAULT '', root_feed TEXT NOT NULL DEFAULT '', likes INTEGER NOT NULL DEFAULT 0, replies_href TEXT NOT NULL DEFAULT '', replies_count INTEGER NOT NULL DEFAULT -1, keywords TEXT NOT NULL DEFAULT '', parent_title TEXT NOT NULL DEFAULT '', parent_desc TEXT NOT NULL DEFAULT '', parent_image TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (feed_url, guid)
);
CREATE INDEX items_pub ON items(published DESC);
CREATE TABLE replies (
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
  received INTEGER NOT NULL, root TEXT NOT NULL DEFAULT '', replies_href TEXT NOT NULL DEFAULT '',
  UNIQUE(post_id, source)
);
CREATE TABLE "likes" (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  target TEXT NOT NULL,
  target_feed TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL,
  updated INTEGER,
  deleted INTEGER NOT NULL DEFAULT 0
, parent_title TEXT NOT NULL DEFAULT '', parent_desc TEXT NOT NULL DEFAULT '', parent_image TEXT NOT NULL DEFAULT '');
CREATE TABLE "likes_in" (
  id INTEGER PRIMARY KEY,
  post_id INTEGER NOT NULL REFERENCES posts(id),
  source TEXT NOT NULL,
  feed_url TEXT NOT NULL DEFAULT '',
  author TEXT NOT NULL DEFAULT '',
  trusted INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL,
  UNIQUE(post_id, source)
);
CREATE TABLE wm_queue (
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
CREATE TABLE subreplies (
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
CREATE UNIQUE INDEX posts_slug ON posts(user_id, slug) WHERE slug != '';
CREATE UNIQUE INDEX likes_active ON likes(user_id, target) WHERE deleted=0;
CREATE TABLE feed_likes (
  feed_url TEXT NOT NULL,
  guid TEXT NOT NULL,
  target TEXT NOT NULL,
  published INTEGER NOT NULL, parent_title TEXT NOT NULL DEFAULT '', parent_desc TEXT NOT NULL DEFAULT '', parent_image TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (feed_url, guid)
);
