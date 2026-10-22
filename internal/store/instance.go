package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

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

// Instance is one Weft server's storage: the user directory and the caches of the public network that
// all its users share (feeds, their items, the likes the people they follow have published), in
// instance.db. Each user's own data is a file of its own, reached through Space.
type Instance struct {
	db   *sql.DB // the writer: one connection, so writes queue up here instead of failing with "database is locked"
	rdb  *sql.DB // read-only connections, several at once; in write-ahead mode readers do not wait for the writer
	dir  string
	pool *pool
	dirs dirCache

	closing  sync.Once
	closeErr error
}

// ErrNameTaken is returned when a user name is already in use.
var ErrNameTaken = errors.New("name taken")

// readers is how many reads of instance.db may run at once.
const readers = 8

const pragmas = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=journal_size_limit(1048576)&_pragma=synchronous(NORMAL)"

// Open opens the data folder, creating it and instance.db if needed. Users' files live under users/,
// in a folder named by the first two hex digits of the hash of the user's name, so no folder grows
// large: users/ab/alice.db.
func Open(dir string) (*Instance, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "instance.db")+"?"+pragmas)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db, instanceMigrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("instance.db: %w", err)
	}
	rdb, err := sql.Open("sqlite", filepath.Join(dir, "instance.db")+"?"+pragmas+"&_pragma=query_only(1)")
	if err != nil {
		db.Close()
		return nil, err
	}
	rdb.SetMaxOpenConns(readers)
	in := &Instance{db: db, rdb: rdb, dir: dir}
	in.pool = newPool(in.openSpace, poolCap(), 5*time.Minute)
	return in, nil
}

// OpenFiles is how many files the process may have open, or 0 if that is not known.
func (in *Instance) OpenFiles() int { return openFileLimit() }

// PoolCap is how many users' databases may be open at once.
func (in *Instance) PoolCap() int { return in.pool.cap }

// Close closes every user's database and then the shared one, each leaving a single file. Close it only
// after everything that uses the instance has stopped.
func (in *Instance) Close() error {
	in.closing.Do(func() {
		in.closeErr = errors.Join(in.pool.close(), in.rdb.Close(), closeDB(in.db)) // the writer closes last, and removes the -wal and -shm
	})
	return in.closeErr
}

// spacePath is where a user's file lives. The name must already be valid: it becomes a file name.
func (in *Instance) spacePath(name string) string { return filepath.Join(in.dir, spaceRel(name)) }

// spaceRel is the path of a user's file below a data folder or a backup: users/ab/alice.db.
func spaceRel(name string) string {
	h := sha256.Sum256([]byte(name))
	return filepath.Join("users", hex.EncodeToString(h[:1]), name+".db")
}

