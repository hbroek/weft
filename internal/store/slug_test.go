package store

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestSlug(t *testing.T) {
	cases := []struct{ title, md, want string }{
		{"My First Essay!", "ignored body", `^my-first-essay\.[a-z0-9]{4}$`},
		{"", "Just a *quick* note, see https://example.com/a/b for more", `^just-a-quick-note-see-for-more\.[a-z0-9]{4}$`},
		{"", "Coffee beats tea. Fight me, really.\n\nSecond paragraph.", `^coffee-beats-tea\.[a-z0-9]{4}$`},
		{"", "Read [this great post](https://x.org/p) today! More later", `^read-this-great-post-today\.[a-z0-9]{4}$`},
		{"", "Hi. Quick thought about feeds", `^hi-quick-thought-about-feeds\.[a-z0-9]{4}$`},
		{"", "# Heading line\n\nbody text", `^heading-line\.[a-z0-9]{4}$`},
		{"", "A short *note*. <script>x</script>", `^a-short-note\.[a-z0-9]{4}$`},
		{"", "one two three four five six seven eight nine ten", `^one-two-three-four-five-six-seven-eight\.[a-z0-9]{4}$`},
		{"", "日本語だけ 🎉", `^post\.[a-z0-9]{4}$`},
		{"", "2026", `^2026\.[a-z0-9]{4}$`},
		{"", strings.Repeat("abcdefghij ", 8), `^abcdefghij-abcdefghij-abcdefghij-abcdefghij-abcdefghij\.[a-z0-9]{4}$`},
	}
	for _, c := range cases {
		if got := Slug(c.title, c.md); !regexp.MustCompile(c.want).MatchString(got) {
			t.Errorf("Slug(%q, %q) = %q, want %s", c.title, c.md, got, c.want)
		}
	}
	// a slug is never purely digits, so it cannot be mistaken for a numeric id
	for i := 0; i < 200; i++ {
		if regexp.MustCompile(`^[0-9]+$`).MatchString(Slug("", "1")) {
			t.Fatal("numeric slug")
		}
	}
	if Slug("same", "") == Slug("same", "") {
		t.Error("two slugs from the same words must still differ")
	}
}

func TestUIDIsUUIDv7AndTimeOrdered(t *testing.T) {
	a := newUID()
	time.Sleep(2 * time.Millisecond)
	b := newUID()
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(a) {
		t.Fatalf("not a UUIDv7: %s", a)
	}
	if !(a < b) {
		t.Errorf("UUIDv7 must sort by creation time: %s %s", a, b)
	}
}

func TestPostsKeepTheirIdentityAndOldPostsGetOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser("alice", "Alice", ""); err != nil {
		t.Fatal(err)
	}
	u, _ := st.UserByName("alice")
	sp := st.SpaceForTests(t, "alice")
	p, err := sp.AddPost(NewPost{UserID: u.ID, Title: "Hello there", Markdown: "x", HTML: "x"})
	if err != nil {
		t.Fatal(err)
	}
	// editing keeps uid and slug
	if _, err := sp.UpdatePost(u.ID, p.ID, "A completely different title", "y", "y", nil); err != nil {
		t.Fatal(err)
	}
	q, _ := sp.Post(u.ID, p.ID)
	if q.UID != p.UID || q.Slug != p.Slug {
		t.Errorf("edit changed identity: %s/%s -> %s/%s", p.UID, p.Slug, q.UID, q.Slug)
	}
	if got, err := sp.PostBySlug(u.ID, p.Slug); err != nil || got.ID != p.ID {
		t.Errorf("PostBySlug: %v %v", got, err)
	}
	// slugs are per user: another user may have the same slug, but cannot see this one
	if _, err := sp.PostBySlug(u.ID+1, p.Slug); err == nil {
		t.Error("slug leaked into another user's namespace")
	}
}

func TestCleanSlug(t *testing.T) {
	for in, want := range map[string]string{
		"my-trip":              "my-trip",
		"  My Trip to Lisbon!": "my-trip-to-lisbon",
		"a.b_c":                "a-b-c",
		"my trip . k3x9":       "my-trip-k3x9",
		"a..b":                 "a-b",
		"--x--":                "x",
		"café":                 "caf",
		"2024 plans":           "2024-plans",
	} {
		if got, err := CleanSlug(in); err != nil || got != want {
			t.Errorf("CleanSlug(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", "!!!", "🏓", strings.Repeat("a", 81)} {
		if got, err := CleanSlug(in); !errors.Is(err, ErrBadSlug) {
			t.Errorf("CleanSlug(%q) = %q, %v; want ErrBadSlug", in, got, err)
		}
	}
}

// The words of an address may be chosen; the short code that makes it unique is always added, so the same
// words never collide.
func TestChosenSlug(t *testing.T) {
	_, alice, bob := twoUsers(t)
	uid := alice.UID()
	shape := regexp.MustCompile(`^my-trip\.[bcdfghjkmnpqrstvwxz23456789]{4}$`)
	p, err := alice.AddPost(NewPost{UserID: uid, Title: "A title", Markdown: "text", HTML: "<p>text</p>", Slug: "My Trip!"})
	if err != nil || !shape.MatchString(p.Slug) {
		t.Fatalf("AddPost with words: %+v, %v", p, err)
	}
	if got, err := alice.PostBySlug(uid, p.Slug); err != nil || got.ID != p.ID {
		t.Fatalf("PostBySlug: %v", err)
	}
	// the same words again, and after the first post is removed: each gets its own code
	seen := map[string]bool{p.Slug: true}
	for i := 0; i < 20; i++ {
		if i == 10 {
			if err := alice.DeletePost(uid, p.ID); err != nil {
				t.Fatal(err)
			}
		}
		q, err := alice.AddPost(NewPost{UserID: uid, Markdown: "again", HTML: "x", Slug: "my-trip"})
		if err != nil || !shape.MatchString(q.Slug) || seen[q.Slug] {
			t.Fatalf("same words, post %d: %v, %v", i, q, err)
		}
		seen[q.Slug] = true
	}
	// a dot typed into the words is a hyphen
	if q, err := alice.AddPost(NewPost{UserID: uid, Markdown: "x", HTML: "x", Slug: "v1.2 notes"}); err != nil || !regexp.MustCompile(`^v1-2-notes\.[a-z0-9]{4}$`).MatchString(q.Slug) {
		t.Errorf("a dot in the words: %v, %v", q, err)
	}
	// another author may use the same words
	if q, err := bob.AddPost(NewPost{UserID: bob.UID(), Markdown: "mine", HTML: "x", Slug: "my-trip"}); err != nil || !shape.MatchString(q.Slug) {
		t.Errorf("bob: %v", err)
	}
	if _, err := alice.AddPost(NewPost{UserID: uid, Markdown: "x", HTML: "x", Slug: "!!!"}); !errors.Is(err, ErrBadSlug) {
		t.Errorf("bad slug: %v", err)
	}
	// without chosen words they come from the post, as before
	if q, err := alice.AddPost(NewPost{UserID: uid, Title: "Made for me", Markdown: "x", HTML: "x"}); err != nil || !strings.HasPrefix(q.Slug, "made-for-me.") {
		t.Errorf("made slug: %v %v", q, err)
	}
}
