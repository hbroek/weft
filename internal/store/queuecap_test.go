package store

import (
	"errors"
	"strconv"
	"testing"
)

func TestSourceOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.org/a/b?c#d": "https://example.org/",
		"https://example.org":         "https://example.org/",
		"http://example.org:8080/x":   "http://example.org:8080/",
		"https://example.org?x=1":     "https://example.org/",
	} {
		if got := sourceOrigin(in); got != want {
			t.Errorf("sourceOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnqueueCapped(t *testing.T) {
	_, alice, bob := twoUsers(t)
	add := func(sp *Space, source string, total, perHost int) error {
		return sp.EnqueueCapped(QueueIn, sp.UID(), source, "http://alice.example/posts/p", total, perHost)
	}
	for i := 0; i < 3; i++ {
		if err := add(alice, "https://spam.example/"+strconv.Itoa(i), 10, 3); err != nil {
			t.Fatal(err)
		}
	}
	if err := add(alice, "https://spam.example/new", 10, 3); !errors.Is(err, ErrQueueFull) {
		t.Errorf("past the per-host cap: %v", err)
	}
	if err := add(alice, "https://spam.example/1", 10, 3); err != nil {
		t.Errorf("a job already waiting was refused: %v", err)
	}
	if err := add(alice, "https://spam.example.evil/1", 10, 3); err != nil {
		t.Errorf("a host that merely starts the same was counted: %v", err)
	}
	if err := add(alice, "https://other.example/1", 10, 3); err != nil {
		t.Errorf("another host: %v", err)
	}
	if err := add(bob, "https://spam.example/new", 10, 3); err != nil { // the caps are per user
		t.Errorf("another user's inbox: %v", err)
	}
	// the total: 5 waiting so far for alice; two more fit under 7, then no more
	if err := add(alice, "https://a.example/1", 7, 3); err != nil {
		t.Fatal(err)
	}
	if err := add(alice, "https://b.example/1", 7, 3); err != nil {
		t.Fatal(err)
	}
	if err := add(alice, "https://c.example/1", 7, 3); !errors.Is(err, ErrQueueFull) {
		t.Errorf("past the total cap: %v", err)
	}
	// finished jobs do not count, and outgoing jobs are not capped
	if err := alice.Enqueue(QueueOut, alice.UID(), "https://x.example/1", "https://y.example/1"); err != nil {
		t.Errorf("an outgoing job: %v", err)
	}
	jobs, err := alice.DueQueue(QueueIn, 100)
	if err != nil || len(jobs) == 0 {
		t.Fatalf("due: %v %d", err, len(jobs))
	}
	for _, j := range jobs {
		if err := alice.FinishQueue(j.ID, "done"); err != nil {
			t.Fatal(err)
		}
	}
	if err := add(alice, "https://c.example/1", 7, 3); err != nil {
		t.Errorf("after the queue was worked off: %v", err)
	}
}
