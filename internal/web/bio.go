package web

import (
	"html"
	"html/template"
	"strings"

	"github.com/hbroek/weft/internal/md"
)

// A bio is Markdown. The home page shows it clamped to two lines of the screen, with a "More" that opens it in place
// when it is longer (a small script on the page); without script it is shown whole.

// bioHTML renders a bio.
func bioHTML(src string) template.HTML {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	out, err := md.Render(src)
	if err != nil {
		return template.HTML("<p>" + html.EscapeString(src) + "</p>")
	}
	return template.HTML(out)
}

// bioText is a bio as plain text on one line, for a meta description and a feed's description.
func bioText(src string) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	out, err := md.Render(src)
	if err != nil {
		out = html.EscapeString(src)
	}
	return plainText(out)
}
