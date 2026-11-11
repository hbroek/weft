package store

import (
	"path/filepath"
	"testing"
)

// A single-host server makes its one account and gives it the host as address, with no DNS proof.
func TestSetUpHost(t *testing.T) {
	pbkdf2Iterations = 1
	in, err := Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close() })

	pw, err := in.SetUpHost("me", "Weft.Example.com")
	if err != nil || pw == "" {
		t.Fatalf("first start: %q %v", pw, err)
	}
	if d, err := in.DomainOf("me"); err != nil || d.Domain != "weft.example.com" || !d.Verified || !d.Active {
		t.Fatalf("domain = %+v %v", d, err)
	}
	if name, ok := in.HostUser("weft.example.com"); !ok || name != "me" {
		t.Errorf("HostUser = %q %v", name, ok)
	}
	sp, release, err := in.Space("me")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := sp.CheckPassword(pw); err != nil {
		t.Errorf("the temporary password does not work: %v", err)
	}

	// the next start changes nothing and makes no new password
	if pw2, err := in.SetUpHost("me", "weft.example.com"); err != nil || pw2 != "" {
		t.Errorf("second start: %q %v", pw2, err)
	}
	// nor is the check that takes an unreachable address back ever asked about it: it is not bound to the domain
	if in.IsBound("me") {
		t.Error("a pinned domain is not an account's own sign-up domain")
	}
}
