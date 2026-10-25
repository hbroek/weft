package web

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hbroek/weft/internal/store"
)

// Likes and replies must behave the same whether the two people share a server or not. These tests run one
// scenario in both layouts, record what each person can see afterwards (feeds, pages as Markdown, API
// answers, the lists of who liked and who replied), and compare the two records once host names, ports,
// random address suffixes and dates are made alike.

// person is a user as the tests drive them: through the API with a token, and through pages with a cookie.
type person struct {
	name, host, base, tok, cookie string
	in                            *instance
}

func (p *person) api(t *testing.T, method, path, body string, want int) string {
	t.Helper()
	w := do(p.in.s, method, p.host, path, p.tok, body)
	if w.Code != want {
		t.Fatalf("%s %s%s: got %d, want %d: %s", method, p.host, path, w.Code, want, w.Body)
	}
	return w.Body.String()
}

func (p *person) post(t *testing.T, body string) string {
	t.Helper()
	var r struct{ URL string }
	if err := json.Unmarshal([]byte(p.api(t, "POST", "/api/posts", body, 201)), &r); err != nil {
		t.Fatal(err)
	}
	return r.URL
}

// page fetches a page as Markdown, signed in as the person (or as a visitor when visitor is set).
func (p *person) page(path string, visitor bool) string {
	r := httptest.NewRequest("GET", "http://"+p.host+path, nil)
	r.Header.Set("Accept", "text/markdown")
	if !visitor {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: p.cookie})
	}
	w := httptest.NewRecorder()
	p.in.s.ServeHTTP(w, r)
	return fmt.Sprintf("[%d]\n%s", w.Code, w.Body.String())
}

var reHidden = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)"`)

