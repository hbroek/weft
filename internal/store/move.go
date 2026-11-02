package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Moving an account to another server, seen from the server it leaves (the "old" server) and the one it
// comes to (the "new" one). The steps are in docs/plan.md, iteration 10. This file holds what the storage does:
// the code, the copy, the state the account is in while it moves, the Webmentions held meanwhile, and the
// swap of the new account for the copied one.

// The states an account is in while it moves away. The empty state is the normal one.
const (
	MoveHold     = "hold"     // a copy was made; mentions are held; the user can still use the site
	MoveLock     = "lock"     // changes are refused for the last round trips
	MoveReceived = "received" // the new server has a good copy; only the user can cancel from here
)

const (
	moveCodeLife   = time.Hour
	moveHoldLife   = 30 * time.Minute
	moveLockLife   = 3 * time.Minute
	moveMaxTries   = 5
	maxHeldPerName = 5000
)

var (
	ErrMoveCode  = errors.New("that move code is not right, or it has expired")
	ErrMoveBusy  = errors.New("a move is already under way")
	ErrMoveState = errors.New("the move is not in the state this step needs")
)

// Move is a row of the moves table: one account on its way out.
type Move struct {
	Name        string
	Dest        string // origin the account will have on the new server
	Expires     time.Time
	State       string // "" until prepare, then one of the Move* states
	File        string // the copy, relative to the data folder
	SHA256      string
	Size        int64
	Schema      int
	Fingerprint string
}

func hashOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// codeAlphabet leaves out what is easily mistaken for something else.
const codeAlphabet = "abcdefghjkmnpqrstvwxyz23456789"

// newMoveCode makes a code of 25 characters (about 120 bits) in groups of five.
func newMoveCode() (string, error) {
	var b [25]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	out := make([]byte, 0, 29)
	for i, c := range b {
		if i > 0 && i%5 == 0 {
			out = append(out, '-')
		}
		out = append(out, codeAlphabet[int(c)%len(codeAlphabet)])
	}
	return string(out), nil
}

// cleanCode lowers a code typed by a person and drops spaces and dashes, so that it matches however it was
// copied.
func cleanCode(c string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '-' || r == ' ' || r == '\t' || r == '\n':
			return -1
		case r >= 'A' && r <= 'Z':
			return r + 32
		}
		return r
	}, c)
}

// MoveCode makes a move code for the user, to be used for the new server whose address for the account is
// dest, and returns it. It is shown once; only a hash is kept. A code that was not used yet is replaced; a move
// already under way is not.
func (in *Instance) MoveCode(name, dest string) (string, error) {
	dest, err := Origin(dest)
	if err != nil {
		return "", err
	}
	var state string
	if err := in.db.QueryRow(`SELECT move_state FROM users WHERE name=? AND status='active'`, name).Scan(&state); err != nil {
		return "", ErrNotFound
	}
	if state != "" {
		return "", ErrMoveBusy
	}
	code, err := newMoveCode()
	if err != nil {
		return "", err
	}
	_, err = in.db.Exec(`INSERT INTO moves(name,dest,code_hash,expires) VALUES(?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET dest=excluded.dest, code_hash=excluded.code_hash, expires=excluded.expires, attempts=0`,
		name, dest, hashOf(cleanCode(code)), time.Now().Add(moveCodeLife).Unix())
	return code, err
}

