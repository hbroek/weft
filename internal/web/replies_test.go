package web

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/store"
)

// Replies under a post are cards with the author's avatar, a relative time and the text, the replies to a
// reply nested beneath it, and microformats that parsers read as comments.
func TestRepliesUnderAPostLookLikeComments(t *testing.T) {
	s, tok := setup(t)
	do(s, "POST", host, "/api/posts", tok, `{"title":"Pickleball","markdown":"We played."}`)
	sp := s.space("alice")
	posts, err := sp.RecentPosts(sp.UID(), 1)
	if err != nil || len(posts) != 1 {
		t.Fatal(err)
	}
	pid := posts[0].ID
	now := time.Now()
	s.st.RawDBForTests().Exec(`INSERT INTO feeds(url,title,image,fetched) VALUES('http://bob.example/feed.xml','Bob B','http://bob.example/me.jpg',?)`, now.Unix())
	for _, r := range []store.Reply{
		{PostID: pid, Source: "http://bob.example/posts/great.k3x9", FeedURL: "http://bob.example/feed.xml", Author: "Bob B", Markdown: "**Great** game, see [court](http://c.example)",
			Status: store.StatusApproved, Published: now.Add(-2 * time.Hour), Received: now},
		{PostID: pid, Source: "http://carol.example/p/1", FeedURL: "http://carol.example/feed.xml", Text: "Count me in <script>alert(1)</script>",
			Status: store.StatusApproved, Published: now.Add(-30 * time.Minute), Received: now},
		{PostID: pid, Source: "http://dave.example/p/9", FeedURL: "http://dave.example/feed.xml", Text: "Hidden until approved",
			Status: store.StatusPending, Published: now, Received: now},
	} {
		if _, err := sp.UpsertReply(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sp.ReplaceSubreplies(pid, "http://bob.example/posts/great.k3x9", []store.Subreply{
		{PostID: pid, Via: "http://bob.example/posts/great.k3x9", Parent: "http://bob.example/posts/great.k3x9", Source: "http://erin.example/p/2", FeedURL: "http://erin.example/feed.xml", Author: "Erin", Text: "Agreed with Bob", Published: now.Add(-time.Hour)},
		{PostID: pid, Via: "http://bob.example/posts/great.k3x9", Parent: "http://erin.example/p/2", Source: "http://frank.example/p/3", FeedURL: "http://frank.example/feed.xml", Author: "Frank", Text: "And with Erin", Published: now.Add(-10 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}

	slug := posts[0].Slug
	page := do(s, "GET", host, "/posts/"+slug, "", "").Body.String()
	for _, want := range []string{
		`<h3>4 replies</h3>`, // two direct, two nested; the pending one is not shown
		`class="p-comment h-cite"`, `class="p-author h-card"`, `class="u-url" href="http://bob.example/posts/great.k3x9"`, `class="dt-published"`, `class="p-content"`,
		`<img src="http://bob.example/me.jpg"`, `<strong class="p-name">Bob B</strong>`, // the avatar of the feed, and the name
		`2 hrs ago`, `<strong>Great</strong> game`, `href="http://c.example"`,
		`<span>E</span>`, // a reply from a feed this server never fetched: the first letter
	} {
		if !strings.Contains(page, want) {
			t.Errorf("replies lack %q", want)
		}
	}
	if strings.Contains(page, "Hidden until approved") || strings.Contains(page, "<script>alert") {
		t.Errorf("a pending reply is shown, or markup came through")
	}
	// the replies to Bob's reply sit inside it, and the reply to Erin inside hers
	bob, carol := strings.Index(page, "Great</strong> game"), strings.Index(page, "Count me in")
	erin, frank := strings.Index(page, "Agreed with Bob"), strings.Index(page, "And with Erin")
	if !(bob < erin && erin < frank && frank < carol) {
		t.Errorf("order: bob %d erin %d frank %d carol %d", bob, erin, frank, carol)
	}
	if n := strings.Count(page, `<ol class="comments">`); n != 3 { // the replies, those to Bob's, the one to Erin's
		t.Errorf("nesting: %d lists", n)
	}
	// only the owner gets a link to answer a reply
	if strings.Contains(page, "/compose?reply_to=") {
		t.Errorf("a visitor is offered to reply as the owner")
	}
	// the owner can answer a reply from under the post
	if err := sp.SetPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	ck := cookieOf(form(s, "POST", "/login", "", url.Values{"password": {"correct horse"}}, ""))
	if p := do3(s, "GET", host, "/posts/"+slug, ck).Body.String(); !strings.Contains(p, `href="/compose?reply_to=http%3A%2F%2Fbob.example%2Fposts%2Fgreat.k3x9&amp;reply_feed=http%3A%2F%2Fbob.example%2Ffeed.xml"`) ||
		!strings.Contains(p, "1 reply awaits your approval") {
		t.Errorf("the owner's page lacks the reply link or the pending notice")
	}
}

// The owner sees how many replies wait for approval: a number in the menu on every page, and a line on the home page.
// It goes when they are dealt with, and a visitor never sees it.
func TestPendingRepliesAreShownToTheOwner(t *testing.T) {
	s, tok := setup(t)
	host := "alice.localhost:8080"
	do(s, "POST", host, "/api/posts", tok, `{"title":"Pickleball","markdown":"We played."}`)
	sp := s.space("alice")
	posts, _ := sp.RecentPosts(sp.UID(), 1)
	pid := posts[0].ID
	ck := ownerCookie(t, s, "alice")
	hasHint := func(path string) (menu, line bool) {
		p := do3(s, "GET", host, path, ck).Body.String()
		return strings.Contains(p, `class="badge"`), strings.Contains(p, "waiting for your review")
	}
	if m, l := hasHint("/"); m || l {
		t.Fatalf("nothing waits, yet the page shows a hint: menu %v line %v", m, l)
	}
	now := time.Now()
	for i, src := range []string{"http://dave.example/p/9", "http://erin.example/p/2"} {
		if _, err := sp.UpsertReply(store.Reply{PostID: pid, Source: src, FeedURL: "http://x.example/feed.xml", Text: "hello " + src,
			Status: store.StatusPending, Published: now.Add(time.Duration(i) * time.Minute), Received: now}); err != nil {
			t.Fatal(err)
		}
	}
	waiting, _ := sp.RepliesForUser(sp.UID(), store.StatusPending)
	if len(waiting) != 2 {
		t.Fatalf("%d replies waiting, want 2", len(waiting))
	}
	if m, l := hasHint("/"); !m || !l {
		t.Errorf("home page: menu %v line %v, want both", m, l)
	}
	if p := do3(s, "GET", host, "/", ck).Body.String(); !strings.Contains(p, "2 replies are waiting for your review") || !strings.Contains(p, `class="badge" title="2 waiting for your approval">2</span>`) {
		t.Errorf("the count is wrong: %s", p)
	}
	if m, l := hasHint("/replies"); !m || l {
		t.Errorf("the Replies page: menu %v line %v, want the number only", m, l)
	}
	if p := do(s, "GET", host, "/", "", "").Body.String(); strings.Contains(p, `class="badge"`) || strings.Contains(p, "waiting for your review") {
		t.Errorf("a visitor sees the owner's hint")
	}
	for _, r := range waiting {
		if _, err := sp.SetReplyStatus(sp.UID(), r.ID, store.StatusApproved); err != nil {
			t.Fatal(err)
		}
	}
	if m, l := hasHint("/"); m || l {
		t.Errorf("all are approved, yet menu %v line %v", m, l)
	}
}
