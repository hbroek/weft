package web

import (
	"encoding/json"
	"html"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hbroek/weft/internal/store"
)

func oauthServer(t *testing.T, on bool) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.AddUser("alice", "Alice", ""); err != nil {
		t.Fatal(err)
	}
	return New(Config{BaseDomain: "localhost", OAuth: on}, st)
}

const alice = "alice.localhost:8080"

func TestOAuthIsAbsentUnlessTheOperatorTurnsItOn(t *testing.T) {
	s := oauthServer(t, false)
	for _, p := range [][2]string{
		{"GET", "/.well-known/oauth-authorization-server"}, {"GET", "/.well-known/oauth-protected-resource"},
		{"POST", "/oauth/register"}, {"OPTIONS", "/oauth/register"},
	} {
		if w := doWith(s, p[0], alice, p[1], "", "application/json", "", `{}`); w.Code != 404 && w.Code != 405 {
			t.Errorf("%s %s with OAuth off: %d", p[0], p[1], w.Code)
		}
	}
}

func TestDiscoveryDocuments(t *testing.T) {
	s := oauthServer(t, true)
	var m map[string]any
	w := doWith(s, "GET", alice, "/.well-known/oauth-authorization-server", "", "", "", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	o := "http://alice.localhost:8080"
	if m["issuer"] != o || m["token_endpoint"] != o+"/oauth/token" || m["registration_endpoint"] != o+"/oauth/register" ||
		m["authorization_endpoint"] != o+"/oauth/authorize" || m["revocation_endpoint"] != o+"/oauth/revoke" {
		t.Errorf("endpoints %v", m)
	}
	if got := m["code_challenge_methods_supported"].([]any); len(got) != 1 || got[0] != "S256" {
		t.Errorf("PKCE methods %v", got)
	}
	if got := m["token_endpoint_auth_methods_supported"].([]any); len(got) != 1 || got[0] != "none" {
		t.Errorf("auth methods %v", got)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("no CORS on the metadata")
	}

	w = doWith(s, "GET", alice, "/.well-known/oauth-protected-resource", "", "", "", "")
	m = nil
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &m) != nil || m["resource"] != o || m["authorization_servers"].([]any)[0] != o {
		t.Errorf("resource %d %s", w.Code, w.Body)
	}
	// an address that is no account has no issuer
	if w := doWith(s, "GET", "nobody.localhost:8080", "/.well-known/oauth-authorization-server", "", "", "", ""); w.Code != 404 {
		t.Errorf("unknown host: %d", w.Code)
	}
}

