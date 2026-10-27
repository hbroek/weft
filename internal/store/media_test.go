package store

import (
	"bytes"
	"errors"
	"testing"
)

func TestMediaStoredOnceAndLimited(t *testing.T) {
	in, _, _ := twoUsers(t)
	sp := in.SpaceForTests(t, "alice")
	pic := Media{Name: "aaaa.jpg", Mime: "image/jpeg", Width: 3, Height: 2, Data: bytes.Repeat([]byte{1}, 1000)}

	if added, err := sp.PutMedia(pic, 2500); err != nil || !added {
		t.Fatalf("first: %v %v", added, err)
	}
	if added, err := sp.PutMedia(pic, 2500); err != nil || added { // the same name is kept once
		t.Fatalf("again: %v %v", added, err)
	}
	other := Media{Name: "bbbb.png", Mime: "image/png", Data: bytes.Repeat([]byte{2}, 1000)}
	if _, err := sp.PutMedia(other, 2500); err != nil {
		t.Fatal(err)
	}
	third := Media{Name: "cccc.gif", Mime: "image/gif", Data: bytes.Repeat([]byte{3}, 1000)}
	if _, err := sp.PutMedia(third, 2500); !errors.Is(err, ErrQuota) {
		t.Fatalf("over the quota: %v", err)
	}
	if n, _ := sp.MediaUsed(); n != 2000 {
		t.Errorf("used %d", n)
	}
	got, err := sp.Media("aaaa.jpg")
	if err != nil || !bytes.Equal(got.Data, pic.Data) || got.Mime != "image/jpeg" || got.Width != 3 {
		t.Fatalf("read back: %+v %v", got, err)
	}
	if _, err := sp.Media("zzzz.jpg"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	list, _ := sp.MediaList()
	if len(list) != 2 || len(list[0].Data) != 0 {
		t.Errorf("list: %+v", list)
	}
	sp.DeleteMedia("aaaa.jpg")
	if _, err := sp.PutMedia(third, 2500); err != nil { // there is room again
		t.Errorf("after deleting: %v", err)
	}
	// another user's pictures are separate
	if _, err := in.SpaceForTests(t, "bob").Media("bbbb.png"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bob sees alice's picture: %v", err)
	}
}

func TestMediaTableIsCreatedInAnOlderFile(t *testing.T) {
	in, _, _ := twoUsers(t)
	sp := in.SpaceForTests(t, "alice")
	sp.db.DB.Exec(`DROP TABLE IF EXISTS media`) // a file whose lazy tables were made before pictures existed
	if _, err := sp.Media("x.jpg"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reading without the table: %v", err)
	}
	if _, err := sp.PutMedia(Media{Name: "x.jpg", Mime: "image/jpeg", Data: []byte{1}}, 0); err != nil {
		t.Fatalf("storing creates the table: %v", err)
	}
}

func TestPostsUsingMedia(t *testing.T) {
	in, _, _ := twoUsers(t)
	sp := in.SpaceForTests(t, "alice")
	u, _ := sp.User()
	if _, err := sp.AddPost(NewPost{UserID: u.ID, Markdown: "![a](/media/abc_123.jpg)", HTML: "<p></p>"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := sp.PostsUsingMedia("abc_123.jpg"); n != 1 {
		t.Errorf("uses: %d", n)
	}
	if n, _ := sp.PostsUsingMedia("abcX123.jpg"); n != 0 { // _ is not a wildcard
		t.Errorf("wildcard matched: %d", n)
	}
}
