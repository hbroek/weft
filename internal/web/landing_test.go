package web

import (
	"os"
	"path/filepath"
	"testing"
)

// www is a reserved name, so the folder of static files can be served there without meeting an account.
func TestLandingServedAtWWW(t *testing.T) {
	s, _ := setup(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<title>Landing</title>"), 0o644)

	if w := do(s, "GET", "www.localhost", "/", "", ""); w.Code != 404 {
		t.Fatalf("without a site folder www answers %d, want 404", w.Code)
	}
	s.cfg.SiteDir = dir
	if w := do(s, "GET", "www.localhost", "/", "", ""); w.Code != 200 || w.Body.String() != "<title>Landing</title>" {
		t.Fatalf("www = %d %q", w.Code, w.Body.String())
	}
	if w := do(s, "POST", "www.localhost", "/", "", ""); w.Code != 405 {
		t.Errorf("POST to www = %d, want 405", w.Code)
	}
	if w := do(s, "GET", "alice.localhost", "/", "", ""); w.Code != 200 || w.Body.String() == "<title>Landing</title>" {
		t.Errorf("a user's site must not be the landing page: %d", w.Code)
	}
}