// MovePending returns the account's move row, if it has one.
func (in *Instance) MovePending(name string) (*Move, error) {
	m := &Move{Name: name}
	var exp int64
	var state string
	err := in.rdb.QueryRow(`SELECT m.dest,m.expires,m.file,m.sha256,m.size,m.schema,m.fingerprint,u.move_state
		FROM moves m JOIN users u ON u.name=m.name WHERE m.name=?`, name).Scan(&m.Dest, &exp, &m.File, &m.SHA256, &m.Size, &m.Schema, &m.Fingerprint, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Expires, m.State = time.Unix(exp, 0), state
	return m, nil
}

// MovePrepare is the new server's first call: it checks the code, and the address the new server says it will
// give the account, and starts the hold. It returns the token for the calls that follow. Wrong guesses are
// counted, and the code is gone after a few.
func (in *Instance) MovePrepare(name, code, dest string) (string, error) {
	var hash, want string
	var exp, tries int64
	var state string
	err := in.db.QueryRow(`SELECT m.code_hash,m.dest,m.expires,m.attempts,u.move_state FROM moves m JOIN users u ON u.name=m.name WHERE m.name=?`, name).Scan(&hash, &want, &exp, &tries, &state)
	if err != nil || hash == "" || state != "" || time.Now().Unix() > exp {
		return "", ErrMoveCode
	}
	dest, derr := Origin(dest)
	ok := subtle.ConstantTimeCompare([]byte(hash), []byte(hashOf(cleanCode(code)))) == 1
	if !ok || derr != nil || dest != want {
		if tries+1 >= moveMaxTries {
			in.db.Exec(`DELETE FROM moves WHERE name=?`, name)
		} else {
			in.db.Exec(`UPDATE moves SET attempts=attempts+1 WHERE name=?`, name)
		}
		return "", ErrMoveCode
	}
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	tx, err := in.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE moves SET code_hash='', xfer_hash=?, expires=? WHERE name=?`, hashOf(tok), time.Now().Add(moveHoldLife).Unix(), name); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`UPDATE users SET move_state=? WHERE name=?`, MoveHold, name); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	in.dirs.drop()
	return tok, nil
}

// MoveAuth checks the token of the calls after prepare, and returns the move.
func (in *Instance) MoveAuth(name, token string) (*Move, error) {
	var h string
	if err := in.rdb.QueryRow(`SELECT xfer_hash FROM moves WHERE name=?`, name).Scan(&h); err != nil || h == "" ||
		subtle.ConstantTimeCompare([]byte(h), []byte(hashOf(token))) != 1 {
		return nil, ErrMoveCode
	}
	return in.MovePending(name)
}

// MoveStore records the copy that is now the one on offer.
func (in *Instance) MoveStore(name string, c Copy) error {
	var old string
	in.db.QueryRow(`SELECT file FROM moves WHERE name=?`, name).Scan(&old)
	if _, err := in.db.Exec(`UPDATE moves SET file=?, sha256=?, size=?, schema=?, fingerprint=? WHERE name=?`,
		c.File, c.SHA256, c.Size, c.Schema, c.Fingerprint, name); err != nil {
		return err
	}
	if old != "" && old != c.File {
		os.Remove(filepath.Join(in.dir, old))
	}
	return nil
}

// MoveSetState moves the account to a state, for as long as life (zero: no limit).
func (in *Instance) MoveSetState(name, state string, life time.Duration) error {
	exp := int64(1 << 62)
	if life > 0 {
		exp = time.Now().Add(life).Unix()
	}
	tx, err := in.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE moves SET expires=? WHERE name=?`, exp, name); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE users SET move_state=? WHERE name=?`, state, name); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	in.dirs.drop()
	return nil
}

// MoveTouch gives the hold a new lease, as a download in progress is a sign of life.
func (in *Instance) MoveTouch(name string) {
	in.db.Exec(`UPDATE moves SET expires=? WHERE name=? AND expires<?`, time.Now().Add(moveHoldLife).Unix(), name, time.Now().Add(moveHoldLife).Unix())
}

// MoveLockLife and MoveHoldLife are how long a step may take before the account is released.
func MoveLockLife() time.Duration { return moveLockLife }
func MoveHoldLife() time.Duration { return moveHoldLife }

// MoveAbort ends a move that did not finish: the account is normal again, its copy is deleted, and the
// mentions held meanwhile are put into its inbox, where they are checked as if they had just arrived.
func (in *Instance) MoveAbort(name string) error {
	var file string
	in.db.QueryRow(`SELECT file FROM moves WHERE name=?`, name).Scan(&file)
	if _, err := in.db.Exec(`UPDATE users SET move_state='' WHERE name=?`, name); err != nil {
		return err
	}
	if _, err := in.db.Exec(`DELETE FROM moves WHERE name=?`, name); err != nil {
		return err
	}
	in.dirs.drop()
	if file != "" {
		os.Remove(filepath.Join(in.dir, file))
	}
	held, err := in.TakeHeld(name)
	if err != nil || len(held) == 0 {
		return err
	}
	sp, release, err := in.Space(name)
	if err != nil {
		return err
	}
	defer release()
	for _, h := range held {
		sp.Enqueue(QueueIn, sp.UID(), h.Source, h.Target)
	}
	return nil
}

// MoveFinish records that the account lives on the new server now: it is redirected there, and the move row and the
// copy go. The held mentions stay, for the server to send on (TakeHeld).
func (in *Instance) MoveFinish(name string) error {
	m, err := in.MovePending(name)
	if err != nil {
		return err
	}
	if m.State != MoveReceived {
		return ErrMoveState
	}
	if err := in.MarkMoved(name, m.Dest); err != nil {
		return err
	}
	in.db.Exec(`UPDATE users SET move_state='' WHERE name=?`, name)
	in.db.Exec(`DELETE FROM moves WHERE name=?`, name)
	in.dirs.drop()
	if m.File != "" {
		os.Remove(filepath.Join(in.dir, m.File))
	}
	return nil
}

// ExpireMoves ends the moves that ran out of time: a code nobody used is forgotten, and a hold or lock whose
// new server went quiet is released. A move that was received waits for the user.
func (in *Instance) ExpireMoves(now time.Time) {
	rows, err := in.db.Query(`SELECT m.name,u.move_state FROM moves m JOIN users u ON u.name=m.name WHERE m.expires<?`, now.Unix())
	if err != nil {
		return
	}
	type row struct{ name, state string }
	var late []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.name, &r.state) == nil {
			late = append(late, r)
		}
	}
	rows.Close()
	for _, r := range late {
		switch r.state {
		case "":
			in.db.Exec(`DELETE FROM moves WHERE name=?`, r.name)
		case MoveHold, MoveLock:
			in.MoveAbort(r.name)
		}
	}
}

// Held is a Webmention that arrived for an account while it moved.
type Held struct{ Source, Target string }

// Hold keeps a Webmention that arrived for an account on its way out. It reports false when too many
// are held already.
func (in *Instance) Hold(name, source, target string) bool {
	var n int
	in.db.QueryRow(`SELECT COUNT(*) FROM held WHERE name=?`, name).Scan(&n)
	if n >= maxHeldPerName {
		return false
	}
	_, err := in.db.Exec(`INSERT INTO held(name,source,target,created) VALUES(?,?,?,?)`, name, source, target, time.Now().Unix())
	return err == nil
}

// TakeHeld returns the held mentions of the account and forgets them.
func (in *Instance) TakeHeld(name string) ([]Held, error) {
	rows, err := in.db.Query(`SELECT source,target FROM held WHERE name=? ORDER BY id`, name)
	if err != nil {
		return nil, err
	}
	var out []Held
	for rows.Next() {
		var h Held
		if err := rows.Scan(&h.Source, &h.Target); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, h)
	}
	rows.Close()
	_, err = in.db.Exec(`DELETE FROM held WHERE name=?`, name)
	return out, err
}

// Copy is a file with a user's export in it, ready to be fetched.
type Copy struct {
	File        string // relative to the data folder
	SHA256      string
	Size        int64
	Schema      int
	Fingerprint string
}

// MakeCopy writes the user's export to a new file in the data folder and describes it.
func (in *Instance) MakeCopy(sp *Space, domain string) (Copy, error) {
	dir := filepath.Join(in.dir, "moves")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Copy{}, err
	}
	var rnd [6]byte
	rand.Read(rnd[:])
	rel := filepath.Join("moves", fmt.Sprintf("%s.%d.%s.zip", sp.name, time.Now().Unix(), hex.EncodeToString(rnd[:])))
	f, err := os.OpenFile(filepath.Join(in.dir, rel), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Copy{}, err
	}
	h := sha256.New()
	fp, schema, err := sp.export(io.MultiWriter(f, h), domain)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(filepath.Join(in.dir, rel))
		return Copy{}, err
	}
	fi, err := os.Stat(filepath.Join(in.dir, rel))
	if err != nil {
		return Copy{}, err
	}
	return Copy{File: rel, SHA256: hex.EncodeToString(h.Sum(nil)), Size: fi.Size(), Schema: schema, Fingerprint: fp}, nil
}

// CopyPath is where a copy's file is.
func (in *Instance) CopyPath(rel string) string { return filepath.Join(in.dir, rel) }

// Fingerprint describes what is in the user's file, for telling whether anything changed between two moments:
// every row of every table but the sessions, the API token, and the bytes of the pictures (a picture is
// named by the hash of its bytes, so its name and size stand for them).
func (s *Space) Fingerprint() (string, error) {
	return fingerprintDB(s.db.DB)
}

func fingerprintFile(path string) (string, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return fingerprintDB(db)
}

type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

func fingerprintDB(db querier) (string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name!='sessions' ORDER BY name`)
	if err != nil {
		return "", err
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return "", err
		}
		tables = append(tables, t)
	}
	rows.Close()
	sort.Strings(tables)
	h := sha256.New()
	for _, t := range tables {
		cols, err := tableColumns(db, t)
		if err != nil {
			return "", err
		}
		var sel []string
		for _, c := range cols {
			switch {
			case t == "users" && c == "token_hash":
				continue
			case t == "media" && c == "data":
				sel = append(sel, `length(data)`)
			default:
				sel = append(sel, `"`+c+`"`)
			}
		}
		fmt.Fprintf(h, "table %s\n", t)
		rs, err := db.Query(fmt.Sprintf(`SELECT %s FROM "%s" ORDER BY rowid`, strings.Join(sel, ","), t))
		if err != nil {
			return "", err
		}
		vals := make([]any, len(sel))
		ptrs := make([]any, len(sel))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rs.Next() {
			if err := rs.Scan(ptrs...); err != nil {
				rs.Close()
				return "", err
			}
			for _, v := range vals {
				switch x := v.(type) {
				case []byte:
					fmt.Fprintf(h, "b%d:", len(x))
					h.Write(x)
				case string:
					fmt.Fprintf(h, "s%d:%s", len(x), x)
				default:
					fmt.Fprintf(h, "%T:%v", v, v)
				}
				h.Write([]byte{0})
			}
			h.Write([]byte{'\n'})
		}
		if err := rs.Err(); err != nil {
			rs.Close()
			return "", err
		}
		rs.Close()
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func tableColumns(db querier, table string) ([]string, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info("%s")`, table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

// WhatIsHere lists what an account holds, for the new server to tell a user why their account is not empty. It
// is empty for an account nobody has used.
func (s *Space) WhatIsHere() []string {
	var out []string
	for _, c := range []struct{ table, what string }{
		{"posts", "posts"}, {"replies", "replies"}, {"likes", "likes"}, {"follows", "followed feeds"},
		{"media", "pictures"}, {"likes_in", "likes from others"},
	} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + c.table).Scan(&n); err == nil && n > 0 {
			out = append(out, fmt.Sprintf("%d %s", n, c.what))
		}
	}
	return out
}

// ReplaceSpace puts the staged file in place of the user's file: the account that was made on this server for the
// move becomes the copy. The password, the API token, the e-mail address and the sessions of the account that is
// replaced carry over, so that the user stays signed in with the password they chose here. The old file is
// kept until the new one is in place and opens.
func (in *Instance) ReplaceSpace(name, staged string) error {
	if _, err := checkImported(staged, name); err != nil {
		return err
	}
	type login struct {
		tokenHash, email string
		salt, hash       []byte
		must             int
	}
	type session struct {
		hash string
		exp  int64
	}
	var lg login
	var sessions []session
	sp, release, err := in.Space(name)
	if err != nil {
		return err
	}
	err = sp.db.QueryRow(`SELECT token_hash,email,pw_salt,pw_hash,must_change FROM users WHERE name=?`, name).Scan(&lg.tokenHash, &lg.email, &lg.salt, &lg.hash, &lg.must)
	if err == nil {
		var rows *sql.Rows
		if rows, err = sp.db.Query(`SELECT token_hash,expires FROM sessions`); err == nil {
			for rows.Next() {
				var s session
				if rows.Scan(&s.hash, &s.exp) == nil {
					sessions = append(sessions, s)
				}
			}
			rows.Close()
		}
	}
	release()
	if err != nil {
		return err
	}

	path := in.spacePath(name)
	var forgotten bool
	for i := 0; i < 100 && !forgotten; i++ { // requests that are using the file finish in a moment
		if forgotten = in.pool.forget(name); !forgotten {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !forgotten {
		return errors.New("the account is busy; try again")
	}
	backup := path + ".replaced"
	for _, ext := range []string{"-wal", "-shm"} {
		os.Remove(path + ext)
	}
	if err := os.Rename(path, backup); err != nil {
		return err
	}
	undo := func(cause error) error {
		os.Remove(path)
		for _, ext := range []string{"-wal", "-shm"} {
			os.Remove(path + ext)
		}
		if err := os.Rename(backup, path); err != nil {
			return fmt.Errorf("%w (and the old file could not be put back: %v)", cause, err)
		}
		return cause
	}
	if err := os.Rename(staged, path); err != nil {
		return undo(err)
	}
	sp, release, err = in.Space(name) // opens the new file, bringing its schema up to date
	if err != nil {
		in.pool.forget(name)
		return undo(err)
	}
	apply := func() error {
		tx, err := sp.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var id int64
		if err := tx.QueryRow(`SELECT id FROM users WHERE name=?`, name).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE users SET token_hash=?,email=?,pw_salt=?,pw_hash=?,must_change=? WHERE id=?`, lg.tokenHash, lg.email, lg.salt, lg.hash, lg.must, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
			return err
		}
		for _, s := range sessions {
			if _, err := tx.Exec(`INSERT INTO sessions(token_hash,user_id,expires) VALUES(?,?,?)`, s.hash, id, s.exp); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	if err := apply(); err != nil {
		release()
		in.pool.forget(name)
		return undo(err)
	}
	err = in.syncUser(sp)
	release()
	in.dirs.drop()
	os.Remove(backup)
	return err
}
