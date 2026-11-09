package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

// Where a user is served from, and what happens to the old address when that changes.
//
// A user has at most one custom domain. Once it is verified (the TXT record) it serves the user, and once it is
// also active (it reaches this instance) it is the user's address, and every other host answers with a
// permanent redirect to it. A user who has moved to another server
// (MarkMoved) is redirected there for good: the directory remembers the new address, the file goes to
// the trash for a while in case the move is undone, and the name stays taken. The web layer asks
// Instance.Dir about every request, so the answers are cached in memory.

var (
	ErrDomainTaken = errors.New("domain already in use")
	ErrBadDomain   = errors.New("not a valid domain name")
	ErrBadAddress  = errors.New("not a valid address: want http(s)://host")
	// ErrDomainBound: the account signed up with this domain and depends on it, so the domain cannot be changed or removed.
	ErrDomainBound = errors.New("the account belongs to its domain")
)

const (
	StatusActive = "active"
	StatusMoved  = "moved"
	// StatusSuspended: the operator has switched the account off. Every address answers 410; the file stays
	// where it is, and the operator can switch it on again.
	StatusSuspended = "suspended"
	// StatusRemoved: the operator has deleted the account. Like a move, its file goes to the trash for TrashDays
	// and the name stays taken, but nothing redirects: every address answers 410.
	StatusRemoved = "removed"
)

// TrashDays is how long the file of a user who moved away is kept before it is deleted.
const TrashDays = 30

var labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// CleanDomain lowercases a domain name and checks it: at least two labels of letters, digits and
// hyphens, not an address, and short enough.
func CleanDomain(s string) (string, error) {
	s = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	if s == "" || len(s) > 253 {
		return "", ErrBadDomain
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return "", ErrBadDomain
	}
	for _, l := range labels {
		if !labelRE.MatchString(l) {
			return "", ErrBadDomain
		}
	}
	if _, err := strconv.Atoi(labels[len(labels)-1]); err == nil {
		return "", ErrBadDomain // 192.168.0.1 is an address, not a name
	}
	return s, nil
}

// ErrSignupDomain is returned for a domain that may not be used to sign up: the reason is in the message.
var ErrSignupDomain = errors.New("not a domain to sign up with")

// SignupDomain checks a domain given at sign-up. It must be exactly weft. followed by the registered domain of
// the person: weft.example.com, weft.example.co.uk. A name further down (weft.blog.example.com) or another
// first label (www.example.com) is refused, as is a registry's own name (weft.co.uk). What a registered domain
// is comes from the public suffix list that is part of golang.org/x/net/publicsuffix, so it is brought up to
// date with the module (go get golang.org/x/net@latest). It returns the cleaned domain and the registered domain,
// which is what one account per domain is counted by.
func SignupDomain(s string) (domain, registered string, err error) {
	bad := func(why string) (string, string, error) { return "", "", fmt.Errorf("%w: %s", ErrSignupDomain, why) }
	d, err := CleanDomain(s)
	if err != nil {
		return bad("that is not a domain name")
	}
	first, rest, _ := strings.Cut(d, ".")
	if first != "weft" {
		return bad("the name must start with weft., as in weft." + rest)
	}
	reg, err := publicsuffix.EffectiveTLDPlusOne(rest)
	if err != nil {
		return bad(rest + " is a registry's name, not a domain of yours")
	}
	if reg != rest {
		return bad("use weft." + reg + ", the name under your registered domain, not weft." + rest)
	}
	return d, reg, nil
}

// Domain is a user's custom domain. It becomes theirs (Verified) once the owner has shown control of it, by
// putting Token in a TXT record. It becomes their address (Active) once it also reaches this instance: until
// then the user is still served, and found, at the instance's address, so a domain whose DNS is not ready
// cannot lock them out.
type Domain struct {
	Domain   string
	Name     string
	Token    string
	Verified bool
	Active   bool
	Created  time.Time
}

