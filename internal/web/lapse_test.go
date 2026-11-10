package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/store"
)

// boundAccount makes an account the way a sign-up with a domain does, and returns its token.
func boundAccount(t *testing.T, s *Server, name, domain string) *store.Signup {
	t.Helper()
	sg, err := s.st.StartSignup(name, domain, name+"@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.FinishSignup(sg, "a long password"); err != nil {
		t.Fatal(err)
	}
	return sg
}

func TestDomainLapse(t *testing.T) {
	s, _ := setup(t)
	st := s.st
	day := 24 * time.Hour
	t0 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

	bob := boundAccount(t, s, "bob", "weft.bob.example")
	// what the DNS and the domain answer; the test changes them
	var mu sync.Mutex
	dnsOK, reachOK := true, true
	s.cfg.LookupTXT = func(_ context.Context, name string) ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		if name == "_weft.weft.bob.example" && dnsOK {
			return []string{"weft-verify=" + bob.Token}, nil
		}
		return nil, errors.New("no such record")
	}
	s.cfg.ReachDomain = func(_ context.Context, domain string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if reachOK {
			return proofPrefix + bob.Token, nil
		}
		return "", errors.New("unreachable")
	}
	set := func(dns, reach bool) { mu.Lock(); dnsOK, reachOK = dns, reach; mu.Unlock() }
	var sent []string
	s.cfg.Mail = func(_ context.Context, to, subject, body string) error {
		sent = append(sent, to+"|"+subject)
		return nil
	}
	st.SetEmail("bob", "bob@example.com")

	// accounts without a bound domain are not checked, and an administrator's is not checked either
	if due, _ := st.DomainsDue(t0, 10); len(due) != 1 || due[0].Name != "bob" {
		t.Fatalf("due = %v", due)
	}

	// a day with a working domain: nothing to see
	s.checkDomains(context.Background(), t0)
	if st.LapseOf("bob") != nil {
		t.Fatal("a working domain has no lapse")
	}
	if due, _ := st.DomainsDue(t0.Add(time.Hour), 10); len(due) != 0 {
		t.Fatal("a domain is checked once a day")
	}

	// three failed checks on three days start the grace period; the third is the one that counts
	set(false, true) // the record is gone
	s.checkDomains(context.Background(), t0.Add(1*day))
	s.checkDomains(context.Background(), t0.Add(2*day))
	if l := st.LapseOf("bob"); l == nil || l.Fails != 2 || l.Lapsed {
		t.Fatalf("after two: %+v", l)
	}
	if len(sent) != 0 || s.lapseWarning("bob") != "" {
		t.Fatal("two failed checks warn nobody yet")
	}
	s.checkDomains(context.Background(), t0.Add(3*day))
	l := st.LapseOf("bob")
	if l == nil || !l.Lapsed || !l.Since.Equal(t0.Add(3*day)) || !l.Deadline.Equal(t0.Add(3*day+90*day)) {
		t.Fatalf("after three: %+v", l)
	}
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "bob@example.com|Your domain weft.bob.example") {
		t.Fatalf("the warning mail: %v", sent)
	}
	s.checkDomains(context.Background(), t0.Add(4*day))
	if len(sent) != 1 {
		t.Errorf("the warning is sent once: %v", sent)
	}
	if w := s.lapseWarning("bob"); !strings.Contains(w, "weft.bob.example") || !strings.Contains(w, "2027") {
		t.Errorf("the warning on the pages: %q", w)
	}

	// the owner sees it on the home page and the profile page; a visitor does not
	sp, rel, _ := st.Space("bob")
	u, _ := sp.User()
	tok, _ := sp.CreateSession(u.ID)
	rel()
	owner := cookieName + "=" + tok
	page := func(path, cookie string) string {
		r := cookieReq("GET", "http://bob.localhost"+path, cookie)
		w := newRecorder()
		s.ServeHTTP(w, r)
		return w.Body.String()
	}
	if b := page("/profile", owner); !strings.Contains(b, "stopped verifying on") || !strings.Contains(b, "cannot be changed or removed") {
		t.Errorf("the profile page does not warn:\n%s", b)
	}
	if !strings.Contains(page("/", owner), "has not verified since") {
		t.Error("the owner's home page does not warn")
	}
	if strings.Contains(page("/", ""), "has not verified since") {
		t.Error("a visitor must not see the warning")
	}

	// the administrator sees it too
	if list, _ := st.Lapsing(); len(list) != 1 || list[0].Name != "bob" || !list[0].Lapsed {
		t.Errorf("Lapsing = %+v", list)
	}

	// the domain cannot be dropped to escape
	if _, err := st.SetDomain("bob", "weft.other.example"); !errors.Is(err, store.ErrDomainBound) {
		t.Errorf("SetDomain on a bound account: %v", err)
	}
	if err := st.RemoveDomain("bob"); !errors.Is(err, store.ErrDomainBound) {
		t.Errorf("RemoveDomain on a bound account: %v", err)
	}

	// a reminder goes out before the end
	s.checkDomains(context.Background(), l.Deadline.Add(-10*day))
	if len(sent) != 2 || !strings.Contains(sent[1], "ends on") {
		t.Errorf("the reminder: %v", sent)
	}

	// the owner fixes it and presses Check now: everything starts over
	set(true, true)
	r := cookieReq("POST", "http://bob.localhost/domain/check", owner)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := newRecorder()
	s.ServeHTTP(w, r)
	if st.LapseOf("bob") != nil {
		t.Errorf("after the owner's check: %+v", st.LapseOf("bob"))
	}

	// and again: it lapses, and this time the end comes
	set(false, false)
	for i := 1; i <= 3; i++ {
		s.checkDomains(context.Background(), t0.Add(100*day).Add(time.Duration(i)*day))
	}
	l = st.LapseOf("bob")
	if l == nil || !l.Lapsed {
		t.Fatalf("lapsed again: %+v", l)
	}
	if got, _ := st.EndLapsed(l.Deadline.Add(-time.Hour)); len(got) != 0 {
		t.Errorf("not before the end: %v", got)
	}
	got, err := st.EndLapsed(l.Deadline)
	if err != nil || len(got) != 1 || got[0] != "bob" {
		t.Fatalf("at the end: %v %v", got, err)
	}
	if c := do(s, "GET", "bob.localhost", "/feed.xml", "", "").Code; c != 410 {
		t.Errorf("an ended account answers %d, want 410", c)
	}
	if _, err := st.AddUser("bob", "", ""); err == nil {
		t.Error("the name stays taken")
	}
}

