package store

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// newUID returns a UUIDv7 as a string: a time-ordered identity that is made once, when the post
// is created, and travels with the post if its author moves to another instance.
func newUID() string {
	var b [16]byte
	rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	b[0], b[1], b[2], b[3], b[4], b[5] = byte(ms>>40), byte(ms>>32), byte(ms>>24), byte(ms>>16), byte(ms>>8), byte(ms)
	b[6] = b[6]&0x0f | 0x70 // version 7
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

const (
	slugWords = 6
	slugChars = 40
	// No vowels or look-alikes, so a random suffix never spells a word or confuses l/1, o/0.
	suffixAlphabet = "bcdfghjkmnpqrstvwxz23456789"
	suffixLen      = 4
)

var (
	urlRe   = regexp.MustCompile(`https?://\S+`)
	nonWord = regexp.MustCompile(`[^a-z0-9]+`)
)

// Slug builds a URL path segment for a post: the words of its title or, for an untitled note,
// of its first words, then a short random suffix that makes it unique in the author's namespace.
// A slug is made once and never changes, even when the post is edited.
func Slug(title, markdown string) string {
	src := title
	if strings.TrimSpace(src) == "" {
		src = markdown
	}
	src = strings.ToLower(urlRe.ReplaceAllString(src, " "))
	var words []string
	n := 0
	for _, w := range nonWord.Split(src, -1) {
		if w == "" {
			continue
		}
		if len(words) == slugWords || n+len(w) > slugChars {
			break
		}
		words = append(words, w)
		n += len(w) + 1
	}
	base := strings.Join(words, "-")
	if base == "" {
		base = "post"
	}
	var suf [suffixLen]byte
	rand.Read(suf[:])
	for i := range suf {
		suf[i] = suffixAlphabet[int(suf[i])%len(suffixAlphabet)]
	}
	return base + "-" + string(suf[:])
}
