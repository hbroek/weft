package web

import (
	"html"
	"regexp"
	"strings"
)

var (
	blockTag = regexp.MustCompile(`</?(p|br|hr|h[1-6]|li|ul|ol|div|blockquote|pre|table|tr|td|th)\b[^>]*>`)
	htmlTag  = regexp.MustCompile(`<[^>]*>`)
)

// describe makes the text for a page's meta description from some HTML or plain text: the first words of it,
// on one line, about 160 characters.
func describe(markup string) string {
	text := plainText(markup)
	const limit = 160
	if r := []rune(text); len(r) > limit {
		cut := string(r[:limit])
		if i := strings.LastIndex(cut, " "); i > limit/2 {
			cut = cut[:i]
		}
		return cut + "…"
	}
	return text
}

// plainText is the text of some HTML or plain text, on one line.
func plainText(markup string) string {
	text := html.UnescapeString(htmlTag.ReplaceAllString(blockTag.ReplaceAllString(markup, " "), ""))
	return strings.Join(strings.Fields(text), " ")
}
