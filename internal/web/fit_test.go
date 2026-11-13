package web

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The script that makes a picture small in the browser: its size rules are run by node (skipped where node is
// not installed). The canvas part needs a browser.
func TestFitScriptSizes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/fit_check.js", "fit.js").CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := map[string]any{
		"big":        []any{1600.0, 1200.0},
		"tall":       []any{1200.0, 1600.0},
		"small":      []any{800.0, 600.0}, // never made larger
		"thin":       []any{1600.0, 1.0},  // never less than one pixel
		"tries":      []any{[]any{1600.0, 0.85}, []any{1600.0, 0.7}, []any{1200.0, 0.7}, []any{800.0, 0.7}},
		"triesSmall": []any{[]any{900.0, 0.85}, []any{900.0, 0.7}, []any{800.0, 0.7}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

// Every script the compose and profile pages carry must at least parse, and both must define weftFit first.
func TestPicturePagesScriptsParse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	i := newInstance(t, "alice")
	ck := loginCookie(t, i)
	for _, path := range []string{"/compose", "/profile"} {
		page := do4(i, path, ck)
		if !strings.Contains(page, "window.weftFit=fit") {
			t.Errorf("%s: weftFit is not defined in the page", path)
		}
		for n, m := range regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindAllStringSubmatch(page, -1) {
			f := filepath.Join(t.TempDir(), "s.js")
			os.WriteFile(f, []byte(m[1]), 0o644)
			if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
				t.Errorf("%s, script %d does not parse: %v\n%s", path, n, err, out)
			}
		}
	}
}
