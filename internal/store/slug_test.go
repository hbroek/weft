package store

import (
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
