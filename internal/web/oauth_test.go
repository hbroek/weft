package web

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/fetch"
	"github.com/hbroek/weft/internal/oauth"
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

// --- the token endpoint ---

const pkceVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk" // the one of RFC 7636 that makes pkceChallenge

func TestTokenEndpoint(t *testing.T) {
	s, _ := setup(t)
	s.cfg.OAuth = true
	if _, err := s.st.AddUser("bob", "Bob", ""); err != nil {
		t.Fatal(err)
	}
	c, _ := s.st.AddOAuthClient(store.OAuthClient{Name: "Quill", RedirectURIs: []string{"https://app.example/cb"}})
	other, _ := s.st.AddOAuthClient(store.OAuthClient{Name: "Other", RedirectURIs: []string{"https://other.example/cb"}})
	const redirect = "https://app.example/cb"
	newCode := func() string {
		g, err := s.st.CreateOAuthGrant(c.ID, "alice", "read post")
		if err != nil {
			t.Fatal(err)
		}
		code, _ := s.st.NewOAuthCode(g, redirect, pkceChallenge)
		return code
	}
	post := func(host, path string, v url.Values) (*httptest.ResponseRecorder, map[string]any) {
		w := doWith(s, "POST", host, path, "", "application/x-www-form-urlencoded", "", v.Encode())
		var m map[string]any
		json.Unmarshal(w.Body.Bytes(), &m)
		return w, m
	}
	exchange := func(code string, over map[string]string) (*httptest.ResponseRecorder, map[string]any) {
		v := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {c.ID}, "redirect_uri": {redirect}, "code_verifier": {pkceVerifier}}
		for k, val := range over {
			v.Set(k, val)
		}
		return post(alice, "/oauth/token", v)
	}

	// the whole exchange
	w, m := exchange(newCode(), nil)
	if w.Code != 200 || m["token_type"] != "Bearer" || m["scope"] != "read post" || m["expires_in"] != float64(3600) ||
		!strings.HasPrefix(m["access_token"].(string), "wat_") || !strings.HasPrefix(m["refresh_token"].(string), "wrt_") {
		t.Fatalf("%d %v", w.Code, m)
	}
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Pragma") != "no-cache" {
		t.Error("a token response can be cached")
	}
	access, refresh := m["access_token"].(string), m["refresh_token"].(string)
	if a, err := s.st.OAuthAccess(access); err != nil || a.User != "alice" || a.ClientID != c.ID {
		t.Errorf("the access token: %+v %v", a, err)
	}

	// every way to get it wrong is "invalid_grant", and a code that was tried is gone
	for name, over := range map[string]map[string]string{
		"wrong verifier":  {"code_verifier": strings.Repeat("a", 43)},
		"no verifier":     {"code_verifier": ""},
		"other redirect":  {"redirect_uri": "https://app.example/cb2"},
		"no redirect":     {"redirect_uri": ""},
		"another app":     {"client_id": other.ID},
		"not a code":      {"code": "wco_" + strings.Repeat("x", 43)},
		"plain challenge": {"code_verifier": pkceChallenge},
	} {
		code := newCode()
		w, m := exchange(code, over)
		if w.Code != 400 || m["error"] != "invalid_grant" || m["access_token"] != nil {
			t.Errorf("%s: %d %v", name, w.Code, m)
		}
		if name != "not a code" {
			if w, _ := exchange(code, nil); w.Code != 400 { // the right request, with the same code, is too late
				t.Errorf("%s: the code worked after a wrong try: %d", name, w.Code)
			}
		}
	}
	// a code of alice's, sent to bob's address
	code := newCode()
	if w, m := post("bob.localhost:8080", "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {c.ID}, "redirect_uri": {redirect}, "code_verifier": {pkceVerifier}}); w.Code != 400 || m["error"] != "invalid_grant" {
		t.Errorf("another account's address: %d %v", w.Code, m)
	}

	// refresh: a new pair, and the old refresh token is spent
	rf := func(host, tok, client string) (*httptest.ResponseRecorder, map[string]any) {
		return post(host, "/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok}, "client_id": {client}})
	}
	if w, m := rf(alice, refresh, other.ID); w.Code != 400 || m["error"] != "invalid_grant" {
		t.Errorf("another app's refresh: %d %v", w.Code, m)
	}
	if w, m := rf("bob.localhost:8080", refresh, c.ID); w.Code != 400 || m["error"] != "invalid_grant" {
		t.Errorf("another account's refresh: %d %v", w.Code, m)
	}
	w, m = rf(alice, refresh, c.ID)
	if w.Code != 200 || m["access_token"] == access || m["refresh_token"] == refresh {
		t.Fatalf("refresh: %d %v", w.Code, m)
	}
	if _, err := s.st.OAuthAccess(access); err == nil {
		t.Error("the old access token still works")
	}
	newAccess, newRefresh := m["access_token"].(string), m["refresh_token"].(string)
	// the old refresh token again, after the moment of grace: someone else has it, so the grant ends
	if _, err := s.st.RawDBForTests().Exec(`UPDATE oauth_tokens SET used=used-3600 WHERE used>0`); err != nil {
		t.Fatal(err)
	}
	if w, m := rf(alice, refresh, c.ID); w.Code != 400 || m["error"] != "invalid_grant" {
		t.Errorf("reused refresh: %d %v", w.Code, m)
	}
	if _, err := s.st.OAuthAccess(newAccess); err == nil {
		t.Error("the grant survived a reused refresh token")
	}
	if w, _ := rf(alice, newRefresh, c.ID); w.Code != 400 {
		t.Error("the newer refresh token survived")
	}

	// malformed requests
	for name, v := range map[string]url.Values{
		"no grant type":  {"client_id": {c.ID}},
		"password grant": {"grant_type": {"password"}, "client_id": {c.ID}},
		"unknown client": {"grant_type": {"authorization_code"}, "client_id": {"wc_nope"}, "code": {"x"}},
		"with a secret":  {"grant_type": {"authorization_code"}, "client_id": {c.ID}, "client_secret": {"s"}, "code": {"x"}},
		"unknown scope":  {"grant_type": {"refresh_token"}, "client_id": {c.ID}, "refresh_token": {"x"}, "scope": {"root"}},
		"missing client": {"grant_type": {"authorization_code"}, "code": {"x"}},
	} {
		if w, m := post(alice, "/oauth/token", v); w.Code < 400 || m["error"] == nil {
			t.Errorf("%s: %d %v", name, w.Code, m)
		}
	}
	// parameters belong in the body, not in the address
	r := httptest.NewRequest("POST", "http://"+alice+"/oauth/token?grant_type=authorization_code&client_id="+c.ID, nil)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, r)
	if rec.Code < 400 || strings.Contains(rec.Body.String(), "access_token") {
		t.Errorf("query parameters: %d", rec.Code)
	}
	if w := doWith(s, "POST", alice, "/oauth/token", "", "application/json", "", `{"grant_type":"authorization_code"}`); w.Code != 400 {
		t.Errorf("json body: %d", w.Code)
	}
	if w := doWith(s, "GET", alice, "/oauth/token", "", "", "", ""); w.Code != 405 {
		t.Errorf("GET: %d", w.Code)
	}

	// revoke: the app ends its own grant; an unknown token is no error and tells nothing
	_, m = exchange(newCode(), nil)
	acc := m["access_token"].(string)
	if w, _ := post(alice, "/oauth/revoke", url.Values{"token": {"wat_nothing"}, "client_id": {c.ID}}); w.Code != 200 {
		t.Errorf("revoking an unknown token: %d", w.Code)
	}
	post(alice, "/oauth/revoke", url.Values{"token": {acc}, "client_id": {other.ID}}) // another app cannot
	if _, err := s.st.OAuthAccess(acc); err != nil {
		t.Errorf("another app revoked it: %v", err)
	}
	if w, _ := post(alice, "/oauth/revoke", url.Values{"token": {acc}, "client_id": {c.ID}}); w.Code != 200 {
		t.Errorf("revoke: %d", w.Code)
	}
	if _, err := s.st.OAuthAccess(acc); err == nil {
		t.Error("a revoked token works")
	}
	if w, _ := post(alice, "/oauth/revoke", url.Values{"token": {acc}, "client_id": {"wc_nope"}}); w.Code != 401 {
		t.Errorf("revoke by an unknown app: %d", w.Code)
	}

	// off: neither exists
	s.cfg.OAuth = false
	if w, _ := post(alice, "/oauth/token", url.Values{"grant_type": {"refresh_token"}}); w.Code != 404 {
		t.Errorf("token with OAuth off: %d", w.Code)
	}
	if w, _ := post(alice, "/oauth/revoke", url.Values{"token": {"x"}}); w.Code != 404 {
		t.Errorf("revoke with OAuth off: %d", w.Code)
	}
}

