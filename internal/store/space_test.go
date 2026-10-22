package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hbroek/weft/internal/feed"
)

func twoUsers(t *testing.T) (in *Instance, alice, bob *Space) {
	t.Helper()
	pbkdf2Iterations = 1
	in, err := Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close() })
	for _, n := range []string{"alice", "bob"} {
		if _, err := in.AddUser(n, n, ""); err != nil {
			t.Fatal(err)
		}
	}
	return in, in.SpaceForTests(t, "alice"), in.SpaceForTests(t, "bob")
}

func cache(t *testing.T, in *Instance, url, guid string) {
	t.Helper()
	p := &feed.Parsed{Title: url, Items: []feed.ParsedItem{{GUID: guid, UID: guid, Link: guid, Title: guid, Published: time.Unix(1700000000, 0), RepliesCount: -1}}}
	if err := in.SaveFeed(url, p); err != nil {
		t.Fatal(err)
	}
}

// Two users on one instance share the cached feeds but each sees only the feeds they follow, in the
// timeline, in a feed's own view and when looking an item up.
func TestSpacesSeeOnlyTheirFollows(t *testing.T) {
	in, alice, bob := twoUsers(t)
	cache(t, in, "http://x.example/feed", "http://x.example/1")
	cache(t, in, "http://y.example/feed", "http://y.example/1")
	alice.Follow(alice.UID(), "http://x.example/feed")
	bob.Follow(bob.UID(), "http://y.example/feed")

	if xs, _ := alice.Timeline(alice.UID(), 10); len(xs) != 1 || xs[0].GUID != "http://x.example/1" {
		t.Errorf("alice's timeline: %+v", xs)
	}
	if xs, _ := bob.Timeline(bob.UID(), 10); len(xs) != 1 || xs[0].GUID != "http://y.example/1" {
		t.Errorf("bob's timeline: %+v", xs)
	}
	if xs, _ := alice.FeedItems(alice.UID(), "http://y.example/feed", 10); len(xs) != 0 {
		t.Errorf("alice read bob's feed: %+v", xs)
	}
	if _, err := alice.ItemByGUID(alice.UID(), "http://y.example/1"); err != ErrNotFound {
		t.Errorf("alice found an item only bob follows the feed of: %v", err)
	}
	if _, err := alice.ItemByTarget(alice.UID(), "http://y.example/1"); err != ErrNotFound {
		t.Errorf("alice found an item by target that only bob follows: %v", err)
	}
	if c, err := alice.ItemByGUID(alice.UID(), "http://x.example/1"); err != nil || c.FeedURL != "http://x.example/feed" {
		t.Errorf("alice's own lookup: %+v %v", c, err)
	}
	fs, _ := alice.Follows(alice.UID())
	if len(fs) != 1 || fs[0].Title != "http://x.example/feed" {
		t.Errorf("alice's follows: %+v", fs)
	}
}

// The directory knows who follows a feed, so the poller fetches it once and a feed nobody follows any
// more is dropped from the list.
func TestLocalFollowsFollowTheUsers(t *testing.T) {
	in, alice, bob := twoUsers(t)
	f := "http://x.example/feed"
	if in.Followed(f) {
		t.Fatal("followed before anyone followed it")
	}
	alice.Follow(alice.UID(), f)
	bob.Follow(bob.UID(), f)
	if urls, _ := in.FollowedFeedURLs(); len(urls) != 1 || urls[0] != f {
		t.Errorf("FollowedFeedURLs = %v, want it once for both users", urls)
	}
	alice.Unfollow(alice.UID(), f)
	if !in.Followed(f) {
		t.Error("unfollowed by alice, but bob still follows it")
	}
	bob.Unfollow(bob.UID(), f)
	if in.Followed(f) {
		t.Error("still followed after both unfollowed")
	}
}

// Webmention jobs belong to a user, and the directory says who has some due.
func TestQueueIsPerUser(t *testing.T) {
	in, alice, bob := twoUsers(t)
	if names, _ := in.UsersWithWork(); len(names) != 0 {
		t.Fatalf("work before any job: %v", names)
	}
	alice.Enqueue(QueueIn, alice.UID(), "http://s/1", "http://alice/p")
	if names, _ := in.UsersWithWork(); len(names) != 1 || names[0] != "alice" {
		t.Errorf("UsersWithWork = %v, want [alice]", names)
	}
	if jobs, _ := bob.DueQueue(QueueIn, 10); len(jobs) != 0 {
		t.Errorf("bob was handed alice's jobs: %+v", jobs)
	}
	jobs, _ := alice.DueQueue(QueueIn, 10)
	if len(jobs) != 1 {
		t.Fatalf("alice's jobs: %+v", jobs)
	}
	// a retry in the future takes her off the due list, finishing the job takes her off the directory
	alice.RetryQueue(jobs[0].ID, time.Hour, 6, "later")
	if names, _ := in.UsersWithWork(); len(names) != 0 {
		t.Errorf("work due while the only job waits an hour: %v", names)
	}
	alice.FinishQueue(jobs[0].ID, "done")
	var n int
	in.db.QueryRow(`SELECT COUNT(*) FROM work`).Scan(&n)
	if n != 0 {
		t.Errorf("work rows after the last job finished: %d", n)
	}
}