func TestRegisterAClient(t *testing.T) {
	s := oauthServer(t, true)
	reg := func(ct, body string) (int, map[string]any) {
		w := doWith(s, "POST", alice, "/oauth/register", "", ct, "", body)
		var m map[string]any
		json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m
	}
	const js = "application/json"

	code, m := reg(js, `{"client_name":"  Quill\n Editor ","redirect_uris":["https://quill.example/cb","http://127.0.0.1/cb","https://quill.example/cb"],
		"client_uri":"https://quill.example/","logo_uri":"https://quill.example/l.png","grant_types":["authorization_code","refresh_token"],
		"response_types":["code"],"token_endpoint_auth_method":"none","scope":"read post","unknown":1}`)
	if code != 201 {
		t.Fatalf("%d %v", code, m)
	}
	id, _ := m["client_id"].(string)
	if !strings.HasPrefix(id, "wc_") || m["client_name"] != "Quill Editor" || m["token_endpoint_auth_method"] != "none" || m["client_secret"] != nil {
		t.Errorf("response %v", m)
	}
	if uris := m["redirect_uris"].([]any); len(uris) != 2 { // the duplicate is dropped
		t.Errorf("redirect_uris %v", uris)
	}
	c, err := s.st.OAuthClient(id)
	if err != nil || c.Name != "Quill Editor" || c.ClientURI != "https://quill.example/" || len(c.RedirectURIs) != 2 {
		t.Errorf("stored %+v %v", c, err)
	}

	for name, body := range map[string]string{
		"no name":            `{"redirect_uris":["https://a.example/cb"]}`,
		"blank name":         `{"client_name":" \n ","redirect_uris":["https://a.example/cb"]}`,
		"no redirects":       `{"client_name":"A"}`,
		"empty redirects":    `{"client_name":"A","redirect_uris":[]}`,
		"http redirect":      `{"client_name":"A","redirect_uris":["http://a.example/cb"]}`,
		"javascript":         `{"client_name":"A","redirect_uris":["javascript:alert(1)"]}`,
		"fragment":           `{"client_name":"A","redirect_uris":["https://a.example/cb#x"]}`,
		"eleven redirects":   `{"client_name":"A","redirect_uris":["` + strings.Repeat(`https://a.example/cb","`, 10) + `https://a.example/cb2"]}`,
		"secret method":      `{"client_name":"A","redirect_uris":["https://a.example/cb"],"token_endpoint_auth_method":"client_secret_basic"}`,
		"implicit grant":     `{"client_name":"A","redirect_uris":["https://a.example/cb"],"grant_types":["implicit"]}`,
		"token response":     `{"client_name":"A","redirect_uris":["https://a.example/cb"],"response_types":["token"]}`,
		"unknown scope":      `{"client_name":"A","redirect_uris":["https://a.example/cb"],"scope":"root"}`,
		"http logo":          `{"client_name":"A","redirect_uris":["https://a.example/cb"],"logo_uri":"http://a.example/l.png"}`,
		"data logo":          `{"client_name":"A","redirect_uris":["https://a.example/cb"],"logo_uri":"data:image/png;base64,AAAA"}`,
		"not an object":      `[1]`,
		"not json":           `nope`,
		"redirects a string": `{"client_name":"A","redirect_uris":"https://a.example/cb"}`,
		"too big":            `{"client_name":"` + strings.Repeat("a", maxRegistration) + `","redirect_uris":["https://a.example/cb"]}`,
	} {
		if code, m := reg(js, body); code != 400 || m["error"] == nil {
			t.Errorf("%s: %d %v", name, code, m)
		}
	}
	if code, m := reg(js, `{"client_name":"A","redirect_uris":["http://a.example/cb"]}`); m["error"] != "invalid_redirect_uri" {
		t.Errorf("error code %d %v", code, m)
	}
	if code, _ := reg("text/plain", `{"client_name":"A","redirect_uris":["https://a.example/cb"]}`); code != 415 {
		t.Errorf("content type: %d", code)
	}
	if code, _ := reg("application/json; charset=utf-8", `{"client_name":"A","redirect_uris":["https://a.example/cb"]}`); code != 201 {
		t.Errorf("charset: %d", code)
	}

	// the script in a name stays text: it is escaped where it is shown
	if code, m := reg(js, `{"client_name":"<script>x</script>","redirect_uris":["https://a.example/cb"]}`); code != 201 || m["client_name"] != "<script>x</script>" {
		t.Errorf("markup name: %d %v", code, m)
	}

	// a preflight from an app in a browser
	if w := doWith(s, "OPTIONS", alice, "/oauth/register", "", "", "", ""); w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("preflight %d %v", w.Code, w.Header())
	}
}

func TestRegistrationIsRateLimited(t *testing.T) {
	s := oauthServer(t, true)
	s.lim = newLimits()
	codes := map[int]int{}
	for i := 0; i < 14; i++ {
		w := doWith(s, "POST", alice, "/oauth/register", "", "application/json", "", `{"client_name":"A","redirect_uris":["https://a.example/cb"]}`)
		codes[w.Code]++
		if w.Code == 429 && w.Header().Get("Retry-After") == "" {
			t.Error("429 without Retry-After")
		}
	}
	if codes[201] != 10 || codes[429] != 4 {
		t.Errorf("%v", codes)
	}
}

func TestRegistrationStopsAtTheServerCap(t *testing.T) {
	s := oauthServer(t, true)
	for i := 0; i < store.MaxOAuthClients; i++ {
		if _, err := s.st.AddOAuthClient(store.OAuthClient{Name: "x", RedirectURIs: []string{"https://a.example/cb"}}); err != nil {
			t.Fatal(err)
		}
	}
	// nothing is old enough to drop yet, so the table is full
	w := doWith(s, "POST", alice, "/oauth/register", "", "application/json", "", `{"client_name":"A","redirect_uris":["https://a.example/cb"]}`)
	if w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}

// --- the consent page ---

const pkceChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

