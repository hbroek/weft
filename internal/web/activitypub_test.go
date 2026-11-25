package web

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func apGet(s *Server, host, path, accept string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://"+host+path, nil)
	if accept != "" {
		r.Header.Set("Accept", accept)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestWantsActivityJSON(t *testing.T) {
	for accept, want := range map[string]bool{
		"application/activity+json":                                            true,
		"application/activity+json, application/ld+json":                       true,
		`application/ld+json; profile="https://www.w3.org/ns/activitystreams"`: true,
		"application/ld+json":                                                  false,
		"text/html,application/xhtml+xml,*/*;q=0.8":                            false,
		"*/*":                           false,
		"text/markdown":                 false,
		"application/activity+json;q=0": false,
		"":                              false,
	} {
		if got := wantsActivityJSON(accept); got != want {
			t.Errorf("%q: %v", accept, got)
		}
	}
}

func TestActivityPubIsOffUnlessTheOperatorAndTheAccountSayYes(t *testing.T) {
	s, _ := setup(t) // the operator has not asked for it
	cookie := ownerCookie(t, s, "alice")
	if w := send(s, "POST", alice, "/profile/activitypub", cookie, url.Values{"enable": {"1"}}); w.Code != 404 {
		t.Errorf("switch with the operator's flag off: %d", w.Code)
	}
	if strings.Contains(send(s, "GET", alice, "/profile", cookie, nil).Body.String(), "Fediverse") {
		t.Errorf("the profile offers ActivityPub that is not on")
	}
	s.cfg.ActivityPub = true
	for _, p := range []string{"/.well-known/webfinger?resource=acct:alice@alice.localhost", "/.well-known/nodeinfo", "/nodeinfo/2.1", "/outbox"} {
		if w := apGet(s, alice, p, ""); w.Code != 404 {
			t.Errorf("%s before the account switches it on: %d", p, w.Code)
		}
	}
	if w := apGet(s, alice, "/", activityJSON); !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Errorf("an account that did not switch it on answers an actor request with %q", w.Header().Get("Content-Type"))
	}
	if w := send(s, "POST", alice, "/profile/activitypub", "", url.Values{"enable": {"1"}}); w.Code == 303 && w.Header().Get("Location") != "/login" {
		t.Errorf("a visitor switched it on: %d %s", w.Code, w.Header().Get("Location"))
	}
	if _, err := s.space("alice").APAccount(s.space("alice").UID()); err == nil {
		t.Errorf("a visitor made an identity")
	}
}

func TestAnAccountIsFoundFromTheFediverse(t *testing.T) {
	s, tok := setup(t)
	s.cfg.ActivityPub = true
	do(s, "POST", alice, "/api/posts", tok, `{"markdown":"hello"}`)
	cookie := ownerCookie(t, s, "alice")
	if w := send(s, "POST", alice, "/profile/activitypub", cookie, url.Values{"enable": {"1"}}); w.Code != 303 {
		t.Fatalf("switch on: %d %s", w.Code, w.Body)
	}
	id := "http://" + alice + "/"

	w := apGet(s, alice, "/", "application/activity+json, application/ld+json")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/activity+json") || !strings.Contains(strings.Join(w.Header().Values("Vary"), ","), "Accept") {
		t.Fatalf("actor: %d %v", w.Code, w.Header())
	}
	var a struct {
		Context           []string `json:"@context"`
		ID, Type, Name    string
		PreferredUsername string
		Summary           string
		Inbox, Outbox     string
		PublicKey         struct{ ID, Owner, PublicKeyPem string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatalf("%v\n%s", err, w.Body)
	}
	if a.ID != id || a.Type != "Person" || a.PreferredUsername != "alice" || a.Name != "Alice" || a.Inbox != id+"inbox" || a.Outbox != id+"outbox" ||
		a.PublicKey.ID != id+"#main-key" || a.PublicKey.Owner != id || !strings.Contains(a.Summary, "Notes on the open web.") || len(a.Context) != 2 {
		t.Errorf("actor: %+v", a)
	}
	blk, _ := pem.Decode([]byte(a.PublicKey.PublicKeyPem))
	if pub, err := x509.ParsePKIXPublicKey(blk.Bytes); err != nil {
		t.Errorf("key: %v", err)
	} else if _, ok := pub.(*rsa.PublicKey); !ok {
		t.Errorf("key is %T", pub)
	}
	if strings.Contains(w.Body.String(), "PRIVATE") {
		t.Errorf("the actor shows a private key")
	}
	// the same address still answers a browser, and the markdown reader, as before
	if h := apGet(s, alice, "/", "text/html,application/xhtml+xml,*/*;q=0.8"); !strings.HasPrefix(h.Header().Get("Content-Type"), "text/html") {
		t.Errorf("browser: %q", h.Header().Get("Content-Type"))
	}
	if h := apGet(s, alice, "/", "text/markdown"); !strings.HasPrefix(h.Header().Get("Content-Type"), "text/markdown") {
		t.Errorf("markdown: %q", h.Header().Get("Content-Type"))
	}

	// WebFinger: the handle with or without the port, and the actor's own address; nothing else
	for _, res := range []string{"acct:alice@alice.localhost:8080", "acct:alice@alice.localhost", "acct:Alice@alice.localhost", id, strings.TrimSuffix(id, "/")} {
		f := apGet(s, alice, "/.well-known/webfinger?resource="+url.QueryEscape(res), "")
		var j struct {
			Subject string
			Aliases []string
			Links   []struct{ Rel, Type, Href string }
		}
		if f.Code != 200 || !strings.HasPrefix(f.Header().Get("Content-Type"), "application/jrd+json") || f.Header().Get("Access-Control-Allow-Origin") != "*" ||
			json.Unmarshal(f.Body.Bytes(), &j) != nil || j.Subject != res || len(j.Aliases) != 1 || j.Aliases[0] != id || len(j.Links) != 2 || j.Links[0].Rel != "self" || j.Links[0].Type != "application/activity+json" || j.Links[0].Href != id {
			t.Errorf("webfinger %q: %d %v %s", res, f.Code, f.Header(), f.Body)
		}
	}
	for _, res := range []string{"acct:bob@alice.localhost", "acct:alice@elsewhere.example", "mailto:alice@alice.localhost", "acct:alice"} {
		if f := apGet(s, alice, "/.well-known/webfinger?resource="+url.QueryEscape(res), ""); f.Code != 404 {
			t.Errorf("webfinger %q: %d", res, f.Code)
		}
	}
	if f := apGet(s, alice, "/.well-known/webfinger", ""); f.Code != 400 {
		t.Errorf("webfinger without a resource: %d", f.Code)
	}

	// NodeInfo
	n := apGet(s, alice, "/.well-known/nodeinfo", "")
	if n.Code != 200 || !strings.Contains(n.Body.String(), "http://"+alice+"/nodeinfo/2.1") {
		t.Errorf("nodeinfo links: %d %s", n.Code, n.Body)
	}
	n = apGet(s, alice, "/nodeinfo/2.1", "")
	var ni struct {
		Version   string
		Software  struct{ Name string }
		Protocols []string
		Usage     struct{ LocalPosts int }
	}
	if json.Unmarshal(n.Body.Bytes(), &ni) != nil || ni.Version != "2.1" || ni.Software.Name != "weft" || len(ni.Protocols) != 1 || ni.Protocols[0] != "activitypub" || ni.Usage.LocalPosts != 1 {
		t.Errorf("nodeinfo: %d %s", n.Code, n.Body)
	}

	// the outbox is an empty collection, and the inbox wants a signature
	if o := apGet(s, alice, "/outbox", activityJSON); o.Code != 200 || !strings.Contains(o.Body.String(), `"totalItems":0`) {
		t.Errorf("outbox: %d %s", o.Code, o.Body)
	}
	if in := do(s, "POST", alice, "/inbox", "", `{}`); in.Code != 401 { // nobody signed it
		t.Errorf("inbox: %d", in.Code)
	}

	// the profile says so; switching off stops all of it, and switching on again brings the same identity back
	if p := send(s, "GET", alice, "/profile", cookie, nil).Body.String(); !strings.Contains(p, "@alice@alice.localhost") || !strings.Contains(p, "Switch off") {
		t.Errorf("profile: no handle or switch")
	}
	key := a.PublicKey.PublicKeyPem
	send(s, "POST", alice, "/profile/activitypub", cookie, url.Values{"enable": {"0"}})
	if f := apGet(s, alice, "/.well-known/webfinger?resource=acct:alice@alice.localhost", ""); f.Code != 404 {
		t.Errorf("webfinger after switching off: %d", f.Code)
	}
	if h := apGet(s, alice, "/", activityJSON); !strings.HasPrefix(h.Header().Get("Content-Type"), "text/html") {
		t.Errorf("actor after switching off: %q", h.Header().Get("Content-Type"))
	}
	send(s, "POST", alice, "/profile/activitypub", cookie, url.Values{"enable": {"1"}})
	var again struct{ PublicKey struct{ PublicKeyPem string } }
	json.Unmarshal(apGet(s, alice, "/", activityJSON).Body.Bytes(), &again)
	if again.PublicKey.PublicKeyPem != key {
		t.Errorf("the key changed when the account was switched on again")
	}
}

// The actor's address is pinned: when the account's address changes, nothing answers the fediverse under the new one.
func TestActivityPubKeepsItsPinnedAddress(t *testing.T) {
	s, _ := setup(t)
	s.cfg.ActivityPub = true
	cookie := ownerCookie(t, s, "alice")
	send(s, "POST", alice, "/profile/activitypub", cookie, url.Values{"enable": {"1"}})
	if err := s.st.PinDomain("alice", "alice.example.test"); err != nil {
		t.Fatal(err)
	}
	if w := apGet(s, alice, "/", activityJSON); w.Code != 301 || !strings.HasPrefix(w.Header().Get("Location"), "http://alice.example.test") {
		t.Errorf("the old address does not send on to the domain: %d %s", w.Code, w.Header().Get("Location"))
	}
	for _, p := range []string{"/.well-known/webfinger?resource=acct:alice@alice.example.test", "/nodeinfo/2.1", "/outbox"} {
		if w := apGet(s, "alice.example.test", p, ""); w.Code != 404 {
			t.Errorf("%s at the new address: %d", p, w.Code)
		}
	}
	if w := apGet(s, "alice.example.test", "/", activityJSON); !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Errorf("actor at the new address: %q", w.Header().Get("Content-Type"))
	}
	if p := send(s, "GET", alice, "/profile", cookie, nil).Body.String(); !strings.Contains(p, "no longer") {
		t.Errorf("the profile does not warn")
	}
}

