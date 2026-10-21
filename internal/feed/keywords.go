package feed

import (
	"strings"
	"unicode"
)

const (
	MaxKeywords   = 10
	MaxKeywordLen = 50 // characters
)

// Keywords cleans a list of keywords or phrases: spaces are collapsed, control characters and
// commas are dropped, empties and repeats (ignoring case) are removed, long ones are cut, and
// only the first MaxKeywords are kept. It is applied to what authors type and to what other hosts publish.
func Keywords(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range in {
		k = strings.Map(func(r rune) rune {
			if r == ',' || unicode.IsControl(r) && !unicode.IsSpace(r) {
				return -1
			}
			return r
		}, k)
		k = strings.Join(strings.Fields(k), " ")
		if r := []rune(k); len(r) > MaxKeywordLen {
			k = strings.TrimSpace(string(r[:MaxKeywordLen]))
		}
		if k == "" || seen[strings.ToLower(k)] {
			continue
		}
		seen[strings.ToLower(k)] = true
		if out = append(out, k); len(out) == MaxKeywords {
			break
		}
	}
	return out
}

// SplitKeywords reads the editor's comma-separated box.
func SplitKeywords(text string) []string { return Keywords(strings.Split(text, ",")) }

// JoinKeywords is the box's text for a list of keywords.
func JoinKeywords(k []string) string { return strings.Join(k, ", ") }

// Keyword cleans one keyword as Keywords would, or returns "" if nothing is left. It reads the
// ?keyword= and ?category= filters.
func Keyword(s string) string {
	if k := Keywords([]string{s}); len(k) > 0 {
		return k[0]
	}
	return ""
}

// HasKeyword reports whether list holds kw, ignoring case.
func HasKeyword(list []string, kw string) bool {
	for _, k := range list {
		if strings.EqualFold(k, kw) {
			return true
		}
	}
	return false
}
