package store

import (
	"slices"
	"strconv"
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
func (s *Space) Follow(userID int64, feedURL string) error {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO follows(user_id,feed_url,added) VALUES(?,?,?)`, userID, feedURL, time.Now().Unix()); err != nil {
		return err
	}
	return s.in.addLocalFollow(s.name, feedURL)
}

func (s *Space) Unfollow(userID int64, feedURL string) error {
	if _, err := s.db.Exec(`DELETE FROM follows WHERE user_id=? AND feed_url=?`, userID, feedURL); err != nil {
		return err
	}
	return s.in.removeLocalFollow(s.name, feedURL)
}

// ReplaceFollow makes the user follow newURL where they followed oldURL, keeping when they started.
func (s *Space) ReplaceFollow(userID int64, oldURL, newURL string) error {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO follows(user_id,feed_url,added) SELECT user_id,?,added FROM follows WHERE user_id=? AND feed_url=?`,
		newURL, userID, oldURL); err != nil {
		return err
	}
	if err := s.Unfollow(userID, oldURL); err != nil {
		return err
	}
	return s.in.addLocalFollow(s.name, newURL)
}

func (s *Space) IsFollowing(userID int64, feedURL string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM follows WHERE user_id=? AND feed_url=?`, userID, feedURL).Scan(&n)
	return n > 0, err
}

// followURLs lists the feeds the user follows, in the order they were added.
func (s *Space) followURLs(userID int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT feed_url FROM follows WHERE user_id=? ORDER BY added, feed_url`, userID)
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

// IsFollowingSite reports whether the user follows the author behind feedURL. A person's
// feeds (posts, likes) share a homepage link, so a match on either the feed URL or the
// channel link of any followed feed counts.
func (s *Space) IsFollowingSite(userID int64, feedURL, homepage string) (bool, error) {
	urls, err := s.followURLs(userID)
	if err != nil {
		return false, err
	}
	if slices.Contains(urls, feedURL) {
		return true, nil
	}
	if homepage == "" {
		return false, nil
	}
	infos, err := s.in.FeedInfos(urls)
	if err != nil {
		return false, err
	}
	for _, f := range infos {
		if f.Link == homepage {
			return true, nil
		}
	}
	return false, nil
}

func (s *Space) Follows(userID int64) ([]Follow, error) {
	urls, err := s.followURLs(userID)
	if err != nil {
		return nil, err
	}
	infos, err := s.in.FeedInfos(urls)
	if err != nil {
		return nil, err
	}
	out := make([]Follow, 0, len(urls))
	for _, u := range urls {
		i := infos[u]
		out = append(out, Follow{URL: u, Title: i.Title, Link: i.Link, Fetched: i.Fetched, Error: i.Error})
	}
	return out, nil
}

