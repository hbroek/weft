package web

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The bookmarklet's script is run by node against fake pages (skipped where node is not installed).
func TestBookmarkletReadsThePage(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	js := filepath.Join(t.TempDir(), "bookmarklet.js")
	if err := os.WriteFile(js, []byte(bookmarkletJS), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "testdata/bookmarklet_check.js", js).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var got map[string]struct{ Title, Desc, Img, Kw string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := map[string]struct{ Title, Desc, Img, Kw string }{
		// a single-page site whose Open Graph tags are only the site's name: the page title and the JSON-LD are used
		"youtube": {"2026 West Hill Shop M60+", "West Hill Shop M60+ race - 2nd of 21 Had my shoes with toe spikes", "https://i.ytimg.com/vi/n7hesOgzRdg/maxresdefault.jpg", ""},
		// ordinary tags still win over JSON-LD
		"article": {"The web of links", "About links.", "https://blog.example/i.png", "web, links"},
		// no tags at all, only JSON-LD (inside a @graph, with an image object)
		"news": {"Big story", "The details.", "https://s.wsj.net/i.jpg", ""},
		// nothing usable, and a broken JSON-LD block: the page title and the first long paragraph
		"bare": {"Plain page", "A paragraph that is long enough to be used as the description of a page that has no tags at all, really.", "", ""},
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %+v, want %+v", k, got[k], w)
		}
	}
}
