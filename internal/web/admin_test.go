package web

import (
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func adminReq(s *Server, method, path, cookie, body string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://admin.localhost"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestAdminPages(t *testing.T) {
	s, _ := setup(t)
	st := s.st
	if w := adminReq(s, "GET", "/", "", ""); w.Code != 404 {
		t.Fatalf("admin pages are off by default: %d, want 404", w.Code)
	}
	s.cfg.Admin, s.cfg.AdminPublic = true, true
	if w := adminReq(s, "GET", "/", "", ""); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Fatalf("not signed in: %d %s", w.Code, w.Header().Get("Location"))
	}

	// only an administrator, with the right password, gets in
	sp, rel, _ := st.Space("alice")
	sp.SetPassword("correct horse")
	rel()
	login := func(name, pw string) *httptest.ResponseRecorder {
		return adminReq(s, "POST", "/login", "", url.Values{"name": {name}, "password": {pw}}.Encode())
	}
	if w := login("alice", "correct horse"); w.Code != 401 {
		t.Fatalf("alice is not an administrator yet: %d", w.Code)
	}
	if err := st.SetAdmin("alice", true); err != nil {
		t.Fatal(err)
	}
	if w := login("alice", "wrong"); w.Code != 401 {
		t.Fatalf("wrong password: %d", w.Code)
	}
	w := login("alice", "correct horse")
	if w.Code != 303 {
		t.Fatalf("sign in: %d %s", w.Code, w.Body.String())
	}
	var cookie string
	for _, c := range w.Result().Cookies() {
		if c.Name == adminCookie {
			cookie = c.Name + "=" + c.Value
			if !c.HttpOnly {
				t.Error("the admin cookie must be HttpOnly")
			}
		}
	}
	if cookie == "" {
		t.Fatal("no cookie")
	}
	// an ordinary session cookie of the user's own site must not open the admin pages
	for _, p := range []string{"/", "/accounts", "/waitlist", "/signups"} {
		if w := adminReq(s, "GET", p, cookie, ""); w.Code != 200 {
			t.Errorf("GET %s: %d %s", p, w.Code, w.Body.String())
		}
	}
	if w := adminReq(s, "GET", "/accounts", "weft_admin=alice:bogus", ""); w.Code != 303 {
		t.Errorf("a made-up cookie: %d", w.Code)
	}
	if w := adminReq(s, "POST", "/accounts/new", cookie, "name=bob", "Origin", "http://evil.example"); w.Code != 403 {
		t.Errorf("a form from another origin: %d, want 403", w.Code)
	}

	// a browser form from the admin pages' own origin is accepted; "null" is what a no-referrer policy would make it send
	if w := adminReq(s, "POST", "/logout", "weft_admin=none", "", "Origin", "http://admin.localhost"); w.Code != 303 {
		t.Errorf("a form from the same origin: %d", w.Code)
	}
	if w := adminReq(s, "GET", "/", cookie, ""); w.Header().Get("Referrer-Policy") != "same-origin" {
		t.Errorf("Referrer-Policy = %q, want same-origin", w.Header().Get("Referrer-Policy"))
	}

	// make accounts within the cap; the administrator is not counted
	s.cfg.MaxUsers = 1
	w = adminReq(s, "POST", "/accounts/new", cookie, "name=bob&display=Bob")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Temporary password") {
		t.Fatalf("make bob: %d %s", w.Code, w.Body.String())
	}
	if w := adminReq(s, "POST", "/accounts/new", cookie, "name=carol"); w.Code != 409 || !strings.Contains(w.Body.String(), "All places are taken") {
		t.Fatalf("at the cap: %d", w.Code)
	}
	if n, _, _ := st.Counts(); n != 1 {
		t.Errorf("counted accounts = %d, want 1 (alice is exempt)", n)
	}
	s.cfg.MaxUsers = 0
	if w := adminReq(s, "POST", "/accounts/new", cookie, "name=admin"); w.Code != 422 {
		t.Errorf("a reserved name: %d, want 422", w.Code)
	}

	// switch an account off and on
	bob := func() int { return do(s, "GET", "bob.localhost", "/feed.xml", "", "").Code }
	if bob() != 200 {
		t.Fatalf("bob's feed: %d", bob())
	}
	if w := adminReq(s, "POST", "/accounts/bob/suspend", cookie, ""); w.Code != 303 {
		t.Fatalf("suspend: %d", w.Code)
	}
	if bob() != 410 {
		t.Errorf("a switched-off account answers %d, want 410", bob())
	}
	if w := adminReq(s, "POST", "/accounts/bob/restore", cookie, ""); w.Code != 303 || bob() != 200 {
		t.Errorf("restore: %d, feed %d", w.Code, bob())
	}
	if w := adminReq(s, "POST", "/accounts/alice/suspend", cookie, ""); w.Code != 409 {
		t.Errorf("suspending an administrator: %d, want 409", w.Code)
	}
	if w := adminReq(s, "POST", "/accounts/alice/remove", cookie, "confirm=yes"); w.Code != 409 {
		t.Errorf("removing an administrator: %d, want 409", w.Code)
	}

	// delete: a question first, then the trash, and the name stays taken
	if w := adminReq(s, "POST", "/accounts/bob/remove", cookie, ""); w.Code != 303 || !strings.HasSuffix(w.Header().Get("Location"), "/accounts/bob/remove") {
		t.Fatalf("delete without a yes: %d %s", w.Code, w.Header().Get("Location"))
	}
	if bob() != 200 {
		t.Fatal("nothing happens before the yes")
	}
	if w := adminReq(s, "POST", "/accounts/bob/remove", cookie, "confirm=yes"); w.Code != 303 {
		t.Fatalf("delete: %d", w.Code)
	}
	if bob() != 410 {
		t.Errorf("a deleted account answers %d, want 410", bob())
	}
	if n, err := st.TrashMoved(time.Now()); err != nil || n != 1 {
		t.Fatalf("the file goes to the trash: %d %v", n, err)
	}
	if _, err := st.AddUser("bob", "", ""); err == nil {
		t.Error("the name of a deleted account stays taken")
	}
	if w := adminReq(s, "GET", "/accounts", cookie, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Bring back") {
		t.Errorf("the list offers to bring bob back: %d", w.Code)
	}
	if w := adminReq(s, "POST", "/accounts/bob/restore", cookie, ""); w.Code != 303 || bob() != 200 {
		t.Errorf("bring back from the trash: %d, feed %d", w.Code, bob())
	}
	if _, err := os.Stat(st.TrashDir()); err != nil {
		t.Error(err)
	}

	// the waiting list: let a person in
	if err := st.JoinWaitlist("dana", "dana@example.com", "https://dana.example/", ""); err != nil {
		t.Fatal(err)
	}
	w = adminReq(s, "GET", "/waitlist", cookie, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "dana@example.com") {
		t.Fatalf("waiting list page: %d", w.Code)
	}
	list, _ := st.Waitlist()
	id := list[0].ID
	w = adminReq(s, "POST", "/accounts/new", cookie, url.Values{"name": {"dana"}, "waiting": {"1"}}.Encode())
	if w.Code != 200 || !strings.Contains(w.Body.String(), "dana@example.com") {
		t.Fatalf("let dana in: %d %s", w.Code, w.Body.String())
	}
	if l, _ := st.Waitlist(); !l[0].Reviewed {
		t.Error("a request that was answered is marked as read")
	}
	if w := adminReq(s, "POST", "/waitlist/"+itoa(id)+"/remove", cookie, ""); w.Code != 303 {
		t.Errorf("remove a request: %d", w.Code)
	}

	// taking the mark off ends the session at once
	st.SetAdmin("alice", false)
	if w := adminReq(s, "GET", "/accounts", cookie, ""); w.Code != 303 {
		t.Errorf("after the mark is off: %d, want 303", w.Code)
	}
}

func TestAdminOverviewListsLapsingDomains(t *testing.T) {
	s, _ := setup(t)
	boundAccount(t, s, "bob", "weft.bob.example")
	boundAccount(t, s, "eve", "weft.eve.example")
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		s.st.RecordCheck("bob", false, now.Add(time.Duration(i)*24*time.Hour))
	}
	s.st.RecordCheck("eve", false, now)
	s.st.SetAdmin("alice", true)
	sp, rel, _ := s.st.Space("alice")
	sp.SetPassword("correct horse")
	u, _ := sp.User()
	tok, _ := sp.CreateSession(u.ID)
	rel()
	s.cfg.Admin, s.cfg.AdminPublic = true, true
	w := adminReq(s, "GET", "/", adminCookie+"=alice:"+tok, "")
	body := w.Body.String()
	for _, want := range []string{"Domains that failed a check", "weft.bob.example", "lapsed since 2026-11-12", "2027-02-10", "weft.eve.example", "1 of 3 checks failed"} {
		if !strings.Contains(body, want) {
			t.Errorf("the overview lacks %q", want)
		}
	}
}
