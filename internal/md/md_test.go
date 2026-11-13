package md

import (
	"strings"
	"testing"
)

func TestTaskListsAreSymbolsNotInputs(t *testing.T) {
	h, err := Render("- [x] done\n- [ ] to do\n- plain\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<span class="task" role="img" aria-label="done">✅</span> done`,
		`<span class="task" role="img" aria-label="not done">⬜</span> to do`,
		"<li>plain</li>",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q in:\n%s", want, h)
		}
	}
	if strings.Contains(h, "<input") {
		t.Errorf("a form control was written:\n%s", h)
	}
	// a box written by hand is raw HTML: dropped, as before
	h, _ = Render(`<input type=checkbox checked onfocus=alert(1)>`)
	if strings.Contains(h, "<input") || strings.Contains(h, "onfocus") {
		t.Errorf("raw input got through:\n%s", h)
	}
}

func TestTables(t *testing.T) {
	h, err := Render("| Name | Qty |\n|:---|---:|\n| tea | 2 |\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<table>", "<thead>", "<th", "Name", "<td", "tea", "</table>"} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q in:\n%s", want, h)
		}
	}
	// cell text is escaped, and a link in a cell still goes through the safe renderer
	h, _ = Render("| a |\n|---|\n| <script>x</script> [l](javascript:alert(1)) |\n")
	if strings.Contains(h, "<script") || strings.Contains(h, "javascript:") {
		t.Errorf("unsafe content in a cell:\n%s", h)
	}
}
