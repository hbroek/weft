package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hbroek/weft/internal/store"
)

// hosts starts a listener and a server whose base domain is base.localhost, so that a custom domain
// such as alice.custom.localhost is outside it and still reachable (*.localhost is loopback).
func hosts(t *testing.T, cfg Config, users ...string) (s *Server, port string, toks map[string]string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg.BaseDomain, cfg.AllowPrivate = "base.localhost", true
	s = New(cfg, st)
	if s.cfg.ReachDomain == nil {
		s.cfg.ReachDomain = reachSelf(s)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	port = ts.URL[strings.LastIndex(ts.URL, ":")+1:]
	toks = map[string]string{}
	for _, u := range users {
		if toks[u], err = st.AddUser(u, strings.ToUpper(u[:1])+u[1:], ""); err != nil {
			t.Fatal(err)
		}
	}
	return
}

// reachSelf stands in for the network: a request to the domain is answered by the server itself, as it would be
// if the domain's DNS pointed here.
func reachSelf(s *Server) func(context.Context, string) (string, error) {
	return func(_ context.Context, domain string) (string, error) {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", "http://"+domain+"/.well-known/weft-domain", nil))
		if w.Code != http.StatusOK {
			return "", fmt.Errorf("status %d", w.Code)
		}
		return w.Body.String(), nil
	}
}

func ownerCookie(t *testing.T, s *Server, name string) string {
	t.Helper()
	sp, release, err := s.st.Space(name)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	tok, err := sp.CreateSession(sp.UID())
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func send(s *Server, method, host, path, cookie string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// A user adds a domain, proves it with a TXT record, and from then on it is their address: the old one
// redirects, the feed too.
func TestCustomDomain(t *testing.T) {
	txt := map[string][]string{}
	s, port, toks := hosts(t, Config{
		CNAMETarget: "custom.example.net",
		LookupTXT:   func(_ context.Context, n string) ([]string, error) { return txt[n], nil },
		LookupCNAME: func(_ context.Context, n string) (string, error) { return "custom.example.net.", nil },
	}, "alice", "bob")
	old, custom := "alice.base.localhost:"+port, "alice.custom.localhost:"+port
	do(s, "POST", old, "/api/posts", toks["alice"], `{"title":"Hello","markdown":"first"}`)
	cookie := ownerCookie(t, s, "alice")

	if w := send(s, "POST", old, "/domain", cookie, url.Values{"domain": {"other.base.localhost"}}); w.Code != 400 {
		t.Fatalf("a subdomain of the instance was accepted: %d", w.Code)
	}
	if w := send(s, "POST", old, "/domain", cookie, url.Values{"domain": {"nodots"}}); w.Code != 400 {
		t.Fatalf("a name without a dot was accepted: %d", w.Code)
	}
	if w := send(s, "POST", old, "/domain", "", url.Values{"domain": {"alice.custom.localhost"}}); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Fatalf("a visitor set a domain: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := send(s, "POST", old, "/domain", cookie, url.Values{"domain": {"Alice.Custom.localhost."}}); w.Code != 303 {
		t.Fatalf("set domain: %d %s", w.Code, w.Body)
	}
	d, err := s.st.DomainOf("alice")
	if err != nil || d.Domain != "alice.custom.localhost" || d.Verified {
		t.Fatalf("domain %+v err %v", d, err)
	}
	profile := send(s, "GET", old, "/profile", cookie, nil).Body.String()
	for _, want := range []string{"_weft.alice.custom.localhost", "weft-verify=" + d.Token, "custom.example.net", "(found)"} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile page lacks %q", want)
		}
	}

	// not verified: nothing changes, and the domain serves nobody
	if w := send(s, "GET", old, "/", "", nil); w.Code != 200 {
		t.Fatalf("home before verification: %d", w.Code)
	}
	if w := send(s, "GET", custom, "/", "", nil); w.Code != 404 {
		t.Fatalf("unverified domain served: %d", w.Code)
	}
	if w := send(s, "POST", old, "/domain/check", cookie, nil); w.Code != 400 || !strings.Contains(w.Body.String(), "not there yet") {
		t.Fatalf("check without the record: %d", w.Code)
	}
	txt["_weft.alice.custom.localhost"] = []string{"v=spf1 -all", "weft-verify=wrong"}
	if w := send(s, "POST", old, "/domain/check", cookie, nil); w.Code != 400 {
		t.Fatalf("check with another token: %d", w.Code)
	}
	txt["_weft.alice.custom.localhost"] = []string{"weft-verify=" + d.Token}
	w := send(s, "POST", old, "/domain/check", cookie, nil)
	if w.Code != 303 || w.Header().Get("Location") != "http://alice.custom.localhost:"+port+"/login" {
		t.Fatalf("check with the record: %d %s", w.Code, w.Header().Get("Location"))
	}

	// the custom domain is the address now
	if w := send(s, "GET", custom, "/", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Hello") {
		t.Fatalf("home on the custom domain: %d", w.Code)
	}
	w = send(s, "GET", old, "/posts/anything", "", nil)
	if w.Code != 301 || w.Header().Get("Location") != "http://alice.custom.localhost:"+port+"/posts/anything" {
		t.Fatalf("old address: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := send(s, "POST", old, "/webmention", "", nil); w.Code != 308 {
		t.Fatalf("a POST to the old address must keep its method: %d", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); cc != domainRedirectCache {
		t.Errorf("Cache-Control on a redirect: %q", cc)
	}
	oldFeed := send(s, "GET", old, "/feed.xml?page=2", "", nil)
	if oldFeed.Code != 301 || oldFeed.Header().Get("Location") != "http://alice.custom.localhost:"+port+"/feed.xml?page=2" {
		t.Fatalf("the old feed must redirect like everything else: %d %s", oldFeed.Code, oldFeed.Header().Get("Location"))
	}
	newFeed := send(s, "GET", custom, "/feed.xml", "", nil).Body.String()
	if !strings.Contains(newFeed, "http://alice.custom.localhost:"+port+"/posts/hello.") {
		t.Fatalf("the new feed:\n%s", newFeed)
	}
	// the sign-in is per address: the old cookie works on the new host too, since sessions are the user's
	if !strings.Contains(send(s, "GET", custom, "/profile", cookie, nil).Body.String(), "(verified") {
		t.Errorf("profile does not show a verified domain")
	}

	// Bob cannot take it; the proxy's question is answered for exactly the hosts we serve
	do(s, "POST", "bob.base.localhost:"+port, "/api/posts", toks["bob"], `{"markdown":"x"}`)
	if _, err := s.st.SetDomain("bob", "alice.custom.localhost"); err != store.ErrDomainTaken {
		t.Fatalf("Bob claimed Alice's verified domain: %v", err)
	}
	in := s.Internal()
	for host, want := range map[string]int{"alice.custom.localhost": 200, "alice.base.localhost": 200, "bob.base.localhost": 200,
		"nobody.base.localhost": 404, "evil.example": 404, "": 404} {
		r := httptest.NewRequest("GET", "http://127.0.0.1/domain-ok?domain="+url.QueryEscape(host), nil)
		w := httptest.NewRecorder()
		in.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("domain-ok %q = %d, want %d", host, w.Code, want)
		}
	}

	// removing it brings the old address back
	if w := send(s, "POST", custom, "/domain/remove", cookie, nil); w.Code != 303 {
		t.Fatalf("remove: %d", w.Code)
	}
	if w := send(s, "GET", old, "/", "", nil); w.Code != 200 {
		t.Fatalf("old address after removing the domain: %d", w.Code)
	}
	if w := send(s, "GET", custom, "/", "", nil); w.Code != 404 {
		t.Fatalf("removed domain still served: %d", w.Code)
	}
}

// Two users claim the same domain; the one whose DNS carries their own token gets it.
func TestOnlyTheClaimantWithTheTokenGetsTheDomain(t *testing.T) {
	var bobToken string
	s, port, _ := hosts(t, Config{LookupTXT: func(_ context.Context, n string) ([]string, error) {
		return []string{"weft-verify=" + bobToken}, nil
	}}, "alice", "bob")
	da, err := s.st.SetDomain("alice", "shared.example.com")
	if err != nil {
		t.Fatal(err)
	}
	db, err := s.st.SetDomain("bob", "shared.example.com")
	if err != nil {
		t.Fatal(err)
	}
	bobToken = db.Token
	if w := send(s, "POST", "alice.base.localhost:"+port, "/domain/check", ownerCookie(t, s, "alice"), nil); w.Code != 400 {
		t.Fatalf("Alice verified a domain whose DNS carries Bob's token: %d (her token %s)", w.Code, da.Token)
	}
	if w := send(s, "POST", "bob.base.localhost:"+port, "/domain/check", ownerCookie(t, s, "bob"), nil); w.Code != 303 {
		t.Fatalf("Bob could not verify: %d", w.Code)
	}
	if n, ok := s.st.HostUser("shared.example.com"); !ok || n != "bob" {
		t.Fatalf("the domain belongs to %q", n)
	}
}

// A user who moved to another server: everything redirects for good, the file goes to the trash, and
// the move can be undone while it is there.
func TestMovedAccount(t *testing.T) {
	s, port, toks := hosts(t, Config{}, "alice")
	host := "alice.base.localhost:" + port
	do(s, "POST", host, "/api/posts", toks["alice"], `{"title":"Hello","markdown":"first"}`)

	if err := s.st.MarkMoved("alice", "https://alice.example.org/some/path"); err != nil {
		t.Fatal(err)
	}
	w := send(s, "GET", host, "/posts/x?a=1", "", nil)
	if w.Code != 301 || w.Header().Get("Location") != "https://alice.example.org/posts/x?a=1" {
		t.Fatalf("page: %d %s", w.Code, w.Header().Get("Location"))
	}
	if cc := w.Header().Get("Cache-Control"); cc != redirectCache { // a move to another server is for good: an hour
		t.Errorf("Cache-Control on the redirect of a moved account: %q", cc)
	}
	if w := send(s, "POST", host, "/api/posts", "", nil); w.Code != 308 {
		t.Fatalf("post: %d", w.Code)
	}
	for _, path := range []string{"/", "/feed.xml", "/likes.xml", "/profile"} {
		if w := send(s, "GET", host, path, "", nil); w.Code != 301 || w.Header().Get("Location") != "https://alice.example.org"+path {
			t.Errorf("%s: %d %s", path, w.Code, w.Header().Get("Location"))
		}
	}

	s.endMoves() // the server's housekeeping puts the file in the trash
	if trashed, _ := filepath.Glob(filepath.Join(s.st.TrashDir(), "alice.*.db")); len(trashed) != 1 {
		t.Fatalf("trash: %v", trashed)
	}
	if w := send(s, "GET", host, "/feed.xml", "", nil); w.Code != 301 {
		t.Errorf("after the file was trashed: %d, want 301", w.Code)
	}
	if _, err := s.st.AddUser("alice", "", ""); err != store.ErrNameTaken {
		t.Errorf("the name was given away: %v", err)
	}
	if w := send(s, "GET", "nobody.base.localhost:"+port, "/", "", nil); w.Code != 404 {
		t.Errorf("an unknown user: %d", w.Code)
	}

	// the move is undone
	if err := s.st.Unmove("alice"); err != nil {
		t.Fatal(err)
	}
	if w := send(s, "GET", host, "/", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Hello") {
		t.Fatalf("after undoing the move: %d", w.Code)
	}
}

// A reader whose feed is redirected for good keeps one copy of each post and follows the new address.
func TestReaderFollowsAFeedThatMoved(t *testing.T) {
	ctx := context.Background()
	s, port, toks := hosts(t, Config{}, "alice", "bob")
	alice, oldFeed := "alice.base.localhost:"+port, "http://alice.base.localhost:"+port+"/feed.xml"
	newFeed := "http://alice.custom.localhost:" + port + "/feed.xml"
	do(s, "POST", alice, "/api/posts", toks["alice"], `{"title":"One","markdown":"one"}`)
	do(s, "POST", alice, "/api/posts", toks["alice"], `{"title":"Two","markdown":"two"}`)
	if _, err := s.Follow(ctx, "bob", oldFeed); err != nil {
		t.Fatal(err)
	}
	s.RefreshAll(ctx)

	timeline := func() []store.TimelineItem {
		sp, release, _ := s.st.Space("bob")
		defer release()
		items, err := sp.Timeline(sp.UID(), 50)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	if n := len(timeline()); n != 2 {
		t.Fatalf("timeline before: %d items", n)
	}

	if _, err := s.st.SetDomain("alice", "alice.custom.localhost"); err != nil {
		t.Fatal(err)
	}
	if err := s.st.VerifyDomain("alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.st.ActivateDomain("alice"); err != nil {
		t.Fatal(err)
	}
	s.Refresh(ctx, oldFeed)
	if names, _ := s.st.FollowersOf(oldFeed); len(names) != 0 {
		t.Errorf("still following the old address: %v", names)
	}
	if names, _ := s.st.FollowersOf(newFeed); len(names) != 1 || names[0] != "bob" {
		t.Errorf("followers of the new address: %v", names)
	}
	items := timeline()
	if len(items) != 2 {
		t.Fatalf("timeline after the move: %d items, want 2 (no duplicates)", len(items))
	}
	for _, it := range items {
		if it.FeedURL != newFeed || !strings.HasPrefix(it.Link, "http://alice.custom.localhost:") {
			t.Errorf("item %+v is not from the new address", it)
		}
	}
	if s.st.HasItems(oldFeed) {
		t.Errorf("the old feed's items were kept")
	}
}

// Only a permanent redirect to a feed moves a follow: a temporary one, or one to a page that is not a
// feed, changes nothing.
func TestReaderIgnoresTemporaryAndBrokenRedirects(t *testing.T) {
	ctx := context.Background()
	s, port, toks := hosts(t, Config{}, "alice", "bob")
	do(s, "POST", "alice.base.localhost:"+port, "/api/posts", toks["alice"], `{"markdown":"one"}`)
	aliceFeed := "http://alice.base.localhost:" + port + "/feed.xml"
	body := send(s, "GET", "alice.base.localhost:"+port, "/feed.xml", "", nil).Body.String()

	var mode atomic.Value
	mode.Store("feed")
	mux := http.NewServeMux()
	mux.HandleFunc("/feed.xml", func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case "feed":
			w.Header().Set("Content-Type", "application/rss+xml")
			w.Write([]byte(body))
		case "temporary":
			http.Redirect(w, r, aliceFeed, http.StatusFound)
		case "page":
			http.Redirect(w, r, "/about", http.StatusMovedPermanently)
		}
	})
	mux.HandleFunc("/about", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>not a feed</html>")) })
	site := httptest.NewServer(mux)
	defer site.Close()
	feedURL := site.URL + "/feed.xml"

	if _, err := s.Follow(ctx, "bob", feedURL); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"temporary", "page"} {
		mode.Store(m)
		s.Refresh(ctx, feedURL)
		if names, _ := s.st.FollowersOf(feedURL); len(names) != 1 {
			t.Errorf("after a %s redirect the followers are %v", m, names)
		}
		if names, _ := s.st.FollowersOf(aliceFeed); len(names) != 0 {
			t.Errorf("after a %s redirect Bob follows Alice: %v", m, names)
		}
	}
}

func TestMetrics(t *testing.T) {
	s, port, toks := hosts(t, Config{}, "alice")
	do(s, "POST", "alice.base.localhost:"+port, "/api/posts", toks["alice"], `{"markdown":"x"}`)
	do(s, "GET", "alice.base.localhost:"+port, "/", "", "")
	w := httptest.NewRecorder()
	s.Internal().ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1/metrics", nil))
	body := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	for _, want := range []string{"\nweft_pool_open 1\n", "\nweft_users 1\n", "\nweft_users_moved 0\n", "weft_pool_cap ", "weft_instance_read_wait_seconds_total "} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "\nweft_pool_hits_total ") || strings.Contains(body, "\nweft_pool_hits_total 0\n") {
		t.Errorf("the second request should have been a pool hit:\n%s", body)
	}
	// the public side has no such page
	if w := do(s, "GET", "alice.base.localhost:"+port, "/metrics", "", ""); w.Code != 404 {
		t.Errorf("/metrics on the public listener: %d", w.Code)
	}
}

