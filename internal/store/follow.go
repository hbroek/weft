package store

import (
	"time"

	"github.com/hbroek/weft/internal/feed"
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
// feeds (posts, likes) share a homepage link, so a match on either the feed URL or the
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

// HasItems reports whether any item of the feed is cached, i.e. whether it has been read before.
func (s *Store) HasItems(feedURL string) bool {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM items WHERE feed_url=?`, feedURL).Scan(&n)
	return n > 0
}

// SaveFeed stores the items just fetched. A feed that shows everything (no rel="next" page) replaces
// the cache. A paged feed shows only its newest page: cached items newer than the oldest one
// fetched that are no longer in it were deleted and are removed, and older ones are kept.
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
	if len(p.Items) == 0 || p.Next == "" {
		// the feed shows everything the author has, so what it lacks is gone
		if _, err := tx.Exec(`DELETE FROM items WHERE feed_url=?`, feedURL); err != nil {
			return err
		}
	} else {
		oldest, keep := p.Items[0].Published.Unix(), map[string]bool{}
		for _, it := range p.Items {
			keep[it.GUID] = true
			if u := it.Published.Unix(); u < oldest {
				oldest = u
			}
		}
		rows, err := tx.Query(`SELECT guid FROM items WHERE feed_url=? AND published>?`, feedURL, oldest)
		if err != nil {
			return err
		}
		var gone []string
		for rows.Next() {
			var g string
			if err := rows.Scan(&g); err != nil {
				rows.Close()
				return err
			}
			if !keep[g] {
				gone = append(gone, g)
			}
		}
		rows.Close()
		for _, g := range gone {
			if _, err := tx.Exec(`DELETE FROM items WHERE feed_url=? AND guid=?`, feedURL, g); err != nil {
				return err
			}
		}
	}
	for _, it := range p.Items {
		var upd any
		if !it.Updated.IsZero() {
			upd = it.Updated.Unix()
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO items(feed_url,guid,link,title,text,markdown,published,updated,in_reply_to,in_reply_to_feed,root,root_feed,likes,replies_href,replies_count,keywords)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, feedURL, it.GUID, it.Link, it.Title, it.Text, it.Markdown,
			it.Published.Unix(), upd, it.InReplyTo, it.InReplyToFeed, it.Root, it.RootFeed, it.Likes, it.RepliesHref, it.RepliesCount, packKeywords(it.Keywords)); err != nil {
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
	Likes              int // the author's published like tally, if any
	RepliesHref        string
	RepliesCount       int // approved replies the author published; -1 when the feed did not say
	Root               string
	InReplyToFeed      string
	Keywords           []string
}

const timelineSelect = `SELECT i.feed_url, COALESCE(d.title,''), i.guid, i.link, i.title, i.text, i.markdown, i.published, i.updated, i.in_reply_to, i.likes, i.replies_href, i.root, i.in_reply_to_feed, i.replies_count, i.keywords
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
		var kw string
		if err := rows.Scan(&t.FeedURL, &t.FeedTitle, &t.GUID, &t.Link, &t.Title, &t.Text, &t.Markdown, &pub, &upd, &t.InReplyTo, &t.Likes, &t.RepliesHref, &t.Root, &t.InReplyToFeed, &t.RepliesCount, &kw); err != nil {
			return nil, err
		}
		t.Published = time.Unix(pub, 0)
		t.Keywords = unpackKeywords(kw)
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

// ItemByTarget returns a cached item of a followed feed by its permalink, or ErrNotFound.
func (s *Store) ItemByTarget(userID int64, target string) (*TimelineItem, error) {
	rows, err := s.queryTimeline(timelineSelect+`WHERE i.guid=? LIMIT 1`, userID, target)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	return &rows[0], nil
}

// ApprovedSubreplyCount is how many approved replies to the direct reply via are cached under postID.
func (s *Store) ApprovedSubreplyCount(postID int64, via string) int {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM subreplies WHERE post_id=? AND via=?`, postID, via).Scan(&n)
	return n
}
