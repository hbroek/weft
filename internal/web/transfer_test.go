package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/store"
)

// twoServers is the same account name on two servers: alice on the old one has an account to move, alice on the
// new one is the empty account she made to receive it.
type twoServers struct {
	old, nw              *instance
	oldCookie, newCookie string
}

func setupTransfer(t *testing.T) *twoServers {
	t.Helper()
	ts := &twoServers{old: newInstance(t, "alice"), nw: newInstance(t, "alice")}
	for _, i := range []*instance{ts.old, ts.nw} {
		sp, release, err := i.s.st.Space("alice")
		if err != nil {
			t.Fatal(err)
		}
		pw := "password-" + i.host // a different password on each server
		if err := sp.SetPassword(pw); err != nil {
			t.Fatal(err)
		}
		release()
	}
	ts.oldCookie = ownerCookie(t, ts.old.s, "alice")
	ts.newCookie = ownerCookie(t, ts.nw.s, "alice")
	return ts
}

func (ts *twoServers) password(i *instance) string { return "password-" + i.host }

// serverOf is how a person names a server in the form: the host and port, without the user's name.
func serverOf(i *instance) string { return strings.TrimPrefix(i.host, "alice.") }

var codeRE = regexp.MustCompile(`<code>([a-z0-9]{5}(?:-[a-z0-9]{5}){4})</code>`)

func (ts *twoServers) code(t *testing.T) string {
	t.Helper()
	w := send(ts.old.s, "POST", ts.old.host, "/move/code", ts.oldCookie, url.Values{"server": {serverOf(ts.nw)}, "password": {ts.password(ts.old)}})
	m := codeRE.FindStringSubmatch(w.Body.String())
	if w.Code != 200 || m == nil {
		t.Fatalf("move code: %d\n%s", w.Code, w.Body.String())
	}
	return m[1]
}