func TestDomainLapseExemptions(t *testing.T) {
	s, _ := setup(t)
	st := s.st
	boundAccount(t, s, "opa", "weft.opa.example")
	st.SetAdmin("opa", true)
	s.cfg.LookupTXT = func(context.Context, string) ([]string, error) { return nil, errors.New("gone") }
	s.cfg.ReachDomain = func(context.Context, string) (string, error) { return "", errors.New("gone") }
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	if due, _ := st.DomainsDue(now, 10); len(due) != 0 {
		t.Fatalf("an administrator's domain is not checked: %v", due)
	}
	// alice has no domain at all: nothing to check, nothing to end
	if got, _ := st.EndLapsed(now.Add(1000 * 24 * time.Hour)); len(got) != 0 {
		t.Fatalf("ended: %v", got)
	}
}

// If every domain fails together, it is this server's DNS that is down, not the domains.
func TestDomainChecksDoNotBlameDomainsForOurOutage(t *testing.T) {
	s, _ := setup(t)
	st := s.st
	for _, n := range []string{"d1", "d2", "d3", "d4", "d5"} {
		boundAccount(t, s, n, "weft."+n+".example")
	}
	s.cfg.LookupTXT = func(context.Context, string) ([]string, error) { return nil, errors.New("resolver down") }
	s.cfg.ReachDomain = func(context.Context, string) (string, error) { return "", errors.New("network down") }
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.checkDomains(context.Background(), now)
	if list, _ := st.Lapsing(); len(list) != 0 {
		t.Fatalf("an outage counted against the domains: %+v", list)
	}
	if due, _ := st.DomainsDue(now.Add(30*time.Minute), 10); len(due) != 0 {
		t.Error("they are tried again in an hour, not at once")
	}
	if due, _ := st.DomainsDue(now.Add(90*time.Minute), 10); len(due) != 5 {
		t.Errorf("they are tried again within the hour: %d due", len(due))
	}
}

func cookieReq(method, url, cookie string) *http.Request {
	r := httptest.NewRequest(method, url, nil)
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	return r
}

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

// An account that is not tied to its custom handle is never removed for it, but a handle that stops reaching the
// server stops being the account's address after three failed days.
func TestHandleThatStopsReachingTheServerIsGivenUp(t *testing.T) {
	s, _ := setup(t)
	st := s.st
	day := 24 * time.Hour
	t0 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	d, err := st.SetDomain("alice", "weft.alice.example")
	if err != nil {
		t.Fatal(err)
	}
	st.VerifyDomain("alice")
	st.ActivateDomain("alice")
	var mu sync.Mutex
	reach := true
	s.cfg.ReachDomain = func(context.Context, string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if reach {
			return proofPrefix + d.Token, nil
		}
		return "", errors.New("unreachable")
	}
	set := func(v bool) { mu.Lock(); reach = v; mu.Unlock() }
	active := func() bool { e := st.Dir("alice"); return e.Domain != "" }

	// the daily check does not look at it as a bound domain
	if due, _ := st.DomainsDue(t0, 10); len(due) != 0 {
		t.Fatalf("a handle that is not bound is in the lapse checks: %v", due)
	}
	s.checkDomains(context.Background(), t0)
	if !active() {
		t.Fatal("a working handle was given up")
	}
	set(false)
	s.checkDomains(context.Background(), t0.Add(1*day))
	s.checkDomains(context.Background(), t0.Add(1*day+time.Hour)) // the same day again: not counted
	s.checkDomains(context.Background(), t0.Add(2*day))
	if !active() {
		t.Fatal("given up after two failures")
	}
	// a pass in between starts the count again
	set(true)
	s.checkDomains(context.Background(), t0.Add(3*day))
	set(false)
	s.checkDomains(context.Background(), t0.Add(4*day))
	s.checkDomains(context.Background(), t0.Add(5*day))
	if !active() {
		t.Fatal("the count did not start again after a pass")
	}
	s.checkDomains(context.Background(), t0.Add(6*day))
	if active() {
		t.Fatal("still the address after three failed days")
	}
	// the account is served at its default address, nothing is removed, and the handle can be made the address again
	if w := do(s, "GET", "alice.localhost:8080", "/", "", ""); w.Code != 200 {
		t.Errorf("the default address answers %d", w.Code)
	}
	if e := st.Dir("alice"); e.Status != "" && e.Status != store.StatusActive {
		t.Errorf("the account is %s", e.Status)
	}
	if dd, _ := st.DomainOf("alice"); dd == nil || !dd.Verified || dd.Active {
		t.Errorf("the handle should stay verified and inactive: %+v", dd)
	}
	if err := st.ActivateDomain("alice"); err != nil {
		t.Errorf("activating again: %v", err)
	}
}
