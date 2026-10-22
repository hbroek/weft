package store

import (
	"slices"
	"time"

	"github.com/hbroek/weft/internal/feed"
)

// SaveFeedLikes replaces the cached likes of the person behind feedURL (their main feed, the one that
// is followed) with the like items of their likes feed, which is small and shows everything.
func (s *Instance) SaveFeedLikes(feedURL string, items []feed.ParsedItem) error {
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
		if _, err := tx.Exec(`INSERT OR REPLACE INTO feed_likes(feed_url,guid,target,published,parent_title,parent_desc,parent_image) VALUES(?,?,?,?,?,?,?)`,
			feedURL, it.GUID, it.LikeOf, it.Published.Unix(), it.Parent.Title, it.Parent.Description, it.Parent.Image); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FeedLike is a like published by someone the user follows.
type FeedLike struct {
	FeedURL, FeedTitle, GUID, Target string
	Published                        time.Time
	Parent                           feed.ParentInfo // what the liked page looked like, if the like said
}

// FeedLikes returns the likes of the feeds the user follows, newest first. A feedURL limits it to one feed.
func (s *Space) FeedLikes(userID int64, feedURL string, limit int) ([]FeedLike, error) {
	urls, err := s.followURLs(userID)
	if err != nil {
		return nil, err
	}
	if feedURL != "" {
		if !slices.Contains(urls, feedURL) {
			return nil, nil
		}
		urls = []string{feedURL}
	}
	return s.in.likesOf(urls, `ORDER BY l.published DESC, l.guid LIMIT ?`, limit)
}

// likesOf reads the cached likes published by the given feeds.
func (in *Instance) likesOf(urls []string, tail string, args ...any) ([]FeedLike, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	rows, err := in.rdb.Query(`SELECT l.feed_url, COALESCE(d.title,''), l.guid, l.target, l.published, l.parent_title, l.parent_desc, l.parent_image
		FROM feed_likes l LEFT JOIN feeds d ON d.url=l.feed_url WHERE l.feed_url IN (`+marks(len(urls))+`) `+tail,
		append(anys(urls), args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FeedLike
	for rows.Next() {
		var l FeedLike
		var pub int64
		if err := rows.Scan(&l.FeedURL, &l.FeedTitle, &l.GUID, &l.Target, &pub, &l.Parent.Title, &l.Parent.Description, &l.Parent.Image); err != nil {
			return nil, err
		}
		l.Published = time.Unix(pub, 0)
		out = append(out, l)
	}
	return out, rows.Err()
}

// FollowedLikers returns, for each post the people the user follows have liked, their names: the
// feed's title, or its address when it has none. The oldest like comes first.
func (s *Space) FollowedLikers(userID int64) (map[string][]string, error) {
	urls, err := s.followURLs(userID)
	if err != nil {
		return nil, err
	}
	likes, err := s.in.likesOf(urls, `ORDER BY l.published, l.guid LIMIT -1`)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, l := range likes {
		name := l.FeedTitle
		if name == "" {
			name = l.FeedURL
		}
		out[l.Target] = append(out[l.Target], name)
	}
	return out, nil
}