// replyViaPage answers a post the way a person does: opens the Reply link (the compose page for that post), and
// submits the form with everything the page filled in.
func (p *person) replyViaPage(t *testing.T, target, targetFeed, text string) string {
	t.Helper()
	q := url.Values{"reply_to": {target}, "reply_feed": {targetFeed}}
	w := do3(p.in.s, "GET", p.host, "/compose?"+q.Encode(), p.cookie)
	if w.Code != 200 {
		t.Fatalf("%s opening the reply page: %d", p.name, w.Code)
	}
	vals := url.Values{}
	for _, m := range reHidden.FindAllStringSubmatch(w.Body.String(), -1) {
		vals.Set(m[1], html.UnescapeString(m[2]))
	}
	vals.Set("markdown", text)
	got := p.form("/compose", "http://"+p.host+"/compose?"+q.Encode(), vals)
	if got.Code != http.StatusSeeOther {
		t.Fatalf("%s publishing the reply: %d %s", p.name, got.Code, got.Body)
	}
	// the new reply is the newest post in the person's feed
	var f struct {
		Items []struct {
			Link string `xml:"link"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal([]byte(p.public("/feed.xml")[len("[200]\n"):]), &f); err != nil || len(f.Items) == 0 {
		t.Fatalf("reading %s's feed: %v", p.name, err)
	}
	return f.Items[0].Link
}

func (p *person) apiGet(path string) string {
	w := do(p.in.s, "GET", p.host, path, p.tok, "")
	return fmt.Sprintf("[%d]\n%s", w.Code, w.Body.String())
}

func (p *person) public(path string) string {
	w := do(p.in.s, "GET", p.host, path, "", "")
	return fmt.Sprintf("[%d]\n%s", w.Code, w.Body.String())
}

func (p *person) form(path, referer string, vals url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://"+p.host+path, strings.NewReader(vals.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: cookieName, Value: p.cookie})
	if referer != "" {
		r.Header.Set("Referer", referer)
	}
	w := httptest.NewRecorder()
	p.in.s.ServeHTTP(w, r)
	return w
}

// instanceOn starts a server like newInstance, but for its own base domain, as two real servers have: its
// users live at <name>.<base>:<port>.
func instanceOn(t *testing.T, name, base string) *instance {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), name+".db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(Config{BaseDomain: base, AllowPrivate: true}, st)
	hits := &atomic.Int64{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		s.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	port := ts.URL[strings.LastIndex(ts.URL, ":")+1:]
	tok, err := st.AddUser(name, strings.ToUpper(name[:1])+name[1:], "")
	if err != nil {
		t.Fatal(err)
	}
	host := name + "." + base + ":" + port
	return &instance{ts: ts, s: s, host: host, base: "http://" + host, tok: tok, hits: hits}
}

// pairOnServers builds Alice and Bob, signed in, with no follows yet: both on one server (same), or each on a
// server of their own with its own base domain (apart).
func pairOnServers(t *testing.T, same bool) (alice, bob *person, settle func()) {
	t.Helper()
	var a, b *instance
	if same {
		a = instanceOn(t, "alice", "one.localhost")
		tok, err := a.s.st.AddUser("bob", "Bob", "")
		if err != nil {
			t.Fatal(err)
		}
		port := a.host[strings.LastIndex(a.host, ":")+1:]
		b = &instance{ts: a.ts, s: a.s, host: "bob.one.localhost:" + port, base: "http://bob.one.localhost:" + port, tok: tok, hits: a.hits}
	} else {
		a, b = instanceOn(t, "alice", "one.localhost"), instanceOn(t, "bob", "two.localhost")
	}
	mk := func(name string, in *instance) *person {
		p := &person{name: name, host: in.host, base: in.base, tok: in.tok, in: in}
		if err := in.s.space(name).SetPassword("correct horse"); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", in.base+"/login", strings.NewReader(url.Values{"password": {"correct horse"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		in.s.ServeHTTP(w, r)
		if p.cookie = cookieOf(w); p.cookie == "" {
			t.Fatalf("%s cannot sign in: %d", name, w.Code)
		}
		return p
	}
	alice, bob = mk("alice", a), mk("bob", b)
	insts := []*instance{a}
	if b != a {
		insts = append(insts, b)
	}
	settle = func() {
		ctx := context.Background()
		for range 3 {
			for _, i := range insts {
				i.s.RefreshAll(ctx)
			}
			settleAll(ctx, insts...)
		}
	}
	return
}

func settleAll(ctx context.Context, insts ...*instance) { settle(ctx, insts...) }

var (
	reHost  = regexp.MustCompile(`([a-z]+)\.(?:[a-z0-9]+\.)?localhost:\d+`)
	reHostE = regexp.MustCompile(`(?:[a-z0-9]+\.)?localhost%3A\d+`)
	reSlugE = regexp.MustCompile(`(%2Fposts%2F[a-z0-9-]+)\.[a-z0-9]{4}\b`)
	reSlug  = regexp.MustCompile(`(/posts/[a-z0-9-]+)\.[a-z0-9]{4}\b`)
	reUUID  = regexp.MustCompile(`urn:uuid:[0-9a-f-]{36}`)
	reRFC   = regexp.MustCompile(`[A-Z][a-z]{2}, \d\d [A-Z][a-z]{2} \d{4} \d\d:\d\d:\d\d [+-]\d{4}`)
	reISO   = regexp.MustCompile(`\d{4}-\d\d-\d\d[T ][\d:]+(Z|[+-]\d\d:?\d\d)?`)
	reAgo   = regexp.MustCompile(`\b(just now|\d+ (sec|secs|min|mins|hr|hrs|hours?|days?) ago)\b`)
	reHuman = regexp.MustCompile(`[A-Z][a-z]{2} \d{1,2}, \d{4}(,? \d{1,2}:\d\d( ?[AP]M)?)?`)
	reTok   = regexp.MustCompile(`"id":\d+`)
)

// norm makes two records of the same scenario comparable.
func norm(s string) string {
	s = reHostE.ReplaceAllString(s, "localhost%3APORT")
	s = reSlugE.ReplaceAllString(s, "$1.SUFFIX")
	s = reHost.ReplaceAllString(s, "$1.H")
	s = reSlug.ReplaceAllString(s, "$1.SUFFIX")
	s = reUUID.ReplaceAllString(s, "urn:uuid:UUID")
	s = reRFC.ReplaceAllString(s, "DATE")
	s = reISO.ReplaceAllString(s, "DATE")
	s = reAgo.ReplaceAllString(s, "AGO")
	s = reHuman.ReplaceAllString(s, "DATE")
	return s
}

// recorder returns where observations are kept and the function that records one.
func recorder() (map[string]string, func(label, text string)) {
	rec := map[string]string{}
	return rec, func(label, text string) {
		text = norm(text)
		if strings.HasSuffix(label, "timeline") || strings.HasSuffix(label, "timeline with likes") || strings.HasSuffix(label, "home") {
			// things that happened in the same second are ordered by their random addresses, which differ from
			// run to run, so compare what is listed and not in which order
			var lines []string
			for _, l := range strings.Split(text, "\n") {
				if strings.TrimSpace(l) != "" && l != "---" {
					lines = append(lines, l)
				}
			}
			sort.Strings(lines)
			text = strings.Join(lines, "\n")
		}
		rec[label] = text
	}
}

// parityRecord runs the scenario and returns what was observed, by label.
func parityRecord(t *testing.T, same bool, scenario func(t *testing.T, alice, bob *person, settle func(), see func(label, text string))) map[string]string {
	alice, bob, settle := pairOnServers(t, same)
	rec, see := recorder()
	scenario(t, alice, bob, settle, see)
	return rec
}

// trioOnServers is pairOnServers with Carol: all three on one server, or each on a server of their own.
func trioOnServers(t *testing.T, same bool) (alice, bob, carol *person, settle func()) {
	t.Helper()
	alice, bob, pairSettle := pairOnServers(t, same)
	var in *instance
	if same {
		in = alice.in
	} else {
		in = instanceOn(t, "carol", "three.localhost")
	}
	if same {
		carol = userOn(t, in, "carol")
	} else {
		carol = &person{name: "carol", host: in.host, base: in.base, tok: in.tok, in: in}
		if err := in.s.space("carol").SetPassword("correct horse"); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", in.base+"/login", strings.NewReader(url.Values{"password": {"correct horse"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		in.s.ServeHTTP(w, r)
		if carol.cookie = cookieOf(w); carol.cookie == "" {
			t.Fatalf("carol cannot sign in: %d", w.Code)
		}
	}
	settle = func() {
		ctx := context.Background()
		insts := []*instance{alice.in, bob.in, carol.in}
		seen := map[*instance]bool{}
		for range 3 {
			for _, i := range insts {
				if !seen[i] || true {
					i.s.RefreshAll(ctx)
				}
			}
			var uniq []*instance
			seen = map[*instance]bool{}
			for _, i := range insts {
				if !seen[i] {
					seen[i] = true
					uniq = append(uniq, i)
				}
			}
			settleAll(ctx, uniq...)
		}
	}
	_ = pairSettle
	return
}

// compare reports every label whose two records differ, with the lines that are in only one of them.
func compare(t *testing.T, same, apart map[string]string) {
	t.Helper()
	labels := map[string]bool{}
	for k := range same {
		labels[k] = true
	}
	for k := range apart {
		labels[k] = true
	}
	var names []string
	for k := range labels {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, l := range names {
		a, b := same[l], apart[l]
		if a == b {
			continue
		}
		as, bs := lineSet(a), lineSet(b)
		var out strings.Builder
		for _, ln := range strings.Split(a, "\n") {
			if !bs[ln] && strings.TrimSpace(ln) != "" {
				fmt.Fprintf(&out, "  same server only: %s\n", ln)
			}
		}
		for _, ln := range strings.Split(b, "\n") {
			if !as[ln] && strings.TrimSpace(ln) != "" {
				fmt.Fprintf(&out, "  two servers only: %s\n", ln)
			}
		}
		if out.Len() == 0 {
			out.WriteString("  same lines, different order or count\n")
		}
		t.Errorf("%s differs:\n%s", l, out.String())
	}
}

func lineSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(s, "\n") {
		m[l] = true
	}
	return m
}

// Who follows whom, for the scenario.
type follows struct{ aliceFollowsBob, bobFollowsAlice bool }

// approvePending approves every reply waiting in the person's queue (a reply from someone the owner does not
// follow waits there), and says how many there were.
func (p *person) approvePending(t *testing.T) int {
	t.Helper()
	var rs []struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(p.api(t, "GET", "/api/replies?status=pending", "", 200)), &rs); err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		p.api(t, "POST", fmt.Sprintf("/api/replies/%d/status", r.ID), `{"status":"approved"}`, 204)
	}
	return len(rs)
}

// Two people like and reply to each other's posts; each then looks at feeds, pages, the API and the lists of
// likers and repliers. Replies from someone the owner does not follow wait for approval, and the owner approves them.
func TestParityMutualFollowers(t *testing.T) {
	parityScenario(t, "page", follows{true, true})
}

// The same, with replies sent through the API, which carries no snapshot of the parent.
func TestParityMutualFollowersAPIReplies(t *testing.T) {
	parityScenario(t, "api", follows{true, true})
}

// Alice follows Bob and Bob does not follow Alice.
func TestParityAliceFollowsBobOnly(t *testing.T) {
	parityScenario(t, "page", follows{aliceFollowsBob: true})
}

// Bob follows Alice and Alice does not follow Bob.
func TestParityBobFollowsAliceOnly(t *testing.T) {
	parityScenario(t, "page", follows{bobFollowsAlice: true})
}

func parityScenario(t *testing.T, how string, fl follows) {
	scenario := func(t *testing.T, alice, bob *person, settle func(), see func(label, text string)) {
		reply := func(p *person, target, targetFeed, text string) string {
			if how == "api" {
				return p.post(t, `{"markdown":"`+text+`","in_reply_to":"`+target+`","in_reply_to_feed":"`+targetFeed+`"}`)
			}
			return p.replyViaPage(t, target, targetFeed, text)
		}
		if fl.aliceFollowsBob {
			alice.api(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
		}
		if fl.bobFollowsAlice {
			bob.api(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
		}
		settle()

		// Alice posts; Bob likes it and replies to it
		pa := alice.post(t, `{"title":"Dark Matter","markdown":"I love watching Dark Matter."}`)
		settle()
		bob.api(t, "POST", "/api/likes", `{"target":"`+pa+`","target_feed":"`+alice.base+`/feed.xml"}`, 200)
		rb := reply(bob, pa, alice.base+"/feed.xml", "Same here, the second season is better.")
		settle()

		observe := func(stage string) {
			for _, p := range []*person{alice, bob} {
				who := p.name + " " + stage + " "
				see(who+"feed", p.public("/feed.xml"))
				see(who+"likes.xml", p.public("/likes.xml"))
				see(who+"home", p.page("/", false))
				see(who+"timeline", p.page("/timeline", false))
				see(who+"timeline with likes", p.page("/timeline?likes=1", false))
				see(who+"replies queue", p.page("/replies", false))
				see(who+"api timeline", p.apiGet("/api/timeline"))
				see(who+"api replies", p.apiGet("/api/replies"))
			}
			see(stage+" alice post as owner", alice.page(pathOf(pa), false))
			see(stage+" alice post as visitor", alice.page(pathOf(pa), true))
			see(stage+" alice post replies.xml", alice.public(pathOf(pa)+"/replies.xml"))
			see(stage+" alice post likes.xml", alice.public(pathOf(pa)+"/likes.xml"))
			see(stage+" alice liked-by as owner", alice.page("/liked-by?target="+url.QueryEscape(pa), false))
			see(stage+" alice liked-by as visitor", alice.page("/liked-by?target="+url.QueryEscape(pa), true))
			see(stage+" bob liked-by on alice's post", bob.page("/liked-by?target="+url.QueryEscape(pa), false))
			see(stage+" alice api likes received", alice.apiGet("/api/likes/received?post_id=1"))
			see(stage+" alice thread", alice.page("/thread?url="+url.QueryEscape(pa), false))
			see(stage+" bob thread of alice's post", bob.page("/thread?url="+url.QueryEscape(pa), false))
			see(stage+" bob thread of his reply", bob.page("/thread?url="+url.QueryEscape(rb), false))
			see(stage+" bob reply page", bob.page(pathOf(rb), true))
			see(stage+" api thread (alice)", alice.apiGet("/api/thread?url="+url.QueryEscape(pa)))
			see(stage+" api thread (bob)", bob.apiGet("/api/thread?url="+url.QueryEscape(pa)))
		}
		observe("after like and reply")

		// Alice approves what waits in her queue, if anything does
		see("replies waiting for alice", fmt.Sprint(alice.approvePending(t)))
		settle()
		observe("after approval")

		// Alice answers Bob's reply, and also likes it; Bob approves what waits for him
		ra := reply(alice, rb, bob.base+"/feed.xml", "Agreed, season two.")
		alice.api(t, "POST", "/api/likes", `{"target":"`+rb+`","target_feed":"`+bob.base+`/feed.xml"}`, 200)
		settle()
		see("replies waiting for bob", fmt.Sprint(bob.approvePending(t)))
		settle()
		see("alice's answer page", alice.page(pathOf(ra), true))
		see("bob's reply liked-by", bob.page("/liked-by?target="+url.QueryEscape(rb), false))
		see("bob's reply page as owner", bob.page(pathOf(rb), false))
		see("bob's reply replies.xml", bob.public(pathOf(rb)+"/replies.xml"))
		observe("after answer")

		// Bob takes his like back
		bob.api(t, "DELETE", "/api/likes", `{"target":"`+pa+`"}`, 204)
		settle()
		observe("after unlike")
	}
	same := parityRecord(t, true, scenario)
	apart := parityRecord(t, false, scenario)
	if dir := os.Getenv("WEFT_PARITY_DUMP"); dir != "" { // for reading both records side by side
		for name, rec := range map[string]map[string]string{"same": same, "apart": apart} {
			var keys []string
			for k := range rec {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var b strings.Builder
			for _, k := range keys {
				fmt.Fprintf(&b, "===== %s\n%s\n", k, rec[k])
			}
			os.WriteFile(filepath.Join(dir, name+".txt"), []byte(b.String()), 0o644)
		}
	}
	compare(t, same, apart)
}

// A host that looks like one of this server's users but is not one (the user is gone) is a post like any other:
// the reply's context comes from the feed the reader follows.
func TestReplyContextWhenTheLocalUserIsGone(t *testing.T) {
	alice, bob := instanceOn(t, "alice", "one.localhost"), instanceOn(t, "bob", "one.localhost") // the same base domain
	ctx := t.Context()
	pa := alice.post(t, `{"title":"Dark Matter","markdown":"I love watching Dark Matter."}`)
	bob.call(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
	settle(ctx, alice, bob)
	bob.s.RefreshAll(ctx)
	u, _ := bob.s.st.UserByName("bob")
	r := httptest.NewRequest("GET", bob.base+"/", nil)
	who, card := bob.s.replyContext(r, &req{sp: bob.s.space("bob"), host: u}, pa)
	if who != "a post from Alice" || card == nil || card.Title != "Dark Matter" {
		t.Errorf("who %q, card %+v", who, card)
	}
}

// Nobody follows anyone: Bob finds Alice's post by its address.
func TestParityNobodyFollows(t *testing.T) {
	parityScenario(t, "page", follows{})
}

// userOn adds a signed-in person to an existing server.
func userOn(t *testing.T, in *instance, name string) *person {
	t.Helper()
	tok, err := in.s.st.AddUser(name, strings.ToUpper(name[:1])+name[1:], "")
	if err != nil {
		t.Fatal(err)
	}
	base := in.base[:strings.Index(in.base, "//")+2] // "http://"
	host := name + in.host[strings.Index(in.host, "."):]
	p := &person{name: name, host: host, base: base + host, tok: tok, in: in}
	if err := in.s.space(name).SetPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", p.base+"/login", strings.NewReader(url.Values{"password": {"correct horse"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	in.s.ServeHTTP(w, r)
	if p.cookie = cookieOf(w); p.cookie == "" {
		t.Fatalf("%s cannot sign in: %d", name, w.Code)
	}
	return p
}

// A post found by its address, not through a followed feed, still has a Likes page for the person who liked it: it
// names them, lists the others from the author's liked-by feed with a Follow button, and is linked from the like.
func TestLikesPageOfAPostFromAStranger(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprint("same server ", same), func(t *testing.T) {
			alice, bob, settle := pairOnServers(t, same)
			carol := userOn(t, alice.in, "carol") // a third person, on Alice's server
			if !same {
				carol = userOn(t, bob.in, "carol") // or on Bob's
			}
			pa := alice.post(t, `{"title":"Dark Matter","markdown":"I love watching Dark Matter."}`)
			carol.api(t, "POST", "/api/likes", `{"target":"`+pa+`"}`, 200)
			bob.api(t, "POST", "/api/likes", `{"target":"`+pa+`"}`, 200)
			settle()
			page := bob.page("/liked-by?target="+url.QueryEscape(pa), false)
			i := strings.Index(page, "Also liked by")
			if !strings.Contains(page, "[200]") || !strings.Contains(page, "of [Dark Matter]") || !strings.Contains(page, "- You") {
				t.Fatalf("bob's Likes page:\n%s", page)
			}
			if i < 0 || !strings.Contains(page[i:], "Carol") {
				t.Errorf("no section for the people Bob does not follow:\n%s", page)
			}
			tl := do4(bob.in, "/timeline?likes=1", bob.cookie)
			if !strings.Contains(tl, `<a href="/liked-by?target=`+url.QueryEscape(pa)+`">Who liked</a>`) || !strings.Contains(tl, "“Dark Matter”") {
				t.Errorf("the like in Bob's timeline lacks the title or the link to who liked:\n%s", tl)
			}
		})
	}
}

// Alice and Bob follow each other and Carol follows Alice. Bob and Carol both like and reply to Alice's post. Carol
// follows neither Bob nor anyone Bob follows, so she meets Bob as a stranger; Alice does not follow Carol.
func TestParityThirdPerson(t *testing.T) {
	scenario := func(t *testing.T, alice, bob, carol *person, settle func(), see func(label, text string)) {
		reply := func(p *person, target, targetFeed, text string) string {
			return p.replyViaPage(t, target, targetFeed, text)
		}
		alice.api(t, "POST", "/api/following", `{"url":"`+bob.base+`/"}`, 200)
		bob.api(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
		carol.api(t, "POST", "/api/following", `{"url":"`+alice.base+`/"}`, 200)
		settle()

		pa := alice.post(t, `{"title":"Dark Matter","markdown":"I love watching Dark Matter."}`)
		settle()
		bob.api(t, "POST", "/api/likes", `{"target":"`+pa+`","target_feed":"`+alice.base+`/feed.xml"}`, 200)
		rb := reply(bob, pa, alice.base+"/feed.xml", "Same here, the second season is better.")
		carol.api(t, "POST", "/api/likes", `{"target":"`+pa+`","target_feed":"`+alice.base+`/feed.xml"}`, 200)
		rc := reply(carol, pa, alice.base+"/feed.xml", "Watching it tonight.")
		settle()

		observe := func(stage string) {
			for _, p := range []*person{alice, bob, carol} {
				who := p.name + " " + stage + " "
				see(who+"feed", p.public("/feed.xml"))
				see(who+"likes.xml", p.public("/likes.xml"))
				see(who+"home", p.page("/", false))
				see(who+"timeline", p.page("/timeline", false))
				see(who+"timeline with likes", p.page("/timeline?likes=1", false))
				see(who+"replies queue", p.page("/replies", false))
				see(who+"api timeline", p.apiGet("/api/timeline"))
				see(who+"api replies", p.apiGet("/api/replies"))
				see(who+"liked-by on alice's post", p.page("/liked-by?target="+url.QueryEscape(pa), false))
				see(who+"thread of alice's post", p.page("/thread?url="+url.QueryEscape(pa), false))
				see(who+"api thread of alice's post", p.apiGet("/api/thread?url="+url.QueryEscape(pa)))
			}
			see(stage+" alice post as visitor", alice.page(pathOf(pa), true))
			see(stage+" alice post replies.xml", alice.public(pathOf(pa)+"/replies.xml"))
			see(stage+" alice post likes.xml", alice.public(pathOf(pa)+"/likes.xml"))
			see(stage+" alice liked-by as visitor", alice.page("/liked-by?target="+url.QueryEscape(pa), true))
			see(stage+" bob reply page", bob.page(pathOf(rb), true))
			see(stage+" carol reply page", carol.page(pathOf(rc), true))
			see(stage+" carol thread of her reply", carol.page("/thread?url="+url.QueryEscape(rc), false))
		}
		observe("after likes and replies")
		// the reader who only follows Alice reads the replies in the thread as Markdown, too
		if th := carol.page("/thread?url="+url.QueryEscape(pa), false); !strings.Contains(th, "Same here, the second season is better.") {
			t.Errorf("carol's thread lacks Bob's reply text:\n%s", th)
		}

		see("replies waiting for alice", fmt.Sprint(alice.approvePending(t)))
		settle()
		observe("after approval")

		// Carol takes her like back
		carol.api(t, "DELETE", "/api/likes", `{"target":"`+pa+`"}`, 204)
		settle()
		observe("after carol unlikes")
	}
	run := func(same bool) map[string]string {
		alice, bob, carol, settle := trioOnServers(t, same)
		rec, see := recorder()
		scenario(t, alice, bob, carol, settle, see)
		return rec
	}
	same, apart := run(true), run(false)
	if dir := os.Getenv("WEFT_PARITY_DUMP"); dir != "" {
		for name, rec := range map[string]map[string]string{"same": same, "apart": apart} {
			var keys []string
			for k := range rec {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var b strings.Builder
			for _, k := range keys {
				fmt.Fprintf(&b, "===== %s\n%s\n", k, rec[k])
			}
			os.WriteFile(filepath.Join(dir, name+".txt"), []byte(b.String()), 0o644)
		}
	}
	compare(t, same, apart)
}
