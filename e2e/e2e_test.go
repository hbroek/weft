// Package e2e runs the whole loop: three independent instances, four users, one of them an AI agent.
package e2e

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hbroek/weft/internal/agent"
	"github.com/hbroek/weft/internal/store"
	"github.com/hbroek/weft/internal/web"
)

type inst struct {
	s    *web.Server
	st   *store.Instance
	port string
	toks map[string]string
}

func start(t *testing.T, name string, users ...string) *inst {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), name+".db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := web.New(web.Config{BaseDomain: "localhost", AllowPrivate: true}, st)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	i := &inst{s: s, st: st, port: ts.URL[strings.LastIndex(ts.URL, ":")+1:], toks: map[string]string{}}
	for _, u := range users {
		tok, err := st.AddUser(u, strings.ToUpper(u[:1])+u[1:], "")
		if err != nil {
			t.Fatal(err)
		}
		sp, release, err := st.Space(u)
		if err != nil {
			t.Fatal(err)
		}
		if err := sp.SetPassword("password-" + u); err != nil {
			t.Fatal(err)
		}
		release()
		i.toks[u] = tok
	}
	return i
}

func (i *inst) client(user string) *agent.Client {
	return agent.NewClient("http://"+user+".localhost:"+i.port, i.toks[user])
}

// settle drains every instance's Webmention queues; it fails if they never empty (a loop).
func settle(t *testing.T, ctx context.Context, insts ...*inst) {
	t.Helper()
	for round := 0; round < 30; round++ {
		for _, i := range insts {
			i.s.ProcessQueues(ctx)
		}
		pending := 0
		for _, i := range insts {
			pending += i.s.Pending()
		}
		if pending == 0 {
			return
		}
	}
	t.Fatal("webmention queues did not settle: notification loop?")
}

func get(t *testing.T, c *agent.Client, path string) string {
	t.Helper()
	res, err := c.HTTP.Get(c.Base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		t.Fatalf("GET %s: %d %s", path, res.StatusCode, b)
	}
	return string(b)
}

// login returns a client holding a session cookie, as a human in a browser would.
func login(t *testing.T, c *agent.Client, user string) *agent.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	h := *c.HTTP
	h.Jar = jar
	form := url.Values{"username": {user}, "password": {"password-" + user}}
	req, _ := http.NewRequest("POST", c.Base+"/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", c.Base)
	res, err := h.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return &agent.Client{Base: c.Base, HTTP: &h}
}

