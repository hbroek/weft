package store

import (
	"strings"
	"testing"
	"time"
)

func TestSignupFailureLog(t *testing.T) {
	pbkdf2Iterations = 1
	in, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()

	in.LogSignupFailure("bob", "weft.bob.example", FailRefused, "that name is taken")
	in.LogSignupFailure("eve", "weft.eve.example", FailNoRecord, "the record was not there")
	got, err := in.SignupFailures(10)
	if err != nil || len(got) != 2 || got[0].Name != "eve" || got[1].Kind != FailRefused || got[1].Domain != "weft.bob.example" {
		t.Fatalf("log = %+v %v", got, err)
	}

	// a sign-up that runs out of time is logged as lapsed when the next sign-up starts
	sg, err := in.StartSignup("carol", "weft.carol.example", "carol@example.com")
	if err != nil {
		t.Fatal(err)
	}
	in.db.Exec(`UPDATE signups SET created=? WHERE id=?`, time.Now().Add(-2*SignupTTL).Unix(), sg.ID)
	if _, err := in.StartSignup("dave", "weft.dave.example", "dave@example.com"); err != nil {
		t.Fatal(err)
	}
	got, _ = in.SignupFailures(10)
	if len(got) != 3 || got[0].Kind != FailLapsed || got[0].Name != "carol" {
		t.Fatalf("after a lapse: %+v", got)
	}
	for _, f := range got {
		if strings.Contains(f.Detail+f.Name+f.Domain, "@") {
			t.Errorf("the log must not hold an email address: %+v", f)
		}
	}

	// it keeps the last MaxSignupFailures, and drops what is older than SignupFailureDays
	for i := 0; i < MaxSignupFailures+50; i++ {
		in.LogSignupFailure("x", "weft.x.example", FailRefused, "again")
	}
	var n int
	in.rdb.QueryRow(`SELECT COUNT(*) FROM signup_failures`).Scan(&n)
	if n != MaxSignupFailures {
		t.Errorf("%d entries kept, want %d", n, MaxSignupFailures)
	}
	in.db.Exec(`UPDATE signup_failures SET at=?`, time.Now().Add(-(SignupFailureDays+1)*24*time.Hour).Unix())
	in.LogSignupFailure("y", "weft.y.example", FailRefused, "new")
	if got, _ := in.SignupFailures(10); len(got) != 1 || got[0].Name != "y" {
		t.Errorf("old entries are dropped: %+v", got)
	}
}

func TestCapSetting(t *testing.T) {
	in, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if limit, _ := in.Cap(); limit != 0 {
		t.Errorf("no server has run: cap %d, want 0", limit)
	}
	in.AddUser("alice", "", "")
	in.AddUser("op", "", "")
	in.SetAdmin("op", true)
	in.RememberCap(5)
	if limit, counted := in.Cap(); limit != 5 || counted != 1 {
		t.Errorf("cap %d, counted %d: want 5 and 1 (the administrator is exempt)", limit, counted)
	}
	in.RememberCap(0)
	if limit, _ := in.Cap(); limit != 0 {
		t.Errorf("cap removed: %d", limit)
	}
}
