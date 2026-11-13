package web

import (
	"html/template"
	"strings"

	"github.com/hbroek/weft/internal/md"
)

// cheatRow is one line of the Markdown cheatsheet beside the editor: what to type and what it turns into. Shown
// is made by the renderer that publishes posts, so the sheet cannot promise anything Weft does not do. Want is
// what the rendered sample must contain (the test checks it); Show replaces the rendered sample where showing
// it would be wrong (a picture would be fetched from the web).
type cheatRow struct {
	Syntax, Sample, Want, Show string
	Shown                      template.HTML
}

type cheatSheet struct{ Left, Right []cheatRow }

var cheatRowsLeft = []cheatRow{
	{Syntax: "## Heading", Sample: "## Heading", Want: "<h2>Heading</h2>"},
	{Syntax: "**bold**", Sample: "**bold**", Want: "<strong>bold</strong>"},
	{Syntax: "*italic*", Sample: "*italic*", Want: "<em>italic</em>"},
	{Syntax: "~~strikethrough~~", Sample: "~~strikethrough~~", Want: "<del>strikethrough</del>"},
	{Syntax: "> quote", Sample: "> quote", Want: "<blockquote>"},
	{Syntax: "- item", Sample: "- item", Want: "<ul>"},
	{Syntax: "1. item", Sample: "1. item", Want: "<ol>"},
	{Syntax: "- [x] done", Sample: "- [x] done\n- [ ] to do", Want: `aria-label="done">✅</span> done`},
}

var cheatRowsRight = []cheatRow{
	{Syntax: "[words](https://example.com)", Sample: "[words](https://example.com)", Want: `<a href="https://example.com">words</a>`},
	{Syntax: "https://example.com", Sample: "https://example.com", Want: `<a href="https://example.com">`},
	{Syntax: "![description](https://example.com/photo.jpg)", Sample: "![description](https://example.com/photo.jpg)", Want: `<img src="https://example.com/photo.jpg" alt="description"`,
		Show: "<em>the picture</em>"},
	{Syntax: "`code`", Sample: "`code`", Want: "<code>code</code>"},
	{Syntax: "```\ncode\n```", Sample: "```\ncode\n```", Want: "<pre><code>code"},
	{Syntax: "---", Sample: "---", Want: "<hr"},
	{Syntax: "| a | b |\n|---|---|\n| 1 | 2 |", Sample: "| a | b |\n|---|---|\n| 1 | 2 |", Want: "<th"},
	{Syntax: `\*not italic\*`, Sample: `\*not italic\*`, Want: "*not italic*"},
}

// cheats is the sheet as the editor shows it.
var cheats = func() cheatSheet {
	build := func(rows []cheatRow) []cheatRow {
		out := make([]cheatRow, len(rows))
		for i, r := range rows {
			r.Shown = template.HTML(r.Show) // from this file, not from anyone's input
			if r.Show == "" {
				h, err := md.Render(r.Sample)
				if err != nil {
					panic(err)
				}
				h = strings.TrimSpace(h)
				if strings.Count(h, "<p>") == 1 && strings.HasPrefix(h, "<p>") && strings.HasSuffix(h, "</p>") {
					h = strings.TrimSuffix(strings.TrimPrefix(h, "<p>"), "</p>") // inline: no paragraph around it
				}
				r.Shown = template.HTML(h) // goldmark safe mode, on a sample written above
			}
			out[i] = r
		}
		return out
	}
	return cheatSheet{Left: build(cheatRowsLeft), Right: build(cheatRowsRight)}
}()
