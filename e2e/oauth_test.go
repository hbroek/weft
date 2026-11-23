package e2e

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hbroek/weft/internal/web"
)

// A Markdown editor connects to Alice's site the way a real app does, over real HTTP: it finds the endpoints, registers,
// sends Alice to the consent page in her browser, swaps the code for tokens, edits a post as a document, is refused what
// it was not allowed, renews its tokens, and is disconnected by Alice.
func TestMarkdownEditorConnectsWithOAuth(t *testing.T) {
	i := startWith(t, web.Config{BaseDomain: "localhost", AllowPrivate: true, OAuth: true}, "a", "alice")
	site := i.client("alice")
	noRedirects := func(c *client) *client {
		h := *c.HTTP
		h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return &client{Base: c.Base, HTTP: &h}
	}
	send := func(c *client, method, path string, hdr map[string]string, body string) (int, http.Header, string) {
		t.Helper()
		req, _ := http.NewRequest(method, c.Base+path, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header, string(b)
	}
	const form = "application/x-www-form-urlencoded"
	api := func(tok, method, path, ct, accept, body string) (int, http.Header, string) {
		return send(site, method, path, map[string]string{"Authorization": "Bearer " + tok, "Content-Type": ct, "Accept": accept}, body)
	}

	// 1. the editor finds the server
	code, _, body := send(site, "GET", "/.well-known/oauth-authorization-server", nil, "")
	var meta map[string]any
	if code != 200 || json.Unmarshal([]byte(body), &meta) != nil || meta["issuer"] != site.Base {
		t.Fatalf("metadata: %d %s", code, body)
	}

	// 2. it registers, with a loopback address, as a desktop editor does
	code, _, body = send(site, "POST", "/oauth/register", map[string]string{"Content-Type": "application/json"},
		`{"client_name":"Quill Editor","redirect_uris":["http://127.0.0.1/callback"],"token_endpoint_auth_method":"none"}`)
	var reg struct {
		ClientID string `json:"client_id"`
	}
	if code != 201 || json.Unmarshal([]byte(body), &reg) != nil || reg.ClientID == "" {
		t.Fatalf("register: %d %s", code, body)
	}

	// 3. Alice's browser: the editor sends her to the consent page; she is not signed in, so she signs in and is brought back
	verifier := "e2e-verifier-" + strings.Repeat("v", 40)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	redirect := "http://127.0.0.1:53124/callback"
	authz := "/oauth/authorize?" + url.Values{"response_type": {"code"}, "client_id": {reg.ClientID}, "redirect_uri": {redirect},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "scope": {"read post"}, "state": {"xyz 123"}}.Encode()

	browser := noRedirects(newClient(site.Base, ""))
	browser.HTTP.Jar = newJar()
	code, hdr, _ := send(browser, "GET", authz, nil, "")
	if code != 303 || !strings.HasPrefix(hdr.Get("Location"), "/login?next=") {
		t.Fatalf("not signed in: %d %q", code, hdr.Get("Location"))
	}
	next, _ := url.Parse(hdr.Get("Location"))
	code, hdr, _ = send(browser, "POST", "/login", map[string]string{"Content-Type": form, "Origin": site.Base},
		url.Values{"password": {"password-alice"}, "next": {next.Query().Get("next")}}.Encode())
	if code != 303 || hdr.Get("Location") != authz {
		t.Fatalf("login: %d %q", code, hdr.Get("Location"))
	}
	code, _, page := send(browser, "GET", authz, nil, "")
	if code != 200 || !strings.Contains(page, "Connect Quill Editor") || !strings.Contains(page, "Unverified app") {
		t.Fatalf("consent page: %d\n%s", code, page)
	}
	fields := url.Values{}
	for _, m := range regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)"`).FindAllStringSubmatch(page, -1) {
		fields.Set(m[1], html.UnescapeString(m[2]))
	}
	fields.Set("decision", "allow")
	code, _, page = send(browser, "POST", "/oauth/authorize", map[string]string{"Content-Type": form, "Origin": site.Base}, fields.Encode())
	m := regexp.MustCompile(`http-equiv="refresh" content="0;url=([^"]+)"`).FindStringSubmatch(page)
	if code != 200 || m == nil {
		t.Fatalf("allow: %d\n%s", code, page)
	}
	back, _ := url.Parse(html.UnescapeString(m[1]))
	if back.Host != "127.0.0.1:53124" || back.Path != "/callback" || back.Query().Get("state") != "xyz 123" || back.Query().Get("iss") != site.Base {
		t.Fatalf("sent back to %v", back)
	}

	// 4. the editor swaps the code, with no cookies: only the verifier proves it is the app that asked
	exchange := func(v url.Values) (int, map[string]any) {
		code, _, body := send(site, "POST", "/oauth/token", map[string]string{"Content-Type": form}, v.Encode())
		var m map[string]any
		json.Unmarshal([]byte(body), &m)
		return code, m
	}
	if code, m := exchange(url.Values{"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "client_id": {reg.ClientID},
		"redirect_uri": {redirect}, "code_verifier": {"another-verifier-" + strings.Repeat("w", 30)}}); code != 400 || m["error"] != "invalid_grant" {
		t.Fatalf("a wrong verifier: %d %v", code, m)
	}
	// that attempt used the code up
	code2, m2 := exchange(url.Values{"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "client_id": {reg.ClientID}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if code2 != 400 {
		t.Fatalf("a used code worked: %v", m2)
	}
	// so Alice is sent through again (her browser is signed in, so it is one click)
	_, _, page = send(browser, "POST", "/oauth/authorize", map[string]string{"Content-Type": form, "Origin": site.Base}, fields.Encode())
	m = regexp.MustCompile(`http-equiv="refresh" content="0;url=([^"]+)"`).FindStringSubmatch(page)
	back, _ = url.Parse(html.UnescapeString(m[1]))
	code, tok := exchange(url.Values{"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "client_id": {reg.ClientID}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if code != 200 || tok["scope"] != "read post" {
		t.Fatalf("token: %d %v", code, tok)
	}
	access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)

	// 5. the editor works on a post as a document
	const md = "text/markdown"
	code, _, body = api(access, "POST", "/api/posts", md, "", "---\ntitle: From the editor\nkeywords: [notes]\n---\nFirst *draft*.\n")
	var created posted
	if code != 201 || json.Unmarshal([]byte(body), &created) != nil || created.ID == 0 {
		t.Fatalf("create: %d %s", code, body)
	}
	ref := strconv.FormatInt(created.ID, 10)
	code, _, doc := api(access, "GET", "/api/posts/"+ref, "", md, "")
	if code != 200 || !strings.Contains(doc, "title: From the editor") || !strings.HasSuffix(doc, "First *draft*.\n") {
		t.Fatalf("open: %d\n%s", code, doc)
	}
	code, _, _ = api(access, "PUT", "/api/posts/"+ref, md, "", strings.Replace(doc, "First *draft*.", "Second draft.", 1))
	if code != 200 {
		t.Fatalf("save: %d", code)
	}
	if _, _, doc = api(access, "GET", "/api/posts/"+ref, "", md, ""); !strings.Contains(doc, "Second draft.") {
		t.Errorf("the edit is not there:\n%s", doc)
	}
	if code, _, body = api(access, "GET", "/api/posts", "", "", ""); code != 200 || !strings.Contains(body, "From the editor") {
		t.Errorf("list: %d %s", code, body)
	}
	if !strings.Contains(get(t, site, "/posts/"+created.URL[strings.LastIndex(created.URL, "/")+1:]), "Second draft.") {
		t.Error("the public page does not show the edit")
	}

	// 6. what it was not allowed is refused, and says what was missing
	code, hdr, _ = api(access, "POST", "/api/likes", "application/json", "", `{"target":"https://x.example/a"}`)
	if code != 403 || !strings.Contains(hdr.Get("WWW-Authenticate"), `error="insufficient_scope"`) {
		t.Errorf("a like without the scope: %d %q", code, hdr.Get("WWW-Authenticate"))
	}
	if code, hdr, _ = api("", "GET", "/api/posts", "", "", ""); code != 401 || !strings.Contains(hdr.Get("WWW-Authenticate"), "/.well-known/oauth-protected-resource") {
		t.Errorf("no token: %d %q", code, hdr.Get("WWW-Authenticate"))
	}

	// 7. renewing: a new pair, and the old access token is gone
	code, tok = exchange(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {reg.ClientID}})
	if code != 200 || tok["access_token"] == access {
		t.Fatalf("refresh: %d %v", code, tok)
	}
	if code, _, _ = api(access, "GET", "/api/posts", "", "", ""); code != 401 {
		t.Errorf("the old access token: %d", code)
	}
	access = tok["access_token"].(string)
	if code, _, _ = api(access, "GET", "/api/posts", "", "", ""); code != 200 {
		t.Errorf("the new access token: %d", code)
	}

	// 8. Alice sees the app on her profile and disconnects it; her own token is untouched
	_, _, profile := send(browser, "GET", "/profile", nil, "")
	if !strings.Contains(profile, "Quill Editor") || !strings.Contains(profile, "Disconnect") {
		t.Fatalf("the profile does not list the app:\n%s", profile)
	}
	grants, _ := i.st.OAuthGrants("alice")
	if len(grants) != 1 {
		t.Fatalf("%d grants", len(grants))
	}
	if code, _, _ = send(browser, "POST", "/profile/apps/revoke", map[string]string{"Content-Type": form, "Origin": site.Base}, url.Values{"id": {strconv.FormatInt(grants[0].ID, 10)}}.Encode()); code != 303 {
		t.Fatalf("disconnect: %d", code)
	}
	if code, _, _ = api(access, "GET", "/api/posts", "", "", ""); code != 401 {
		t.Errorf("a disconnected app: %d", code)
	}
	if code, _, _ = api(refresh, "GET", "/api/posts", "", "", ""); code == 200 {
		t.Error("a refresh token works as an access token")
	}
	if code, _, _ = api(i.toks["alice"], "GET", "/api/posts", "", "", ""); code != 200 {
		t.Errorf("Alice's own token: %d", code)
	}
}

// Without -oauth none of it exists.
func TestNoOAuthWhenTheOperatorLeavesItOff(t *testing.T) {
	i := start(t, "a", "alice")
	site := i.client("alice")
	for _, p := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource", "/oauth/authorize", "/oauth/logo/x"} {
		res, err := site.HTTP.Get(site.Base + p)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Errorf("%s: %d", p, res.StatusCode)
		}
	}
	res, err := site.HTTP.Post(site.Base+"/oauth/register", "application/json", strings.NewReader(`{"client_name":"A","redirect_uris":["https://a.example/cb"]}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("register: %d", res.StatusCode)
	}
}

func newJar() http.CookieJar {
	jar, _ := cookiejar.New(nil)
	return jar
}
