package feed

import (
	"strings"
	"unicode"
)

// Limits on what a reply may say about its parent.
const (
	MaxParentTitle = 200
	MaxParentDesc  = 300
)

// ParentInfo is what a reply remembers about the page it answers: the optional weft:context title,
// description and image.
type ParentInfo struct{ Title, Description, Image string }

// Empty reports whether nothing is known about the parent.
func (p ParentInfo) Empty() bool { return p.Title == "" && p.Description == "" && p.Image == "" }

// Clean makes the text single-line and short, and keeps the image only if it is an http(s) address.
// It is applied to what authors send and to what other hosts publish.
func (p ParentInfo) Clean() ParentInfo {
	return ParentInfo{Title: oneLineText(p.Title, MaxParentTitle), Description: oneLineText(p.Description, MaxParentDesc), Image: httpAddress(p.Image)}
}

func oneLineText(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && !unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return strings.TrimSpace(string(r[:max-1])) + "…"
	}
	return s
}