// backTo is where the page that sends a person back to the app sends them.
func backTo(t *testing.T, body string) *url.URL {
	t.Helper()
	m := regexp.MustCompile(`http-equiv="refresh" content="0;url=([^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("not a page that goes back to the app:\n%s", body)
	}
	u, err := url.Parse(html.UnescapeString(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAuthorizeAndConsent(t *testing.T) {
	s, _ := setup(t)
	s.cfg.OAuth = true
	s.space("alice").SetPassword("correct horse")
	c, err := s.st.AddOAuthClient(store.OAuthClient{Name: "<b>Quill</b>", RedirectURIs: []string{"https://app.example/cb", "http://127.0.0.1/cb"}, ClientURI: "https://app.example/"})
	if err != nil {
		t.Fatal(err)
	}
	q := func(over map[string]string) string {
		v := url.Values{"response_type": {"code"}, "client_id": {c.ID}, "redirect_uri": {"https://app.example/cb"}, "code_challenge": {pkceChallenge},
			"code_challenge_method": {"S256"}, "scope": {"post read"}, "state": {"s t&a=te"}}
		for k, val := range over {
			if val == "-" {
				v.Del(k)
			} else {
				v.Set(k, val)
			}
		}
		return "/oauth/authorize?" + v.Encode()
	}
	cookie := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))

	// not signed in: sign in, and come back to this same request
	w := form(s, "GET", q(nil), "", nil, "")
	if loc := w.Header().Get("Location"); w.Code != 303 || !strings.HasPrefix(loc, "/login?next=") {
		t.Fatalf("anonymous: %d %q", w.Code, loc)
	} else if u, _ := url.Parse(loc); u.Query().Get("next") != q(nil) {
		t.Errorf("next is %q", u.Query().Get("next"))
	}

	// signed in: the consent page
	w = form(s, "GET", q(nil), cookie, nil, "")
	page := w.Body.String()
	if w.Code != 200 {
		t.Fatalf("consent: %d\n%s", w.Code, page)
	}
	for _, want := range []string{"&lt;b&gt;Quill&lt;/b&gt;", "Read your posts", "Write, change and delete your posts", "app.example", `name="decision" value="allow"`, `name="decision" value="deny"`, `name="csrf"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(page, "<b>Quill") || strings.Contains(page, "Follow and unfollow") {
		t.Error("the app's name is not escaped, or a scope that was not asked for is shown")
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"frame-ancestors 'none'", "form-action 'self' https://app.example", "script-src 'none'", "default-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if w.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Error("framing or caching headers")
	}
	// a loopback app may listen on any port
	if csp := form(s, "GET", q(map[string]string{"redirect_uri": "http://127.0.0.1:5123/cb"}), cookie, nil, "").Header().Get("Content-Security-Policy"); !strings.Contains(csp, "http://127.0.0.1:*") {
		t.Errorf("loopback CSP %q", csp)
	}

	// an app or a redirect address that is not known: shown to the person, never redirected to
	for name, p := range map[string]map[string]string{
		"unknown client":      {"client_id": "wc_nope"},
		"unregistered":        {"redirect_uri": "https://evil.example/cb"},
		"other path":          {"redirect_uri": "https://app.example/cb/x"},
		"no redirect":         {"redirect_uri": "-"},
		"unregistered deny":   {"redirect_uri": "https://evil.example/cb", "response_type": "token"},
		"unregistered scheme": {"redirect_uri": "javascript:alert(1)"},
	} {
		w := form(s, "GET", q(p), cookie, nil, "")
		if w.Code != 400 || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "refresh") {
			t.Errorf("%s: %d %q", name, w.Code, w.Header().Get("Location"))
		}
		if w := form(s, "GET", q(p), "", nil, ""); w.Code != 400 { // not even a sign-in first
			t.Errorf("%s, signed out: %d", name, w.Code)
		}
	}

	// the app's own mistakes go back to the app, with its state
	for name, c := range map[string]struct {
		over map[string]string
		code string
	}{
		"response type": {map[string]string{"response_type": "token"}, "unsupported_response_type"},
		"no challenge":  {map[string]string{"code_challenge": "-"}, "invalid_request"},
		"plain":         {map[string]string{"code_challenge_method": "plain"}, "invalid_request"},
		"no method":     {map[string]string{"code_challenge_method": "-"}, "invalid_request"},
		"scope":         {map[string]string{"scope": "read root"}, "invalid_scope"},
	} {
		for _, ck := range []string{cookie, ""} { // before any sign-in
			w := form(s, "GET", q(c.over), ck, nil, "")
			u := backTo(t, w.Body.String())
			if w.Code != 200 || u.Host != "app.example" || u.Query().Get("error") != c.code || u.Query().Get("state") != "s t&a=te" || u.Query().Get("iss") != "http://alice.localhost:8080" {
				t.Errorf("%s: %d %v", name, w.Code, u)
			}
		}
	}

	csrf := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(page)[1]
	answer := func(ck string, over map[string]string, origin string) *httptest.ResponseRecorder {
		v := url.Values{"response_type": {"code"}, "client_id": {c.ID}, "redirect_uri": {"https://app.example/cb"}, "code_challenge": {pkceChallenge},
			"code_challenge_method": {"S256"}, "scope": {"read post"}, "state": {"s t&a=te"}, "csrf": {csrf}, "decision": {"allow"}}
		for k, val := range over {
			v.Set(k, val)
		}
		return form(s, "POST", "/oauth/authorize", ck, v, origin)
	}
	grants := func() int {
		g, _ := s.st.OAuthGrants("alice")
		return len(g)
	}

	// everything that must refuse, refuses and makes no grant
	for name, w := range map[string]*httptest.ResponseRecorder{
		"signed out":       answer("", nil, ""),
		"no token":         answer(cookie, map[string]string{"csrf": ""}, ""),
		"wrong token":      answer(cookie, map[string]string{"csrf": strings.Repeat("0", 64)}, ""),
		"another redirect": answer(cookie, map[string]string{"redirect_uri": "https://evil.example/cb"}, ""),
		"another client":   answer(cookie, map[string]string{"client_id": "wc_nope"}, ""),
		"cross-origin":     answer(cookie, nil, "https://evil.example"),
	} {
		if w.Code < 400 || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "wco_") || grants() != 0 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if w := answer(cookie, map[string]string{"csrf": ""}, ""); strings.Contains(w.Body.String(), "refresh") {
		t.Error("a request without the token was sent back to the app")
	}

	// deny
	w = answer(cookie, map[string]string{"decision": "deny"}, "http://"+host)
	if u := backTo(t, w.Body.String()); u.Query().Get("error") != "access_denied" || u.Query().Get("code") != "" || u.Query().Get("state") != "s t&a=te" || grants() != 0 {
		t.Errorf("deny: %v", u)
	}
	// anything but "allow" is a no
	if u := backTo(t, answer(cookie, map[string]string{"decision": "ALLOW"}, "").Body.String()); u.Query().Get("error") != "access_denied" {
		t.Errorf("odd decision: %v", u)
	}

	// allow: a code, the state, the issuer, and a pending grant for exactly what was shown
	w = answer(cookie, map[string]string{"scope": "post  read read"}, "http://"+host)
	u := backTo(t, w.Body.String())
	if w.Code != 200 || u.Host != "app.example" || u.Path != "/cb" || u.Query().Get("state") != "s t&a=te" || u.Query().Get("iss") != "http://alice.localhost:8080" || !strings.HasPrefix(u.Query().Get("code"), "wco_") {
		t.Fatalf("allow: %d %v", w.Code, u)
	}
	got, err := s.st.RedeemOAuthCode(u.Query().Get("code"))
	if err != nil || got.User != "alice" || got.ClientID != c.ID || got.Scope != "read post" || got.RedirectURI != "https://app.example/cb" || got.Challenge != pkceChallenge {
		t.Errorf("code %+v %v", got, err)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Error("the page with the code can be cached")
	}

	// an address that already has a query keeps it
	c2, _ := s.st.AddOAuthClient(store.OAuthClient{Name: "Q", RedirectURIs: []string{"https://q.example/cb?tenant=1"}})
	u = backTo(t, form(s, "GET", q(map[string]string{"client_id": c2.ID, "redirect_uri": "https://q.example/cb?tenant=1", "code_challenge": "-"}), cookie, nil, "").Body.String())
	if u.Query().Get("tenant") != "1" || u.Query().Get("error") != "invalid_request" {
		t.Errorf("query lost: %v", u)
	}

	// off: the page does not exist
	s.cfg.OAuth = false
	if w := form(s, "GET", q(nil), cookie, nil, ""); w.Code != 404 {
		t.Errorf("with OAuth off: %d", w.Code)
	}
	if w := answer(cookie, nil, ""); w.Code != 404 {
		t.Errorf("answer with OAuth off: %d", w.Code)
	}
}
