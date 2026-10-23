package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ---- Item lookup (for resolving a reply's thread root) ----

type CachedItem struct {
	FeedURL, GUID, InReplyTo, Root, RootFeed string
}

// ItemByGUID finds a cached item by its permalink, in the feeds the user follows.
func (s *Space) ItemByGUID(userID int64, guid string) (*CachedItem, error) {
	urls, err := s.followURLs(userID)
	if err != nil {
		return nil, err
	}
	return s.in.itemByGUID(urls, guid)
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
	Root, RepliesHref                    string
	Published, Received                  time.Time
	Updated                              *time.Time
}

// UpsertReply stores or refreshes a reply. A new reply gets r.Status; an existing one keeps
// its status, except that pending is promoted to approved when r.Status says so. changed
// reports whether anything a reader would see differs from what was stored before.
func (s *Space) UpsertReply(r Reply) (changed bool, err error) {
	prior, err := s.ReplyBySource(r.PostID, r.Source)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return false, err
	}
	var upd any
	if r.Updated != nil {
		upd = r.Updated.Unix()
	}
	_, err = s.db.Exec(`INSERT INTO replies(post_id,source,feed_url,author,title,text,markdown,published,updated,status,received,root,replies_href)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(post_id,source) DO UPDATE SET feed_url=excluded.feed_url, author=excluded.author, title=excluded.title,
		  text=excluded.text, markdown=excluded.markdown, published=excluded.published, updated=excluded.updated,
		  root=excluded.root, replies_href=excluded.replies_href,
		  status=CASE WHEN replies.status='pending' THEN excluded.status ELSE replies.status END`,
		r.PostID, r.Source, r.FeedURL, r.Author, r.Title, r.Text, r.Markdown, r.Published.Unix(), upd, r.Status, time.Now().Unix(),
		r.Root, r.RepliesHref)
	if err != nil {
		return false, err
	}
	if prior == nil {
		return true, nil
	}
	same := prior.FeedURL == r.FeedURL && prior.Author == r.Author && prior.Title == r.Title && prior.Text == r.Text &&
		prior.Markdown == r.Markdown && prior.Published.Unix() == r.Published.Unix() && prior.Root == r.Root &&
		prior.RepliesHref == r.RepliesHref && sameTime(prior.Updated, r.Updated) &&
		!(prior.Status == StatusPending && r.Status == StatusApproved)
	return !same, nil
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Unix() == b.Unix()
}

// ReplyBySource finds the reply to a post that came from source.
func (s *Space) ReplyBySource(postID int64, source string) (*Reply, error) {
	rows, err := s.db.Query(`SELECT `+replyCols+` FROM replies WHERE post_id=? AND source=?`, postID, source)
	if err != nil {
		return nil, err
	}
	rs, err := scanReplies(rows)
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, ErrNotFound
	}
	return &rs[0], nil
}

// DeleteReply removes a reply whose source has gone away or no longer replies, together with
// the subtree cached under it. It returns the removed reply, or nil if there was none.
func (s *Space) DeleteReply(postID int64, source string) (*Reply, error) {
	prior, err := s.ReplyBySource(postID, source)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`DELETE FROM replies WHERE post_id=? AND source=?`, postID, source); err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`DELETE FROM subreplies WHERE post_id=? AND via=?`, postID, source)
	return prior, err
}

const replyCols = `id,post_id,source,feed_url,author,title,text,markdown,published,updated,status,received,root,replies_href`

