package store

import (
	"database/sql"
	"errors"
	"time"
)

// ---- Likes the user has published ----

type Like struct {
	ID, UserID         int64
	Target, TargetFeed string
	Created            time.Time
	Updated            *time.Time
	Deleted            bool
}

const likeCols = `id,user_id,target,target_feed,created,updated,deleted`

func scanLike(r interface{ Scan(...any) error }) (*Like, error) {
	x := &Like{}
	var created int64
	var upd *int64
	var del int
	if err := r.Scan(&x.ID, &x.UserID, &x.Target, &x.TargetFeed, &created, &upd, &del); err != nil {
		return nil, err
	}
	x.Created = time.Unix(created, 0)
	if upd != nil {
		t := time.Unix(*upd, 0)
		x.Updated = &t
	}
	x.Deleted = del != 0
	return x, nil
}

// SetLike publishes a like of target. Liking again returns the existing like; a feed hint, if given,
// replaces the stored one.
func (s *Store) SetLike(userID int64, target, targetFeed string) (*Like, error) {
	if old, err := s.ActiveLike(userID, target); err == nil {
		if targetFeed != "" && targetFeed != old.TargetFeed {
			if _, err := s.db.Exec(`UPDATE likes SET target_feed=?,updated=? WHERE id=?`, targetFeed, time.Now().Unix(), old.ID); err != nil {
				return nil, err
			}
			return s.likeByID(old.ID)
		}
		return old, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	res, err := s.db.Exec(`INSERT INTO likes(user_id,target,target_feed,created) VALUES(?,?,?,?)`,
		userID, target, targetFeed, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.likeByID(id)
}

func (s *Store) likeByID(id int64) (*Like, error) {
	x, err := scanLike(s.db.QueryRow(`SELECT `+likeCols+` FROM likes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return x, err
}

// Like returns one of the user's likes, including a withdrawn one (for its tombstone).
func (s *Store) Like(userID, id int64) (*Like, error) {
	x, err := s.likeByID(id)
	if err != nil {
		return nil, err
	}
	if x.UserID != userID {
		return nil, ErrNotFound
	}
	return x, nil
}

// ActiveLike returns the user's current like of target.
func (s *Store) ActiveLike(userID int64, target string) (*Like, error) {
	x, err := scanLike(s.db.QueryRow(`SELECT `+likeCols+` FROM likes WHERE user_id=? AND target=? AND deleted=0`, userID, target))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return x, err
}

// RemoveLike withdraws the user's like of target. The row stays so its permalink can answer 410.
func (s *Store) RemoveLike(userID int64, target string) (*Like, error) {
	x, err := s.ActiveLike(userID, target)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`UPDATE likes SET deleted=1, updated=? WHERE id=?`, time.Now().Unix(), x.ID); err != nil {
		return nil, err
	}
	return s.likeByID(x.ID)
}

// ActiveLikes lists the user's current likes, newest first.
func (s *Store) ActiveLikes(userID int64, limit int) ([]*Like, error) {
	rows, err := s.db.Query(`SELECT `+likeCols+` FROM likes WHERE user_id=? AND deleted=0 ORDER BY created DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Like
	for rows.Next() {
		x, err := scanLike(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ---- Likes received by Webmention ----

type LikeIn struct {
	ID, PostID              int64
	Source, FeedURL, Author string
	Trusted                 bool
	Created                 time.Time
}

// UpsertLikeIn stores or refreshes a verified like.
func (s *Store) UpsertLikeIn(r LikeIn) error {
	_, err := s.db.Exec(`INSERT INTO likes_in(post_id,source,feed_url,author,trusted,created) VALUES(?,?,?,?,?,?)
		ON CONFLICT(post_id,source) DO UPDATE SET feed_url=excluded.feed_url, author=excluded.author, trusted=excluded.trusted`,
		r.PostID, r.Source, r.FeedURL, r.Author, b2i(r.Trusted), time.Now().Unix())
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) DeleteLikeIn(postID int64, source string) error {
	_, err := s.db.Exec(`DELETE FROM likes_in WHERE post_id=? AND source=?`, postID, source)
	return err
}

// LikesInFor lists every verified like of a post, trusted or not.
func (s *Store) LikesInFor(postID int64) ([]LikeIn, error) {
	rows, err := s.db.Query(`SELECT id,post_id,source,feed_url,author,trusted,created FROM likes_in WHERE post_id=? ORDER BY created,id`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LikeIn
	for rows.Next() {
		var r LikeIn
		var tr int
		var c int64
		if err := rows.Scan(&r.ID, &r.PostID, &r.Source, &r.FeedURL, &r.Author, &tr, &c); err != nil {
			return nil, err
		}
		r.Trusted, r.Created = tr != 0, time.Unix(c, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Tallies returns, for each of the user's posts that has any, the number of likes from trusted senders.
func (s *Store) Tallies(userID int64) (map[int64]int, error) {
	rows, err := s.db.Query(`SELECT l.post_id, COUNT(*) FROM likes_in l JOIN posts p ON p.id=l.post_id
		WHERE p.user_id=? AND l.trusted=1 GROUP BY l.post_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// Likers returns, for each of the user's posts that has any, the names of the senders whose likes
// count (the ones the user follows), oldest like first. A sender without a name is shown by its feed.
func (s *Store) Likers(userID int64) (map[int64][]string, error) {
	rows, err := s.db.Query(`SELECT l.post_id, COALESCE(NULLIF(l.author,''), l.feed_url) FROM likes_in l JOIN posts p ON p.id=l.post_id
		WHERE p.user_id=? AND l.trusted=1 ORDER BY l.created, l.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = append(out[id], name)
	}
	return out, rows.Err()
}
