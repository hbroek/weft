package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanDomain(t *testing.T) {
	for in, want := range map[string]string{
		"weft.example.com":   "weft.example.com",
		" Weft.Example.COM.": "weft.example.com",
		"a-b.example.co.uk":  "a-b.example.co.uk",
	} {
		if got, err := CleanDomain(in); err != nil || got != want {
			t.Errorf("CleanDomain(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "localhost", "example", "-a.example.com", "a-.example.com", "a..example.com", "a b.example.com",
		"192.168.0.1", "example.com/path", "http://example.com", "exa_mple.com", strings.Repeat("a", 64) + ".com", "ünï.example.com"} {
		if got, err := CleanDomain(in); err == nil {
			t.Errorf("CleanDomain(%q) = %q, want an error", in, got)
		}
	}
}

func newInst(t *testing.T, users ...string) *Instance {
	t.Helper()
	in, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close() })
	for _, u := range users {
		if _, err := in.AddUser(u, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	return in
}

func TestDomainLifecycle(t *testing.T) {
	in := newInst(t, "alice", "bob")
	if e := in.Dir("alice"); e.Status != StatusActive || e.Domain != "" {
		t.Fatalf("a plain user has %+v", e)
	}
	d, err := in.SetDomain("alice", "weft.alice.com")
	if err != nil || d.Verified || len(d.Token) < 32 {
		t.Fatalf("SetDomain = %+v, %v", d, err)
	}
	if _, ok := in.HostUser("weft.alice.com"); ok {
		t.Fatal("an unverified domain serves its claimant")
	}
	// Bob may claim it too, but cannot beat Alice's verification
	if _, err := in.SetDomain("bob", "weft.alice.com"); err != nil {
		t.Fatalf("a second claim: %v", err)
	}
	if err := in.VerifyDomain("alice"); err != nil {
		t.Fatal(err)
	}
	if n, ok := in.HostUser("weft.alice.com"); !ok || n != "alice" {
		t.Fatalf("HostUser = %q, %v", n, ok)
	}
	// verified is not yet the address: the domain serves the user, and nothing redirects
	if e := in.Dir("alice"); e.Domain != "" || e.Status != StatusActive {
		t.Fatalf("a verified domain that is not active is already the address: %+v", e)
	}
	if err := in.ActivateDomain("bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("activating an unverified domain: %v", err)
	}
	if err := in.ActivateDomain("alice"); err != nil {
		t.Fatal(err)
	}
	if e := in.Dir("alice"); e.Domain != "weft.alice.com" || e.Status != StatusActive {
		t.Fatalf("Dir = %+v", e)
	}
	if d, err := in.DomainOf("alice"); err != nil || !d.Verified || !d.Active {
		t.Fatalf("DomainOf = %+v, %v", d, err)
	}
	if err := in.VerifyDomain("bob"); !errors.Is(err, ErrDomainTaken) {
		t.Fatalf("Bob verified a verified domain: %v", err)
	}
	if _, err := in.SetDomain("bob", "weft.alice.com"); !errors.Is(err, ErrDomainTaken) {
		t.Fatalf("a claim on a verified domain: %v", err)
	}
	if _, err := in.SetDomain("nobody", "x.example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a claim for no user: %v", err)
	}
	// a new domain replaces the old and is unverified until shown
	if _, err := in.SetDomain("alice", "other.alice.com"); err != nil {
		t.Fatal(err)
	}
	if _, ok := in.HostUser("weft.alice.com"); ok {
		t.Fatal("the replaced domain still serves")
	}
	if err := in.RemoveDomain("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := in.DomainOf("alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DomainOf after removal: %v", err)
	}
}

func TestMoveAndTrash(t *testing.T) {
	in := newInst(t, "alice", "bob")
	sp, release, _ := in.Space("alice")
	if err := sp.Follow(sp.UID(), "http://x.example/feed.xml"); err != nil {
		t.Fatal(err)
	}
	release()
	if err := in.MarkMoved("alice", "not a url"); !errors.Is(err, ErrBadAddress) {
		t.Fatalf("a bad address: %v", err)
	}
	if err := in.MarkMoved("nobody", "https://x.example"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown user: %v", err)
	}
	if err := in.MarkMoved("alice", "https://Alice.Example.org/path?x"); err != nil {
		t.Fatal(err)
	}
	if e := in.Dir("alice"); e.Status != StatusMoved || e.MovedTo != "https://alice.example.org" {
		t.Fatalf("Dir = %+v", e)
	}

	// a file in use waits for the next call
	sp, release, _ = in.Space("alice")
	now := time.Now()
	if n, err := in.TrashMoved(now); n != 0 || err != nil {
		t.Fatalf("trashed a file in use: %d %v", n, err)
	}
	release()
	if n, err := in.TrashMoved(now); n != 1 || err != nil {
		t.Fatalf("TrashMoved = %d, %v", n, err)
	}
	if n, _ := in.TrashMoved(now); n != 0 {
		t.Fatalf("trashed %d twice", n)
	}
	if _, err := os.Stat(in.spacePath("alice")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the file is still there: %v", err)
	}
	if len(in.trashed("alice")) != 1 {
		t.Fatalf("trash holds %v", in.trashed("alice"))
	}
	if _, _, err := in.Space("alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Space of a moved user: %v", err)
	}
	if _, err := in.AddUser("alice", "", ""); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("the name of a moved user: %v", err)
	}
	if in.Followed("http://x.example/feed.xml") {
		t.Error("a moved user still counts as a follower")
	}
	if names, _ := in.UserNames(); len(names) != 1 || names[0] != "bob" {
		t.Errorf("UserNames = %v", names)
	}
	if err := in.Reindex(); err != nil {
		t.Errorf("Reindex with a moved user: %v", err)
	}
	if !in.UserExists("alice") || !in.UserExists("bob") || in.UserExists("carol") {
		t.Error("UserExists")
	}

	// undoing the move brings the file and the directory rows back
	if err := in.Unmove("alice"); err != nil {
		t.Fatal(err)
	}
	if e := in.Dir("alice"); e.Status != StatusActive || e.MovedTo != "" {
		t.Fatalf("after Unmove: %+v", e)
	}
	if !in.Followed("http://x.example/feed.xml") {
		t.Error("the follow did not come back")
	}
	if _, release, err := in.Space("alice"); err != nil {
		t.Fatalf("Space after Unmove: %v", err)
	} else {
		release()
	}
	if err := in.Unmove("alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Unmove of a user who has not moved: %v", err)
	}

	// once the trash is emptied the move cannot be undone; it is emptied after TrashDays, not before
	in.MarkMoved("alice", "https://alice.example.org")
	in.TrashMoved(now)
	if n, _ := in.PurgeTrash(now.Add(time.Duration(TrashDays-1) * 24 * time.Hour)); n != 0 {
		t.Fatalf("purged %d early", n)
	}
	if n, _ := in.PurgeTrash(now.Add(time.Duration(TrashDays+1) * 24 * time.Hour)); n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	if err := in.Unmove("alice"); err == nil {
		t.Fatal("Unmove with no file succeeded")
	}
	if e := in.Dir("alice"); e.Status != StatusMoved {
		t.Fatalf("a failed Unmove changed the status: %+v", e)
	}
}

