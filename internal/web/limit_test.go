package web

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/store"
)

// clock is a time the tests move by hand.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestRateLimiterIsATokenBucket(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	l := newRateLimiter(60, 3) // one a second, three at once
	l.now = c.now
	for i := 0; i < 3; i++ {
		if ok, _ := l.take("a"); !ok {
			t.Fatalf("take %d of the burst refused", i)
		}
	}
	ok, wait := l.take("a")
	if ok || wait < 900*time.Millisecond || wait > time.Second {
		t.Fatalf("fourth take: %v, wait %v", ok, wait)
	}
	if ok, _ := l.take("b"); !ok {
		t.Errorf("another caller was refused")
	}
	c.advance(1100 * time.Millisecond)
	if ok, _ := l.take("a"); !ok {
		t.Errorf("no token a second later")
	}
	if ok, _ := l.take("a"); ok {
		t.Errorf("two tokens after one second")
	}
	// has looks without taking
	c.advance(time.Second)
	if ok, _ := l.has("a"); !ok {
		t.Errorf("has: no token")
	}
	if ok, _ := l.has("a"); !ok {
		t.Errorf("has used the token")
	}
	// idle callers are forgotten, so the map does not grow without end
	c.advance(time.Hour)
	l.take("c")
	if len(l.m) != 1 {
		t.Errorf("idle callers kept: %d", len(l.m))
	}
}

func TestFailLimiter(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	f := newFailLimiter(15*time.Minute, 3, 5)
	f.now = c.now
	for i := 0; i < 2; i++ {
		f.fail("alice", "1.1.1.1")
	}
	if b, _ := f.blocked("alice", "1.1.1.1"); b {
		t.Fatal("blocked after two failures")
	}
	f.fail("alice", "1.1.1.1")
	b, wait := f.blocked("alice", "1.1.1.1")
	if !b || wait != 15*time.Minute {
		t.Fatalf("after three failures: %v, %v", b, wait)
	}
	if b, _ := f.blocked("alice", "2.2.2.2"); b {
		t.Errorf("another address is blocked by the first one's failures")
	}
	if b, _ := f.blocked("bob", "1.1.1.1"); b {
		t.Errorf("another user is blocked")
	}
	c.advance(10 * time.Minute)
	if _, wait := f.blocked("alice", "1.1.1.1"); wait != 5*time.Minute {
		t.Errorf("wait after ten minutes: %v", wait)
	}
	c.advance(5 * time.Minute)
	if b, _ := f.blocked("alice", "1.1.1.1"); b {
		t.Errorf("still blocked when the window is over")
	}
	// many addresses together: past perUser failures nobody can sign in as the user
	for i := 0; i < 5; i++ {
		f.fail("alice", "9.9.9."+strconv.Itoa(i))
	}
	if b, _ := f.blocked("alice", "7.7.7.7"); !b {
		t.Errorf("a spread-out guess is not stopped")
	}
	// a success clears the address's own count only
	g := newFailLimiter(time.Hour, 3, 100)
	g.now = c.now
	g.fail("carol", "1.1.1.1")
	g.fail("carol", "1.1.1.1")
	g.ok("carol", "1.1.1.1")
	g.fail("carol", "1.1.1.1")
	if b, _ := g.blocked("carol", "1.1.1.1"); b {
		t.Errorf("a success did not start the count again")
	}
}

