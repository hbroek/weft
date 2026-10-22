package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// directory adds the two tables that tell the instance, without opening any user's data, who follows
// a feed and which users have Webmention jobs waiting. They are a function of the user data and can be
// rebuilt from it.
func directory(tx *sql.Tx) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS local_follows (
		  feed_url TEXT NOT NULL,
		  name TEXT NOT NULL,
		  PRIMARY KEY (feed_url, name)
		)`,
		`CREATE INDEX IF NOT EXISTS local_follows_name ON local_follows(name)`,
		`CREATE TABLE IF NOT EXISTS work (
		  name TEXT PRIMARY KEY,
		  next_due INTEGER NOT NULL
		)`,
		`INSERT OR IGNORE INTO local_follows(feed_url,name) SELECT f.feed_url, u.name FROM follows f JOIN users u ON u.id=f.user_id`,
		`INSERT OR REPLACE INTO work(name,next_due) SELECT u.name, MIN(q.next_try) FROM wm_queue q JOIN users u ON u.id=q.user_id
		   WHERE q.done=0 GROUP BY u.name`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// Space opens the named user's data. The returned function releases it and must be called when the
// caller is done; a Space must not be used after that. ErrNotFound if there is no such user.
func (in *Instance) Space(name string) (*Space, func(), error) {
	var id int64
	err := in.db.QueryRow(`SELECT id FROM users WHERE name=?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	return &Space{in: in, db: in.db, name: name, uid: id}, func() {}, nil
}

// UID is the user's row id, the userID that the Space methods take. A space holds one user, so it
// is the same for the life of the space.
func (s *Space) UID() int64 { return s.uid }

// Name is the user's name, which is also the key of their directory rows.
func (s *Space) Name() string { return s.name }

// User returns the account this space belongs to.
func (s *Space) User() (*User, error) {
	u := &User{}
	err := s.db.QueryRow(`SELECT id,name,display,bio,avatar FROM users WHERE name=?`, s.name).Scan(&u.ID, &u.Name, &u.Display, &u.Bio, &u.Avatar)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// ---- Directory ----

// Followed reports whether any user of this instance follows the feed.
func (in *Instance) Followed(feedURL string) bool {
	var n int
	in.db.QueryRow(`SELECT COUNT(*) FROM local_follows WHERE feed_url=?`, feedURL).Scan(&n)
	return n > 0
}

func (in *Instance) addLocalFollow(name, feedURL string) error {
	_, err := in.db.Exec(`INSERT OR IGNORE INTO local_follows(feed_url,name) VALUES(?,?)`, feedURL, name)
	return err
}

func (in *Instance) removeLocalFollow(name, feedURL string) error {
	_, err := in.db.Exec(`DELETE FROM local_follows WHERE feed_url=? AND name=?`, feedURL, name)
	return err
}

// UsersWithWork returns the users that have a Webmention job due now, so the worker opens only those.
func (in *Instance) UsersWithWork() ([]string, error) {
	rows, err := in.db.Query(`SELECT name FROM work WHERE next_due<=? ORDER BY next_due, name`, time.Now().Unix())
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

// setWork records when the user's next Webmention job is due, or that none is waiting (due 0).
func (in *Instance) setWork(name string, due int64) error {
	if due == 0 {
		_, err := in.db.Exec(`DELETE FROM work WHERE name=?`, name)
		return err
	}
	_, err := in.db.Exec(`INSERT INTO work(name,next_due) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET next_due=excluded.next_due`, name, due)
	return err
}

// ---- Shared caches, read for a set of feeds ----
//
// A user's follows live in their own space and the cached feeds in the instance, so a read of "the
// items of the feeds I follow" is two steps: the space names the feeds, then these methods read them.

// FeedInfo is what the instance has cached about a feed itself.
type FeedInfo struct {
	Title, Link, Error, Image string
	Fetched                   time.Time
}

// FeedInfos returns the cached description of each feed that has been fetched at least once.
func (in *Instance) FeedInfos(urls []string) (map[string]FeedInfo, error) {
	out := map[string]FeedInfo{}
	if len(urls) == 0 {
		return out, nil
	}
	rows, err := in.db.Query(`SELECT url,title,link,fetched,error,image FROM feeds WHERE url IN (`+marks(len(urls))+`)`, anys(urls)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		var f FeedInfo
		var fetched int64
		if err := rows.Scan(&u, &f.Title, &f.Link, &fetched, &f.Error, &f.Image); err != nil {
			return nil, err
		}
		if fetched > 0 {
			f.Fetched = time.Unix(fetched, 0)
		}
		out[u] = f
	}
	return out, rows.Err()
}

// itemsOf reads cached items of the given feeds. where, if given, narrows them (args follow the urls);
// tail is the ORDER BY and LIMIT.
func (in *Instance) itemsOf(urls []string, where, tail string, args ...any) ([]TimelineItem, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	q := timelineSelect + `WHERE i.feed_url IN (` + marks(len(urls)) + `) `
	if where != "" {
		q += `AND ` + where + ` `
	}
	return in.queryTimeline(q+tail, append(anys(urls), args...)...)
}

// ItemByGUID finds a cached item by its permalink in the given feeds.
func (in *Instance) itemByGUID(urls []string, guid string) (*CachedItem, error) {
	if len(urls) == 0 {
		return nil, ErrNotFound
	}
	c := &CachedItem{}
	err := in.db.QueryRow(`SELECT feed_url,guid,in_reply_to,root,root_feed FROM items WHERE guid=? AND feed_url IN (`+marks(len(urls))+`) LIMIT 1`,
		append([]any{guid}, anys(urls)...)...).Scan(&c.FeedURL, &c.GUID, &c.InReplyTo, &c.Root, &c.RootFeed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func anys(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}