// SetDomain gives the user a (still unverified) custom domain, replacing any earlier one. Several users
// may claim the same domain; the first to show control of it with VerifyDomain keeps it. A domain
// somebody has already verified is refused.
func (in *Instance) SetDomain(name, domain string) (*Domain, error) {
	domain, err := CleanDomain(domain)
	if err != nil {
		return nil, err
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	tx, err := in.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE name=? AND status='active'`, name).Scan(&n); err != nil || n == 0 {
		return nil, ErrNotFound
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM domains WHERE name=? AND bound=1`, name).Scan(&n); err != nil {
		return nil, err
	} else if n > 0 {
		return nil, ErrDomainBound
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM domains WHERE domain=? AND verified=1 AND name!=?`, domain, name).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, ErrDomainTaken
	}
	if _, err := tx.Exec(`DELETE FROM domains WHERE name=?`, name); err != nil {
		return nil, err
	}
	now := time.Now()
	if _, err := tx.Exec(`INSERT INTO domains(domain,name,token,created) VALUES(?,?,?,?)`, domain, name, token, now.Unix()); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	in.dirs.drop()
	return &Domain{Domain: domain, Name: name, Token: token, Created: now}, nil
}

// DomainOf returns the user's custom domain, verified or not, or ErrNotFound.
func (in *Instance) DomainOf(name string) (*Domain, error) {
	d := &Domain{Name: name}
	var v, a, c int64
	err := in.rdb.QueryRow(`SELECT domain,token,verified,active,created FROM domains WHERE name=?`, name).Scan(&d.Domain, &d.Token, &v, &a, &c)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.Verified, d.Active, d.Created = v != 0, a != 0, time.Unix(c, 0)
	return d, nil
}

// VerifyDomain marks the user's domain verified: a request that arrives on the domain is served as the user, and
// the proxy may get a certificate for it. The user's address does not change until ActivateDomain.
func (in *Instance) VerifyDomain(name string) error {
	res, err := in.db.Exec(`UPDATE domains SET verified=1 WHERE name=?`, name)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrDomainTaken // another claim was verified first
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	in.dirs.drop()
	return nil
}

// ActivateDomain makes a verified domain the user's address: the other addresses redirect to it. Call it only
// once requests to the domain are known to reach this instance.
func (in *Instance) ActivateDomain(name string) error {
	res, err := in.db.Exec(`UPDATE domains SET active=1 WHERE name=? AND verified=1`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	in.dirs.drop()
	return nil
}

// DeactivateDomain takes the address back from a domain that stopped reaching this instance. The domain stays
// verified, so it can become the address again without a new record in its DNS.
func (in *Instance) DeactivateDomain(name string) error {
	if _, err := in.db.Exec(`UPDATE domains SET active=0 WHERE name=?`, name); err != nil {
		return err
	}
	in.dirs.drop()
	return nil
}

// RemoveDomain takes the custom domain away; the user is served at the instance's address again.
func (in *Instance) RemoveDomain(name string) error {
	if in.IsBound(name) {
		return ErrDomainBound
	}
	if _, err := in.db.Exec(`DELETE FROM domains WHERE name=?`, name); err != nil {
		return err
	}
	in.dirs.drop()
	return nil
}

// UserExists reports whether the name belongs to a user here, one who has moved away included: their
// address still answers, with a redirect, and so needs a certificate.
func (in *Instance) UserExists(name string) bool {
	var n int
	in.rdb.QueryRow(`SELECT COUNT(*) FROM users WHERE name=?`, name).Scan(&n)
	return n > 0
}

// Entry is what the directory knows about a user that changes how requests are answered. Users with
// nothing special have none.
type Entry struct {
	Status  string
	MovedTo string // origin of the new home, while Status is moved
	Domain  string // the active custom domain, if any
	Move    string // while the account moves away: "hold", "lock" or "received"; "" otherwise
}

// dirCache holds the entries of the few users who have one, and the hosts of verified domains. A
// change made through this Instance forgets it at once; one made by another process (the weft
// command) shows up within dirTTL.
type dirCache struct {
	mu     sync.Mutex
	loaded time.Time
	byName map[string]Entry
	hosts  map[string]string
}

const dirTTL = 10 * time.Second

func (c *dirCache) drop() {
	c.mu.Lock()
	c.loaded = time.Time{}
	c.mu.Unlock()
}

func (in *Instance) loadDirs() (map[string]Entry, map[string]string, error) {
	c := &in.dirs
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded.IsZero() && time.Since(c.loaded) < dirTTL {
		return c.byName, c.hosts, nil
	}
	byName, hosts := map[string]Entry{}, map[string]string{}
	rows, err := in.rdb.Query(`SELECT name,status,moved_to,move_state FROM users WHERE status!='active' OR move_state!=''`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var n string
		var e Entry
		if err := rows.Scan(&n, &e.Status, &e.MovedTo, &e.Move); err != nil {
			rows.Close()
			return nil, nil, err
		}
		byName[n] = e
	}
	rows.Close()
	rows, err = in.rdb.Query(`SELECT domain,name,active FROM domains WHERE verified=1`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var d, n string
		var active int
		if err := rows.Scan(&d, &n, &active); err != nil {
			rows.Close()
			return nil, nil, err
		}
		hosts[d] = n // a verified domain serves the user, active or not
		if active == 0 {
			continue
		}
		e := byName[n]
		if e.Status == "" {
			e.Status = StatusActive
		}
		e.Domain = d // only an active one is the address the others redirect to
		byName[n] = e
	}
	rows.Close()
	c.byName, c.hosts, c.loaded = byName, hosts, time.Now()
	return byName, hosts, nil
}

// Dir returns the user's entry; the zero Entry (status active, no domain) if there is nothing special.
func (in *Instance) Dir(name string) Entry {
	byName, _, err := in.loadDirs()
	if err != nil {
		return Entry{Status: StatusActive}
	}
	e, ok := byName[name]
	if !ok {
		return Entry{Status: StatusActive}
	}
	return e
}

// HostUser returns the user a verified custom domain belongs to, active or not.
func (in *Instance) HostUser(host string) (string, bool) {
	_, hosts, err := in.loadDirs()
	if err != nil {
		return "", false
	}
	n, ok := hosts[host]
	return n, ok
}

// Origin reduces an address to scheme://host[:port], or says it is not an http(s) address.
func Origin(s string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", ErrBadAddress
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

// MarkMoved records that the user now lives at the server whose origin is to. From then on every
// request to the old address, the feed included, is redirected there with a permanent redirect. The
// user's file is moved to the trash by TrashMoved, which the server runs; the name stays taken.
func (in *Instance) MarkMoved(name, to string) error {
	to, err := Origin(to)
	if err != nil {
		return err
	}
	res, err := in.db.Exec(`UPDATE users SET status='moved', moved_to=? WHERE name=?`, to, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	in.dirs.drop()
	return nil
}

// TrashDir is where the files of users who moved away wait to be deleted.
func (in *Instance) TrashDir() string { return filepath.Join(in.dir, "trash") }

// trashed lists the user's files in the trash, newest first.
func (in *Instance) trashed(name string) []string {
	files, _ := filepath.Glob(filepath.Join(in.TrashDir(), name+".*.db"))
	sort.Sort(sort.Reverse(sort.StringSlice(files))) // the names end in the time they were trashed
	return files
}

// Unmove cancels a move: the user is served here again, their file brought back from the trash if it
// was already there. It fails once the file is gone.
func (in *Instance) Unmove(name string) error {
	var status string
	if err := in.db.QueryRow(`SELECT status FROM users WHERE name=?`, name).Scan(&status); err != nil || (status != StatusMoved && status != StatusRemoved && status != StatusSuspended) {
		return ErrNotFound
	}
	restored := false
	if _, err := os.Stat(in.spacePath(name)); errors.Is(err, os.ErrNotExist) {
		files := in.trashed(name)
		if len(files) == 0 {
			return fmt.Errorf("the file of %s has been deleted from the trash", name)
		}
		if err := os.MkdirAll(filepath.Dir(in.spacePath(name)), 0o755); err != nil {
			return err
		}
		if err := os.Rename(files[0], in.spacePath(name)); err != nil {
			return err
		}
		restored = true
	}
	if _, err := in.db.Exec(`UPDATE users SET status='active', moved_to='' WHERE name=?`, name); err != nil {
		return err
	}
	in.dirs.drop()
	if restored {
		return in.Reindex() // the follows and work of the user are back in the directory
	}
	return nil
}

// TrashMoved moves the files of users who have moved away into trash/ (to be deleted after
// TrashDays), and takes their follows, work and domains out of the directory. A file that a request is
// using waits for the next call. Only the server process may call it: it is the one that holds the
// files open.
func (in *Instance) TrashMoved(now time.Time) (int, error) {
	rows, err := in.db.Query(`SELECT name FROM users WHERE status IN ('moved','removed')`)
	if err != nil {
		return 0, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return 0, err
		}
		names = append(names, n)
	}
	rows.Close()
	done := 0
	var errs []error
	for _, n := range names {
		if _, err := os.Stat(in.spacePath(n)); errors.Is(err, os.ErrNotExist) {
			continue // already in the trash
		}
		if !in.pool.forget(n) {
			continue
		}
		if err := os.MkdirAll(in.TrashDir(), 0o755); err != nil {
			return done, err
		}
		dst := filepath.Join(in.TrashDir(), fmt.Sprintf("%s.%d.db", n, now.Unix()))
		if err := os.Rename(in.spacePath(n), dst); err != nil {
			errs = append(errs, fmt.Errorf("moving %s to trash: %w", n, err))
			continue
		}
		for _, q := range []string{`DELETE FROM local_follows WHERE name=?`, `DELETE FROM work WHERE name=?`, `DELETE FROM domains WHERE name=?`} {
			if _, err := in.db.Exec(q, n); err != nil {
				errs = append(errs, err)
			}
		}
		done++
	}
	if done > 0 {
		in.dirs.drop()
	}
	return done, errors.Join(errs...)
}

// PurgeTrash deletes trashed files that are older than TrashDays and returns how many.
func (in *Instance) PurgeTrash(now time.Time) (int, error) {
	files, err := os.ReadDir(in.TrashDir())
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range files {
		parts := strings.Split(f.Name(), ".") // name.unix.db
		if len(parts) < 3 {
			continue
		}
		at, err := strconv.ParseInt(parts[len(parts)-2], 10, 64)
		if err != nil || now.Sub(time.Unix(at, 0)) < TrashDays*24*time.Hour {
			continue
		}
		if err := os.Remove(filepath.Join(in.TrashDir(), f.Name())); err == nil {
			n++
		}
	}
	return n, nil
}