func TestClientIP(t *testing.T) {
	req := func(remote, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	plain, proxied := &Server{}, &Server{cfg: Config{TrustProxy: true}}
	for _, c := range []struct {
		s      *Server
		r      *http.Request
		want   string
		reason string
	}{
		{plain, req("203.0.113.5:4000", ""), "203.0.113.5", "plain"},
		{plain, req("203.0.113.5:4000", "198.51.100.9"), "203.0.113.5", "X-Forwarded-For is ignored unless the proxy is trusted"},
		{proxied, req("127.0.0.1:4000", "6.6.6.6, 198.51.100.9"), "198.51.100.9", "the last entry is the one the proxy added"},
		{proxied, req("127.0.0.1:4000", "not an address"), "127.0.0.1", "a bad header is ignored"},
		{proxied, req("127.0.0.1:4000", ""), "127.0.0.1", "no header"},
		{plain, req("[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:4000", ""), "2001:db8:1:2::", "an IPv6 caller is its /64"},
		{plain, req("[2001:db8:1:2:1111:2222:3333:4444]:4000", ""), "2001:db8:1:2::", "the low bits do not make a new caller"},
	} {
		if got := c.s.clientIP(c.r); got != c.want {
			t.Errorf("%s: %q, want %q", c.reason, got, c.want)
		}
	}
}

// A server with the limits on, one user, and a way to send requests from chosen addresses.
func limited(t *testing.T) (s *Server, tok string, clk *clock, from func(ip, method, host, path string, body string, hdr map[string]string) *httptest.ResponseRecorder) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if tok, err = st.AddUser("alice", "Alice", ""); err != nil {
		t.Fatal(err)
	}
	s = New(Config{BaseDomain: "localhost"}, st)
	s.lim = newLimits()
	clk = &clock{t: time.Unix(1_000_000, 0)}
	s.lim.logins.now = clk.now
	for _, l := range []*rateLimiter{s.lim.webment, s.lim.api, s.lim.apiWrite, s.lim.follow, s.lim.badToken} {
		l.now = clk.now
	}
	from = func(ip, method, host, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
		r.RemoteAddr = ip + ":5555"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	return
}

func TestLoginGuessesAreLimited(t *testing.T) {
	s, _, clk, from := limited(t)
	if err := s.space("alice").SetPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	login := func(ip, pw string) *httptest.ResponseRecorder {
		return from(ip, "POST", host, "/login", url.Values{"password": {pw}}.Encode(), nil)
	}
	for i := 0; i < 5; i++ {
		if w := login("10.0.0.1", "wrong"); w.Code != 401 {
			t.Fatalf("guess %d: %d", i, w.Code)
		}
	}
	// the sixth is refused whatever it says, and says when to come back
	w := login("10.0.0.1", "correct horse")
	if w.Code != 429 || w.Header().Get("Retry-After") != "900" || !strings.Contains(w.Body.String(), "Try again in 15 minutes") {
		t.Fatalf("after five wrong passwords: %d retry %q\n%s", w.Code, w.Header().Get("Retry-After"), w.Body)
	}
	if w := login("10.0.0.2", "correct horse"); w.Code != 303 {
		t.Errorf("another address is locked out too: %d", w.Code)
	}
	clk.advance(16 * time.Minute)
	if w := login("10.0.0.1", "correct horse"); w.Code != 303 {
		t.Errorf("after the window: %d", w.Code)
	}
	// a right password starts the count again
	for i := 0; i < 4; i++ {
		login("10.0.0.3", "wrong")
	}
	login("10.0.0.3", "correct horse")
	for i := 0; i < 4; i++ {
		if w := login("10.0.0.3", "wrong"); w.Code != 401 {
			t.Fatalf("guess %d after a success: %d", i, w.Code)
		}
	}

	// the password-change form checks the old password, so it is a way to guess, and counts the same
	w = login("10.0.0.4", "correct horse")
	ck := cookieOf(w)
	change := func(cur string) *httptest.ResponseRecorder {
		return from("10.0.0.4", "POST", host, "/password", url.Values{"current": {cur}, "new": {"another one"}, "again": {"another one"}}.Encode(),
			map[string]string{"Cookie": cookieName + "=" + ck})
	}
	for i := 0; i < 5; i++ {
		if w := change("wrong"); w.Code != 400 {
			t.Fatalf("change guess %d: %d", i, w.Code)
		}
	}
	if w := change("correct horse"); w.Code != 429 {
		t.Errorf("a sixth try at the old password: %d", w.Code)
	}
}

func TestWebmentionAndAPIFloodsAreLimited(t *testing.T) {
	_, tok, _, from := limited(t)
	auth := map[string]string{"Authorization": "Bearer " + tok}

	// webmentions: 20 at once per address (the target is not a post, which costs nothing: it is still counted)
	wm := func(ip string) int {
		return from(ip, "POST", host, "/webmention", url.Values{"source": {"http://a.example/1"}, "target": {"http://" + host + "/posts/x"}}.Encode(), nil).Code
	}
	for i := 0; i < 20; i++ {
		if c := wm("10.0.0.1"); c == 429 {
			t.Fatalf("webmention %d was limited", i)
		}
	}
	if w := from("10.0.0.1", "POST", host, "/webmention", "source=http://a.example/1&target=http://x", nil); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Errorf("the 21st webmention: %d retry %q", w.Code, w.Header().Get("Retry-After"))
	}
	if c := wm("10.0.0.2"); c == 429 {
		t.Errorf("another address was limited")
	}

	// API writes: 30 at once per address and user
	post := func(ip string) int {
		return from(ip, "POST", host, "/api/posts", `{"markdown":"x"}`, auth).Code
	}
	for i := 0; i < 30; i++ {
		if c := post("10.0.0.3"); c != 201 {
			t.Fatalf("post %d: %d", i, c)
		}
	}
	if w := from("10.0.0.3", "POST", host, "/api/posts", `{"markdown":"x"}`, auth); w.Code != 429 || w.Header().Get("Retry-After") != "1" {
		t.Errorf("the 31st post: %d retry %q", w.Code, w.Header().Get("Retry-After"))
	}
	if c := from("10.0.0.3", "GET", host, "/api/timeline", "", auth).Code; c != 200 {
		t.Errorf("a read right after the writes were limited: %d", c)
	}
	if c := post("10.0.0.4"); c != 201 {
		t.Errorf("another address: %d", c)
	}

	// following: 10 at once
	for i := 0; i < 10; i++ {
		from("10.0.0.5", "POST", host, "/api/following", `{"url":"http://nowhere.invalid/feed.xml"}`, auth)
	}
	if c := from("10.0.0.5", "POST", host, "/api/following", `{"url":"http://nowhere.invalid/feed.xml"}`, auth).Code; c != 429 {
		t.Errorf("the 11th follow: %d", c)
	}

	// wrong tokens: 20 from one address, then even the right one is refused from there
	for i := 0; i < 20; i++ {
		if c := from("10.0.0.6", "GET", host, "/api/timeline", "", map[string]string{"Authorization": "Bearer wrong"}).Code; c != 401 {
			t.Fatalf("wrong token %d: %d", i, c)
		}
	}
	if c := from("10.0.0.6", "GET", host, "/api/timeline", "", auth).Code; c != 429 {
		t.Errorf("the right token after twenty wrong ones: %d", c)
	}
	if c := from("10.0.0.7", "GET", host, "/api/timeline", "", auth).Code; c != 200 {
		t.Errorf("another address: %d", c)
	}
	// public pages are not limited here
	for i := 0; i < 300; i++ {
		if c := from("10.0.0.8", "GET", host, "/feed.xml", "", nil).Code; c != 200 {
			t.Fatalf("feed %d: %d", i, c)
		}
	}
}

