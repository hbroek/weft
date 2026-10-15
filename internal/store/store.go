// Package store holds the SQLite persistence layer.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")
var ErrInvalid = errors.New("invalid")

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

type Store struct{ db *sql.DB }

type User struct {
	ID      int64
	Name    string
	Display string
	Bio     string
}

type Post struct {
	ID       int64
	UserID   int64
	UID      string // stable identity (UUIDv7); published as the feed item's guid
	Slug     string // the permalink's last segment; unique per user, never changes
	Title    string
	Markdown string
	HTML     string
	Created  time.Time
	Updated  *time.Time
	Deleted  bool
	// Reply links; empty unless the post is a reply.
	InReplyTo, InReplyToFeed, Root, RootFeed string
}

// NewPost is the input for AddPost.
type NewPost struct {
	UserID                                   int64
	Title, Markdown, HTML                    string
	InReplyTo, InReplyToFeed, Root, RootFeed string
}

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
  root_feed TEXT NOT NULL DEFAULT ''
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
CREATE TABLE IF NOT EXISTS reactions (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id),
  target TEXT NOT NULL,
  target_feed TEXT NOT NULL DEFAULT '',
  polarity TEXT NOT NULL,
  emoji TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL,
  updated INTEGER,
  deleted INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS reactions_active ON reactions(user_id, target) WHERE deleted=0;