// --- scopes on the API ---

// tokenWith makes an access token for the user with the scope, as the token endpoint would.
func tokenWith(t *testing.T, s *Server, c *store.OAuthClient, user, scope string) string {
	t.Helper()
	g, err := s.st.CreateOAuthGrant(c.ID, user, scope)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := s.st.NewOAuthCode(g, "https://app.example/cb", pkceChallenge)
	if _, err := s.st.RedeemOAuthCode(code); err != nil {
		t.Fatal(err)
	}
	tk, err := s.st.IssueOAuthTokens(g)
	if err != nil {
		t.Fatal(err)
	}
	return tk.Access
}

// Every address of the API names the scope it needs, a token with that scope gets in, a token with any other does not,
// and a token of another account, or the account's own token, is judged as before.
func TestScopesOnTheAPI(t *testing.T) {
	s, static := setup(t)
	s.cfg.OAuth = true
	if _, err := s.st.AddUser("bob", "Bob", ""); err != nil {
		t.Fatal(err)
	}
	c, _ := s.st.AddOAuthClient(store.OAuthClient{Name: "Quill", RedirectURIs: []string{"https://app.example/cb"}})
	routes := apiRoutes(s)
	if len(routes) < 17 {
		t.Fatalf("only %d routes", len(routes))
	}
	valid := map[string]bool{}
	for _, sc := range oauth.Scopes {
		valid[sc.Name] = true
	}
	// a token for each scope in turn; one of the others never goes in
	for _, a := range routes {
		if !valid[a.scope] {
			t.Errorf("%s names the scope %q, which does not exist", a.route, a.scope)
		}
	}
	method := func(route string) (string, string) {
		m, p, _ := strings.Cut(route, " ")
		return m, strings.NewReplacer("{ref}", "1", "{id}", "1", "{slug}", "1", "{$}", "").Replace(p)
	}
	only := map[string]string{}
	for _, sc := range oauth.Scopes {
		app, _ := s.st.AddOAuthClient(store.OAuthClient{Name: sc.Name, RedirectURIs: []string{"https://app.example/cb"}}) // one grant per app and account
		only[sc.Name] = tokenWith(t, s, app, "alice", sc.Name)
	}
	for _, a := range routes {
		m, p := method(a.route)
		for name, tok := range only {
			w := doWith(s, m, alice, p, tok, "", "", "")
			if name == a.scope && (w.Code == 401 || w.Code == 403) {
				t.Errorf("%s with the %s scope: %d %s", a.route, name, w.Code, w.Body)
			}
			if name != a.scope && (w.Code != 403 || !strings.Contains(w.Header().Get("WWW-Authenticate"), `error="insufficient_scope"`) ||
				!strings.Contains(w.Header().Get("WWW-Authenticate"), `scope="`+a.scope+`"`)) {
				t.Errorf("%s with only the %s scope: %d %q", a.route, name, w.Code, w.Header().Get("WWW-Authenticate"))
			}
		}
		// the account's own token is not limited by scopes
		if w := doWith(s, m, alice, p, static, "", "", ""); w.Code == 401 || w.Code == 403 {
			t.Errorf("%s with the account's token: %d", a.route, w.Code)
		}
	}

	// a token with several scopes
	multi := tokenWith(t, s, c, "alice", "read post")
	if w := doWith(s, "GET", alice, "/api/posts", multi, "", "", ""); w.Code != 200 {
		t.Errorf("read+post reading: %d", w.Code)
	}
	if w := doWith(s, "POST", alice, "/api/posts", multi, "text/markdown", "", "hello"); w.Code != 201 {
		t.Errorf("read+post writing: %d %s", w.Code, w.Body)
	}
	if w := doWith(s, "POST", alice, "/api/likes", multi, "application/json", "", "{}"); w.Code != 403 {
		t.Errorf("read+post liking: %d", w.Code)
	}

	// refused as unauthenticated: no token, a wrong one, another account's, a made-up one that looks like one
	bobTok := tokenWith(t, s, c, "bob", "read")
	for name, tok := range map[string]string{"none": "", "wrong": "wat_" + strings.Repeat("x", 43), "another account": bobTok, "refresh token": "wrt_" + strings.Repeat("x", 43)} {
		w := doWith(s, "GET", alice, "/api/posts", tok, "", "", "")
		if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), `resource_metadata="http://alice.localhost:8080/.well-known/oauth-protected-resource"`) {
			t.Errorf("%s: %d %q", name, w.Code, w.Header().Get("WWW-Authenticate"))
		}
	}
	if w := doWith(s, "GET", alice, "/api/posts", "", "", "", ""); strings.Contains(w.Header().Get("WWW-Authenticate"), "error=") {
		t.Error("a call with no token says its token is invalid")
	}

	// a revoked grant, and an expired token
	if err := s.st.RevokeOAuthGrants("alice"); err != nil {
		t.Fatal(err)
	}
	if w := doWith(s, "GET", alice, "/api/posts", multi, "", "", ""); w.Code != 401 {
		t.Errorf("revoked: %d", w.Code)
	}
	if w := doWith(s, "GET", alice, "/api/posts", static, "", "", ""); w.Code != 200 {
		t.Errorf("revoking the apps closed the account's own token: %d", w.Code)
	}

	// with OAuth off, a token issued earlier is not accepted, and the answer does not mention OAuth
	again := tokenWith(t, s, c, "alice", "read")
	s.cfg.OAuth = false
	w := doWith(s, "GET", alice, "/api/posts", again, "", "", "")
	if w.Code != 401 || strings.Contains(w.Header().Get("WWW-Authenticate"), "resource_metadata") {
		t.Errorf("OAuth off: %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	if w := doWith(s, "GET", alice, "/api/posts", static, "", "", ""); w.Code != 200 {
		t.Errorf("OAuth off, account token: %d", w.Code)
	}
	s.cfg.OAuth = true
	if w := doWith(s, "GET", alice, "/api/posts", again, "", "", ""); w.Code != 200 {
		t.Errorf("OAuth on again, the earlier token: %d", w.Code)
	}
}

