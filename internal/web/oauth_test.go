package web

import (
	"encoding/json"
	"path/filepath"
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
	w := doWith(s, "POST", alice, "/oauth/register", "", "application/json", "", `{"client_name":"A","redirect_uris":["https://a.example/cb"]}`)
	if w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}
