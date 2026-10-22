package store

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckName(t *testing.T) {
	for _, ok := range []string{"alice", "a", "bob-2", "2fast", "pg", strings.Repeat("a", 31), "a-b-c", "ab-c"} {
		if err := CheckName(ok); err != nil {
			t.Errorf("%q should be fine: %v", ok, err)
		}
	}
	for name, why := range map[string]string{
		"":                      "not a name",
		"Alice":                 "not a name",
		"al ice":                "not a name",
		"al_ice":                "not a name",
		"-alice":                "not a name",
		"alice.example":         "not a name",
		"a.b":                   "not a name",
		".alice":                "not a name",
		"alice.":                "not a name",
		"_alice":                "not a name",
		"alice_":                "not a name",
		"a_b":                   "not a name",
		"alice🏓":                "not a name",
		"🏓":                     "not a name",
		"👩🏽‍💻":                  "not a name",
		"café":                  "not a name",
		"ａlice":                 "not a name", // a full-width letter that looks like an a
		strings.Repeat("a", 32): "not a name",
		"alice-":                "end with a hyphen",
		"xn--bcher-kva":         "third and fourth",
		"ab--cd":                "third and fourth",
		"root":                  "reserved",
		"www":                   "reserved",
		"www-data":              "reserved",
		"admin":                 "reserved",
		"localhost":             "reserved",
		"weft":                  "reserved",
		"metrics":               "reserved",
	} {
		err := CheckName(name)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), why) {
			t.Errorf("%q: want an ErrInvalid saying %q, got %v", name, why, err)
		}
	}
}

func TestNewUserNamesAreChecked(t *testing.T) {
	in, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close() })
	for _, n := range []string{"root", "alice-", "ab--c", "Bob", "a_b", "a.b", "alice🏓"} {
		if _, err := in.AddUser(n, "", ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("AddUser(%q) = %v, want ErrInvalid", n, err)
		}
		if in.UserExists(n) {
			t.Errorf("%q left a directory row behind", n)
		}
	}
	if _, err := in.AddUser("alice", "", ""); err != nil {
		t.Fatal(err)
	}
}
