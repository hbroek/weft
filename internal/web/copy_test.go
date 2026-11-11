package web

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The values a person must carry to their DNS (the names, the token, the CNAME target) have a Copy button, on
// the profile page and on the sign-up page.
func TestDomainValuesHaveACopyButton(t *testing.T) {
	s, _ := setup(t)
	s.cfg.CNAMETarget = "custom.localhost"
	s.cfg.Signups = true
	s.cfg.LookupTXT = func(context.Context, string) ([]string, error) { return nil, nil }
	s.cfg.LookupCNAME = func(context.Context, string) (string, error) { return "", errors.New("no CNAME") }
	script := "querySelectorAll('[data-copy]')"

	// the profile page of an account whose domain is not verified yet
	if _, err := s.st.SetDomain("alice", "weft.example.org"); err != nil {
		t.Fatal(err)
	}
	sp, rel, _ := s.st.Space("alice")
	u, _ := sp.User()
	tok, _ := sp.CreateSession(u.ID)
	rel()
	w := newRecorder()
	s.ServeHTTP(w, cookieReq("GET", "http://alice.localhost/profile", cookieName+"="+tok))
	body := w.Body.String()
	for _, want := range []string{
		`<strong data-copy>alice.localhost</strong>`,
		`<code data-copy>_weft.weft.example.org</code>`,
		`<code data-copy>weft-verify=`,
		`<th>Name</th><td><code data-copy>weft.example.org</code></td>`,
		`<th>Target</th><td><code data-copy>custom.localhost</code>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the profile page lacks %s", want)
		}
	}
	if !strings.Contains(body, "Full (strict)") {
		t.Error("the profile page does not warn about Cloudflare's Flexible mode")
	}
	if n := strings.Count(body, script); n != 1 {
		t.Errorf("the profile page has the copy script %d times, want 1", n)
	}
	// a person who presses the check button is told what happens when it works
	if !strings.Contains(body, "you are taken to it at once and have to sign in again there") {
		t.Error("the profile page does not say that a working domain takes you to it")
	}
	// the sections of the profile page are set apart by rules
	if h, r := strings.Count(body, "<h3>"), strings.Count(body, "<hr>\n<h3>"); h < 3 || h != r {
		t.Errorf("the profile page has %d section headings and %d of them follow a rule", h, r)
	}

	// the first sign-up page
	w = postForm(s, "www.localhost", "/signup", url.Values{"domain": {"weft.example.co.uk"}, "name": {"bob"}, "email": {"bob@example.org"}}.Encode())
	body = w.Body.String()
	for _, want := range []string{`<code data-copy>_weft.weft.example.co.uk</code>`, `<code data-copy>weft-verify=`, `<code data-copy>weft.example.co.uk</code>`, `<code data-copy>custom.localhost</code>`} {
		if !strings.Contains(body, want) {
			t.Errorf("the sign-up page lacks %s", want)
		}
	}
	if !strings.Contains(body, "Full (strict)") {
		t.Error("the sign-up page does not warn about Cloudflare's Flexible mode")
	}
	if n := strings.Count(body, script); n != 1 {
		t.Errorf("the sign-up page has the copy script %d times, want 1", n)
	}
}

// The pages ask for the style sheet by a versioned address, so that a cache cannot hold on to an old one.
func TestStyleSheetAddressChangesWithTheSheet(t *testing.T) {
	s, _ := setup(t)
	w := do(s, "GET", "alice.localhost", "/", "", "")
	m := regexp.MustCompile(`href="/static/weft\.css\?v=([0-9a-f]{8})"`).FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatalf("the page does not ask for a versioned style sheet:\n%s", w.Body)
	}
	if m[1] != cssVersion() {
		t.Errorf("the version in the page is %s, the sheet's is %s", m[1], cssVersion())
	}
	if w := do(s, "GET", "alice.localhost", "/static/weft.css?v="+m[1], "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "button.copy") {
		t.Errorf("the versioned address does not serve the sheet: %d", w.Code)
	}
}
