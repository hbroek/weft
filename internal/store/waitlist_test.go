package store

import (
	"errors"
	"testing"
)

func TestWaitlist(t *testing.T) {
	in, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	in.AddUser("alice", "", "")

	if err := in.JoinWaitlist("bob", "bob@example.com", "https://bob.example/blog", ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, contact string
		want          error
	}{
		{"bob", "other@example.com", ErrOnList},
		{"robert", "BOB@example.com", ErrOnList},
		{"alice", "a@example.com", ErrNameTaken},
		{"admin", "x@example.com", ErrInvalid},
	} {
		if err := in.JoinWaitlist(c.name, c.contact, "links", ""); !errors.Is(err, c.want) {
			t.Errorf("JoinWaitlist(%q, %q) = %v, want %v", c.name, c.contact, err, c.want)
		}
	}
	accounts, waiting, err := in.Counts()
	if err != nil || accounts != 1 || waiting != 1 {
		t.Fatalf("Counts = %d, %d, %v", accounts, waiting, err)
	}
	list, _ := in.Waitlist()
	if len(list) != 1 || list[0].Name != "bob" || list[0].Links != "https://bob.example/blog" {
		t.Fatalf("list = %+v", list)
	}
	if err := in.RemoveWaiting(list[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := in.RemoveWaiting(list[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing twice: %v", err)
	}
}
