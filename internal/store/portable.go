package store

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

// ExportFormat is the version of the export file: a zip holding manifest.json and user.db.
const ExportFormat = 1

// MaxImportBytes bounds how much an import unpacks, so that a hostile zip cannot fill the disk.
const MaxImportBytes = 8 << 30

// Manifest describes an export. It is the first thing an import reads.
type Manifest struct {
	Format   int       `json:"format"`
	Weft     string    `json:"weft"`   // the Weft that wrote it
	Schema   int       `json:"schema"` // the user file's schema version
	Name     string    `json:"name"`
	Domain   string    `json:"domain"` // the base domain the user was served under, if known
	Exported time.Time `json:"exported"`
}

func weftVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "devel"
}

// Export writes the user's whole account as a zip: a manifest and one consistent copy of their database.
// The copy has no sessions and no API token, so that an export is safe to keep and send; the password hash
// stays, so the user can log in where they import it. domain is the base domain they are served under,
// recorded in the manifest. Nothing is written to w until the copy is ready.
func (s *Space) Export(w io.Writer, domain string) error {
	_, _, err := s.export(w, domain)
	return err
}

// export is Export, and also returns the fingerprint of the copy it made and its schema version.
func (s *Space) export(w io.Writer, domain string) (fp string, schema int, err error) {
	tmp, err := os.MkdirTemp(s.in.dir, ".export-")
	if err != nil {
		return "", 0, err
	}
	defer os.RemoveAll(tmp)
	first, final := filepath.Join(tmp, "first.db"), filepath.Join(tmp, "user.db")
	if _, err := s.db.Exec(`VACUUM INTO ?`, first); err != nil {
		return "", 0, fmt.Errorf("snapshot: %w", err)
	}
	// scrub a copy, then VACUUM INTO another so that what was deleted is not left in free pages
	db, err := sql.Open("sqlite", first)
	if err != nil {
		return "", 0, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{`DELETE FROM sessions`, `UPDATE users SET token_hash=''`} {
		if _, err := db.Exec(q); err != nil && !missing(err) { // a file with no sessions table has none to scrub
			db.Close()
			return "", 0, err
		}
	}
	db.QueryRow(`PRAGMA user_version`).Scan(&schema)
	_, err = db.Exec(`VACUUM INTO ?`, final)
	db.Close()
	if err != nil {
		return "", 0, fmt.Errorf("scrubbed copy: %w", err)
	}
	if fp, err = fingerprintFile(final); err != nil {
		return "", 0, err
	}

	zw := zip.NewWriter(w)
	m, err := zw.Create("manifest.json")
	if err != nil {
		return "", 0, err
	}
	enc := json.NewEncoder(m)
	enc.SetIndent("", "  ")
	if err := enc.Encode(Manifest{Format: ExportFormat, Weft: weftVersion(), Schema: schema, Name: s.name, Domain: domain, Exported: time.Now().UTC().Truncate(time.Second)}); err != nil {
		return "", 0, err
	}
	f, err := os.Open(final)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	u, err := zw.CreateHeader(&zip.FileHeader{Name: "user.db", Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return "", 0, err
	}
	if _, err := io.Copy(u, f); err != nil {
		return "", 0, err
	}
	return fp, schema, zw.Close()
}

// Staged is an export that was unpacked and checked: the user's database as a file of its own, in a folder of
// the data folder, waiting to become an account. Close removes what is left of it.
type Staged struct {
	Path       string   // the unpacked user.db
	Manifest   Manifest // what the export says about itself
	Created    int64    // when the account was created
	MediaBytes int64    // how much the pictures in it take
	dir        string
}

// Close removes the staged file, unless it was moved away.
func (st *Staged) Close() { os.RemoveAll(st.dir) }

// StageExport unpacks an export file and checks it: the format, a schema no newer than this Weft knows, a
// database that is intact and holds exactly the one account the manifest names. Nothing in the data folder
// is changed.
func (in *Instance) StageExport(zipPath string) (*Staged, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("not an export file: %w", err)
	}
	defer zr.Close()
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	mf, db := files["manifest.json"], files["user.db"]
	if mf == nil || db == nil {
		return nil, errors.New("not an export file: manifest.json and user.db are required")
	}
	if mf.UncompressedSize64 > 1<<16 {
		return nil, errors.New("manifest.json is too large")
	}
	r, err := mf.Open()
	if err != nil {
		return nil, err
	}
	var m Manifest
	err = json.NewDecoder(io.LimitReader(r, 1<<16)).Decode(&m)
	r.Close()
	if err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	if m.Format != ExportFormat {
		return nil, fmt.Errorf("export format %d is not one this Weft can read (it reads %d)", m.Format, ExportFormat)
	}
	if err := CheckName(m.Name); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	if m.Schema > SpaceSchemaVersion {
		return nil, fmt.Errorf("the export has schema version %d, but this Weft only knows up to %d: upgrade Weft", m.Schema, SpaceSchemaVersion)
	}

	tmp, err := os.MkdirTemp(in.dir, ".import-")
	if err != nil {
		return nil, err
	}
	st := &Staged{Path: filepath.Join(tmp, "user.db"), Manifest: m, dir: tmp}
	fail := func(err error) (*Staged, error) {
		st.Close()
		return nil, err
	}
	src, err := db.Open()
	if err != nil {
		return fail(err)
	}
	out, err := os.OpenFile(st.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		src.Close()
		return fail(err)
	}
	n, err := io.Copy(out, io.LimitReader(src, MaxImportBytes+1))
	src.Close()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}
	if n > MaxImportBytes {
		return fail(errors.New("user.db is larger than an import may unpack"))
	}
	if st.Created, err = checkImported(st.Path, m.Name); err != nil {
		return fail(err)
	}
	if mdb, err := sql.Open("sqlite", st.Path); err == nil {
		mdb.SetMaxOpenConns(1)
		mdb.QueryRow(`SELECT COALESCE(SUM(length(data)),0) FROM media`).Scan(&st.MediaBytes) // a file with no pictures has no table yet
		mdb.Close()
	}
	return st, nil
}

