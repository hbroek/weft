package store

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exportBytes(t *testing.T, sp *Space) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := sp.Export(&buf, "weft.example"); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeZip(t *testing.T, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x.zip")
	f, _ := os.Create(path)
	zw := zip.NewWriter(f)
	for n, b := range files {
		w, _ := zw.Create(n)
		w.Write(b)
	}
	zw.Close()
	f.Close()
	return path
}

func unzip(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		r, _ := f.Open()
		out[f.Name], _ = io.ReadAll(r)
		r.Close()
	}
	return out
}

func manifestJSON(m Manifest) []byte { b, _ := json.Marshal(m); return b }

// An account leaves one instance and arrives on another with everything it had, a new API token, and
// no sessions; the old token and the sessions are not in the file at all.
func TestExportImportRoundTrip(t *testing.T) {
	a, alice, _ := twoUsers(t)
	oldTok, _ := alice.ResetToken()
	alice.SetPassword("correct horse")
	alice.CreateSession(alice.UID())
	p, _ := alice.AddPost(NewPost{UserID: alice.UID(), Title: "Hello", Markdown: "hi", HTML: "<p>hi</p>", Keywords: []string{"go"}})
	alice.SetLike(alice.UID(), "http://x.example/p", "")
	alice.Follow(alice.UID(), "http://x.example/feed")
	alice.Enqueue(QueueOut, alice.UID(), "http://alice/p", "http://x.example/p")
	pic := Media{Name: "0123456789abcdef0123456789abcdef.jpg", Mime: "image/jpeg", Width: 2, Height: 1, Origin: "http://x.example/a.jpg", Data: []byte("picture bytes")}
	if _, err := alice.PutMedia(pic, 0); err != nil {
		t.Fatal(err)
	}

	data := exportBytes(t, alice)
	files := unzip(t, data)
	var m Manifest
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	if m.Format != ExportFormat || m.Name != "alice" || m.Domain != "weft.example" || m.Schema != SpaceSchemaVersion || m.Exported.IsZero() {
		t.Errorf("manifest: %+v", m)
	}
	if bytes.Contains(files["user.db"], []byte(hashToken(oldTok))) {
		t.Error("the old token's hash is somewhere in the export")
	}

	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	zipPath := filepath.Join(t.TempDir(), "alice.zip")
	os.WriteFile(zipPath, data, 0o600)
	name, newTok, err := b.Import(zipPath)
	if err != nil || name != "alice" || newTok == "" {
		t.Fatalf("Import = %q %q %v", name, newTok, err)
	}
	bs := b.SpaceForTests(t, "alice")
	if _, err := bs.UserByToken(newTok); err != nil {
		t.Errorf("the new token does not work: %v", err)
	}
	if _, err := bs.UserByToken(oldTok); err == nil {
		t.Error("the old token works on the new instance")
	}
	if _, err := bs.CheckPassword("correct horse"); err != nil {
		t.Errorf("the password did not travel: %v", err)
	}
	if got, err := bs.Media(pic.Name); err != nil || !bytes.Equal(got.Data, pic.Data) || got.Origin != pic.Origin || got.Width != 2 {
		t.Errorf("the pictures did not travel with the account: %+v %v", got, err)
	}
	var sessions int
	bs.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions)
	if sessions != 0 {
		t.Errorf("%d sessions came along", sessions)
	}
	posts, _ := bs.PostsPage(bs.UID(), 10, 0)
	if len(posts) != 1 || posts[0].UID != p.UID || posts[0].Slug != p.Slug || posts[0].Title != "Hello" || len(posts[0].Keywords) != 1 {
		t.Errorf("posts after import: %+v", posts)
	}
	if ls, _ := bs.ActiveLikes(bs.UID(), 10); len(ls) != 1 {
		t.Errorf("likes after import: %+v", ls)
	}
	if !b.Followed("http://x.example/feed") {
		t.Error("the directory does not know the imported user's follow")
	}
	if names, _ := b.UsersWithWork(); len(names) != 1 || names[0] != "alice" {
		t.Errorf("the directory does not know the imported user's job: %v", names)
	}
	// the source is untouched, and the same name cannot be imported twice
	if _, err := alice.UserByToken(oldTok); err != nil {
		t.Errorf("exporting changed the source: %v", err)
	}
	before, _ := os.ReadFile(b.spacePath("alice"))
	if _, _, err := b.Import(zipPath); err == nil || !strings.Contains(err.Error(), "already a user") {
		t.Errorf("second import: %v, want a refusal because the name is taken", err)
	}
	if after, _ := os.ReadFile(b.spacePath("alice")); !bytes.Equal(before, after) {
		t.Error("a refused import changed the existing user's file")
	}
	_ = a
}

