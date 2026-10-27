// Package store holds the SQLite persistence layer.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/feed"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")
var ErrInvalid = errors.New("invalid")

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// Space is one user's data: profile and credentials, posts, replies and likes received, likes given,
// follows and the Webmention queue. A Space never reads another user's rows, and reads the shared
// caches only through Instance methods, so that each can live in its own database file.
type Space struct {
	in   *Instance
	db   lazyDB
	name string
	uid  int64
}

type User struct {
	ID      int64
	Name    string
	Display string
	Bio     string
	Avatar  string // an http(s) image address, or "" (the page shows the first letter of the name)
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
	Keywords                                 []string
	Parent                                   feed.ParentInfo // optional title, description and image of the page replied to
}

// NewPost is the input for AddPost.
type NewPost struct {
	UserID                                   int64
	Title, Markdown, HTML                    string
	InReplyTo, InReplyToFeed, Root, RootFeed string
	Keywords                                 []string
	Parent                                   feed.ParentInfo // optional title, description and image of the page replied to
	// Slug is the words of the address the author chose, if any (see CleanSlug); empty means the words are
	// taken from the title or the first sentence. The short code that makes it unique is added either way.
	Slug string
	// Created is when the post was written; the zero value means now. Importers set it.
	Created time.Time
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// UpdateProfile sets the name shown, the bio and the avatar address of a user. An empty display
// name falls back to the user name.
func (s *Space) UpdateProfile(userID int64, display, bio, avatar string) error {
	if display == "" {
		if err := s.db.QueryRow(`SELECT name FROM users WHERE id=?`, userID).Scan(&display); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`UPDATE users SET display=?, bio=?, avatar=? WHERE id=?`, display, bio, avatar, userID)
	return err
}

// ResetToken replaces the user's API token and returns the new one (shown once).
func (s *Space) ResetToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b)
	res, err := s.db.Exec(`UPDATE users SET token_hash=? WHERE name=?`, hashToken(tok), s.name)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return tok, nil
}