// FollowedFeedURLs returns every feed that at least one user follows.
func (s *Instance) FollowedFeedURLs() ([]string, error) {
	rows, err := s.rdb.Query(`SELECT DISTINCT feed_url FROM local_follows ORDER BY feed_url`)
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

// FollowersOf names the users of this instance who follow the feed.
func (s *Instance) FollowersOf(feedURL string) ([]string, error) {
	rows, err := s.rdb.Query(`SELECT name FROM local_follows WHERE feed_url=? ORDER BY name`, feedURL)
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

// DropFeed forgets a feed nobody follows any more: its cached items, likes and entry.
func (s *Instance) DropFeed(feedURL string) error {
	if s.Followed(feedURL) {
		return nil
	}
	for _, q := range []string{`DELETE FROM items WHERE feed_url=?`, `DELETE FROM feed_likes WHERE feed_url=?`, `DELETE FROM feeds WHERE url=?`} {
		if _, err := s.db.Exec(q, feedURL); err != nil {
			return err
		}
	}
	return nil
}

// HasItems reports whether any item of the feed is cached, i.e. whether it has been read before.
func (s *Instance) HasItems(feedURL string) bool {
	var n int
	s.rdb.QueryRow(`SELECT COUNT(*) FROM items WHERE feed_url=?`, feedURL).Scan(&n)
	return n > 0
}

// SaveFeed stores the items just fetched. A feed that shows everything (no rel="next" page) replaces
// the cache. A paged feed shows only its newest page: cached items newer than the oldest one
// fetched that are no longer in it were deleted and are removed, and older ones are kept.
func (s *Instance) SaveFeed(feedURL string, p *feed.Parsed) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO feeds(url,title,link,fetched,error,image) VALUES(?,?,?,?,'',?)
		ON CONFLICT(url) DO UPDATE SET title=excluded.title, link=excluded.link, fetched=excluded.fetched, error='', image=excluded.image, fails=0, next_try=0`,
		feedURL, p.Title, p.Link, time.Now().Unix(), p.Image); err != nil {
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
		if _, err := tx.Exec(`INSERT OR REPLACE INTO items(feed_url,guid,link,title,text,markdown,published,updated,in_reply_to,in_reply_to_feed,root,root_feed,likes,replies_href,replies_count,keywords,parent_title,parent_desc,parent_image,likes_href,canonical)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, feedURL, it.GUID, it.Link, it.Title, it.Text, it.Markdown,
			it.Published.Unix(), upd, it.InReplyTo, it.InReplyToFeed, it.Root, it.RootFeed, it.Likes, it.RepliesHref, it.RepliesCount, packKeywords(it.Keywords), it.Parent.Title, it.Parent.Description, it.Parent.Image, it.LikesHref, it.Canonical); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SaveFeedError records a failed fetch without discarding cached items.
func (s *Instance) SaveFeedError(feedURL, msg string) error {
	return s.SaveFeedErrorRetry(feedURL, msg, 0)
}

// MaxBackoff is the longest a failing feed is left alone.
const MaxBackoff = 24 * time.Hour

// Backoff is how long to wait after the given number of failures in a row: 15 minutes, doubling each time.
func Backoff(fails int) time.Duration {
	d := 15 * time.Minute
	for i := 1; i < fails && d < MaxBackoff; i++ {
		d *= 2
	}
	if d > MaxBackoff {
		d = MaxBackoff
	}
	return d
}

// SaveFeedErrorRetry records a failed fetch and when the poller may try again: after the longer of
// the server's own request (wait, e.g. from Retry-After) and the backoff for the failures so far.
func (s *Instance) SaveFeedErrorRetry(feedURL, msg string, wait time.Duration) error {
	now := time.Now()
	var fails int
	s.rdb.QueryRow(`SELECT fails FROM feeds WHERE url=?`, feedURL).Scan(&fails)
	fails++
	d := Backoff(fails)
	if wait > d {
		d = wait
	}
	if d > MaxBackoff {
		d = MaxBackoff
	}
	_, err := s.db.Exec(`INSERT INTO feeds(url,error,fetched,fails,next_try) VALUES(?,?,?,?,?)
		ON CONFLICT(url) DO UPDATE SET error=excluded.error, fetched=excluded.fetched, fails=excluded.fails, next_try=excluded.next_try`,
		feedURL, msg, now.Unix(), fails, now.Add(d).Unix())
	return err
}

// Validators returns what the feed's server sent with the cached copy, for a conditional request. They
// are empty when nothing is cached, since a "not modified" answer is only useful beside the copy.
func (s *Instance) Validators(feedURL string) (etag, lastModified string) {
	if !s.HasItems(feedURL) {
		return "", ""
	}
	s.rdb.QueryRow(`SELECT etag, last_modified FROM feeds WHERE url=?`, feedURL).Scan(&etag, &lastModified)
	return
}

// SaveValidators remembers the validators the server sent with the copy just saved.
func (s *Instance) SaveValidators(feedURL, etag, lastModified string) error {
	_, err := s.db.Exec(`UPDATE feeds SET etag=?, last_modified=? WHERE url=?`, etag, lastModified, feedURL)
	return err
}

// FeedUnchanged records that the server said the cached copy is still current.
func (s *Instance) FeedUnchanged(feedURL string) error {
	_, err := s.db.Exec(`UPDATE feeds SET fetched=?, error='', fails=0, next_try=0 WHERE url=?`, time.Now().Unix(), feedURL)
	return err
}

// DueFeedURLs is FollowedFeedURLs without the feeds that are waiting out a backoff.
func (s *Instance) DueFeedURLs(now time.Time) ([]string, error) {
	urls, err := s.FollowedFeedURLs()
	if err != nil {
		return nil, err
	}
	var due []string
	for _, u := range urls {
		var next int64
		s.rdb.QueryRow(`SELECT next_try FROM feeds WHERE url=?`, u).Scan(&next)
		if next <= now.Unix() {
			due = append(due, u)
		}
	}
	return due, nil
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
	LikesHref          string // where the author lists everyone who liked the post
	RepliesCount       int    // approved replies the author published; -1 when the feed did not say
	Root               string
	InReplyToFeed      string
	Keywords           []string
	FeedImage          string // the feed's channel image: the author's avatar
	Parent             feed.ParentInfo
	Canonical          string // where a repost first appeared
}

const timelineSelect = `SELECT i.feed_url, COALESCE(d.title,''), i.guid, i.link, i.title, i.text, i.markdown, i.published, i.updated, i.in_reply_to, i.likes, i.replies_href, i.root, i.in_reply_to_feed, i.replies_count, i.keywords, COALESCE(d.image,''), i.parent_title, i.parent_desc, i.parent_image, i.likes_href, i.canonical
		FROM items i LEFT JOIN feeds d ON d.url=i.feed_url `

func (in *Instance) queryTimeline(q string, args ...any) ([]TimelineItem, error) {
	rows, err := in.rdb.Query(q, args...)
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
		if err := rows.Scan(&t.FeedURL, &t.FeedTitle, &t.GUID, &t.Link, &t.Title, &t.Text, &t.Markdown, &pub, &upd, &t.InReplyTo, &t.Likes, &t.RepliesHref, &t.Root, &t.InReplyToFeed, &t.RepliesCount, &kw, &t.FeedImage, &t.Parent.Title, &t.Parent.Description, &t.Parent.Image, &t.LikesHref, &t.Canonical); err != nil {
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
func (s *Space) Timeline(userID int64, limit int) ([]TimelineItem, error) {
	return s.TimelineBefore(userID, 0, limit)
}

// TimelineBefore is Timeline for the items published before the Unix time before (0: no bound).
func (s *Space) TimelineBefore(userID, before int64, limit int) ([]TimelineItem, error) {
	urls, err := s.followURLs(userID)
	if err != nil {
		return nil, err
	}
	return s.in.itemsOf(urls, publishedBefore(before), `ORDER BY i.published DESC, i.guid LIMIT ?`, limit)
}

// publishedBefore is the condition for items older than the Unix time before, or none if it is 0.
func publishedBefore(before int64) string {
	if before <= 0 {
		return ``
	}
	return `i.published<` + strconv.FormatInt(before, 10)
}

// ItemsInThread returns cached items from followed feeds that belong to the thread rooted at root
// (including the root itself).
func (s *Space) ItemsInThread(userID int64, root string) ([]TimelineItem, error) {
	urls, err := s.followURLs(userID)
	if err != nil {
		return nil, err
	}
	return s.in.itemsOf(urls, `(i.root=? OR i.guid=?)`, `ORDER BY i.published, i.guid LIMIT 300`, root, root)
}

// FeedItems returns the cached items of one followed feed, newest first. It is empty if the user
// does not follow that feed.
func (s *Space) FeedItems(userID int64, feedURL string, limit int) ([]TimelineItem, error) {
	return s.FeedItemsBefore(userID, feedURL, 0, limit)
}

// FeedItemsBefore is FeedItems for the items published before the Unix time before (0: no bound).
func (s *Space) FeedItemsBefore(userID int64, feedURL string, before int64, limit int) ([]TimelineItem, error) {
	if ok, err := s.IsFollowing(userID, feedURL); err != nil || !ok {
		return nil, err
	}
	return s.in.itemsOf([]string{feedURL}, publishedBefore(before), `ORDER BY i.published DESC, i.guid LIMIT ?`, limit)
}

// ItemByTarget returns a cached item of a followed feed by its permalink, or ErrNotFound.
func (s *Space) ItemByTarget(userID int64, target string) (*TimelineItem, error) {
	urls, err := s.followURLs(userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.in.itemsOf(urls, `i.guid=?`, `LIMIT 1`, target)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	return &rows[0], nil
}

// ApprovedSubreplyCount is how many approved replies to the direct reply via are cached under postID.
func (s *Space) ApprovedSubreplyCount(postID int64, via string) int {
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM subreplies WHERE post_id=? AND via=?`, postID, via).Scan(&n)
	return n
}