func TestBehindAProxyTheForwardedAddressIsTheCaller(t *testing.T) {
	s, _, _, from := limited(t)
	s.cfg.TrustProxy = true
	if err := s.space("alice").SetPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	login := func(xff, pw string) int {
		return from("127.0.0.1", "POST", host, "/login", url.Values{"password": {pw}}.Encode(), map[string]string{"X-Forwarded-For": xff}).Code
	}
	for i := 0; i < 5; i++ {
		login("198.51.100.1", "wrong")
	}
	if c := login("198.51.100.1", "correct horse"); c != 429 {
		t.Errorf("the guesser: %d", c)
	}
	if c := login("198.51.100.2", "correct horse"); c != 303 {
		t.Errorf("somebody else behind the same proxy: %d", c)
	}
}

func TestWebmentionInboxIsCapped(t *testing.T) {
	s, _ := setup(t)
	do(s, "POST", host, "/api/posts", mustToken(t, s), `{"markdown":"a post","slug":"target"}`)
	sp := s.space("alice")
	ps, _ := sp.RecentPosts(sp.UID(), 1)
	target := "http://" + host + "/posts/" + ps[0].Slug
	send := func(source string) int {
		return form(s, "POST", "/webmention", "", url.Values{"source": {source}, "target": {target}}, "").Code
	}
	for i := 0; i < maxInboxJobsPerHost; i++ {
		if c := send("http://spam.example/p/" + strconv.Itoa(i)); c != 202 {
			t.Fatalf("webmention %d: %d", i, c)
		}
	}
	w := form(s, "POST", "/webmention", "", url.Values{"source": {"http://spam.example/p/new"}, "target": {target}}, "")
	if w.Code != 503 || w.Header().Get("Retry-After") != "300" {
		t.Errorf("past the per-host cap: %d retry %q", w.Code, w.Header().Get("Retry-After"))
	}
	if c := send("http://spam.example/p/3"); c != 202 { // one that is already waiting is not new
		t.Errorf("a repeat: %d", c)
	}
	if c := send("http://spam.example.evil/p/1"); c != 202 { // a different host, though it starts the same
		t.Errorf("another host: %d", c)
	}
	if c := send("http://friend.example/p/1"); c != 202 {
		t.Errorf("another host: %d", c)
	}
}

func mustToken(t *testing.T, s *Server) string {
	t.Helper()
	tok, err := s.space("alice").ResetToken()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// A connection that sends part of a request and stops is closed, and so is one that goes quiet.
func TestSlowConnectionsAreClosed(t *testing.T) {
	if DefaultTimeouts.ReadHeader == 0 || DefaultTimeouts.Read == 0 || DefaultTimeouts.Write == 0 || DefaultTimeouts.Idle == 0 {
		t.Fatalf("a default timeout is zero: %+v", DefaultTimeouts)
	}
	srv := NewHTTPServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }),
		Timeouts{ReadHeader: 200 * time.Millisecond, Read: 400 * time.Millisecond, Write: time.Second, Idle: 300 * time.Millisecond})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	defer srv.Close()

	closedWithin := func(c net.Conn, d time.Duration) bool {
		c.SetReadDeadline(time.Now().Add(d))
		_, err := bufio.NewReader(c).ReadByte()
		ne, timedOut := err.(net.Error)
		return err != nil && !(timedOut && ne.Timeout())
	}
	slow, _ := net.Dial("tcp", l.Addr().String())
	defer slow.Close()
	slow.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n")) // the headers never end
	if !closedWithin(slow, 3*time.Second) {
		t.Errorf("a connection that never finished its headers was kept")
	}
	idle, _ := net.Dial("tcp", l.Addr().String())
	defer idle.Close()
	idle.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	br := bufio.NewReader(idle)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body) // the answer is read; now the connection is idle
	idle.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := br.ReadByte(); err == nil || (func() bool { ne, ok := err.(net.Error); return ok && ne.Timeout() })() {
		t.Errorf("an idle connection was kept: %v", err)
	}
}
