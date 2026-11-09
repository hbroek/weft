package store

import (
	"errors"
	"testing"
)

func TestSignupWithADomain(t *testing.T) {
	pbkdf2Iterations = 1
	in, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	in.AddUser("alice", "", "")

	for _, c := range []struct {
		name, domain string
		want         error
	}{
		{"alice", "weft.example.com", ErrNameTaken},
		{"admin", "weft.example.com", ErrInvalid},
		{"bob", "weft.blog.example.com", ErrSignupDomain},
		{"bob", "example.com", ErrSignupDomain},
	} {
		if _, err := in.StartSignup(c.name, c.domain); !errors.Is(err, c.want) {
			t.Errorf("StartSignup(%q, %q) = %v, want %v", c.name, c.domain, err, c.want)
		}
	}
	sg, err := in.StartSignup("bob", "weft.example.co.uk")
	if err != nil {
		t.Fatal(err)
	}
	// the name and the registered domain are held while the sign-up waits
	if _, err := in.StartSignup("bob", "weft.other.com"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("the same name twice: %v", err)
	}
	if _, err := in.StartSignup("robert", "weft.example.co.uk"); !errors.Is(err, ErrDomainTaken) {
		t.Errorf("the same domain twice: %v", err)
	}
	if got, err := in.SignupByID(sg.ID); err != nil || got.Domain != "weft.example.co.uk" || got.Registered != "example.co.uk" {
		t.Fatalf("SignupByID = %+v, %v", got, err)
	}
	if _, err := in.SignupByID("nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown id: %v", err)
	}

	if err := in.FinishSignup(sg, "short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("a weak password: %v", err)
	}
	if in.UserExists("bob") {
		t.Fatal("a refused sign-up left an account")
	}
	if err := in.FinishSignup(sg, "a good password"); err != nil {
		t.Fatal(err)
	}
	sp, release, err := in.Space("bob")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := sp.CheckPassword("a good password"); err != nil {
		t.Errorf("the password does not work: %v", err)
	}
	if d, err := in.DomainOf("bob"); err != nil || !d.Verified || d.Active || d.Domain != "weft.example.co.uk" || d.Token != sg.Token {
		t.Errorf("DomainOf = %+v, %v", d, err)
	}
	if _, err := in.SignupByID(sg.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a used sign-up is still there: %v", err)
	}
	// the registered domain is now an account's
	if _, err := in.StartSignup("carol", "weft.example.co.uk"); !errors.Is(err, ErrDomainTaken) {
		t.Errorf("a domain with an account: %v", err)
	}
}
