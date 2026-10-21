package store

import (
	"time"

	"github.com/hbroek/weft/internal/feed"
)

// SaveFeedLikes replaces the cached likes of the person behind feedURL (their main feed, the one that
// is followed) with the like items of their likes feed, which is small and shows everything.
func (s *Store) SaveFeedLikes(feedURL string, items []feed.ParsedItem) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM feed_likes WHERE feed_url=?`, feedURL); err != nil {
		return err
	}
	for _, it := range items {
		if it.LikeOf == "" || it.GUID == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO feed_likes(feed_url,guid,target,published) VALUES(?,?,?,?)`,
			feedURL, it.GUID, it.LikeOf, it.Published.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FeedLike is a like published by someone the user follows.
type FeedLike struct {
	FeedURL, FeedTitle, GUID, Target string
	Published                        time.Time
}

// FeedLikes returns the likes of the feeds the user follows, newest first. A feedURL limits it to one feed.
func (s *Store) FeedLikes(userID int64, feedURL string, limit int) ([]FeedLike, error) {
	q := `SELECT l.feed_url, COALESCE(d.title,''), l.guid, l.target, l.published
		FROM feed_likes l JOIN follows f ON f.feed_url=l.feed_url AND f.user_id=?
		LEFT JOIN feeds d ON d.url=l.feed_url `
	args := []any{userID}
	if feedURL != "" {
		q += `WHERE l.feed_url=? `
		args = append(args, feedURL)
	}
	rows, err := s.db.Query(q+`ORDER BY l.published DESC, l.guid LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FeedLike
	for rows.Next() {
		var l FeedLike
		var pub int64
		if err := rows.Scan(&l.FeedURL, &l.FeedTitle, &l.GUID, &l.Target, &pub); err != nil {
			return nil, err
		}
		l.Published = time.Unix(pub, 0)
		out = append(out, l)
	}
	return out, rows.Err()
}

// FollowedLikers returns, for each post the people the user follows have liked, their names: the
// feed's title, or its address when it has none. The oldest like comes first.
func (s *Store) FollowedLikers(userID int64) (map[string][]string, error) {
	rows, err := s.db.Query(`SELECT l.target, COALESCE(NULLIF(d.title,''), l.feed_url)
		FROM feed_likes l JOIN follows f ON f.feed_url=l.feed_url AND f.user_id=?
		LEFT JOIN feeds d ON d.url=l.feed_url ORDER BY l.published, l.guid`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var target, name string
		if err := rows.Scan(&target, &name); err != nil {
			return nil, err
		}
		out[target] = append(out[target], name)
	}
	return out, rows.Err()
}