// A password, a session and an API token open only the space of the user they belong to.
func TestCredentialsStayInTheirSpace(t *testing.T) {
	in, alice, bob := twoUsers(t)
	if err := alice.SetPassword("correct horse"); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.CheckPassword("correct horse"); err != nil {
		t.Errorf("alice's own password: %v", err)
	}
	if _, err := bob.CheckPassword("correct horse"); err != ErrBadLogin {
		t.Errorf("alice's password opened bob: %v", err)
	}
	tok, _ := alice.CreateSession(alice.UID())
	if _, err := alice.UserBySession(tok); err != nil {
		t.Errorf("alice's session: %v", err)
	}
	if _, err := bob.UserBySession(tok); err != ErrNotFound {
		t.Errorf("alice's session opened bob: %v", err)
	}
	api, _ := alice.ResetToken()
	if _, err := alice.UserByToken(api); err != nil {
		t.Errorf("alice's token: %v", err)
	}
	if _, err := bob.UserByToken(api); err != ErrNotFound {
		t.Errorf("alice's token opened bob: %v", err)
	}
	if _, _, err := in.Space("nobody"); err != ErrNotFound {
		t.Errorf("Space of a missing user: %v", err)
	}
}

// The directory is rebuilt from the users' files when an old database is split.
func TestSplitFillsTheDirectory(t *testing.T) {
	path := fixture(t, "v0-current.sql")
	db := openRaw(t, path)
	for _, q := range []string{
		`INSERT INTO follows(user_id,feed_url,added) VALUES (1,'http://x.example/feed',1)`,
		`INSERT INTO wm_queue(dir,user_id,source,target,next_try) VALUES ('in',1,'http://s/1','http://alice/p',1700000000)`,
		// a job queued to send before jobs had an owner: the host of its source names the user
		`INSERT INTO wm_queue(dir,user_id,source,target,next_try) VALUES ('out',0,'http://alice.localhost:8080/posts/p','http://t/1',1700000000)`,
		`INSERT INTO wm_queue(dir,user_id,source,target,next_try,done) VALUES ('out',0,'http://nobody.example/p','http://t/2',1,1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	db.Close()
	data := filepath.Join(t.TempDir(), "data")
	rep, err := Split(path, data)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rows["wm_queue"] != 2 || len(rep.Dropped) != 1 {
		t.Errorf("report: %+v", rep)
	}
	in, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if !in.Followed("http://x.example/feed") {
		t.Error("existing follow missing from local_follows")
	}
	if names, _ := in.UsersWithWork(); len(names) != 1 || names[0] != "alice" {
		t.Errorf("existing job missing from work: %v", names)
	}
	jobs, _ := in.SpaceForTests(t, "alice").DueQueue(QueueOut, 10)
	if len(jobs) != 1 {
		t.Errorf("alice's outgoing job: %+v", jobs)
	}
}

// Two users cannot share a name, and a failed creation leaves nothing behind.
func TestAddUserIsExclusive(t *testing.T) {
	in, _, _ := twoUsers(t)
	if _, err := in.AddUser("alice", "", ""); err != ErrNameTaken {
		t.Errorf("second alice: %v, want ErrNameTaken", err)
	}
	for _, bad := range []string{"", "../x", "a/b", "Alice", "a.b"} {
		if _, err := in.AddUser(bad, "", ""); !errors.Is(err, ErrInvalid) {
			t.Errorf("AddUser(%q) = %v, want ErrInvalid", bad, err)
		}
		if _, _, err := in.Space(bad); err != ErrNotFound {
			t.Errorf("Space(%q) = %v, want ErrNotFound", bad, err)
		}
	}
	// a file in the way is never overwritten, and the name is not left in the directory
	path := in.spacePath("carol")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("not yours"), 0o644)
	if _, err := in.AddUser("carol", "", ""); err == nil {
		t.Error("AddUser over an existing file succeeded")
	}
	if b, _ := os.ReadFile(path); string(b) != "not yours" {
		t.Errorf("existing file overwritten: %q", b)
	}
	if names, _ := in.UserNames(); len(names) != 2 {
		t.Errorf("directory after the failure: %v", names)
	}
}

// Only so many databases are open at once, and one that is in use is never closed under a request.
func TestPoolCapAndInUse(t *testing.T) {
	in, _, _ := twoUsers(t)
	for _, n := range []string{"carol", "dave", "erin"} {
		in.AddUser(n, "", "")
	}
	in.pool.cap = 2
	held, release, err := in.Space("alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"bob", "carol", "dave", "erin"} {
		sp, rel, err := in.Space(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sp.User(); err != nil {
			t.Fatal(err)
		}
		rel()
	}
	if got := in.pool.size(); got > 2 {
		t.Errorf("%d databases open with a cap of 2", got)
	}
	if _, err := held.User(); err != nil {
		t.Errorf("the database in use was closed: %v", err)
	}
	release()
}

// A user's file uses small pages, so an account costs little disk; the shared file keeps the default.
func TestSpaceFilesUseSmallPages(t *testing.T) {
	in, _, _ := twoUsers(t)
	var ps int
	sp := in.SpaceForTests(t, "alice")
	if err := sp.db.QueryRow(`PRAGMA page_size`).Scan(&ps); err != nil || ps != 1024 {
		t.Errorf("alice.db page_size = %d (%v), want 1024", ps, err)
	}
	in.Close()
	if fi, err := os.Stat(in.spacePath("alice")); err != nil || fi.Size() > 40*1024 {
		t.Errorf("an empty user file takes %v bytes (%v), want under 40 KB", fi.Size(), err)
	}
}