// A domain whose TXT record is in place but whose DNS does not reach this server yet must not take the
// address from the user: nothing redirects, and the owner keeps the pages that manage the domain even
// after it has gone live and then stopped working.
func TestUnreachableDomainDoesNotLockTheOwnerOut(t *testing.T) {
	var token string
	s, port, toks := hosts(t, Config{
		LookupTXT: func(_ context.Context, n string) ([]string, error) { return []string{"weft-verify=" + token}, nil },
	}, "alice")
	self := s.cfg.ReachDomain
	s.cfg.ReachDomain = func(context.Context, string) (string, error) { return "", fmt.Errorf("no route to host") }
	old, custom := "alice.base.localhost:"+port, "alice.custom.localhost"
	do(s, "POST", old, "/api/posts", toks["alice"], `{"title":"Hello","markdown":"first"}`)
	cookie := ownerCookie(t, s, "alice")
	d, err := s.st.SetDomain("alice", "alice.custom.localhost")
	if err != nil {
		t.Fatal(err)
	}
	token = d.Token

	// TXT record found, domain unreachable: verified, not active, and the owner stays where they are
	w := send(s, "POST", old, "/domain/check", cookie, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "does not reach this server yet") {
		t.Fatalf("check, unreachable: %d\n%s", w.Code, w.Body)
	}
	if d, _ := s.st.DomainOf("alice"); !d.Verified || d.Active {
		t.Fatalf("domain %+v", d)
	}
	for _, path := range []string{"/", "/feed.xml", "/posts/anything", "/api/posts", "/login"} {
		if w := send(s, "GET", old, path, "", nil); w.Code >= 300 && w.Code < 400 {
			t.Errorf("%s redirects before the domain is active: %d %s", path, w.Code, w.Header().Get("Location"))
		}
	}
	profile := send(s, "GET", old, "/profile", cookie, nil).Body.String()
	for _, want := range []string{"(verified, but it does not reach this server yet)", "Check again", "nothing is redirected"} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile lacks %q", want)
		}
	}
	// the proof a domain is asked for: only on the domain itself
	if w := send(s, "GET", custom, "/.well-known/weft-domain", "", nil); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "weft-domain="+d.Token {
		t.Errorf("proof on the domain: %d %q", w.Code, w.Body)
	}
	if w := send(s, "GET", old, "/.well-known/weft-domain", "", nil); w.Code != 404 {
		t.Errorf("proof on the old address: %d", w.Code)
	}
	if w := send(s, "GET", "nobody.example", "/.well-known/weft-domain", "", nil); w.Code != 404 {
		t.Errorf("proof on a stranger's host: %d", w.Code)
	}

	// DNS arrives: "Check again" makes it the address
	s.cfg.ReachDomain = self
	w = send(s, "POST", old, "/domain/check", cookie, nil)
	if w.Code != 303 || w.Header().Get("Location") != "http://alice.custom.localhost:"+port+"/login" {
		t.Fatalf("check, reachable: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := send(s, "GET", old, "/feed.xml", "", nil); w.Code != 301 {
		t.Fatalf("the old feed once active: %d", w.Code)
	}
	// the domain later stops working: "Check" gives the address back, and the claim stays verified
	s.cfg.ReachDomain = func(context.Context, string) (string, error) { return "", fmt.Errorf("no route to host") }
	if w := send(s, "POST", old, "/domain/check", cookie, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "no longer reaches this server") {
		t.Fatalf("check of a domain that stopped working: %d\n%s", w.Code, w.Body)
	}
	if d, _ := s.st.DomainOf("alice"); !d.Verified || d.Active {
		t.Fatalf("after falling back: %+v", d)
	}
	if w := send(s, "GET", old, "/feed.xml", "", nil); w.Code != 200 {
		t.Fatalf("the old feed after falling back: %d", w.Code)
	}
	s.cfg.ReachDomain = self
	if w := send(s, "POST", old, "/domain/check", cookie, nil); w.Code != 303 {
		t.Fatalf("check once it works again: %d", w.Code)
	}
	if w := send(s, "POST", old, "/domain/check", cookie, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "The domain reaches this server.") {
		t.Fatalf("check of a working domain: %d", w.Code)
	}
	// and the pages that manage it answer at the old address even while the domain is the address
	for _, path := range []string{"/profile", "/login"} {
		if w := send(s, "GET", old, path, cookie, nil); w.Code != 200 {
			t.Errorf("%s at the old address once active: %d %s", path, w.Code, w.Header().Get("Location"))
		}
	}
	if w := send(s, "POST", old, "/domain/remove", cookie, nil); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "http://alice.base.localhost:") {
		t.Fatalf("remove from the old address: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w := send(s, "GET", old, "/", "", nil); w.Code != 200 {
		t.Fatalf("old address after removing: %d", w.Code)
	}
}

// With no host name configured, the profile page still says what a CNAME value is, and says when this server
// is one that no domain can reach.
func TestProfileExplainsTheCNAMEValue(t *testing.T) {
	for _, dev := range []bool{true, false} {
		s, port, _ := hosts(t, Config{}, "alice")
		s.cfg.AllowPrivate = dev
		cookie := ownerCookie(t, s, "alice")
		if _, err := s.st.SetDomain("alice", "alice.custom.localhost"); err != nil {
			t.Fatal(err)
		}
		page := send(s, "GET", "alice.base.localhost:"+port, "/profile", cookie, nil).Body.String()
		for _, want := range []string{"CNAME record whose value is this server's host name", "without <code>http://</code> or a port"} {
			if !strings.Contains(page, want) {
				t.Errorf("dev=%v: profile lacks %q", dev, want)
			}
		}
		if got := strings.Contains(page, "development mode and can only be reached from this computer"); got != dev {
			t.Errorf("dev=%v: the development note is shown: %v", dev, got)
		}
		if got := strings.Contains(page, "Ask whoever runs this server"); got == dev {
			t.Errorf("dev=%v: the 'ask' note is shown: %v", dev, got)
		}
	}
}
