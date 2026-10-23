package store

import (
	"errors"
	"path/filepath"
	"regexp"
	"testing"
)

func TestTemporaryPassword(t *testing.T) {
	pbkdf2Iterations = 1
	in, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if _, err := in.AddUser("alice", "", ""); err != nil {
		t.Fatal(err)
	}
	sp, release, err := in.Space("alice")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if sp.MustChangePassword() {
		t.Fatal("a new account must not be marked before it has a password")
	}
	if err := sp.SetTemporaryPassword("short"); err == nil {
		t.Fatal("a short temporary password was accepted")
	}
	if err := sp.SetTemporaryPassword("temporary one"); err != nil {
		t.Fatal(err)
	}
	if !sp.MustChangePassword() {
		t.Fatal("the temporary password is not marked")
	}
	if _, err := sp.CheckPassword("temporary one"); err != nil {
		t.Fatalf("the temporary password does not sign in: %v", err)
	}

	keep, _ := sp.CreateSession(sp.UID())
	other, _ := sp.CreateSession(sp.UID())
	for _, c := range []struct {
		cur, next string
		want      error
	}{
		{"wrong password", "a brand new one", ErrBadLogin},
		{"temporary one", "short", ErrWeakPassword},
		{"temporary one", "temporary one", ErrSamePassword},
	} {
		if err := sp.ChangePassword(c.cur, c.next, keep); !errors.Is(err, c.want) {
			t.Errorf("ChangePassword(%q, %q) = %v, want %v", c.cur, c.next, err, c.want)
		}
		if !sp.MustChangePassword() {
			t.Errorf("a refused change cleared the mark")
		}
	}
	if err := sp.ChangePassword("temporary one", "a brand new one", keep); err != nil {
		t.Fatal(err)
	}
	if sp.MustChangePassword() {
		t.Error("the new password is still marked temporary")
	}
	if _, err := sp.CheckPassword("temporary one"); !errors.Is(err, ErrBadLogin) {
		t.Errorf("the temporary password still works: %v", err)
	}
	if _, err := sp.CheckPassword("a brand new one"); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
	if _, err := sp.UserBySession(keep); err != nil {
		t.Errorf("the session that changed the password ended: %v", err)
	}
	if _, err := sp.UserBySession(other); err == nil {
		t.Errorf("another session survived the change")
	}
	// setting a password for good clears the mark too
	sp.SetTemporaryPassword("temporary two")
	if err := sp.SetPassword("permanent one"); err != nil || sp.MustChangePassword() {
		t.Errorf("SetPassword after a temporary one: %v, marked %v", err, sp.MustChangePassword())
	}
}

// A file made before the flag existed gets the column, off.
func TestMustChangeMigration(t *testing.T) {
	dir := t.TempDir()
	in, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	in.AddUser("alice", "", "")
	in.Close()
	raw := openRaw(t, filepath.Join(dir, spaceRel("alice")))
	for _, q := range []string{`ALTER TABLE users DROP COLUMN must_change`, `PRAGMA user_version=1`} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	raw.Close()
	in, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	sp, release, err := in.Space("alice")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if sp.MustChangePassword() {
		t.Error("an old account is marked")
	}
	if err := sp.SetTemporaryPassword("temporary one"); err != nil || !sp.MustChangePassword() {
		t.Errorf("the migrated file cannot hold the mark: %v", err)
	}
}

func TestGeneratePassword(t *testing.T) {
	shape := regexp.MustCompile(`^[bcdfghjkmnpqrstvwxz2-9]{5}-[bcdfghjkmnpqrstvwxz2-9]{5}$`)
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		p := GeneratePassword()
		if !shape.MatchString(p) || len(p) < MinPassword || seen[p] {
			t.Fatalf("password %d: %q", i, p)
		}
		seen[p] = true
	}
}