func scanReplies(rows *sql.Rows) ([]Reply, error) {
	defer rows.Close()
	var out []Reply
	for rows.Next() {
		var r Reply
		var pub, rec int64
		var upd *int64
		if err := rows.Scan(&r.ID, &r.PostID, &r.Source, &r.FeedURL, &r.Author, &r.Title, &r.Text, &r.Markdown, &pub, &upd, &r.Status, &rec, &r.Root, &r.RepliesHref); err != nil {
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
func (s *Space) RepliesFor(postID int64, status string) ([]Reply, error) {
	rows, err := s.db.Query(`SELECT `+replyCols+` FROM replies WHERE post_id=? AND status=? ORDER BY published, id`, postID, status)
	if err != nil {
		return nil, err
	}
	return scanReplies(rows)
}

// RepliesForUser lists all replies to a user's posts, newest first (for moderation).
func (s *Space) RepliesForUser(userID int64, status string) ([]Reply, error) {
	q := `SELECT r.id,r.post_id,r.source,r.feed_url,r.author,r.title,r.text,r.markdown,r.published,r.updated,r.status,r.received,r.root,r.replies_href
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

// SetReplyStatus changes a reply's moderation status and returns the id of the post it belongs
// to. The reply must belong to one of the user's posts.
func (s *Space) SetReplyStatus(userID, replyID int64, status string) (postID int64, err error) {
	if status != StatusApproved && status != StatusRejected && status != StatusPending {
		return 0, ErrInvalid
	}
	err = s.db.QueryRow(`SELECT r.post_id FROM replies r JOIN posts p ON p.id=r.post_id WHERE r.id=? AND p.user_id=?`, replyID, userID).Scan(&postID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	_, err = s.db.Exec(`UPDATE replies SET status=? WHERE id=?`, status, replyID)
	return postID, err
}

// ApprovedCounts maps post id to its number of approved replies.
func (s *Space) ApprovedCounts(userID int64) (map[int64]int, error) {
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
func (s *Space) Enqueue(dir string, userID int64, source, target string) error {
	return s.EnqueueCapped(dir, userID, source, target, 0, 0)
}

// ErrQueueFull is returned by EnqueueCapped when the queue holds too much already.
var ErrQueueFull = errors.New("queue full")

// EnqueueCapped is Enqueue for jobs that strangers can ask for: a job not already waiting is refused with
// ErrQueueFull when maxTotal jobs (of the user's, for this direction) are waiting, or maxPerHost from the same
// host as source. A cap of 0 means none.
func (s *Space) EnqueueCapped(dir string, userID int64, source, target string, maxTotal, maxPerHost int) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM wm_queue WHERE dir=? AND user_id=? AND source=? AND target=? AND done=0`,
		dir, userID, source, target).Scan(&n); err != nil || n > 0 {
		return err
	}
	if maxTotal > 0 || maxPerHost > 0 {
		var total, fromHost int
		prefix := sourceOrigin(source)
		if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN substr(source,1,?)=? THEN 1 ELSE 0 END),0)
			FROM wm_queue WHERE dir=? AND user_id=? AND done=0`, len(prefix), prefix, dir, userID).Scan(&total, &fromHost); err != nil {
			return err
		}
		if (maxTotal > 0 && total >= maxTotal) || (maxPerHost > 0 && fromHost >= maxPerHost) {
			return ErrQueueFull
		}
	}
	if _, err := s.db.Exec(`INSERT INTO wm_queue(dir,user_id,source,target,next_try) VALUES(?,?,?,?,?)`,
		dir, userID, source, target, time.Now().Unix()); err != nil {
		return err
	}
	return s.syncWork()
}

// syncWork tells the directory when this user's next job is due, or that there is none.
func (s *Space) syncWork() error {
	var due sql.NullInt64
	if err := s.db.QueryRow(`SELECT MIN(next_try) FROM wm_queue WHERE done=0 AND user_id IN (0,?)`, s.uid).Scan(&due); err != nil {
		return err
	}
	return s.in.setWork(s.name, due.Int64)
}

// DueQueue returns this user's jobs that are ready to run. Jobs queued before they were stamped with
// their owner have user_id 0 and run for whichever user's turn comes first.
func (s *Space) DueQueue(dir string, limit int) ([]QueueItem, error) {
	rows, err := s.db.Query(`SELECT id,user_id,attempts,source,target FROM wm_queue WHERE dir=? AND user_id IN (0,?) AND done=0 AND next_try<=? ORDER BY id LIMIT ?`,
		dir, s.uid, time.Now().Unix(), limit)
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

func (s *Space) FinishQueue(id int64, note string) error {
	if _, err := s.db.Exec(`UPDATE wm_queue SET done=1, note=? WHERE id=?`, note, id); err != nil {
		return err
	}
	return s.syncWork()
}

// RetryQueue schedules another attempt after delay, or gives up after maxAttempts.
func (s *Space) RetryQueue(id int64, delay time.Duration, maxAttempts int64, note string) error {
	if _, err := s.db.Exec(`UPDATE wm_queue SET attempts=attempts+1, note=?, next_try=?,
		done=CASE WHEN attempts+1>=? THEN 1 ELSE 0 END WHERE id=?`, note, time.Now().Add(delay).Unix(), maxAttempts, id); err != nil {
		return err
	}
	return s.syncWork()
}

// ---- Cached second-level replies (the replies feed's depth) ----

// Subreply is an approved reply to one of a post's replies, as reported by that reply's own
// replies feed. via is the direct reply it was learned through.
type Subreply struct {
	PostID                               int64
	Via, Parent, Source, FeedURL, Author string
	Title, Text                          string
	Published                            time.Time
}

// ReplaceSubreplies swaps the cached subtree under a direct reply. changed reports whether
// the set differs from before.
func (s *Space) ReplaceSubreplies(postID int64, via string, rows []Subreply) (changed bool, err error) {
	old, err := s.subreplies(`WHERE post_id=? AND via=?`, postID, via)
	if err != nil {
		return false, err
	}
	changed = len(old) != len(rows)
	if !changed {
		byKey := map[string]Subreply{}
		for _, o := range old {
			byKey[o.Source] = o
		}
		for _, n := range rows {
			o, ok := byKey[n.Source]
			if !ok || o.Text != n.Text || o.Title != n.Title || o.Author != n.Author || o.Parent != n.Parent {
				changed = true
				break
			}
		}
	}
	if !changed {
		return false, nil
	}
	if err := s.db.ensure(); err != nil {
		return false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM subreplies WHERE post_id=? AND via=?`, postID, via); err != nil {
		return false, err
	}
	for _, r := range rows {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO subreplies(post_id,via,parent,source,feed_url,author,title,text,published) VALUES(?,?,?,?,?,?,?,?,?)`,
			postID, via, r.Parent, r.Source, r.FeedURL, r.Author, r.Title, r.Text, r.Published.Unix()); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// SubrepliesFor returns all cached second-level replies for a post, oldest first.
func (s *Space) SubrepliesFor(postID int64) ([]Subreply, error) {
	return s.subreplies(`WHERE post_id=?`, postID)
}

func (s *Space) subreplies(where string, args ...any) ([]Subreply, error) {
	rows, err := s.db.Query(`SELECT post_id,via,parent,source,feed_url,author,title,text,published FROM subreplies `+where+` ORDER BY published, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subreply
	for rows.Next() {
		var r Subreply
		var pub int64
		if err := rows.Scan(&r.PostID, &r.Via, &r.Parent, &r.Source, &r.FeedURL, &r.Author, &r.Title, &r.Text, &pub); err != nil {
			return nil, err
		}
		r.Published = time.Unix(pub, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// sourceOrigin is the start of every address on source's host: "https://example.org/".
func sourceOrigin(source string) string {
	i := strings.Index(source, "://")
	if i < 0 {
		return source
	}
	if j := strings.IndexAny(source[i+3:], "/?#"); j >= 0 {
		return source[:i+3+j] + "/"
	}
	return source + "/"
}
