package web

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/fetch"
)

var (
	keysOnce sync.Once
	keyA     *rsa.PrivateKey
	keyB     *rsa.PrivateKey
)

func testKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	keysOnce.Do(func() {
		keyA, _ = rsa.GenerateKey(rand.Reader, 2048)
		keyB, _ = rsa.GenerateKey(rand.Reader, 2048)
	})
	return keyA, keyB
}

func pemOf(k *rsa.PrivateKey) string {
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// A remote server with one actor, bob, who signs with whatever key the test hands him, and an inbox that keeps what it gets.
type remote struct {
	srv    *httptest.Server
	mu     sync.Mutex
	key    *rsa.PrivateKey // the key bob's actor document shows
	got    chan *http.Request
	bodies chan []byte
	gets   int
	inbox  string // the inbox the actor names, if not its own server's
	// verifyGet, if set, is asked about every fetch of bob's actor, and a refusal is a 401: a server that answers only
	// signed requests
	verifyGet func(*http.Request) error
}

func newRemote(t *testing.T, key *rsa.PrivateKey) *remote {
	t.Helper()
	r := &remote{key: key, got: make(chan *http.Request, 10), bodies: make(chan []byte, 10)}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		switch {
		case q.Method == "GET" && q.URL.Path == "/users/bob":
			if r.verifyGet != nil {
				if err := r.verifyGet(q); err != nil {
					http.Error(w, err.Error(), 401)
					return
				}
			}
			r.mu.Lock()
			r.gets++
			k := r.key
			r.mu.Unlock()
			w.Header().Set("Content-Type", "application/activity+json")
			fmt.Fprintf(w, `{"id":%q,"type":"Person","preferredUsername":"bob","name":"Bob B","inbox":%q,"endpoints":{"sharedInbox":%q},"publicKey":{"id":%q,"owner":%q,"publicKeyPem":%q}}`,
				r.actor(), r.inboxURL(), r.srv.URL+"/inbox", r.actor()+"#main-key", r.actor(), pemOf(k))
		case q.Method == "POST" && q.URL.Path == "/users/bob/inbox":
			b, _ := io.ReadAll(q.Body)
			r.got <- q
			r.bodies <- b
			w.WriteHeader(202)
		default:
			http.NotFound(w, q)
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *remote) actor() string { return r.srv.URL + "/users/bob" }
func (r *remote) inboxURL() string {
	if r.inbox != "" {
		return r.inbox
	}
	return r.srv.URL + "/users/bob/inbox"
}
func (r *remote) setKey(k *rsa.PrivateKey) {
	r.mu.Lock()
	r.key = k
	r.mu.Unlock()
}

// signedPost builds a request to the account's inbox signed by hand, the way another server does, so that the checks in
// the code under test are not tested against the code that signs for it.
func signedPost(host, path string, body []byte, keyID string, key *rsa.PrivateKey, date time.Time, headers string) *http.Request {
	r := httptest.NewRequest("POST", "http://"+host+path, strings.NewReader(string(body)))
	sum := sha256.Sum256(body)
	digest := "SHA-256=" + base64.StdEncoding.EncodeToString(sum[:])
	d := date.UTC().Format(http.TimeFormat)
	r.Header.Set("Date", d)
	r.Header.Set("Digest", digest)
	r.Header.Set("Content-Type", "application/activity+json")
	var lines []string
	for _, h := range strings.Fields(headers) {
		switch h {
		case "(request-target)":
			lines = append(lines, "(request-target): post "+path)
		case "host":
			lines = append(lines, "host: "+host)
		case "date":
			lines = append(lines, "date: "+d)
		case "digest":
			lines = append(lines, "digest: "+digest)
		}
	}
	h := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	r.Header.Set("Signature", fmt.Sprintf(`keyId=%q,algorithm="rsa-sha256",headers=%q,signature=%q`, keyID, headers, base64.StdEncoding.EncodeToString(sig)))
	return r
}

const allSigned = "(request-target) host date digest"

func inboxServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, _ := setup(t)
	s.cfg.ActivityPub = true
	s.web = fetch.New(true) // the other server in these tests is on this computer
	s.apBackoff = []time.Duration{0, 0}
	cookie := ownerCookie(t, s, "alice")
	if w := send(s, "POST", alice, "/profile/activitypub", cookie, url.Values{"enable": {"1"}}); w.Code != 303 {
		t.Fatalf("switch on: %d", w.Code)
	}
	return s, cookie
}

func deliver(s *Server, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func followBody(rem *remote, id string) []byte {
	b, _ := json.Marshal(map[string]any{"@context": "https://www.w3.org/ns/activitystreams", "id": rem.srv.URL + "/follows/" + id, "type": "Follow", "actor": rem.actor(), "object": "http://" + alice + "/"})
	return b
}

func (rem *remote) post(s *Server, body []byte, key *rsa.PrivateKey) *httptest.ResponseRecorder {
	return deliver(s, signedPost(alice, "/inbox", body, rem.actor()+"#main-key", key, time.Now(), allSigned))
}

func followerCount(s *Server) int {
	sp := s.space("alice")
	return sp.APFollowerCount(sp.UID())
}

func TestAFollowIsAcceptedAndUndone(t *testing.T) {
	a, _ := testKeys(t)
	s, cookie := inboxServer(t)
	rem := newRemote(t, a)

	if w := rem.post(s, followBody(rem, "1"), a); w.Code != 202 {
		t.Fatalf("follow: %d %s", w.Code, w.Body)
	}
	if followerCount(s) != 1 {
		t.Fatalf("followers: %d", followerCount(s))
	}
	// the Accept comes back to bob's inbox, signed with the account's key, naming the Follow
	var acc *http.Request
	select {
	case acc = <-rem.got:
	case <-time.After(5 * time.Second):
		t.Fatal("no Accept was sent")
	}
	body := <-rem.bodies
	var ac struct {
		Type, Actor string
		Object      struct{ ID, Type, Actor, Object string }
	}
	json.Unmarshal(body, &ac)
	id := "http://" + alice + "/"
	if ac.Type != "Accept" || ac.Actor != id || ac.Object.ID != rem.srv.URL+"/follows/1" || ac.Object.Type != "Follow" || ac.Object.Actor != rem.actor() || ac.Object.Object != id {
		t.Errorf("accept: %s", body)
	}
	sig, err := checkSignedRequest(acc, body, time.Now())
	if err != nil || sig.keyID != id+"#main-key" {
		t.Fatalf("the Accept is not properly signed: %v", err)
	}
	if err := sig.verify(acc, apPublicPEM(t, s)); err != nil {
		t.Errorf("the Accept's signature: %v", err)
	}

	// the same Follow again: still one follower, and answered again
	rem.post(s, followBody(rem, "1"), a)
	if followerCount(s) != 1 {
		t.Errorf("a repeated follow made %d followers", followerCount(s))
	}
	select {
	case <-rem.got:
		<-rem.bodies
	case <-time.After(5 * time.Second):
		t.Error("a repeated Follow was not answered")
	}

	// collections: how many, not who
	if c := apGet(s, alice, "/followers", activityJSON); c.Code != 200 || !strings.Contains(c.Body.String(), `"totalItems":1`) || strings.Contains(c.Body.String(), "bob") {
		t.Errorf("followers: %d %s", c.Code, c.Body)
	}
	if c := apGet(s, alice, "/following", activityJSON); c.Code != 200 || !strings.Contains(c.Body.String(), `"totalItems":0`) {
		t.Errorf("following: %d %s", c.Code, c.Body)
	}
	if c := apGet(s, alice, "/following", ""); c.Code == 200 { // the page is the owner's
		t.Errorf("a visitor sees the following page")
	}
	if p := send(s, "GET", alice, "/profile", cookie, nil).Body.String(); !strings.Contains(p, "Followers: 1") || !strings.Contains(p, "bob@") {
		t.Errorf("the profile does not show the follower")
	}

	unfollow := func(obj any) {
		b, _ := json.Marshal(map[string]any{"id": rem.srv.URL + "/undo/x", "type": "Undo", "actor": rem.actor(), "object": obj})
		if w := rem.post(s, b, a); w.Code != 202 {
			t.Fatalf("undo: %d", w.Code)
		}
	}
	unfollow(map[string]any{"id": rem.srv.URL + "/follows/1", "type": "Follow", "actor": rem.actor(), "object": id})
	if followerCount(s) != 0 {
		t.Errorf("undo left %d followers", followerCount(s))
	}
	drain := func() {
		select {
		case <-rem.got:
			<-rem.bodies
		case <-time.After(5 * time.Second):
		}
	}
	rem.post(s, followBody(rem, "2"), a)
	drain()
	unfollow(rem.srv.URL + "/follows/2") // an Undo that names the Follow by its id
	if followerCount(s) != 0 {
		t.Errorf("undo by id left %d followers", followerCount(s))
	}
	rem.post(s, followBody(rem, "3"), a)
	drain()
	b, _ := json.Marshal(map[string]any{"id": rem.srv.URL + "/del", "type": "Delete", "actor": rem.actor(), "object": rem.actor()})
	if w := rem.post(s, b, a); w.Code != 202 || followerCount(s) != 0 {
		t.Errorf("delete of the actor: %d, %d followers", w.Code, followerCount(s))
	}

	// what is not handled yet is acknowledged and changes nothing; a follow of somebody else is not ours
	for _, act := range []map[string]any{
		{"id": rem.srv.URL + "/l", "type": "Like", "actor": rem.actor(), "object": id + "posts/x"},
		{"id": rem.srv.URL + "/f", "type": "Follow", "actor": rem.actor(), "object": "http://other.example/"},
	} {
		b, _ := json.Marshal(act)
		if w := rem.post(s, b, a); w.Code != 202 || followerCount(s) != 0 {
			t.Errorf("%v: %d, %d followers", act["type"], w.Code, followerCount(s))
		}
	}
	select {
	case <-rem.got:
		t.Error("something was sent for an activity that was ignored")
	default:
	}
}

func TestTheInboxRefusesWhatIsNotProperlySigned(t *testing.T) {
	a, b := testKeys(t)
	s, _ := inboxServer(t)
	rem := newRemote(t, a)
	body := followBody(rem, "1")
	keyID := rem.actor() + "#main-key"
	now := time.Now()

	// a request that is wrong in one way each
	good := func() *http.Request { return signedPost(alice, "/inbox", body, keyID, a, now, allSigned) }
	cases := map[string]*http.Request{
		"unsigned": func() *http.Request {
			r := good()
			r.Header.Del("Signature")
			return r
		}(),
		"another key": signedPost(alice, "/inbox", body, keyID, b, now, allSigned),
		"a body that is not the one signed": func() *http.Request {
			r := good()
			r.Body = io.NopCloser(strings.NewReader(string(followBody(rem, "2"))))
			return r
		}(),
		"an old date":                   signedPost(alice, "/inbox", body, keyID, a, now.Add(-3*time.Hour), allSigned),
		"a date ahead":                  signedPost(alice, "/inbox", body, keyID, a, now.Add(3*time.Hour), allSigned),
		"no digest in the signature":    signedPost(alice, "/inbox", body, keyID, a, now, "(request-target) host date"),
		"no host in the signature":      signedPost(alice, "/inbox", body, keyID, a, now, "(request-target) date digest"),
		"a signature for another path":  signedPost(alice, "/elsewhere", body, keyID, a, now, allSigned),
		"a key that is not there":       signedPost(alice, "/inbox", body, rem.srv.URL+"/users/nobody#main-key", a, now, allSigned),
		"a key on a server that is not": signedPost(alice, "/inbox", body, "http://127.0.0.1:1/users/x#main-key", a, now, allSigned),
	}
	cases["a signature for another path"].URL.Path = "/inbox"
	for name, r := range cases {
		if w := deliver(s, r); w.Code != 401 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if n := followerCount(s); n != 0 {
		t.Errorf("a refused request made %d followers", n)
	}

	// signed by bob, but says carol: not accepted
	other, _ := json.Marshal(map[string]any{"id": "x", "type": "Follow", "actor": "http://carol.example/u", "object": "http://" + alice + "/"})
	if w := rem.post(s, other, a); w.Code != 403 || followerCount(s) != 0 {
		t.Errorf("a stranger's activity: %d", w.Code)
	}
	if w := rem.post(s, []byte("not json"), a); w.Code != 400 {
		t.Errorf("not json: %d", w.Code)
	}
	if w := rem.post(s, []byte(strings.Repeat("x", maxInbox+1)), a); w.Code != 413 {
		t.Errorf("a very large body: %d", w.Code)
	}
	// the inbox of an account that has not switched ActivityPub on is not there
	s2, _ := setup(t)
	s2.cfg.ActivityPub = true
	if w := deliver(s2, signedPost(alice, "/inbox", body, keyID, a, now, allSigned)); w.Code != 404 {
		t.Errorf("an account without ActivityPub: %d", w.Code)
	}
}

// By default the server will not fetch from an address on its own network: a key address pointing there gets nothing.
func TestTheInboxDoesNotFetchFromPrivateAddresses(t *testing.T) {
	a, _ := testKeys(t)
	s, _ := inboxServer(t)
	s.web = fetch.New(false)
	rem := newRemote(t, a)
	if w := rem.post(s, followBody(rem, "1"), a); w.Code != 401 || rem.gets != 0 {
		t.Errorf("fetched from a private address: %d, %d fetches", w.Code, rem.gets)
	}
}

// An actor that has changed its key is fetched again once, and a wrong signature does not cause fetches over and over.
func TestAChangedKeyIsFetchedAgain(t *testing.T) {
	a, b := testKeys(t)
	s, _ := inboxServer(t)
	rem := newRemote(t, a)
	if w := rem.post(s, followBody(rem, "1"), a); w.Code != 202 {
		t.Fatalf("first follow: %d", w.Code)
	}
	if rem.gets != 1 {
		t.Fatalf("%d fetches", rem.gets)
	}
	// the cached key is used without asking again
	rem.post(s, followBody(rem, "1"), a)
	if rem.gets != 1 {
		t.Errorf("a cached key was fetched again: %d", rem.gets)
	}
	// bob changes his key; for the first minute a bad signature is refused without a fetch
	rem.setKey(b)
	if w := rem.post(s, followBody(rem, "1"), b); w.Code != 401 || rem.gets != 1 {
		t.Errorf("fresh cache, new key: %d, %d fetches", w.Code, rem.gets)
	}
	old := actorRefetchIn
	actorRefetchIn = 0
	defer func() { actorRefetchIn = old }()
	if w := rem.post(s, followBody(rem, "1"), b); w.Code != 202 || rem.gets != 2 {
		t.Errorf("stale cache, new key: %d, %d fetches", w.Code, rem.gets)
	}
	if w := rem.post(s, followBody(rem, "1"), a); w.Code != 401 {
		t.Errorf("the old key still works: %d", w.Code)
	}
}

func apPublicPEM(t *testing.T, s *Server) string {
	t.Helper()
	sp := s.space("alice")
	a, err := sp.APAccount(sp.UID())
	if err != nil {
		t.Fatal(err)
	}
	return a.PublicPEM
}

// An actor that names an inbox on some other server is not believed: Weft would be made to send to it.
func TestAnActorsInboxMustBeOnItsOwnServer(t *testing.T) {
	a, _ := testKeys(t)
	s, _ := inboxServer(t)
	rem := newRemote(t, a)
	rem.inbox = strings.Replace(rem.srv.URL, "127.0.0.1", "localhost", 1) + "/users/bob/inbox" // same computer, another host name
	if w := rem.post(s, followBody(rem, "1"), a); w.Code != 401 || followerCount(s) != 0 {
		t.Errorf("an inbox elsewhere: %d, %d followers", w.Code, followerCount(s))
	}
}

// A server that answers only signed requests (Mastodon's authorized fetch) is asked with the account's signature.
func TestAnActorIsFetchedWithASignatureWhenTheServerWantsOne(t *testing.T) {
	a, _ := testKeys(t)
	s, _ := inboxServer(t)
	rem := newRemote(t, a)
	var sawKey string
	rem.verifyGet = func(r *http.Request) error {
		sig, err := parseSignature(r.Header.Get("Signature"))
		if err != nil {
			return err
		}
		if strings.Join(sig.headers, " ") != "(request-target) host date" || r.Header.Get("Digest") != "" {
			return fmt.Errorf("signed headers %v", sig.headers)
		}
		if d, err := http.ParseTime(r.Header.Get("Date")); err != nil || time.Since(d) > time.Minute {
			return fmt.Errorf("date %v", err)
		}
		sawKey = sig.keyID
		return sig.verify(r, apPublicPEM(t, s)) // the server finds the key at the account's actor, which is public
	}
	if w := rem.post(s, followBody(rem, "1"), a); w.Code != 202 || followerCount(s) != 1 {
		t.Fatalf("a server that wants signed fetches: %d, %d followers: %s", w.Code, followerCount(s), w.Body)
	}
	if sawKey != "http://"+alice+"/#main-key" {
		t.Errorf("signed as %q", sawKey)
	}
}
