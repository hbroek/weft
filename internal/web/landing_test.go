package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// www is a reserved name, so the folder of static files can be served there without meeting an account.
func TestLandingServedAtWWW(t *testing.T) {
	s, _ := setup(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<title>Landing</title>"), 0o644)

	if w := do(s, "GET", "www.localhost", "/", "", ""); w.Code != 404 {
		t.Fatalf("without a site folder www answers %d, want 404", w.Code)
	}
	s.cfg.SiteDir = dir
	if w := do(s, "GET", "www.localhost", "/", "", ""); w.Code != 200 || w.Body.String() != "<title>Landing</title>" {
		t.Fatalf("www = %d %q", w.Code, w.Body.String())
	}
	if w := do(s, "POST", "www.localhost", "/", "", ""); w.Code != 405 {
		t.Errorf("POST to www = %d, want 405", w.Code)
	}
	if w := do(s, "GET", "alice.localhost", "/", "", ""); w.Code != 200 || w.Body.String() == "<title>Landing</title>" {
		t.Errorf("a user's site must not be the landing page: %d", w.Code)
	}
}

func postForm(s *Server, host, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://"+host+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestWaitingListForm(t *testing.T) {
	s, _ := setup(t)
	s.cfg.SiteDir = t.TempDir()
	s.cfg.MaxUsers = 1000

	good := url.Values{"name": {"bob"}, "contact": {"bob@example.com"}, "links": {"https://bob.example/blog"}}
	if w := postForm(s, "www.localhost", "/waitlist", good.Encode()); w.Code != 200 || !strings.Contains(w.Body.String(), "on the list") {
		t.Fatalf("a good request: %d %s", w.Code, w.Body.String())
	}
	for name, v := range map[string]url.Values{
		"the same again":  good,
		"a name in use":   {"name": {"alice"}, "contact": {"a@example.com"}, "links": {"x"}},
		"no links":        {"name": {"carol"}, "contact": {"c@example.com"}},
		"a bad address":   {"name": {"carol"}, "contact": {"not an address"}, "links": {"x"}},
		"a reserved name": {"name": {"admin"}, "contact": {"d@example.com"}, "links": {"x"}},
		"an address list": {"name": {"dave"}, "contact": {"a@example.com, b@example.com"}, "links": {"x"}},
	} {
		if w := postForm(s, "www.localhost", "/waitlist", v.Encode()); w.Code != 400 {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}
	// a program that fills the hidden field is told thanks and nothing is kept
	bot := url.Values{"name": {"spam"}, "contact": {"s@example.com"}, "links": {"x"}, "website": {"http://spam.example"}}
	if w := postForm(s, "www.localhost", "/waitlist", bot.Encode()); w.Code != 200 {
		t.Errorf("bot: %d", w.Code)
	}
	list, _ := s.st.Waitlist()
	if len(list) != 1 || list[0].Name != "bob" {
		t.Fatalf("the list holds %+v", list)
	}

	w := do(s, "GET", "www.localhost", "/status.json", "", "")
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got["accounts"] != 1.0 || got["waiting"] != 1.0 || got["cap"] != 1000.0 || got["signups"] != false {
		t.Errorf("status = %s (%v)", w.Body.String(), err)
	}
	// only the landing host takes the form
	if w := postForm(s, "alice.localhost", "/waitlist", good.Encode()); w.Code == 200 && strings.Contains(w.Body.String(), "on the list") {
		t.Error("a user's site took a waiting list request")
	}
}

// A sign-up with a domain, from the form to the account.
func TestSignupWithADomain(t *testing.T) {
	s, _ := setup(t)
	s.cfg.CNAMETarget = "custom.localhost"
	s.cfg.MaxUsers = 5
	txt := map[string][]string{}
	s.cfg.LookupTXT = func(_ context.Context, name string) ([]string, error) { return txt[name], nil }
	s.cfg.LookupCNAME = func(context.Context, string) (string, error) { return "", errors.New("no CNAME") }

	// off by default
	for _, p := range []string{"/signup", "/signup/check", "/signup/finish"} {
		if w := do(s, "GET", "www.localhost", p, "", ""); w.Code != 404 {
			t.Fatalf("%s with sign-ups off: %d, want 404", p, w.Code)
		}
	}
	s.cfg.Signups = true
	if w := do(s, "GET", "www.localhost", "/signup", "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "weft.example.com") {
		t.Fatalf("the form: %d", w.Code)
	}

	start := func(domain, name string) *httptest.ResponseRecorder {
		return postForm(s, "www.localhost", "/signup", url.Values{"domain": {domain}, "name": {name}}.Encode())
	}
	for name, c := range map[string][2]string{
		"a name further down":   {"weft.blog.example.com", "bob"},
		"the wrong first label": {"www.example.com", "bob"},
		"a registry's name":     {"weft.co.uk", "bob"},
		"a taken name":          {"weft.example.com", "alice"},
		"a reserved name":       {"weft.example.com", "admin"},
		"the server's own name": {"weft.localhost", "bob"},
	} {
		if w := start(c[0], c[1]); w.Code != 400 {
			t.Errorf("%s: %d, want 400", name, w.Code)
		}
	}

	w := start("weft.example.co.uk", "bob")
	if w.Code != 200 {
		t.Fatalf("a good start: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	m := regexp.MustCompile(`weft-verify=([0-9a-f]+)`).FindStringSubmatch(body)
	id := regexp.MustCompile(`name="id" value="([0-9a-f]+)"`).FindStringSubmatch(body)
	if m == nil || id == nil || !strings.Contains(body, "_weft.weft.example.co.uk") || !strings.Contains(body, "custom.localhost") {
		t.Fatalf("the instructions lack the record: %s", body)
	}
	if w := start("weft.example.co.uk", "robert"); w.Code != 400 {
		t.Errorf("the same domain twice: %d", w.Code)
	}

	check := func(path string, v url.Values) *httptest.ResponseRecorder {
		v.Set("id", id[1])
		return postForm(s, "www.localhost", path, v.Encode())
	}
	// no record yet
	if w := check("/signup/check", url.Values{}); w.Code != 422 || !strings.Contains(w.Body.String(), "not there yet") {
		t.Errorf("without the record: %d", w.Code)
	}
	// the account cannot be made without it, whatever the second page said
	if w := check("/signup/finish", url.Values{"password": {"a good password"}, "password2": {"a good password"}}); w.Code == 200 && strings.Contains(w.Body.String(), "is made") {
		t.Fatal("an account was made without the record")
	}
	txt["_weft.weft.example.co.uk"] = []string{"weft-verify=" + m[1]}
	if w := check("/signup/check", url.Values{}); w.Code != 200 || !strings.Contains(w.Body.String(), "Choose a password") {
		t.Fatalf("with the record: %d %s", w.Code, w.Body.String())
	}
	if w := check("/signup/finish", url.Values{"password": {"a good password"}, "password2": {"different one"}}); w.Code != 400 {
		t.Errorf("passwords that differ: %d", w.Code)
	}
	if w := check("/signup/finish", url.Values{"password": {"short"}, "password2": {"short"}}); w.Code != 400 {
		t.Errorf("a short password: %d", w.Code)
	}
	if s.st.UserExists("bob") {
		t.Fatal("a refused form made the account")
	}
	w = check("/signup/finish", url.Values{"password": {"a good password"}, "password2": {"a good password"}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "is made") {
		t.Fatalf("finish: %d %s", w.Code, w.Body.String())
	}
	sp, release, err := s.st.Space("bob")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := sp.CheckPassword("a good password"); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
	if d, err := s.st.DomainOf("bob"); err != nil || !d.Verified || d.Domain != "weft.example.co.uk" {
		t.Errorf("the domain: %+v %v", d, err)
	}
	// the sign-up is used up
	if w := check("/signup/finish", url.Values{"password": {"a good password"}, "password2": {"a good password"}}); w.Code != 410 {
		t.Errorf("a second finish: %d, want 410", w.Code)
	}
	// the counts say so
	if got := do(s, "GET", "www.localhost", "/status.json", "", "").Body.String(); !strings.Contains(got, `"signups":true`) || !strings.Contains(got, `"accounts":2`) {
		t.Errorf("status = %s", got)
	}

	// the places are all taken
	s.cfg.MaxUsers = 2
	if w := start("weft.example.org", "carol"); w.Code != 200 || !strings.Contains(w.Body.String(), "All places are taken") {
		t.Errorf("at the cap: %d %s", w.Code, w.Body.String())
	}
	if s.st.UserExists("carol") {
		t.Error("an account was made at the cap")
	}
}
