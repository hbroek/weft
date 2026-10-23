package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/hbroek/weft/internal/feed"
)

// ---- Likes the user has published ----

type Like struct {
	ID, UserID         int64
	Target, TargetFeed string
	Created            time.Time
	Updated            *time.Time
	Deleted            bool
	Parent             feed.ParentInfo // optional title, description and image of the liked page
}

const likeCols = `id,user_id,target,target_feed,created,updated,deleted,parent_title,parent_desc,parent_image`

func scanLike(r interface{ Scan(...any) error }) (*Like, error) {
	x := &Like{}
	var created int64
	var upd *int64
	var del int
	if err := r.Scan(&x.ID, &x.UserID, &x.Target, &x.TargetFeed, &created, &upd, &del, &x.Parent.Title, &x.Parent.Description, &x.Parent.Image); err != nil {
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
func (s *Space) SetLike(userID int64, target, targetFeed string) (*Like, error) {
	return s.SetLikeInfo(userID, target, targetFeed, feed.ParentInfo{})
}

// SetLikeInfo is SetLike with what the liked page looked like (title, description, image), for a
// page that publishes no feed. Liking again with new information replaces the stored one.
func (s *Space) SetLikeInfo(userID int64, target, targetFeed string, info feed.ParentInfo) (*Like, error) {
	info = info.Clean()
	if old, err := s.ActiveLike(userID, target); err == nil {
		newFeed := targetFeed != "" && targetFeed != old.TargetFeed
		newInfo := !info.Empty() && info != old.Parent
		if newFeed || newInfo {
			if !newFeed {
				targetFeed = old.TargetFeed
			}
			if !newInfo {
				info = old.Parent
			}
			if _, err := s.db.Exec(`UPDATE likes SET target_feed=?,parent_title=?,parent_desc=?,parent_image=?,updated=? WHERE id=?`,
				targetFeed, info.Title, info.Description, info.Image, time.Now().Unix(), old.ID); err != nil {
				return nil, err
			}
			return s.likeByID(old.ID)
		}
		return old, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	res, err := s.db.Exec(`INSERT INTO likes(user_id,target,target_feed,created,parent_title,parent_desc,parent_image) VALUES(?,?,?,?,?,?,?)`,
		userID, target, targetFeed, time.Now().Unix(), info.Title, info.Description, info.Image)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.likeByID(id)
}

func (s *Space) likeByID(id int64) (*Like, error) {
	x, err := scanLike(s.db.QueryRow(`SELECT `+likeCols+` FROM likes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return x, err
}

// Like returns one of the user's likes, including a withdrawn one (for its tombstone).
func (s *Space) Like(userID, id int64) (*Like, error) {
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
func (s *Space) ActiveLike(userID int64, target string) (*Like, error) {
	x, err := scanLike(s.db.QueryRow(`SELECT `+likeCols+` FROM likes WHERE user_id=? AND target=? AND deleted=0`, userID, target))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return x, err
}

// RemoveLike withdraws the user's like of target. The row stays so its permalink can answer 410.
func (s *Space) RemoveLike(userID int64, target string) (*Like, error) {
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
func (s *Space) ActiveLikes(userID int64, limit int) ([]*Like, error) {
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
func (s *Space) UpsertLikeIn(r LikeIn) error {
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

func (s *Space) DeleteLikeIn(postID int64, source string) error {
	_, err := s.db.Exec(`DELETE FROM likes_in WHERE post_id=? AND source=?`, postID, source)
	return err
}

// LikesInFor lists every verified like of a post, trusted or not.
func (s *Space) LikesInFor(postID int64) ([]LikeIn, error) {
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

// LikesInOf lists every received like on the user's posts, counted or not.
func (s *Space) LikesInOf(userID int64) ([]LikeIn, error) {
	rows, err := s.db.Query(`SELECT l.id,l.post_id,l.source,l.feed_url,l.author,l.trusted,l.created FROM likes_in l
		JOIN posts p ON p.id=l.post_id WHERE p.user_id=?`, userID)
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

// SetLikeInTrust changes whether a received like is counted.
func (s *Space) SetLikeInTrust(postID int64, source string, trusted bool) error {
	_, err := s.db.Exec(`UPDATE likes_in SET trusted=? WHERE post_id=? AND source=?`, b2i(trusted), postID, source)
	return err
}

// Tallies returns, for each of the user's posts that has any, the number of likes received.
func (s *Space) Tallies(userID int64) (map[int64]int, error) {
	rows, err := s.db.Query(`SELECT l.post_id, COUNT(*) FROM likes_in l JOIN posts p ON p.id=l.post_id
		WHERE p.user_id=? GROUP BY l.post_id`, userID)
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
// arrived, oldest like first. A sender without a name is shown by its feed.
func (s *Space) Likers(userID int64) (map[int64][]string, error) {
	rows, err := s.db.Query(`SELECT l.post_id, COALESCE(NULLIF(l.author,''), l.feed_url) FROM likes_in l JOIN posts p ON p.id=l.post_id
		WHERE p.user_id=? ORDER BY l.created, l.id`, userID)
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
