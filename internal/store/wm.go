package store

import (
	"database/sql"
	"errors"
	"time"
)

// ---- Item lookup (for resolving a reply's thread root) ----

type CachedItem struct {
	FeedURL, GUID, InReplyTo, Root, RootFeed string
}

// ItemByGUID finds a cached feed item by its permalink, from any followed feed.
func (s *Store) ItemByGUID(guid string) (*CachedItem, error) {
	c := &CachedItem{}
	err := s.db.QueryRow(`SELECT feed_url,guid,in_reply_to,root,root_feed FROM items WHERE guid=? LIMIT 1`, guid).
		Scan(&c.FeedURL, &c.GUID, &c.InReplyTo, &c.Root, &c.RootFeed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// ---- Replies received by Webmention ----

const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusRejected = "rejected"
)

type Reply struct {
	ID, PostID                           int64
	Source, FeedURL, Author, Title, Text string
	Markdown, Status                     string
	Published, Received                  time.Time
	Updated                              *time.Time
}

// UpsertReply stores or refreshes a reply. A new reply gets r.Status; an existing one keeps
// its status, except that pending is promoted to approved when r.Status says so.
func (s *Store) UpsertReply(r Reply) error {
	var upd any
	if r.Updated != nil {
		upd = r.Updated.Unix()
	}
	_, err := s.db.Exec(`INSERT INTO replies(post_id,source,feed_url,author,title,text,markdown,published,updated,status,received)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(post_id,source) DO UPDATE SET feed_url=excluded.feed_url, author=excluded.author, title=excluded.title,
		  text=excluded.text, markdown=excluded.markdown, published=excluded.published, updated=excluded.updated,
		  status=CASE WHEN replies.status='pending' THEN excluded.status ELSE replies.status END`,
		r.PostID, r.Source, r.FeedURL, r.Author, r.Title, r.Text, r.Markdown, r.Published.Unix(), upd, r.Status, time.Now().Unix())
	return err
}

// DeleteReply removes a reply whose source has gone away or no longer replies.
func (s *Store) DeleteReply(postID int64, source string) error {
	_, err := s.db.Exec(`DELETE FROM replies WHERE post_id=? AND source=?`, postID, source)
	return err
}

const replyCols = `id,post_id,source,feed_url,author,title,text,markdown,published,updated,status,received`

func scanReplies(rows *sql.Rows) ([]Reply, error) {
	defer rows.Close()
	var out []Reply
	for rows.Next() {
		var r Reply
		var pub, rec int64
		var upd *int64
		if err := rows.Scan(&r.ID, &r.PostID, &r.Source, &r.FeedURL, &r.Author, &r.Title, &r.Text, &r.Markdown, &pub, &upd, &r.Status, &rec); err != nil {
			return nil, err
		}
		r.Published, r.Received = time.Unix(pub, 0), time.Unix(rec, 0)
		if upd != nil {
			t := time.Unix(*upd, 0)
			r.Updated = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RepliesFor lists a post's replies with the given status, oldest first.
func (s *Store) RepliesFor(postID int64, status string) ([]Reply, error) {
	rows, err := s.db.Query(`SELECT `+replyCols+` FROM replies WHERE post_id=? AND status=? ORDER BY published, id`, postID, status)
	if err != nil {
		return nil, err
	}
	return scanReplies(rows)
}

// RepliesForUser lists all replies to a user's posts, newest first (for moderation).
func (s *Store) RepliesForUser(userID int64, status string) ([]Reply, error) {
	q := `SELECT r.id,r.post_id,r.source,r.feed_url,r.author,r.title,r.text,r.markdown,r.published,r.updated,r.status,r.received
		FROM replies r JOIN posts p ON p.id=r.post_id WHERE p.user_id=?`
	args := []any{userID}
	if status != "" {
		q += ` AND r.status=?`
		args = append(args, status)
	}
	rows, err := s.db.Query(q+` ORDER BY r.received DESC, r.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	return scanReplies(rows)
}

// SetReplyStatus changes a reply's moderation status; it must belong to one of the user's posts.
func (s *Store) SetReplyStatus(userID, replyID int64, status string) error {
	if status != StatusApproved && status != StatusRejected && status != StatusPending {
		return ErrInvalid
	}
	res, err := s.db.Exec(`UPDATE replies SET status=? WHERE id=? AND post_id IN (SELECT id FROM posts WHERE user_id=?)`, status, replyID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ApprovedCounts maps post id to its number of approved replies.
func (s *Store) ApprovedCounts(userID int64) (map[int64]int, error) {
	rows, err := s.db.Query(`SELECT r.post_id, COUNT(*) FROM replies r JOIN posts p ON p.id=r.post_id
		WHERE p.user_id=? AND r.status='approved' GROUP BY r.post_id`, userID)
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

// ---- Webmention queue (outbound sends and inbound receipts) ----

const (
	QueueOut = "out"
	QueueIn  = "in"
)

type QueueItem struct {
	ID, UserID, Attempts int64
	Source, Target       string
}

// Enqueue adds a Webmention job unless an identical one is already waiting.
func (s *Store) Enqueue(dir string, userID int64, source, target string) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM wm_queue WHERE dir=? AND user_id=? AND source=? AND target=? AND done=0`,
		dir, userID, source, target).Scan(&n); err != nil || n > 0 {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO wm_queue(dir,user_id,source,target,next_try) VALUES(?,?,?,?,?)`,
		dir, userID, source, target, time.Now().Unix())
	return err
}

// DueQueue returns jobs ready to run.
func (s *Store) DueQueue(dir string, limit int) ([]QueueItem, error) {
	rows, err := s.db.Query(`SELECT id,user_id,attempts,source,target FROM wm_queue WHERE dir=? AND done=0 AND next_try<=? ORDER BY id LIMIT ?`,
		dir, time.Now().Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueItem
	for rows.Next() {
		var q QueueItem
		if err := rows.Scan(&q.ID, &q.UserID, &q.Attempts, &q.Source, &q.Target); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

func (s *Store) FinishQueue(id int64, note string) error {
	_, err := s.db.Exec(`UPDATE wm_queue SET done=1, note=? WHERE id=?`, note, id)
	return err
}

// RetryQueue schedules another attempt after delay, or gives up after maxAttempts.
func (s *Store) RetryQueue(id int64, delay time.Duration, maxAttempts int64, note string) error {
	_, err := s.db.Exec(`UPDATE wm_queue SET attempts=attempts+1, note=?, next_try=?,
		done=CASE WHEN attempts+1>=? THEN 1 ELSE 0 END WHERE id=?`, note, time.Now().Add(delay).Unix(), maxAttempts, id)
	return err
}
