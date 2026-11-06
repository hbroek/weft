package web

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
	var got map[string]int
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got["accounts"] != 1 || got["waiting"] != 1 || got["cap"] != 1000 {
		t.Errorf("status = %s (%v)", w.Body.String(), err)
	}
	// only the landing host takes the form
	if w := postForm(s, "alice.localhost", "/waitlist", good.Encode()); w.Code == 200 && strings.Contains(w.Body.String(), "on the list") {
		t.Error("a user's site took a waiting list request")
	}
}