// Alice (instance 1) posts. Bob (instance 2) replies, Carol (instance 3) reacts, and Dana, an
// agent on instance 1, replies and reacts through the public API alone. Everything travels over
// real HTTP between the instances.
func TestRoundTripWithAgent(t *testing.T) {
	ctx := t.Context()
	one := start(t, "one", "alice", "dana")
	two := start(t, "two", "bob")
	three := start(t, "three", "carol")
	all := []*inst{one, two, three}

	alice, dana := one.client("alice"), one.client("dana")
	bob, carol := two.client("bob"), three.client("carol")

	// Follows: Alice follows everyone (so replies auto-approve); the others follow Alice.
	for _, c := range []*agent.Client{bob, carol, dana} {
		if err := alice.Follow(ctx, c.Base+"/"); err != nil {
			t.Fatal(err)
		}
		if err := c.Follow(ctx, alice.Base+"/"); err != nil {
			t.Fatal(err)
		}
	}

	root, err := alice.Post(ctx, "Textcasting", "Who wants to try a reader that **replies**?", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Readers fetch Alice's feed.
	for _, i := range all {
		i.s.RefreshAll(ctx)
	}

	// Bob replies and Carol reacts, as humans or scripts would.
	if _, err := bob.Post(ctx, "", "I do.", root.URL, alice.FeedURL()); err != nil {
		t.Fatal(err)
	}
	if err := carol.Like(ctx, root.URL, alice.FeedURL()); err != nil {
		t.Fatal(err)
	}

	// The agent, using nothing but Dana's token and the public API.
	ag := agent.New(dana, &agent.Stub{}, agent.Config{Persona: "Dana-bot"})
	n, err := ag.RunCycle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 { // one like and one reply to Alice's post
		t.Fatalf("agent acted %d times, want 2", n)
	}
	// Running again does nothing: each post is handled once.
	if n, _ := ag.RunCycle(ctx); n != 0 {
		t.Fatalf("second cycle acted %d times, want 0", n)
	}
	settle(t, ctx, all...)

	// Alice's replies feed lists Bob and Dana; her moderation API shows both approved.
	rs, err := alice.Replies(ctx, "approved")
	if err != nil {
		t.Fatal(err)
	}
	authors := map[string]bool{}
	for _, r := range rs {
		authors[r.Author] = true
	}
	if len(rs) != 2 || !authors["Bob"] || !authors["Dana"] {
		t.Fatalf("approved replies: %+v", rs)
	}
	replies := get(t, alice, strings.TrimPrefix(root.URL, alice.Base)+"/replies.xml")
	if !strings.Contains(replies, "I do.") || !strings.Contains(replies, "Dana-bot here") {
		t.Fatalf("replies feed:\n%s", replies)
	}

	// Alice's feed counts the two likes (Carol's and Dana's).
	feed := get(t, alice, "/feed.xml")
	if !strings.Contains(feed, `<weft:likes count="2">`) || !strings.Contains(feed, `<weft:replies href="`) {
		t.Fatalf("alice's feed lacks the tallies:\n%s", feed)
	}

	// The agent can read the whole conversation from any post in it, root first.
	if th, err := dana.Thread(ctx, root.URL); err != nil || len(th) != 3 || th[0].URL != root.URL || th[0].Depth != 0 || th[1].Depth != 1 {
		t.Fatalf("thread for the agent: %+v err=%v", th, err)
	}

	// Alice answers Dana's reply; the agent notices a reply to its own post and answers back
	// exactly once (a budget keeps two bots from talking forever).
	m := regexp.MustCompile(`<link>(` + regexp.QuoteMeta(dana.Base) + `/posts/[^<]+)</link>`).FindStringSubmatch(get(t, dana, "/feed.xml"))
	if m == nil {
		t.Fatal("no post link in Dana's feed")
	}
	dr := agent.Reply{Source: m[1]}
	if _, err := alice.Post(ctx, "", "Thanks Dana!", dr.Source, dana.FeedURL()); err != nil {
		t.Fatal(err)
	}
	settle(t, ctx, all...)
	// Alice's follow-up is on Dana's timeline at once (same instance): one like and one reply,
	// not two replies, because the follow-up and the reply notification are the same thread.
	if n, err := ag.RunCycle(ctx); err != nil || n != 2 {
		t.Fatalf("agent reply to a reply: n=%d err=%v", n, err)
	}
	settle(t, ctx, all...)
	if n, _ := ag.RunCycle(ctx); n != 0 {
		t.Fatalf("agent kept talking: %d", n)
	}

	// Alice's curated thread view (a logged-in human) shows everyone.
	page := get(t, login(t, alice, "alice"), "/thread?view=curated&url="+url.QueryEscape(root.URL))
	for _, want := range []string{"I do.", "Dana-bot here", "Thanks Dana!"} {
		if !strings.Contains(page, want) {
			t.Errorf("curated thread lacks %q:\n%s", want, page)
		}
	}
}

type failingBrain struct{ calls int }

func (b *failingBrain) Decide(context.Context, agent.Persona, agent.Item, []agent.ThreadNode) (agent.Decision, error) {
	b.calls++
	return agent.Decision{}, errors.New("model said no")
}
func (b *failingBrain) Post(context.Context, agent.Persona) (string, string, error) {
	return "", "", errors.New("model said no")
}

// A post the model failed on is tried again in later cycles, but not forever.
func TestAgentRetriesAFailedPostThreeTimes(t *testing.T) {
	ctx := t.Context()
	one := start(t, "one", "alice", "dana")
	alice, dana := one.client("alice"), one.client("dana")
	if err := dana.Follow(ctx, alice.Base+"/"); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Post(ctx, "", "Anyone there?", "", ""); err != nil {
		t.Fatal(err)
	}
	one.s.RefreshAll(ctx)
	b := &failingBrain{}
	ag := agent.New(dana, b, agent.Config{Persona: "Dana-bot", Log: log.New(io.Discard, "", 0)})
	for i := 0; i < 6; i++ {
		ag.RunCycle(ctx)
	}
	if b.calls != 3 {
		t.Fatalf("the brain was asked %d times, want 3", b.calls)
	}
}
