package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// A backup is a folder that mirrors the data folder: instance.db and users/ab/name.db, every file a
// consistent snapshot made with VACUUM INTO, so it can be copied (rsync) while the server runs. The live
// files are never copied directly. backup.json records, per user, how recent the live file was when its
// snapshot was taken, so the next backup copies only the users who changed.

const backupState = "backup.json"

type backupFile struct {
	Taken time.Time        `json:"taken"`
	Users map[string]int64 `json:"users"` // name -> modification time (ns) of the live file when it was copied
}

// BackupReport says what a backup did.
type BackupReport struct {
	Users, Copied, Unchanged int
	Bytes                    int64
}

// modTime is when a user's live files were last written: the database or its write-ahead log, whichever
// is newer, since a write lands in the log and reaches the database only at a checkpoint. An empty log
// is ignored: merely opening a database creates one, and that is not a change.
func modTime(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	t := fi.ModTime().UnixNano()
	if w, err := os.Stat(path + "-wal"); err == nil && w.Size() > 0 && w.ModTime().UnixNano() > t {
		t = w.ModTime().UnixNano()
	}
	return t, nil
}

// vacuumTo writes a snapshot atomically: into a temporary name, then renamed over the old copy, so a
// reader of the backup folder never sees half a file.
func vacuumTo(exec func(q string, args ...any) error, dest string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, err
	}
	tmp := dest + ".partial"
	os.Remove(tmp)
	if err := exec(`VACUUM INTO ?`, tmp); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	fi, err := os.Stat(tmp)
	if err != nil {
		return 0, err
	}
	return fi.Size(), os.Rename(tmp, dest)
}

// Backup snapshots instance.db and each user's file into dest. A user whose file has not been written
// since their last snapshot there is skipped, unless full is set.
func (in *Instance) Backup(dest string, full bool) (*BackupReport, error) {
	if abs, _ := filepath.Abs(dest); abs == mustAbs(in.dir) {
		return nil, errors.New("the backup folder must not be the data folder")
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	state := backupFile{Users: map[string]int64{}}
	if b, err := os.ReadFile(filepath.Join(dest, backupState)); err == nil {
		if err := json.Unmarshal(b, &state); err != nil {
			return nil, fmt.Errorf("%s: %w", backupState, err)
		}
		if state.Users == nil {
			state.Users = map[string]int64{}
		}
	}
	rep := &BackupReport{}
	// the directory first: it names the users, and one created after this point is simply not in this backup
	names, err := in.UserNames()
	if err != nil {
		return nil, err
	}
	n, err := vacuumTo(func(q string, a ...any) error { _, err := in.db.Exec(q, a...); return err }, filepath.Join(dest, "instance.db"))
	if err != nil {
		return nil, fmt.Errorf("instance.db: %w", err)
	}
	rep.Bytes += n
	next := backupFile{Users: map[string]int64{}}
	for _, name := range names {
		rep.Users++
		src := in.spacePath(name)
		mt, err := modTime(src)
		if err != nil {
			return rep, fmt.Errorf("%s: %w", name, err)
		}
		copyTo := filepath.Join(dest, spaceRel(name))
		if _, err := os.Stat(copyTo); err == nil && !full && state.Users[name] == mt {
			next.Users[name] = mt
			rep.Unchanged++
			continue
		}
		sp, release, err := in.Space(name)
		if err != nil {
			return rep, fmt.Errorf("%s: %w", name, err)
		}
		// the time is read before the snapshot: a write during it makes the file look changed next time
		n, err := vacuumTo(func(q string, a ...any) error { _, err := sp.db.Exec(q, a...); return err }, copyTo)
		release()
		if err != nil {
			return rep, fmt.Errorf("%s: %w", name, err)
		}
		next.Users[name] = mt
		rep.Copied++
		rep.Bytes += n
	}
	next.Taken = time.Now().UTC().Truncate(time.Second)
	b, _ := json.MarshalIndent(next, "", "  ")
	tmp := filepath.Join(dest, backupState+".partial")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return rep, err
	}
	return rep, os.Rename(tmp, filepath.Join(dest, backupState))
}

func mustAbs(p string) string { a, _ := filepath.Abs(p); return a }

// RestoreReport says what a restore did.
type RestoreReport struct {
	Users int
	Bytes int64
}

// Restore builds a data folder from a backup folder. dataDir must not exist or must be empty. The files
// are copied, opened (which brings older ones up to date), checked, and the directory is rebuilt from
// them. It is all or nothing: on an error dataDir is left for the operator to delete.
func Restore(from, dataDir string) (*RestoreReport, error) {
	if es, err := os.ReadDir(dataDir); err == nil && len(es) > 0 {
		return nil, fmt.Errorf("%s is not empty", dataDir)
	}
	if _, err := os.Stat(filepath.Join(from, "instance.db")); err != nil {
		return nil, fmt.Errorf("%s is not a backup: %w", from, err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	rep := &RestoreReport{}
	n, err := copyFile(filepath.Join(from, "instance.db"), filepath.Join(dataDir, "instance.db"))
	if err != nil {
		return nil, err
	}
	rep.Bytes += n
	in, err := Open(dataDir)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	names, err := in.UserNames()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		n, err := copyFile(filepath.Join(from, spaceRel(name)), in.spacePath(name))
		if err != nil {
			return rep, fmt.Errorf("user %s: %w", name, err)
		}
		rep.Bytes += n
		sp, release, err := in.Space(name)
		if err != nil {
			return rep, fmt.Errorf("user %s: %w", name, err)
		}
		var res string
		err = sp.db.QueryRow(`PRAGMA quick_check`).Scan(&res)
		release()
		if err != nil || res != "ok" {
			return rep, fmt.Errorf("user %s is damaged in the backup: %s %v", name, res, err)
		}
		rep.Users++
	}
	return rep, in.Reindex()
}

// copyFile copies src to a new file dst, creating its folder. It never overwrites.
func copyFile(src, dst string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return n, err
}