// openSpaceDB opens a user's file, bringing its schema up to date. A new file gets 1 KiB pages: a user
// owns few tables and little data, and an empty file then takes 24 KB instead of 77 KB.
func openSpaceDB(path string) (*sql.DB, error) {
	if fi, err := os.Stat(path); err == nil && fi.Size() == 0 {
		if err := setPageSize(path, 1024); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path+"?"+pragmas+"&_pragma=cache_size(-1000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db, spaceMigrations); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return db, nil
}

// setPageSize gives an empty database file its page size. It must happen before anything is written,
// and switching on the write-ahead log is the first write, so it cannot be left to the open pragmas.
func setPageSize(path string, size int) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA page_size = %d`, size)); err != nil {
		return err
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil { // writes the header, fixing the size
		return err
	}
	return nil
}

// openSpace is the pool's way of opening a file that must already exist.
func (in *Instance) openSpace(name string) (*sql.DB, int64, error) {
	path := in.spacePath(name)
	if _, err := os.Stat(path); err != nil {
		return nil, 0, ErrNotFound
	}
	db, err := openSpaceDB(path)
	if err != nil {
		return nil, 0, err
	}
	var id int64
	if err := db.QueryRow(`SELECT id FROM users WHERE name=?`, name).Scan(&id); err != nil {
		db.Close()
		return nil, 0, fmt.Errorf("%s has no account row for %q: %w", filepath.Base(path), name, err)
	}
	return db, id, nil
}

// Space opens the named user's data. The returned function releases it and must be called when the
// caller is done; a Space must not be used after that. ErrNotFound if there is no such user.
func (in *Instance) Space(name string) (*Space, func(), error) {
	if !nameRE.MatchString(name) {
		return nil, nil, ErrNotFound // a host name from a request: never let it reach the file system
	}
	e, err := in.pool.get(name)
	if err != nil {
		return nil, nil, err
	}
	var once sync.Once
	return &Space{in: in, db: lazyDB{e.db}, name: name, uid: e.uid}, func() { once.Do(func() { in.pool.release(e) }) }, nil
}

func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// AddUser creates a user and returns a fresh API token (shown once). The name is entered in the
// directory first, which makes it unique, and then the user's file is created, exclusively.
func (in *Instance) AddUser(name, display, bio string) (token string, err error) {
	if !nameRE.MatchString(name) {
		return "", ErrInvalid
	}
	if token, err = newToken(); err != nil {
		return "", err
	}
	if display == "" {
		display = name
	}
	now := time.Now().Unix()
	if _, err := in.db.Exec(`INSERT INTO users(name,created) VALUES(?,?)`, name, now); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", ErrNameTaken
		}
		return "", err
	}
	path := in.spacePath(name)
	fail := func(err error) (string, error) {
		in.db.Exec(`DELETE FROM users WHERE name=?`, name)
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fail(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fail(err) // a file is already there: never overwrite someone's data
	}
	f.Close()
	db, err := openSpaceDB(path)
	if err == nil {
		_, err = db.Exec(`INSERT INTO users(name,display,bio,token_hash,created) VALUES(?,?,?,?,?)`, name, display, bio, hashToken(token), now)
		closeDB(db)
	}
	if err != nil {
		os.Remove(path)
		return fail(err)
	}
	return token, nil
}

// UserNames lists the names of the users who are served here, oldest first.
func (in *Instance) UserNames() ([]string, error) {
	rows, err := in.rdb.Query(`SELECT name FROM users WHERE status!='moved' ORDER BY created, name`)
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

// UserByName returns the named user's account, or ErrNotFound.
func (in *Instance) UserByName(name string) (*User, error) {
	sp, release, err := in.Space(name)
	if err != nil {
		return nil, err
	}
	defer release()
	return sp.User()
}

// Reindex rebuilds local_follows and work from the users' files, which are the truth. It is how the
// directory is brought back after a restore or a split.
func (in *Instance) Reindex() error {
	names, err := in.UserNames()
	if err != nil {
		return err
	}
	type follow struct{ url, name string }
	var follows []follow
	due := map[string]int64{}
	for _, name := range names {
		sp, release, err := in.Space(name)
		if errors.Is(err, ErrNotFound) {
			continue // a user without a file has nothing to index; fsck reports it
		}
		if err != nil {
			return fmt.Errorf("reindex %s: %w", name, err)
		}
		urls, err := sp.followURLs(sp.UID())
		if err == nil {
			for _, u := range urls {
				follows = append(follows, follow{u, name})
			}
			var d sql.NullInt64
			if err = sp.db.QueryRow(`SELECT MIN(next_try) FROM wm_queue WHERE done=0`).Scan(&d); err == nil && d.Valid {
				due[name] = d.Int64
			}
		}
		release()
		if err != nil {
			return fmt.Errorf("reindex %s: %w", name, err)
		}
	}
	tx, err := in.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM local_follows`, `DELETE FROM work`} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	for _, f := range follows {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO local_follows(feed_url,name) VALUES(?,?)`, f.url, f.name); err != nil {
			return err
		}
	}
	for n, d := range due {
		if _, err := tx.Exec(`INSERT INTO work(name,next_due) VALUES(?,?)`, n, d); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- Directory ----

// Followed reports whether any user of this instance follows the feed.
func (in *Instance) Followed(feedURL string) bool {
	var n int
	in.rdb.QueryRow(`SELECT COUNT(*) FROM local_follows WHERE feed_url=?`, feedURL).Scan(&n)
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
	rows, err := in.rdb.Query(`SELECT name FROM work WHERE next_due<=? ORDER BY next_due, name`, time.Now().Unix())
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
	rows, err := in.rdb.Query(`SELECT url,title,link,fetched,error,image FROM feeds WHERE url IN (`+marks(len(urls))+`)`, anys(urls)...)
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
	err := in.rdb.QueryRow(`SELECT feed_url,guid,in_reply_to,root,root_feed FROM items WHERE guid=? AND feed_url IN (`+marks(len(urls))+`) LIMIT 1`,
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

// syncUser rewrites this user's rows in the directory (the feeds they follow, when their next Webmention
// job is due) from their own file, which is the truth.
func (in *Instance) syncUser(sp *Space) error {
	urls, err := sp.followURLs(sp.UID())
	if err != nil {
		return err
	}
	var due sql.NullInt64
	if err := sp.db.QueryRow(`SELECT MIN(next_try) FROM wm_queue WHERE done=0`).Scan(&due); err != nil {
		return err
	}
	tx, err := in.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM local_follows WHERE name=?`, sp.name); err != nil {
		return err
	}
	for _, u := range urls {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO local_follows(feed_url,name) VALUES(?,?)`, u, sp.name); err != nil {
			return err
		}
	}
	if due.Valid {
		_, err = tx.Exec(`INSERT INTO work(name,next_due) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET next_due=excluded.next_due`, sp.name, due.Int64)
	} else {
		_, err = tx.Exec(`DELETE FROM work WHERE name=?`, sp.name)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