// A route that was added without a scope is closed to OAuth tokens, not open to them.
func TestRouteWithoutScopeIsClosedToApps(t *testing.T) {
	s, _ := setup(t)
	s.cfg.OAuth = true
	c, _ := s.st.AddOAuthClient(store.OAuthClient{Name: "Quill", RedirectURIs: []string{"https://app.example/cb"}})
	tok := tokenWith(t, s, c, "alice", "read post follow like moderate")
	s.mux.HandleFunc("GET /api/forgotten", s.user(func(w http.ResponseWriter, r *http.Request, q *req) {
		if !s.apiAuth(w, r, q) {
			return
		}
		w.Write([]byte("secret"))
	}))
	if w := doWith(s, "GET", alice, "/api/forgotten", tok, "", "", ""); w.Code != 403 || strings.Contains(w.Body.String(), "secret") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	defer func() {
		if recover() == nil {
			t.Error("a route with no scope was accepted")
		}
	}()
	s.api("", "GET /api/another", func(http.ResponseWriter, *http.Request, *req) {})
}

// --- connected apps ---

func TestConnectedAppsOnTheProfile(t *testing.T) {
	s, static := setup(t)
	s.cfg.OAuth = true
	s.space("alice").SetPassword("correct horse")
	if _, err := s.st.AddUser("bob", "Bob", ""); err != nil {
		t.Fatal(err)
	}
	cookie := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))
	quill, _ := s.st.AddOAuthClient(store.OAuthClient{Name: "<i>Quill</i>", RedirectURIs: []string{"https://app.example/cb"}, ClientURI: "https://quill.example/"})
	other, _ := s.st.AddOAuthClient(store.OAuthClient{Name: "Other", RedirectURIs: []string{"https://app.example/cb"}})

	if page := form(s, "GET", "/profile", cookie, nil, "").Body.String(); strings.Contains(page, "Connected apps") {
		t.Error("the section shows with no apps")
	}
	tok := tokenWith(t, s, quill, "alice", "read post")
	bobTok := tokenWith(t, s, other, "bob", "read")
	page := form(s, "GET", "/profile", cookie, nil, "").Body.String()
	for _, want := range []string{"Connected apps", "&lt;i&gt;Quill&lt;/i&gt;", "quill.example", "Read your posts", "Write, change and delete", "last used not yet", `action="/profile/apps/revoke"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the profile lacks %q", want)
		}
	}
	if strings.Contains(page, "Other") || strings.Contains(page, "Follow and unfollow") || strings.Contains(page, "<i>Quill") {
		t.Error("another account's app, a scope not granted, or unescaped markup is on the page")
	}
	grants, _ := s.st.OAuthGrants("alice")
	id := strconv.FormatInt(grants[0].ID, 10)

	// disconnecting needs the owner, and cannot reach another account's app
	if w := form(s, "POST", "/profile/apps/revoke", "", url.Values{"id": {id}}, ""); w.Code == 200 || w.Code == 303 && w.Header().Get("Location") == "/profile#apps" {
		if _, err := s.st.OAuthAccess(tok); err != nil {
			t.Errorf("signed out, the app was disconnected: %d", w.Code)
		}
	}
	bobGrants, _ := s.st.OAuthGrants("bob")
	form(s, "POST", "/profile/apps/revoke", cookie, url.Values{"id": {strconv.FormatInt(bobGrants[0].ID, 10)}}, "")
	if _, err := s.st.OAuthAccess(bobTok); err != nil {
		t.Errorf("alice disconnected bob's app: %v", err)
	}
	if w := form(s, "POST", "/profile/apps/revoke", cookie, url.Values{"id": {id}}, "https://evil.example"); w.Code != 403 {
		t.Errorf("cross-origin: %d", w.Code)
	}
	if _, err := s.st.OAuthAccess(tok); err != nil {
		t.Fatalf("still connected, but: %v", err)
	}
	if w := form(s, "POST", "/profile/apps/revoke", cookie, url.Values{"id": {id}}, "http://"+host); w.Code != 303 {
		t.Errorf("disconnect: %d", w.Code)
	}
	if _, err := s.st.OAuthAccess(tok); err == nil {
		t.Error("a disconnected app still works")
	}
	if w := doWith(s, "GET", alice, "/api/posts", static, "", "", ""); w.Code != 200 {
		t.Errorf("the account's own token: %d", w.Code)
	}
	if w := form(s, "POST", "/profile/apps/revoke", cookie, url.Values{"id": {"junk"}}, ""); w.Code != 303 {
		t.Errorf("junk id: %d", w.Code)
	}
}

// A new password ends what apps were allowed under the old one; the account's own token is not touched.
func TestPasswordChangeDisconnectsApps(t *testing.T) {
	s, static := setup(t)
	s.cfg.OAuth = true
	s.space("alice").SetPassword("correct horse")
	cookie := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))
	c, _ := s.st.AddOAuthClient(store.OAuthClient{Name: "Quill", RedirectURIs: []string{"https://app.example/cb"}})
	tok := tokenWith(t, s, c, "alice", "read")

	if w := form(s, "POST", "/password", cookie, url.Values{"current": {"wrong"}, "new": {"another horse"}, "again": {"another horse"}}, ""); w.Code != 400 {
		t.Fatalf("wrong current password: %d", w.Code)
	}
	if _, err := s.st.OAuthAccess(tok); err != nil {
		t.Fatalf("a failed change disconnected the app: %v", err)
	}
	if w := form(s, "POST", "/password", cookie, url.Values{"current": {"correct horse"}, "new": {"another horse"}, "again": {"another horse"}}, ""); w.Code != 303 {
		t.Fatalf("change: %d", w.Code)
	}
	if _, err := s.st.OAuthAccess(tok); err == nil {
		t.Error("the app survived a password change")
	}
	if w := doWith(s, "GET", alice, "/api/posts", static, "", "", ""); w.Code != 200 {
		t.Errorf("the account's token after a password change: %d", w.Code)
	}
}

func TestPruningRunsOncePerHour(t *testing.T) {
	s, _ := setup(t)
	s.pruneOAuth()
	first := s.oauthPruned
	if first.IsZero() {
		t.Fatal("did not run")
	}
	s.pruneOAuth()
	if !s.oauthPruned.Equal(first) {
		t.Error("ran twice within the hour")
	}
}

// --- logos ---

func TestAppLogoIsKeptHereAndServedFromHere(t *testing.T) {
	var hits atomic.Int32
	var logo []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/logo.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(logo)
		case "/text.png":
			w.Write([]byte("<html>not a picture</html>"))
		case "/huge.png":
			w.Write(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 3<<20)...))
		case "/slow.png":
			time.Sleep(7 * time.Second)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	// a 300 x 200 picture with some transparency, as a logo might be
	pic := image.NewRGBA(image.Rect(0, 0, 300, 200))
	for x := 0; x < 300; x++ {
		for y := 0; y < 200; y++ {
			pic.Set(x, y, color.RGBA{200, 30, 60, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, pic)
	logo = buf.Bytes()

	s := oauthServer(t, true)
	s.web = fetch.New(true) // the test's picture is on this computer
	s.space("alice").SetPassword("correct horse")
	cookie := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))
	// the test's server speaks http, which registration refuses for a logo; the same code runs from keepLogo
	register := func(logoURI string) map[string]any {
		c, err := s.st.AddOAuthClient(store.OAuthClient{Name: "Quill", RedirectURIs: []string{"https://app.example/cb"}, LogoURI: logoURI})
		if err != nil {
			t.Fatal(err)
		}
		s.keepLogo(context.Background(), c)
		return map[string]any{"client_id": c.ID, "logo_uri": logoURI}
	}

	m := register(srv.URL + "/logo.png")
	id := m["client_id"].(string)
	if m["logo_uri"] == nil {
		t.Error("the logo address is not echoed")
	}
	c, _ := s.st.OAuthClient(id)
	if !c.HasLogo {
		t.Fatal("the logo was not kept")
	}
	w := doWith(s, "GET", alice, "/oauth/logo/"+id, "", "", "", "")
	cfg, format, err := image.DecodeConfig(w.Body) // an opaque picture is kept as a JPEG
	if w.Code != 200 || err != nil || format != "jpeg" || cfg.Width != 96 || cfg.Height != 96 || w.Header().Get("Content-Type") != "image/jpeg" ||
		w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("the logo: %d %v %dx%d %v", w.Code, err, cfg.Width, cfg.Height, w.Header())
	}

	// the consent page shows it from here, never from the app, and says it is unverified
	hits.Store(0)
	q := url.Values{"response_type": {"code"}, "client_id": {id}, "redirect_uri": {"https://app.example/cb"}, "code_challenge": {pkceChallenge}, "code_challenge_method": {"S256"}, "scope": {"read"}}
	w = form(s, "GET", "/oauth/authorize?"+q.Encode(), cookie, nil, "")
	page := w.Body.String()
	if !strings.Contains(page, `src="/oauth/logo/`+id+`"`) || strings.Contains(page, srv.URL) || !strings.Contains(page, "Unverified app") || hits.Load() != 0 {
		t.Errorf("consent page: hits %d\n%s", hits.Load(), page)
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "img-src 'self'") {
		t.Error("the page may load pictures from elsewhere")
	}
	tokenWith(t, s, c, "alice", "read")
	if page := form(s, "GET", "/profile", cookie, nil, "").Body.String(); !strings.Contains(page, `src="/oauth/logo/`+id+`"`) {
		t.Error("the connected apps list lacks the logo")
	}

	// a logo that cannot be used is dropped, and the app is registered all the same
	for name, u := range map[string]string{
		"not a picture": srv.URL + "/text.png", "too big": srv.URL + "/huge.png", "missing": srv.URL + "/none.png",
		"unreachable": "http://127.0.0.1:1/logo.png", "slow": srv.URL + "/slow.png",
	} {
		if name == "slow" && testing.Short() {
			continue
		}
		start := time.Now()
		bad := register(u)["client_id"].(string)
		if c, _ := s.st.OAuthClient(bad); c.HasLogo {
			t.Errorf("%s: a logo was kept", name)
		}
		if w := doWith(s, "GET", alice, "/oauth/logo/"+bad, "", "", "", ""); w.Code != 404 {
			t.Errorf("%s: the logo answers %d", name, w.Code)
		}
		if time.Since(start) > 6*time.Second {
			t.Errorf("%s: registration waited %v", name, time.Since(start))
		}
	}
	if w := doWith(s, "GET", alice, "/oauth/logo/wc_nope", "", "", "", ""); w.Code != 404 {
		t.Errorf("unknown app: %d", w.Code)
	}
	// an app with no logo address makes no fetch
	hits.Store(0)
	body := `{"client_name":"Plain","redirect_uris":["https://app.example/cb"]}`
	if w := doWith(s, "POST", alice, "/oauth/register", "", "application/json", "", body); w.Code != 201 || hits.Load() != 0 {
		t.Errorf("no logo: %d, %d fetches", w.Code, hits.Load())
	}
	// off: no logos either
	s.cfg.OAuth = false
	if w := doWith(s, "GET", alice, "/oauth/logo/"+id, "", "", "", ""); w.Code != 404 {
		t.Errorf("logo with OAuth off: %d", w.Code)
	}
}

// Through /oauth/register itself: a logo that cannot be fetched does not stop the app registering, and the fetch
// does not reach private addresses.
func TestRegistrationWithALogoThatCannotBeFetched(t *testing.T) {
	s := oauthServer(t, true) // the server's fetcher does not reach private addresses
	for _, u := range []string{"https://127.0.0.1:1/logo.png", "https://localhost/logo.png", "https://nothing.invalid/logo.png"} {
		body, _ := json.Marshal(map[string]any{"client_name": "Quill", "redirect_uris": []string{"https://app.example/cb"}, "logo_uri": u})
		w := doWith(s, "POST", alice, "/oauth/register", "", "application/json", "", string(body))
		var m map[string]any
		json.Unmarshal(w.Body.Bytes(), &m)
		if w.Code != 201 {
			t.Fatalf("%s: %d %v", u, w.Code, m)
		}
		if c, _ := s.st.OAuthClient(m["client_id"].(string)); c.HasLogo {
			t.Errorf("%s: a logo was kept", u)
		}
	}
}