// An import trusts nothing in the file: it refuses what is damaged, from the future, or not one user's data.
func TestImportRefusals(t *testing.T) {
	_, alice, _ := twoUsers(t)
	good := unzip(t, exportBytes(t, alice))
	goodM := Manifest{Format: ExportFormat, Schema: SpaceSchemaVersion, Name: "alice"}

	withDB := func(mut func(db *sql.DB)) []byte {
		p := filepath.Join(t.TempDir(), "u.db")
		os.WriteFile(p, good["user.db"], 0o600)
		db := openRaw(t, p)
		mut(db)
		db.Close()
		b, _ := os.ReadFile(p)
		return b
	}
	cases := []struct {
		name  string
		files map[string][]byte
		want  string
	}{
		{"not an export", map[string][]byte{"a.txt": []byte("x")}, "manifest.json and user.db are required"},
		{"newer format", map[string][]byte{"manifest.json": manifestJSON(Manifest{Format: 99, Name: "alice"}), "user.db": good["user.db"]}, "format 99"},
		{"newer schema", map[string][]byte{"manifest.json": manifestJSON(Manifest{Format: 1, Schema: 99, Name: "alice"}), "user.db": good["user.db"]}, "version 99"},
		{"bad name", map[string][]byte{"manifest.json": manifestJSON(Manifest{Format: 1, Name: "../x"}), "user.db": good["user.db"]}, "is not a name"},
		{"wrong name", map[string][]byte{"manifest.json": manifestJSON(Manifest{Format: 1, Schema: 1, Name: "mallory"}), "user.db": good["user.db"]}, "want exactly"},
		{"garbage db", map[string][]byte{"manifest.json": manifestJSON(goodM), "user.db": bytes.Repeat([]byte("not sqlite "), 500)}, "user.db"},
		{"truncated db", map[string][]byte{"manifest.json": manifestJSON(goodM), "user.db": good["user.db"][:len(good["user.db"])/2]}, "user.db"},
		{"trigger", map[string][]byte{"manifest.json": manifestJSON(goodM), "user.db": withDB(func(db *sql.DB) {
			db.Exec(`CREATE TRIGGER t AFTER INSERT ON posts BEGIN DELETE FROM likes; END`)
		})}, "triggers or views"},
		{"second account", map[string][]byte{"manifest.json": manifestJSON(goodM), "user.db": withDB(func(db *sql.DB) {
			db.Exec(`INSERT INTO users(name,created) VALUES ('bob',1)`)
		})}, "2 accounts"},
		{"dangling row", map[string][]byte{"manifest.json": manifestJSON(goodM), "user.db": withDB(func(db *sql.DB) {
			db.Exec(`PRAGMA foreign_keys=OFF`)
			db.Exec(lazySchema)
			db.Exec(`INSERT INTO replies(post_id,source,published,status,received) VALUES (999,'s',1,'approved',1)`)
		})}, "refer to rows"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			_, _, err = in.Import(writeZip(t, c.files))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Import = %v, want an error containing %q", err, c.want)
			}
			if names, _ := in.UserNames(); len(names) != 0 {
				t.Errorf("a refused import left %v in the directory", names)
			}
			if left, _ := filepath.Glob(filepath.Join(in.dir, ".import-*")); len(left) != 0 {
				t.Errorf("temporary files left behind: %v", left)
			}
		})
	}
}
