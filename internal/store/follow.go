package store

import (
	"time"

	"github.com/hbroek/text-thread-casting/internal/feed"
)

type Follow struct {
	URL     string
	Title   string // from the last fetch; empty before the first
	Link    string
	Fetched time.Time
	Error   string
}

// Follow records that the user follows a feed. Following twice is a no-op.
func (s *Store) Follow(userID int64, feedURL string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO follows(user_id,feed_url,added) VALUES(?,?,?)`, userID, feedURL, time.Now().Unix())
	return err
}

func (s *Store) Unfollow(userID int64, feedURL string) error {
	_, err := s.db.Exec(`DELETE FROM follows WHERE user_id=? AND feed_url=?`, userID, feedURL)
	return err
}

func (s *Store) IsFollowing(userID int64, feedURL string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM follows WHERE user_id=? AND feed_url=?`, userID, feedURL).Scan(&n)
	return n > 0, err
}

// IsFollowingSite reports whether the user follows the author behind feedURL. A person's
// feeds (posts, reactions) share a homepage link, so a match on either the feed URL or the
// channel link of any followed feed counts.
func (s *Store) IsFollowingSite(userID int64, feedURL, homepage string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM follows f LEFT JOIN feeds d ON d.url=f.feed_url
		WHERE f.user_id=? AND (f.feed_url=? OR (?<>'' AND d.link=?))`, userID, feedURL, homepage, homepage).Scan(&n)
	return n > 0, err
}

func (s *Store) Follows(userID int64) ([]Follow, error) {
	rows, err := s.db.Query(`SELECT f.feed_url, COALESCE(d.title,''), COALESCE(d.link,''), COALESCE(d.fetched,0), COALESCE(d.error,'')
		FROM follows f LEFT JOIN feeds d ON d.url=f.feed_url WHERE f.user_id=? ORDER BY f.added, f.feed_url`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Follow
	for rows.Next() {
		var f Follow
		var fetched int64
		if err := rows.Scan(&f.URL, &f.Title, &f.Link, &fetched, &f.Error); err != nil {
			return nil, err
		}
		if fetched > 0 {
			f.Fetched = time.Unix(fetched, 0)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FollowedFeedURLs returns every feed that at least one user follows.
func (s *Store) FollowedFeedURLs() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT feed_url FROM follows ORDER BY feed_url`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SaveFeed replaces the cached items of a feed with those just fetched.
func (s *Store) SaveFeed(feedURL string, p *feed.Parsed) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO feeds(url,title,link,fetched,error) VALUES(?,?,?,?,'')
		ON CONFLICT(url) DO UPDATE SET title=excluded.title, link=excluded.link, fetched=excluded.fetched, error=''`,
		feedURL, p.Title, p.Link, time.Now().Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM items WHERE feed_url=?`, feedURL); err != nil {
		return err
	}
	for _, it := range p.Items {
		var upd any
		if !it.Updated.IsZero() {
			upd = it.Updated.Unix()
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO items(feed_url,guid,link,title,text,markdown,published,updated,in_reply_to,in_reply_to_feed,root,root_feed,plus,minus,replies_href)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, feedURL, it.GUID, it.Link, it.Title, it.Text, it.Markdown,
			it.Published.Unix(), upd, it.InReplyTo, it.InReplyToFeed, it.Root, it.RootFeed, it.Plus, it.Minus, it.RepliesHref); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SaveFeedError records a failed fetch without discarding cached items.
func (s *Store) SaveFeedError(feedURL, msg string) error {
	_, err := s.db.Exec(`INSERT INTO feeds(url,error,fetched) VALUES(?,?,?)
		ON CONFLICT(url) DO UPDATE SET error=excluded.error, fetched=excluded.fetched`, feedURL, msg, time.Now().Unix())
	return err
}

type TimelineItem struct {
	FeedURL, FeedTitle string
	GUID, Link, Title  string
	Text, Markdown     string
	Published          time.Time
	Updated            *time.Time
	InReplyTo          string
	Plus, Minus        int // the author's published reaction tally, if any
	RepliesHref        string
	Root               string
}

const timelineSelect = `SELECT i.feed_url, COALESCE(d.title,''), i.guid, i.link, i.title, i.text, i.markdown, i.published, i.updated, i.in_reply_to, i.plus, i.minus, i.replies_href, i.root
		FROM items i JOIN follows f ON f.feed_url=i.feed_url AND f.user_id=?
		LEFT JOIN feeds d ON d.url=i.feed_url `

func (s *Store) queryTimeline(q string, args ...any) ([]TimelineItem, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimelineItem
	for rows.Next() {
		var t TimelineItem
		var pub int64
		var upd *int64
		if err := rows.Scan(&t.FeedURL, &t.FeedTitle, &t.GUID, &t.Link, &t.Title, &t.Text, &t.Markdown, &pub, &upd, &t.InReplyTo, &t.Plus, &t.Minus, &t.RepliesHref, &t.Root); err != nil {
			return nil, err
		}
		t.Published = time.Unix(pub, 0)
		if upd != nil {
			u := time.Unix(*upd, 0)
			t.Updated = &u
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Timeline returns items from the feeds the user follows, newest first.
func (s *Store) Timeline(userID int64, limit int) ([]TimelineItem, error) {
	return s.queryTimeline(timelineSelect+`ORDER BY i.published DESC, i.guid LIMIT ?`, userID, limit)
}

// ItemsInThread returns cached items from followed feeds that belong to the thread rooted at root
// (including the root itself).
func (s *Store) ItemsInThread(userID int64, root string) ([]TimelineItem, error) {
	return s.queryTimeline(timelineSelect+`WHERE i.root=? OR i.guid=? ORDER BY i.published, i.guid LIMIT 300`, userID, root, root)
}

// FeedItems returns the cached items of one followed feed, newest first. It is empty if the user
// does not follow that feed.
func (s *Store) FeedItems(userID int64, feedURL string, limit int) ([]TimelineItem, error) {
	return s.queryTimeline(timelineSelect+`WHERE i.feed_url=? ORDER BY i.published DESC, i.guid LIMIT ?`, userID, feedURL, limit)
}
