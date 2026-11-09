package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Problem is one thing fsck found wrong. Name is the user it concerns, or "" for the instance.
type Problem struct {
	Name  string
	What  string
	Fixes bool // Reindex repairs it
}

func (p Problem) String() string {
	who := "instance"
	if p.Name != "" {
		who = p.Name
	}
	return who + ": " + p.What
}

// FsckReport is what Fsck found.
type FsckReport struct {
	Users    int // files examined
	Problems []Problem
	Old      []string // users whose file is at an older schema than this Weft; opening them upgrades them
}

// CheckDirectory compares the directory with the files without opening any of them: every user has a
// file (unless they moved away), every file has a user and is where its name says. It is cheap enough
// to run at every start.
func (in *Instance) CheckDirectory() ([]Problem, error) {
	rows, err := in.rdb.Query(`SELECT name, status FROM users`)
	if err != nil {
		return nil, err
	}
	want := map[string]string{}
	for rows.Next() {
		var n, st string
		if err := rows.Scan(&n, &st); err != nil {
			rows.Close()
			return nil, err
		}
		want[n] = st
	}
	rows.Close()
	var out []Problem
	have := map[string]bool{}
	dirs, err := os.ReadDir(filepath.Join(in.dir, "users"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, d := range dirs {
		files, err := os.ReadDir(filepath.Join(in.dir, "users", d.Name()))
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			name, ok := strings.CutSuffix(f.Name(), ".db")
			if !ok || f.IsDir() {
				continue // -wal and -shm files of a database in use, and anything else
			}
			have[name] = true
			switch {
			case !nameRE.MatchString(name):
				out = append(out, Problem{Name: name, What: "a file with a name no user can have: users/" + d.Name() + "/" + f.Name()})
			case spaceRel(name) != filepath.Join("users", d.Name(), f.Name()):
				out = append(out, Problem{Name: name, What: "the file is in the wrong folder: users/" + d.Name() + "/" + f.Name()})
			default:
				if _, ok := want[name]; !ok {
					out = append(out, Problem{Name: name, What: "a file with no user in the directory (an unfinished sign-up or import?)"})
				}
			}
		}
	}
	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		if want[n] != StatusMoved && want[n] != StatusRemoved && !have[n] {
			out = append(out, Problem{Name: n, What: "the user has no file"})
		}
	}
	return out, nil
}

// Fsck checks the instance's database and every user's file (or only the named users'). A quick check
// looks at the structure of the database; full also checks every row against its indexes, which takes
// longer. It only reads. It does not open files through the pool, and works while the server runs.
func (in *Instance) Fsck(ctx context.Context, full bool, only ...string) (*FsckReport, error) {
	rep := &FsckReport{}
	if p, err := in.CheckDirectory(); err != nil {
		return nil, err
	} else {
		for _, q := range p {
			if len(only) == 0 || slices.Contains(only, q.Name) {
				rep.Problems = append(rep.Problems, q)
			}
		}
	}
	check := "quick_check"
	if full {
		check = "integrity_check"
	}
	if len(only) == 0 {
		if msgs, err := pragmaLines(ctx, in.rdb, check); err != nil {
			return nil, err
		} else {
			for _, m := range msgs {
				rep.Problems = append(rep.Problems, Problem{What: m})
			}
		}
	}
	names, err := in.UserNames()
	if err != nil {
		return nil, err
	}
	if len(only) > 0 {
		names = only
	}
	follows, work, err := in.directoryRows()
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if len(only) > 0 && !in.UserExists(n) {
			rep.Problems = append(rep.Problems, Problem{Name: n, What: "no such user"})
			continue
		}
		if _, err := os.Stat(in.spacePath(n)); err != nil {
			continue // CheckDirectory has said so
		}
		rep.Users++
		probs, old := in.fsckUser(ctx, n, check, follows[n], work[n])
		rep.Problems = append(rep.Problems, probs...)
		if old {
			rep.Old = append(rep.Old, n)
		}
	}
	return rep, nil
}

// directoryRows reads what the directory says about each user's follows and next work.
func (in *Instance) directoryRows() (follows map[string][]string, work map[string]int64, err error) {
	follows, work = map[string][]string{}, map[string]int64{}
	rows, err := in.rdb.Query(`SELECT name, feed_url FROM local_follows ORDER BY name, feed_url`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var n, u string
		if err := rows.Scan(&n, &u); err != nil {
			rows.Close()
			return nil, nil, err
		}
		follows[n] = append(follows[n], u)
	}
	rows.Close()
	rows, err = in.rdb.Query(`SELECT name, next_due FROM work`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		var d int64
		if err := rows.Scan(&n, &d); err != nil {
			return nil, nil, err
		}
		work[n] = d
	}
	return follows, work, rows.Err()
}

func pragmaLines(ctx context.Context, db *sql.DB, pragma string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA `+pragma)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if s != "ok" {
			out = append(out, pragma+": "+s)
		}
	}
	return out, rows.Err()
}

// fsckUser examines one user's file against the directory. old is true when its schema is older than
// this Weft's.
func (in *Instance) fsckUser(ctx context.Context, name, check string, dirFollows []string, dirWork int64) (probs []Problem, old bool) {
	bad := func(fixes bool, format string, a ...any) {
		probs = append(probs, Problem{Name: name, What: fmt.Sprintf(format, a...), Fixes: fixes})
	}
	db, err := sql.Open("sqlite", in.spacePath(name)+"?_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		bad(false, "cannot open: %v", err)
		return
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		bad(false, "cannot read: %v", err)
		return
	}
	switch {
	case v > SpaceSchemaVersion:
		bad(false, "schema version %d is newer than this Weft knows (%d)", v, SpaceSchemaVersion)
		return
	case v < SpaceSchemaVersion:
		old = true
	}
	msgs, err := pragmaLines(ctx, db, check)
	if err != nil {
		bad(false, "%s: %v", check, err)
		return
	}
	for _, m := range msgs {
		bad(false, "%s", m)
	}
	if len(msgs) > 0 {
		return // the rest would read a damaged file
	}
	if rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`); err == nil {
		n := 0
		for rows.Next() {
			n++
		}
		rows.Close()
		if n > 0 {
			bad(false, "%d rows refer to rows that do not exist", n)
		}
	}
	var n int
	var got string
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(name),'') FROM users`).Scan(&n, &got); err != nil {
		bad(false, "no users table: %v", err)
		return
	}
	if n != 1 || got != name {
		bad(false, "the file holds %d accounts, the first named %q", n, got)
	}
	// what the directory says the user follows and when they next have work is derived from the file
	var follows []string
	if rows, err := db.QueryContext(ctx, `SELECT feed_url FROM follows ORDER BY feed_url`); err == nil {
		for rows.Next() {
			var u string
			rows.Scan(&u)
			follows = append(follows, u)
		}
		rows.Close()
	}
	if !slices.Equal(follows, dirFollows) {
		bad(true, "the directory lists %d followed feeds, the file has %d", len(dirFollows), len(follows))
	}
	var due sql.NullInt64
	lazy := lazyDB{db}
	if err := lazy.QueryRow(`SELECT MIN(next_try) FROM wm_queue WHERE done=0`).Scan(&due); err == nil && due.Int64 != dirWork {
		bad(true, "the directory has next work at %d, the file at %d", dirWork, due.Int64)
	}
	return
}
