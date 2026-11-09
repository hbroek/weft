package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// What the operator can do to accounts, and what the administration pages may know about them: the name, the
// state, the domain, the size of the file and when the last post was made. Never what the posts say.

var (
	ErrAdmin    = errors.New("this is an administrator's account")
	ErrNotReady = errors.New("the account is not in a state that allows this")
)

// SetAdmin marks the account as an administrator's, or takes the mark off. Administrators sign in to the
// administration pages and do not count toward the cap. The account must be one that is served here.
func (in *Instance) SetAdmin(name string, on bool) error {
	v := 0
	if on {
		v = 1
	}
	res, err := in.db.Exec(`UPDATE users SET admin=? WHERE name=? AND status='active'`, v, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: no active account called %s", ErrNotFound, name)
	}
	return nil
}

// IsAdmin reports whether the named account is an administrator's, and active.
func (in *Instance) IsAdmin(name string) bool {
	var n int
	err := in.rdb.QueryRow(`SELECT COUNT(*) FROM users WHERE name=? AND admin=1 AND status='active'`, name).Scan(&n)
	return err == nil && n == 1
}

// Suspend switches an account off: every address answers 410 until Restore. An administrator's account cannot be
// suspended, so the operator cannot lock themselves out.
func (in *Instance) Suspend(name string) error {
	return in.setStatus(name, StatusActive, StatusSuspended)
}

// Remove deletes an account: it answers 410 everywhere, its file goes to the trash for TrashDays (the server
// does that within a minute), and the name stays taken. Restore brings it back while the file is in the trash.
func (in *Instance) Remove(name string) error {
	var status string
	if err := in.rdb.QueryRow(`SELECT status FROM users WHERE name=?`, name).Scan(&status); err != nil {
		return ErrNotFound
	}
	if status == StatusMoved || status == StatusRemoved {
		return ErrNotReady
	}
	return in.setStatus(name, status, StatusRemoved)
}

func (in *Instance) setStatus(name, from, to string) error {
	res, err := in.db.Exec(`UPDATE users SET status=? WHERE name=? AND status=? AND admin=0`, to, name, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var admin int
		if in.rdb.QueryRow(`SELECT admin FROM users WHERE name=?`, name).Scan(&admin) == nil && admin == 1 {
			return ErrAdmin
		}
		return ErrNotReady
	}
	in.dirs.drop()
	return nil
}

// Restore switches a suspended account on again, or brings a removed one back from the trash.
func (in *Instance) Restore(name string) error {
	var status string
	if err := in.rdb.QueryRow(`SELECT status FROM users WHERE name=?`, name).Scan(&status); err != nil {
		return ErrNotFound
	}
	if status != StatusSuspended && status != StatusRemoved {
		return ErrNotReady
	}
	return in.Unmove(name)
}

// Account is a line of the list of accounts.
type Account struct {
	Name     string
	Status   string
	Admin    bool
	Created  int64
	Domain   string // the verified custom domain, if any
	Active   bool   // the domain reaches this server
	Bytes    int64  // the size of the file on disk
	LastPost int64  // when the last post was made, 0 for none; only filled in by FillLastPost
}

// Accounts lists the accounts, oldest first, from the offset on, at most limit; total is how many there are.
func (in *Instance) Accounts(offset, limit int) (list []Account, total int, err error) {
	if err = in.rdb.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&total); err != nil {
		return
	}
	rows, err := in.rdb.Query(`SELECT u.name,u.status,u.admin,u.created,COALESCE(d.domain,''),COALESCE(d.active,0)
		FROM users u LEFT JOIN domains d ON d.name=u.name AND d.verified=1
		ORDER BY u.created,u.name LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var a Account
		var admin, active int
		if err = rows.Scan(&a.Name, &a.Status, &admin, &a.Created, &a.Domain, &active); err != nil {
			return
		}
		a.Admin, a.Active = admin == 1, active == 1
		a.Bytes = fileSize(in.spacePath(a.Name))
		list = append(list, a)
	}
	return list, total, rows.Err()
}

// FillLastPost reads when each account last posted. It opens the files of the accounts that are served here, one
// after the other, and reads one number from each.
func (in *Instance) FillLastPost(list []Account) {
	for i := range list {
		if list[i].Status == StatusMoved || list[i].Status == StatusRemoved {
			continue
		}
		sp, release, err := in.Space(list[i].Name)
		if err != nil {
			continue
		}
		var last *int64
		sp.db.QueryRow(`SELECT MAX(created) FROM posts WHERE deleted=0 AND in_reply_to=''`).Scan(&last)
		release()
		if last != nil {
			list[i].LastPost = *last
		}
	}
}

// fileSize is the size of a SQLite file with its write-ahead log, or 0.
func fileSize(path string) int64 {
	var n int64
	for _, p := range []string{path, path + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// DiskUse is how much room the data folder takes, in bytes: the users' files, instance.db and the trash.
type DiskUse struct {
	Spools, Instance, Trash int64
}

// DiskUse walks the data folder.
func (in *Instance) DiskUse() DiskUse {
	var d DiskUse
	filepath.WalkDir(in.dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		fi, err := e.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(in.dir, p)
		switch {
		case strings.HasPrefix(rel, "trash"+string(filepath.Separator)):
			d.Trash += fi.Size()
		case strings.HasPrefix(rel, "users"+string(filepath.Separator)):
			d.Spools += fi.Size()
		case strings.HasPrefix(rel, "instance.db"):
			d.Instance += fi.Size()
		}
		return nil
	})
	return d
}

// DataDir is the data folder.
func (in *Instance) DataDir() string { return in.dir }

// HeldCount is how many Webmentions wait for accounts that are moving.
func (in *Instance) HeldCount() (n int) {
	in.rdb.QueryRow(`SELECT COUNT(*) FROM held`).Scan(&n)
	return
}

// PendingSignups lists the sign-ups that still wait for their DNS record, newest first. The secret that finds a
// sign-up again and the token are left out: the list is for the operator to see, not to act on.
func (in *Instance) PendingSignups() ([]Signup, error) {
	rows, err := in.rdb.Query(`SELECT name,domain,registered,created FROM signups WHERE created>? ORDER BY created DESC`,
		time.Now().Add(-SignupTTL).Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Signup
	for rows.Next() {
		var s Signup
		var created int64
		if err := rows.Scan(&s.Name, &s.Domain, &s.Registered, &created); err != nil {
			return nil, err
		}
		s.Created = time.Unix(created, 0)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Tally counts the accounts by state.
type Tally struct {
	Active, Suspended, Removed, Moved, Admins int
}

func (in *Instance) Tally() (t Tally, err error) {
	rows, err := in.rdb.Query(`SELECT status,admin,COUNT(*) FROM users GROUP BY status,admin`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var admin, n int
		if err = rows.Scan(&st, &admin, &n); err != nil {
			return
		}
		switch st {
		case StatusActive:
			t.Active += n
		case StatusSuspended:
			t.Suspended += n
		case StatusRemoved:
			t.Removed += n
		case StatusMoved:
			t.Moved += n
		}
		if admin == 1 {
			t.Admins += n
		}
	}
	return t, rows.Err()
}