func (ts *twoServers) receive(t *testing.T, code string) {
	t.Helper()
	w := send(ts.nw.s, "POST", ts.nw.host, "/move/receive", ts.newCookie, url.Values{"server": {serverOf(ts.old)}, "code": {code}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("move/receive: %d\n%s", w.Code, w.Body.String())
	}
}

func (ts *twoServers) wait(t *testing.T) pullView {
	t.Helper()
	for i := 0; i < 300; i++ {
		if p := ts.nw.s.pulls.get("alice"); p != nil {
			if v := p.view(); v.Done || v.Err != "" {
				return v
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the move did not end")
	return pullView{}
}

// The whole move: the account's posts arrive, the new account keeps its own password and stays signed in, and the
// old address redirects.
func TestAccountMovesToAnotherServer(t *testing.T) {
	ts := setupTransfer(t)
	ts.old.post(t, `{"title":"First","markdown":"Written on the old server."}`)
	ts.old.post(t, `{"markdown":"A second post."}`)
	sp, release, _ := ts.old.s.st.Space("alice")
	if _, err := sp.PutMedia(store.Media{Name: "0123456789abcdef0123456789abcdef.png", Mime: "image/png", Width: 1, Height: 1, Data: []byte("not really a png")}, 0); err != nil {
		t.Fatal(err)
	}
	release()

	ts.receive(t, ts.code(t))
	if v := ts.wait(t); v.Err != "" || !v.Done {
		t.Fatalf("move: %+v", v)
	}

	// the posts and the picture are on the new server
	feed := ts.nw.get("/feed.xml")
	for _, want := range []string{"Written on the old server.", "A second post."} {
		if !strings.Contains(feed, want) {
			t.Errorf("new feed lacks %q", want)
		}
	}
	if w := do(ts.nw.s, "GET", ts.nw.host, "/media/0123456789abcdef0123456789abcdef.png", "", ""); w.Code != 200 {
		t.Errorf("picture on the new server: %d", w.Code)
	}
	// the account here has its own password and token, and the session still works
	sp, release, _ = ts.nw.s.st.Space("alice")
	if _, err := sp.CheckPassword(ts.password(ts.nw)); err != nil {
		t.Errorf("the new password does not work: %v", err)
	}
	if _, err := sp.CheckPassword(ts.password(ts.old)); err == nil {
		t.Errorf("the old password works on the new server")
	}
	release()
	if w := send(ts.nw.s, "GET", ts.nw.host, "/profile", ts.newCookie, nil); w.Code != 200 {
		t.Errorf("signed in after the move: %d", w.Code)
	}
	if w := do(ts.nw.s, "GET", ts.nw.host, "/api/following", ts.nw.tok, ""); w.Code != 200 {
		t.Errorf("API token after the move: %d", w.Code)
	}
	// the old address redirects, the feed too
	w := do(ts.old.s, "GET", ts.old.host, "/feed.xml", "", "")
	if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != ts.nw.base+"/feed.xml" {
		t.Errorf("old feed: %d %q", w.Code, w.Header().Get("Location"))
	}
	if e := ts.old.s.st.Dir("alice"); e.Status != store.StatusMoved || e.Move != "" {
		t.Errorf("old entry: %+v", e)
	}
}

// A code works once, only for the destination it was made for, and not after a few wrong guesses.
func TestMoveCodeIsSingleUseAndGuardedAgainstGuessing(t *testing.T) {
	ts := setupTransfer(t)
	code := ts.code(t)
	call := func(c, to string) int {
		r := httptestRequest(ts.old, "POST", movePath+"prepare", `{"to":"`+to+`"}`)
		r.Header.Set("X-Weft-Move-Code", c)
		w := serve(ts.old.s, r)
		return w.Code
	}
	if got := call(code, "http://alice.localhost:1"); got != 403 {
		t.Errorf("the wrong destination: %d", got)
	}
	for i := 0; i < 4; i++ {
		if got := call("aaaaa-aaaaa-aaaaa-aaaaa-aaaaa", ts.nw.base); got != 403 {
			t.Errorf("a guess: %d", got)
		}
	}
	if got := call(code, ts.nw.base); got != 403 { // five failures used the code up
		t.Errorf("the right code after five wrong ones: %d", got)
	}

	code = ts.code(t)
	if got := call(strings.ToUpper(strings.ReplaceAll(code, "-", " ")), ts.nw.base); got != 200 {
		t.Errorf("a code typed in capitals with spaces: %d", got)
	}
	if got := call(code, ts.nw.base); got != 403 {
		t.Errorf("a second use of the same code: %d", got)
	}
}

// The account here is replaced, so it must be empty.
func TestMoveNeedsAnEmptyAccount(t *testing.T) {
	ts := setupTransfer(t)
	ts.nw.post(t, `{"markdown":"Already something here."}`)
	w := send(ts.nw.s, "POST", ts.nw.host, "/move/receive", ts.newCookie, url.Values{"server": {serverOf(ts.old)}, "code": {ts.code(t)}})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "1 posts") {
		t.Errorf("non-empty account: %d\n%s", w.Code, w.Body.String())
	}
	if e := ts.old.s.st.Dir("alice"); e.Move != "" {
		t.Errorf("the old account was touched: %+v", e)
	}
}

// Anything posted between the copy and the lock is noticed, and copied again under the lock.
func TestMoveCopiesAgainWhenTheAccountChangedMeanwhile(t *testing.T) {
	ts := setupTransfer(t)
	ts.old.post(t, `{"markdown":"Before."}`)
	code := ts.code(t)
	r := httptestRequest(ts.old, "POST", movePath+"prepare", `{"to":"`+ts.nw.base+`"}`)
	r.Header.Set("X-Weft-Move-Code", code)
	var pre struct {
		Token    string
		Manifest moveManifest
	}
	w := serve(ts.old.s, r)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &pre) != nil {
		t.Fatalf("prepare: %d %s", w.Code, w.Body.String())
	}
	ts.old.post(t, `{"markdown":"After the copy."}`) // allowed: only the hold is on

	lock := func() (changed bool, m moveManifest) {
		r := httptestRequest(ts.old, "POST", movePath+"lock", "")
		r.Header.Set("Authorization", "Bearer "+pre.Token)
		w := serve(ts.old.s, r)
		var out struct {
			Changed  bool
			Manifest moveManifest
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("lock: %d %s", w.Code, w.Body.String())
		}
		return out.Changed, out.Manifest
	}
	changed, m := lock()
	if !changed || m.SHA256 == pre.Manifest.SHA256 {
		t.Errorf("a post after the copy was not noticed: changed=%v", changed)
	}
	// locked: changes are refused, mentions and the move itself are not
	if w := do(ts.old.s, "POST", ts.old.host, "/api/posts", ts.old.tok, `{"markdown":"Too late."}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("a post while locked: %d", w.Code)
	}
	if changed, _ = lock(); changed {
		t.Errorf("locking twice must find nothing new")
	}
	ts.old.s.st.MoveAbort("alice")
	if w := do(ts.old.s, "POST", ts.old.host, "/api/posts", ts.old.tok, `{"markdown":"Free again."}`); w.Code != 201 {
		t.Errorf("a post after the move was cancelled: %d", w.Code)
	}
}

// Webmentions that arrive while the account moves are kept, and sent to the new server when it is done; if the move
// is cancelled they go to the inbox.
func TestMentionsDuringAMoveAreHeldAndForwarded(t *testing.T) {
	ts := setupTransfer(t)
	post := ts.old.post(t, `{"markdown":"Mention me."}`)
	code := ts.code(t)
	r := httptestRequest(ts.old, "POST", movePath+"prepare", `{"to":"`+ts.nw.base+`"}`)
	r.Header.Set("X-Weft-Move-Code", code)
	if w := serve(ts.old.s, r); w.Code != 200 {
		t.Fatalf("prepare: %d", w.Code)
	}
	mention := func() *httptest.ResponseRecorder {
		return send(ts.old.s, "POST", ts.old.host, "/webmention", "", url.Values{"source": {"http://elsewhere.example/reply"}, "target": {post}})
	}
	if w := mention(); w.Code != http.StatusAccepted {
		t.Fatalf("webmention during the hold: %d %s", w.Code, w.Body.String())
	}
	if n := heldCount(t, ts.old.s.st, "alice"); n != 1 {
		t.Errorf("held mentions: %d, want 1", n)
	}
	if n := ts.old.s.Pending(); n != 0 {
		t.Errorf("a held mention reached the user's queue: %d", n)
	}
	ts.old.s.st.MoveAbort("alice")
	if n := heldCount(t, ts.old.s.st, "alice"); n != 0 {
		t.Errorf("held after the cancel: %d", n)
	}
	if n := ts.old.s.Pending(); n != 1 {
		t.Errorf("after the cancel the mention should be in the queue: %d", n)
	}
}

func heldCount(t *testing.T, st *store.Instance, name string) int {
	t.Helper()
	h, err := st.TakeHeld(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range h { // put them back, so that the count only looks
		st.Hold(name, x.Source, x.Target)
	}
	return len(h)
}

// A move that stops before the old server has a good copy leaves it as it was.
func TestMoveThatFailsLeavesBothAsTheyWere(t *testing.T) {
	ts := setupTransfer(t)
	ts.old.post(t, `{"markdown":"Mine."}`)
	ts.receive(t, "aaaaa-aaaaa-aaaaa-aaaaa-aaaaa") // not a code
	if v := ts.wait(t); v.Err == "" {
		t.Fatalf("a wrong code was accepted")
	}
	if e := ts.old.s.st.Dir("alice"); e.Status != store.StatusActive || e.Move != "" {
		t.Errorf("old account: %+v", e)
	}
	if feed := ts.nw.get("/feed.xml"); strings.Contains(feed, "Mine.") {
		t.Errorf("the new account has the posts of a failed move")
	}
}

func TestMoveCodeExpires(t *testing.T) {
	ts := setupTransfer(t)
	ts.code(t)
	if _, err := ts.old.s.st.MovePending("alice"); err != nil {
		t.Fatal(err)
	}
	ts.old.s.st.ExpireMoves(time.Now().Add(2 * time.Hour))
	if _, err := ts.old.s.st.MovePending("alice"); err == nil {
		t.Errorf("an unused code outlived its hour")
	}
}

func httptestRequest(i *instance, method, path, body string) *http.Request {
	return httptest.NewRequest(method, "http://"+i.host+path, strings.NewReader(body))
}

func serve(s *Server, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// The mentions held during a move that finishes are sent to the new server, with the target at the new address.
func TestHeldMentionsAreSentToTheNewServerWhenTheMoveIsDone(t *testing.T) {
	ts := setupTransfer(t)
	post := ts.old.post(t, `{"markdown":"Mention me."}`)
	step := func(name, token string, hdr map[string]string, body string) *httptest.ResponseRecorder {
		r := httptestRequest(ts.old, "POST", movePath+name, body)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return serve(ts.old.s, r)
	}
	w := step("prepare", "", map[string]string{"X-Weft-Move-Code": ts.code(t)}, `{"to":"`+ts.nw.base+`"}`)
	var pre struct{ Token string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &pre) != nil {
		t.Fatalf("prepare: %d", w.Code)
	}
	if w := send(ts.old.s, "POST", ts.old.host, "/webmention", "", url.Values{"source": {"http://elsewhere.example/reply"}, "target": {post}}); w.Code != http.StatusAccepted {
		t.Fatalf("webmention: %d", w.Code)
	}
	for _, s := range []string{"lock", "received", "done"} {
		if w := step(s, pre.Token, nil, ""); w.Code != 200 {
			t.Fatalf("%s: %d %s", s, w.Code, w.Body.String())
		}
	}
	before := ts.nw.hits.Load()
	for i := 0; i < 100 && ts.nw.hits.Load() == before; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if ts.nw.hits.Load() == before {
		t.Fatalf("the held mention never reached the new server")
	}
	if n := heldCount(t, ts.old.s.st, "alice"); n != 0 {
		t.Errorf("held mentions left behind: %d", n)
	}
}