CREATE TABLE IF NOT EXISTS reactions_in (
  id INTEGER PRIMARY KEY,
  post_id INTEGER NOT NULL REFERENCES posts(id),
  source TEXT NOT NULL,
  feed_url TEXT NOT NULL DEFAULT '',
  author TEXT NOT NULL DEFAULT '',
  polarity TEXT NOT NULL,
  emoji TEXT NOT NULL,
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
  plus INTEGER NOT NULL DEFAULT 0,
  minus INTEGER NOT NULL DEFAULT 0,
  replies_href TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (feed_url, guid)
);
CREATE INDEX IF NOT EXISTS items_pub ON items(published DESC);
CREATE INDEX IF NOT EXISTS posts_user ON posts(user_id, created DESC);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// Databases created by iteration 1 lack the password columns; errors mean they already exist.
	db.Exec(`ALTER TABLE users ADD COLUMN pw_salt BLOB`)
	db.Exec(`ALTER TABLE users ADD COLUMN pw_hash BLOB`)
	for _, c := range []string{"in_reply_to", "in_reply_to_feed", "root", "root_feed"} {
		db.Exec(`ALTER TABLE posts ADD COLUMN ` + c + ` TEXT NOT NULL DEFAULT ''`)
	}
	for _, c := range []string{"in_reply_to_feed", "root", "root_feed"} {
		db.Exec(`ALTER TABLE items ADD COLUMN ` + c + ` TEXT NOT NULL DEFAULT ''`)
	}
	db.Exec(`ALTER TABLE items ADD COLUMN replies_href TEXT NOT NULL DEFAULT ''`)
	for _, c := range []string{"uid", "slug"} {
		db.Exec(`ALTER TABLE posts ADD COLUMN ` + c + ` TEXT NOT NULL DEFAULT ''`)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS posts_slug ON posts(user_id, slug) WHERE slug != ''`); err != nil {
		db.Close()
		return nil, err
	}
	if err := backfillIdentity(db); err != nil {
		db.Close()
		return nil, err
	}
	for _, c := range []string{"root", "replies_href"} {
		db.Exec(`ALTER TABLE replies ADD COLUMN ` + c + ` TEXT NOT NULL DEFAULT ''`)
	}
	for _, c := range []string{"plus", "minus"} {
		db.Exec(`ALTER TABLE items ADD COLUMN ` + c + ` INTEGER NOT NULL DEFAULT 0`)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// AddUser creates a user and returns a fresh API token (shown once).
func (s *Store) AddUser(name, display, bio string) (token string, err error) {
	if !nameRE.MatchString(name) {
		return "", ErrInvalid
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token = hex.EncodeToString(b)
	if display == "" {
		display = name
	}
	_, err = s.db.Exec(`INSERT INTO users(name,display,bio,token_hash,created) VALUES(?,?,?,?,?)`,
		name, display, bio, hashToken(token), time.Now().Unix())
	return token, err
}

// ResetToken replaces a user's API token and returns the new one (shown once).
func (s *Store) ResetToken(name string) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	res, err := s.db.Exec(`UPDATE users SET token_hash=? WHERE name=?`, hashToken(tok), name)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return tok, nil
}

// UserNames lists all user names.
func (s *Store) UserNames() ([]string, error) {
	rows, err := s.db.Query(`SELECT name FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) UserByID(id int64) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`SELECT id,name,display,bio FROM users WHERE id=?`, id).Scan(&u.ID, &u.Name, &u.Display, &u.Bio)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) UserByName(name string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`SELECT id,name,display,bio FROM users WHERE name=?`, name).Scan(&u.ID, &u.Name, &u.Display, &u.Bio)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) UserByToken(token string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`SELECT id,name,display,bio FROM users WHERE token_hash=?`, hashToken(token)).Scan(&u.ID, &u.Name, &u.Display, &u.Bio)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) AddPost(n NewPost) (*Post, error) {
	now := time.Now()
	uid := newUID()
	var res sql.Result
	var slug string
	var err error
	for try := 0; try < 8; try++ { // a collision in the 4-character suffix is rare; draw again
		slug = Slug(n.Title, n.Markdown)
		res, err = s.db.Exec(`INSERT INTO posts(user_id,uid,slug,title,markdown,html,created,in_reply_to,in_reply_to_feed,root,root_feed) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			n.UserID, uid, slug, n.Title, n.Markdown, n.HTML, now.Unix(), n.InReplyTo, n.InReplyToFeed, n.Root, n.RootFeed)
		if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Post{ID: id, UserID: n.UserID, UID: uid, Slug: slug, Title: n.Title, Markdown: n.Markdown, HTML: n.HTML, Created: time.Unix(now.Unix(), 0),
		InReplyTo: n.InReplyTo, InReplyToFeed: n.InReplyToFeed, Root: n.Root, RootFeed: n.RootFeed}, nil
}

// backfillIdentity gives posts made before uid and slug existed an identity.
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
func (s *Store) PostBySlug(userID int64, slug string) (*Post, error) {
	p, err := scanPost(s.db.QueryRow(`SELECT `+postCols+` FROM posts WHERE user_id=? AND slug=?`, userID, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// SlugByID returns the slug of a post, whoever owns it.
func (s *Store) SlugByID(id int64) (string, error) {
	var slug string
	err := s.db.QueryRow(`SELECT slug FROM posts WHERE id=?`, id).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return slug, err
}

func scanPost(r interface{ Scan(...any) error }) (*Post, error) {
	p := &Post{}
	var created int64
	var updated sql.NullInt64
	var del int
	if err := r.Scan(&p.ID, &p.UserID, &p.UID, &p.Slug, &p.Title, &p.Markdown, &p.HTML, &created, &updated, &del,
		&p.InReplyTo, &p.InReplyToFeed, &p.Root, &p.RootFeed); err != nil {
		return nil, err
	}
	p.Created = time.Unix(created, 0)
	if updated.Valid {
		t := time.Unix(updated.Int64, 0)
		p.Updated = &t
	}
	p.Deleted = del != 0
	return p, nil
}

const postCols = `id,user_id,uid,slug,title,markdown,html,created,updated,deleted,in_reply_to,in_reply_to_feed,root,root_feed`

func (s *Store) Post(userID, id int64) (*Post, error) {
	p, err := scanPost(s.db.QueryRow(`SELECT `+postCols+` FROM posts WHERE user_id=? AND id=?`, userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// RecentPosts returns a user's non-deleted posts, newest first.
func (s *Store) RecentPosts(userID int64, limit int) ([]*Post, error) {
	rows, err := s.db.Query(`SELECT `+postCols+` FROM posts WHERE user_id=? AND deleted=0 ORDER BY created DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdatePost replaces a post's title and content and stamps its update time.
func (s *Store) UpdatePost(userID, id int64, title, markdown, html string) (*Post, error) {
	res, err := s.db.Exec(`UPDATE posts SET title=?,markdown=?,html=?,updated=? WHERE user_id=? AND id=? AND deleted=0`,
		title, markdown, html, time.Now().Unix(), userID, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.Post(userID, id)
}

// DeletePost tombstones a post and discards its content. The id stays reserved so the
// permalink can answer 410 Gone.
func (s *Store) DeletePost(userID, id int64) error {
	res, err := s.db.Exec(`UPDATE posts SET deleted=1,title='',markdown='',html='',updated=? WHERE user_id=? AND id=? AND deleted=0`,
		time.Now().Unix(), userID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PostsInThread returns the user's own live posts that belong to the thread rooted at root.
func (s *Store) PostsInThread(userID int64, root string) ([]*Post, error) {
	rows, err := s.db.Query(`SELECT `+postCols+` FROM posts WHERE user_id=? AND deleted=0 AND root=? ORDER BY created, id`, userID, root)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