// UserByToken returns the space's user if token is their API token.
func (s *Space) UserByToken(token string) (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`SELECT id,name,display,bio,avatar FROM users WHERE token_hash=? AND name=?`, hashToken(token), s.name).Scan(&u.ID, &u.Name, &u.Display, &u.Bio, &u.Avatar)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Space) AddPost(n NewPost) (*Post, error) {
	now := n.Created
	if now.IsZero() {
		now = time.Now()
	}
	uid := newUID()
	parent := n.Parent.Clean()
	var res sql.Result
	var slug string
	var err error
	words := SlugBase(n.Title, n.Markdown)
	if strings.TrimSpace(n.Slug) != "" { // the words the author chose
		if words, err = CleanSlug(n.Slug); err != nil {
			return nil, err
		}
	}
	for try := 0; try < 8; try++ { // a collision in the 4-character code is rare; draw again
		slug = words + slugSep + Suffix()
		res, err = s.db.Exec(`INSERT INTO posts(user_id,uid,slug,title,markdown,html,created,in_reply_to,in_reply_to_feed,root,root_feed,keywords,parent_title,parent_desc,parent_image) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			n.UserID, uid, slug, n.Title, n.Markdown, n.HTML, now.Unix(), n.InReplyTo, n.InReplyToFeed, n.Root, n.RootFeed, packKeywords(n.Keywords), parent.Title, parent.Description, parent.Image)
		if err == nil || !strings.Contains(err.Error(), "UNIQUE") {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Post{ID: id, UserID: n.UserID, UID: uid, Slug: slug, Title: n.Title, Markdown: n.Markdown, HTML: n.HTML, Created: time.Unix(now.Unix(), 0),
		InReplyTo: n.InReplyTo, InReplyToFeed: n.InReplyToFeed, Root: n.Root, RootFeed: n.RootFeed, Keywords: feed.Keywords(n.Keywords), Parent: parent}, nil
}

// backfillIdentity gives posts made before uid and slug existed an identity.
func (s *Space) PostBySlug(userID int64, slug string) (*Post, error) {
	p, err := scanPost(s.db.QueryRow(`SELECT `+postCols+` FROM posts WHERE user_id=? AND slug=?`, userID, slug))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// SlugByID returns the slug of a post, whoever owns it.
func (s *Space) SlugByID(id int64) (string, error) {
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
	var kw string
	if err := r.Scan(&p.ID, &p.UserID, &p.UID, &p.Slug, &p.Title, &p.Markdown, &p.HTML, &created, &updated, &del,
		&p.InReplyTo, &p.InReplyToFeed, &p.Root, &p.RootFeed, &kw, &p.Parent.Title, &p.Parent.Description, &p.Parent.Image); err != nil {
		return nil, err
	}
	p.Keywords = unpackKeywords(kw)
	p.Created = time.Unix(created, 0)
	if updated.Valid {
		t := time.Unix(updated.Int64, 0)
		p.Updated = &t
	}
	p.Deleted = del != 0
	return p, nil
}

const postCols = `id,user_id,uid,slug,title,markdown,html,created,updated,deleted,in_reply_to,in_reply_to_feed,root,root_feed,keywords,parent_title,parent_desc,parent_image`

// packKeywords stores keywords one per line; a keyword never holds a newline.
func packKeywords(k []string) string { return strings.Join(feed.Keywords(k), "\n") }

func unpackKeywords(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func (s *Space) Post(userID, id int64) (*Post, error) {
	p, err := scanPost(s.db.QueryRow(`SELECT `+postCols+` FROM posts WHERE user_id=? AND id=?`, userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// RecentPosts returns a user's non-deleted posts, newest first.
func (s *Space) RecentPosts(userID int64, limit int) ([]*Post, error) {
	return s.PostsPage(userID, limit, 0)
}

// PostsBefore returns limit of the user's posts created before the Unix time before (0: no bound), newest first.
func (s *Space) PostsBefore(userID, before int64, limit int) ([]*Post, error) {
	if before <= 0 {
		before = math.MaxInt64
	}
	rows, err := s.db.Query(`SELECT `+postCols+` FROM posts WHERE user_id=? AND deleted=0 AND created<? ORDER BY created DESC, id DESC LIMIT ?`, userID, before, limit)
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

// PostsPage returns limit of the user's posts, newest first, after skipping offset of them.
func (s *Space) PostsPage(userID int64, limit, offset int) ([]*Post, error) {
	rows, err := s.db.Query(`SELECT `+postCols+` FROM posts WHERE user_id=? AND deleted=0 ORDER BY created DESC, id DESC LIMIT ? OFFSET ?`, userID, limit, offset)
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

// PostsWithKeyword is PostsPage for the posts that carry the keyword (ignoring case).
func (s *Space) PostsWithKeyword(userID int64, kw string, limit, offset int) ([]*Post, error) {
	rows, err := s.db.Query(`SELECT `+postCols+` FROM posts WHERE user_id=? AND deleted=0 AND keywords<>'' ORDER BY created DESC, id DESC`, userID)
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
		if !feed.HasKeyword(p.Keywords, kw) {
			continue
		}
		if offset > 0 {
			offset--
			continue
		}
		if out = append(out, p); len(out) == limit {
			break
		}
	}
	return out, rows.Err()
}

// UpdatePost replaces a post's title, content and keywords and stamps its update time.
func (s *Space) UpdatePost(userID, id int64, title, markdown, html string, keywords []string) (*Post, error) {
	res, err := s.db.Exec(`UPDATE posts SET title=?,markdown=?,html=?,keywords=?,updated=? WHERE user_id=? AND id=? AND deleted=0`,
		title, markdown, html, packKeywords(keywords), time.Now().Unix(), userID, id)
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
func (s *Space) DeletePost(userID, id int64) error {
	res, err := s.db.Exec(`UPDATE posts SET deleted=1,title='',markdown='',html='',keywords='',parent_title='',parent_desc='',parent_image='',updated=? WHERE user_id=? AND id=? AND deleted=0`,
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
func (s *Space) PostsInThread(userID int64, root string) ([]*Post, error) {
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