// An instance.db at schema 1 gains the domains table without losing rows.
func TestInstanceUpgradesFromV1(t *testing.T) {
	dir := t.TempDir()
	db := openRaw(t, filepath.Join(dir, "instance.db"))
	if err := runSteps(db, instanceMigrations[:1], 0); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users(name,created) VALUES('alice',1)`,
		`INSERT INTO items(feed_url,guid,link,published) VALUES('http://f/feed.xml','http://f/p1','http://f/p1',5)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	in, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	var items, domains int
	if err := in.db.QueryRow(`SELECT (SELECT COUNT(*) FROM items), (SELECT COUNT(*) FROM domains)`).Scan(&items, &domains); err != nil || items != 1 || domains != 0 {
		t.Fatalf("after the upgrade: items=%d domains=%d err=%v", items, domains, err)
	}
	if _, err := in.SetDomain("alice", "a.example.com"); err != nil {
		t.Fatal(err)
	}
}

// A domain verified before domains could be inactive was already the address, so the migration makes it active;
// one that was not verified stays so.
func TestDomainsActiveMigration(t *testing.T) {
	dir := t.TempDir()
	in, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"alice", "bob"} {
		if _, err := in.AddUser(n, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := in.SetDomain("alice", "a.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := in.SetDomain("bob", "b.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := in.VerifyDomain("alice"); err != nil {
		t.Fatal(err)
	}
	in.Close()

	// back to the schema before the change
	raw := openRaw(t, filepath.Join(dir, "instance.db"))
	for _, q := range []string{`ALTER TABLE domains DROP COLUMN active`, `ALTER TABLE feeds DROP COLUMN etag`, `ALTER TABLE feeds DROP COLUMN last_modified`,
		`ALTER TABLE feeds DROP COLUMN fails`, `ALTER TABLE feeds DROP COLUMN next_try`, `ALTER TABLE items DROP COLUMN canonical`, `ALTER TABLE items DROP COLUMN likes_href`, `ALTER TABLE users DROP COLUMN admin`, `ALTER TABLE users DROP COLUMN move_state`, `DROP TABLE moves`, `DROP TABLE held`, `DROP TABLE waitlist`, `DROP TABLE signups`, `PRAGMA user_version=2`} {
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
	if d, err := in.DomainOf("alice"); err != nil || !d.Verified || !d.Active {
		t.Errorf("a verified domain after the migration: %+v, %v", d, err)
	}
	if d, err := in.DomainOf("bob"); err != nil || d.Verified || d.Active {
		t.Errorf("an unverified domain after the migration: %+v, %v", d, err)
	}
	if e := in.Dir("alice"); e.Domain != "a.example.com" {
		t.Errorf("Dir = %+v", e)
	}
}

func TestSignupDomain(t *testing.T) {
	for _, c := range []struct {
		in, domain, registered string // domain "" when refused
	}{
		{"weft.example.com", "weft.example.com", "example.com"},
		{"WEFT.Example.com.", "weft.example.com", "example.com"},
		{"weft.example.co.uk", "weft.example.co.uk", "example.co.uk"},
		{"weft.example.com.au", "weft.example.com.au", "example.com.au"},
		{"weft.blog.example.com", "", ""},   // further down than the registered domain
		{"weft.shop.example.co.uk", "", ""}, // the same under a two-part suffix
		{"www.example.com", "", ""},         // another first label
		{"example.com", "", ""},             // no weft.
		{"weft.co.uk", "", ""},              // a registry's name
		{"weft.com", "", ""},                // a top-level name
		{"weft", "", ""},                    // not a domain
		{"weft.192.168.0.1", "", ""},        // an address
		{"weft.exa mple.com", "", ""},       // not a name
	} {
		d, reg, err := SignupDomain(c.in)
		if c.domain == "" {
			if !errors.Is(err, ErrSignupDomain) {
				t.Errorf("SignupDomain(%q) = %q, %q, %v; want refused", c.in, d, reg, err)
			}
			continue
		}
		if err != nil || d != c.domain || reg != c.registered {
			t.Errorf("SignupDomain(%q) = %q, %q, %v; want %q, %q", c.in, d, reg, err, c.domain, c.registered)
		}
	}
}
