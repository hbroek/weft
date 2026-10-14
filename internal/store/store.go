// Package store holds the SQLite persistence layer.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
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
	Title    string
	Markdown string
	HTML     string
	Created  time.Time
	Updated  *time.Time
	Deleted  bool
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
  deleted INTEGER NOT NULL DEFAULT 0
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

func (s *Store) AddPost(userID int64, title, markdown, html string) (*Post, error) {
	now := time.Now()
	res, err := s.db.Exec(`INSERT INTO posts(user_id,title,markdown,html,created) VALUES(?,?,?,?,?)`,
		userID, title, markdown, html, now.Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Post{ID: id, UserID: userID, Title: title, Markdown: markdown, HTML: html, Created: time.Unix(now.Unix(), 0)}, nil
}

func scanPost(r interface{ Scan(...any) error }) (*Post, error) {
	p := &Post{}
	var created int64
	var updated sql.NullInt64
	var del int
	if err := r.Scan(&p.ID, &p.UserID, &p.Title, &p.Markdown, &p.HTML, &created, &updated, &del); err != nil {
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

const postCols = `id,user_id,title,markdown,html,created,updated,deleted`

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