// Import creates a user from an export file and returns their name and a new API token (shown once).
// It refuses a name that is taken, a file from a newer Weft, and a file that is damaged or holds
// anything but a user's data. The user's address does not change on import; moving a user whose address
// is on this instance's domain is a decision for the people involved, not something import renames.
func (in *Instance) Import(zipPath string) (name, token string, err error) {
	st, err := in.StageExport(zipPath)
	if err != nil {
		return "", "", err
	}
	defer st.Close()
	m, path, created := st.Manifest, st.Path, st.Created

	// claim the name, then put the file in place; either failing gives the name back
	if _, err := in.db.Exec(`INSERT INTO users(name,created) VALUES(?,?)`, m.Name, created); err != nil {
		if isUnique(err) {
			return "", "", fmt.Errorf("%w: there is already a user %q here", ErrNameTaken, m.Name)
		}
		return "", "", err
	}
	fail := func(err error) (string, string, error) {
		in.db.Exec(`DELETE FROM users WHERE name=?`, m.Name)
		return "", "", err
	}
	dst := in.spacePath(m.Name)
	if _, err := os.Lstat(dst); err == nil {
		return fail(fmt.Errorf("%s already exists; not overwriting it", dst))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fail(err)
	}
	if err := os.Rename(path, dst); err != nil {
		return fail(err)
	}
	sp, release, err := in.Space(m.Name)
	if err != nil {
		os.Remove(dst)
		return fail(err)
	}
	defer release()
	if token, err = sp.ResetToken(); err != nil {
		return m.Name, "", err
	}
	if err := in.syncUser(sp); err != nil {
		return m.Name, token, err
	}
	return m.Name, token, nil
}

func isUnique(err error) bool { return err != nil && strings.Contains(err.Error(), "UNIQUE") }

// checkImported opens an unpacked user.db and makes sure it is one intact user's database before it is
// trusted: no corruption, no schema from the future, no triggers or views that would run code of the
// file's choosing, exactly one account and that account the one the manifest names. It returns when the
// account was created.
func checkImported(path, name string) (created int64, err error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var res string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&res); err != nil || res != "ok" {
		return 0, fmt.Errorf("user.db is damaged: %s %v", res, err)
	}
	var v int
	db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v > SpaceSchemaVersion {
		return 0, fmt.Errorf("user.db has schema version %d, but this Weft only knows up to %d: upgrade Weft", v, SpaceSchemaVersion)
	}
	if v < 1 {
		return 0, errors.New("user.db is not a Weft user database")
	}
	var extra int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type IN ('trigger','view')`).Scan(&extra); err != nil || extra > 0 {
		return 0, fmt.Errorf("user.db holds triggers or views, which a user database never does (%v)", err)
	}
	var users int
	var got string
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(MIN(name),''), COALESCE(MIN(created),0) FROM users`).Scan(&users, &got, &created); err != nil {
		return 0, fmt.Errorf("user.db has no users table: %w", err)
	}
	if users != 1 || got != name {
		return 0, fmt.Errorf("user.db holds %d accounts (%q), want exactly %q", users, got, name)
	}
	if rows, err := db.Query(`PRAGMA foreign_key_check`); err == nil {
		bad := rows.Next()
		rows.Close()
		if bad {
			return 0, errors.New("user.db has rows that refer to rows that do not exist")
		}
	}
	return created, nil
}