// An account known as name@example.com: its WebFinger names that handle, whichever way it is asked, so that servers
// show the handle the owner wants. The domain's own file is what points at the actor; the asking server checks it.
func TestTheHandleCanBeOnAnotherDomain(t *testing.T) {
	s, _ := setup(t)
	s.cfg.ActivityPub = true
	cookie := ownerCookie(t, s, "alice")
	if w := send(s, "POST", alice, "/profile/activitypub/handle", cookie, url.Values{"handle_host": {"example.com"}}); w.Code != 409 {
		t.Errorf("a handle before ActivityPub is on: %d", w.Code)
	}
	send(s, "POST", alice, "/profile/activitypub", cookie, url.Values{"enable": {"1"}})
	subject := func(res string) string {
		f := apGet(s, alice, "/.well-known/webfinger?resource="+url.QueryEscape(res), "")
		var j struct{ Subject string }
		json.Unmarshal(f.Body.Bytes(), &j)
		return j.Subject
	}
	if got := subject("acct:alice@alice.localhost"); got != "acct:alice@alice.localhost" {
		t.Errorf("before a handle domain is set: %q", got)
	}
	for _, bad := range []string{"https://example.com", "example.com:8080", "nodots", "a b.example"} {
		if w := send(s, "POST", alice, "/profile/activitypub/handle", cookie, url.Values{"handle_host": {bad}}); w.Code != 400 {
			t.Errorf("%q was accepted: %d", bad, w.Code)
		}
	}
	if w := send(s, "POST", alice, "/profile/activitypub/handle", "", url.Values{"handle_host": {"example.com"}}); w.Header().Get("Location") != "/login" {
		t.Errorf("a visitor set the handle: %d", w.Code)
	}
	if w := send(s, "POST", alice, "/profile/activitypub/handle", cookie, url.Values{"handle_host": {" Example.COM "}}); w.Code != 303 {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	for _, res := range []string{"acct:alice@alice.localhost", "acct:alice@example.com", "http://" + alice + "/"} {
		if got := subject(res); got != "acct:alice@example.com" {
			t.Errorf("%q: subject %q", res, got)
		}
	}
	if f := apGet(s, alice, "/.well-known/webfinger?resource=acct:alice@other.example", ""); f.Code != 404 {
		t.Errorf("another domain: %d", f.Code)
	}
	if p := send(s, "GET", alice, "/profile", cookie, nil).Body.String(); !strings.Contains(p, "@alice@example.com") {
		t.Errorf("the profile shows another handle")
	}
	send(s, "POST", alice, "/profile/activitypub/handle", cookie, url.Values{"handle_host": {""}})
	if got := subject("acct:alice@alice.localhost"); got != "acct:alice@alice.localhost" {
		t.Errorf("after clearing: %q", got)
	}
}
