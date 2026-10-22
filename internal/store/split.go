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

// SplitReport says what Split wrote.
type SplitReport struct {
	Users   int
	Rows    map[string]int // rows written per table, summed over the users' files
	Shared  map[string]int // rows written to instance.db
	Dropped []string       // rows that could not be attributed to a user, and were left behind
}

// Split reads a database in the single-file layout and writes the per-user layout into dataDir, which
// must not exist or must be empty. The old file is only ever read: a snapshot of it is taken first (so
// a server may still be using it), and that snapshot is brought up to date and copied from. Row counts
// are compared afterwards, and a mismatch is an error.
func Split(legacyPath, dataDir string) (*SplitReport, error) {
	if es, err := os.ReadDir(dataDir); err == nil && len(es) > 0 {
		return nil, fmt.Errorf("%s is not empty", dataDir)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	snap := filepath.Join(dataDir, ".split-snapshot.db")
	defer func() {
		for _, s := range []string{"", "-wal", "-shm"} {
			os.Remove(snap + s)
		}
	}()
	if err := snapshot(legacyPath, snap); err != nil {
		return nil, fmt.Errorf("snapshot of %s: %w", legacyPath, err)
	}
	old, err := sql.Open("sqlite", snap+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	old.SetMaxOpenConns(1)
	if err := migrateLegacy(old); err != nil {
		old.Close()
		return nil, err
	}
	old.Close()

	in, err := Open(dataDir)
	if err != nil {
		return nil, err
	}
	rep, err := split(in, snap)
	if cerr := in.Close(); err == nil {
		err = cerr
	}
	return rep, err
}

// snapshot copies a database, consistently, without writing to it.
func snapshot(from, to string) error {
	if _, err := os.Stat(from); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", from+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`VACUUM INTO ?`, to)
	return err
}

// columnsOf returns the columns the table has in main, which are the ones a copy names.
func columnsOf(ctx context.Context, c *sql.Conn, schema, table string) ([]string, error) {
	rows, err := c.QueryContext(ctx, `SELECT name FROM pragma_table_info(?, ?)`, table, schema)
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

// copyRows copies the rows of old.table that match where into main.table, naming the columns both have,
// and returns how many it copied. Ids are kept, so every address that contains one stays valid.
func copyRows(ctx context.Context, c *sql.Conn, table, where string, args ...any) (int, error) {
	dst, err := columnsOf(ctx, c, "main", table)
	if err != nil {
		return 0, err
	}
	src, err := columnsOf(ctx, c, "old", table)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for _, s := range src {
		have[s] = true
	}
	var cols []string
	for _, d := range dst {
		if have[d] {
			cols = append(cols, d)
		}
	}
	list := strings.Join(cols, ",")
	q := `INSERT INTO main.` + table + `(` + list + `) SELECT ` + list + ` FROM old.` + table
	if where != "" {
		q += ` WHERE ` + where
	}
	res, err := c.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("copying %s: %w", table, err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func countOld(ctx context.Context, c *sql.Conn, table string) (n int, err error) {
	err = c.QueryRowContext(ctx, `SELECT COUNT(*) FROM old.`+table).Scan(&n)
	return
}

func split(in *Instance, snap string) (*SplitReport, error) {
	ctx := context.Background()
	rep := &SplitReport{Rows: map[string]int{}, Shared: map[string]int{}}

	// the shared tables go to instance.db
	c, err := in.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := c.ExecContext(ctx, `ATTACH DATABASE ? AS old`, snap); err != nil {
		c.Close()
		return nil, err
	}
	type user struct {
		id      int64
		name    string
		created int64
	}
	var users []user
	rows, err := c.QueryContext(ctx, `SELECT id,name,created FROM old.users ORDER BY id`)
	if err != nil {
		c.Close()
		return nil, err
	}
	for rows.Next() {
		var u user
		if err := rows.Scan(&u.id, &u.name, &u.created); err != nil {
			rows.Close()
			c.Close()
			return nil, err
		}
		users = append(users, u)
	}
	rows.Close()
	want := map[string]int{}
	for _, t := range []string{"feeds", "items", "feed_likes"} {
		if want[t], err = countOld(ctx, c, t); err != nil {
			c.Close()
			return nil, err
		}
		n, err := copyRows(ctx, c, t, "")
		if err != nil {
			c.Close()
			return nil, err
		}
		if n != want[t] {
			c.Close()
			return nil, fmt.Errorf("%s: copied %d of %d rows", t, n, want[t])
		}
		rep.Shared[t] = n
	}
	for _, t := range []string{"users", "sessions", "posts", "follows", "replies", "subreplies", "likes", "likes_in", "wm_queue"} {
		if want[t], err = countOld(ctx, c, t); err != nil {
			c.Close()
			return nil, err
		}
	}
	for _, u := range users {
		if !nameRE.MatchString(u.name) {
			rep.Dropped = append(rep.Dropped, fmt.Sprintf("user %q: not a valid name", u.name))
			continue
		}
		if _, err := c.ExecContext(ctx, `INSERT INTO main.users(name,created) VALUES(?,?)`, u.name, u.created); err != nil {
			c.Close()
			return nil, err
		}
	}
	c.Close() // detaches old

	for _, u := range users {
		if !nameRE.MatchString(u.name) {
			continue
		}
		if err := splitUser(ctx, in, snap, u.id, u.name, rep); err != nil {
			return nil, fmt.Errorf("user %s: %w", u.name, err)
		}
		rep.Users++
	}

	// every row is accounted for, except what had no owner
	for _, t := range []string{"users", "sessions", "posts", "follows", "replies", "subreplies", "likes", "likes_in", "wm_queue"} {
		if rep.Rows[t] != want[t] {
			if t == "wm_queue" {
				rep.Dropped = append(rep.Dropped, fmt.Sprintf("%d finished or unattributable Webmention jobs", want[t]-rep.Rows[t]))
				continue
			}
			return rep, fmt.Errorf("%s: wrote %d rows, the old file has %d", t, rep.Rows[t], want[t])
		}
	}
	if err := in.Reindex(); err != nil {
		return rep, err
	}
	return rep, nil
}

// splitUser writes one user's file from the snapshot.
func splitUser(ctx context.Context, in *Instance, snap string, id int64, name string, rep *SplitReport) error {
	path := in.spacePath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	f.Close()
	db, err := openSpaceDB(path)
	if err != nil {
		return err
	}
	defer closeDB(db)
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, `ATTACH DATABASE ? AS old`, snap); err != nil {
		return err
	}
	defer c.ExecContext(ctx, `DETACH DATABASE old`)
	// a job queued to send before jobs were stamped with their owner has user_id 0; the host of its
	// source names the owner (name.domain)
	mine := `(SELECT id FROM old.posts WHERE user_id=?)`
	steps := []struct {
		table, where string
		args         []any
	}{
		{"users", "id=?", []any{id}},
		{"sessions", "user_id=?", []any{id}},
		{"posts", "user_id=?", []any{id}},
		{"follows", "user_id=?", []any{id}},
		{"replies", "post_id IN " + mine, []any{id}},
		{"subreplies", "post_id IN " + mine, []any{id}},
		{"likes", "user_id=?", []any{id}},
		{"likes_in", "post_id IN " + mine, []any{id}},
		{"wm_queue", `user_id=? OR (user_id=0 AND substr(source, instr(source,'//')+2, length(?)+1) = ?||'.')`, []any{id, name, name}},
	}
	for _, s := range steps {
		if slices.Contains(lazyTables, s.table) { // only an account that has such rows gets the tables
			var have int
			if err := c.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM old.`+s.table+` WHERE `+s.where+`)`, s.args...).Scan(&have); err != nil {
				return err
			}
			if have == 0 {
				continue
			}
			if _, err := c.ExecContext(ctx, lazySchema); err != nil {
				return err
			}
		}
		n, err := copyRows(ctx, c, s.table, s.where, s.args...)
		if err != nil {
			return err
		}
		rep.Rows[s.table] += n
	}
	// the user's one row carries the id the rest of the file refers to
	var got int64
	if err := c.QueryRowContext(ctx, `SELECT id FROM users`).Scan(&got); err != nil || got != id {
		return errors.New("account row missing after copy")
	}
	return nil
}
