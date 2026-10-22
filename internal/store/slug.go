package store

import (
	"crypto/rand"
	"errors"
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
	slugWords = 8
	slugChars = 60
	// No vowels or look-alikes, so a random suffix never spells a word or confuses l/1, o/0.
	suffixAlphabet = "bcdfghjkmnpqrstvwxz23456789"
	suffixLen      = 4
	// slugSep joins the words to the suffix, like a full stop ending the sentence. A dot is never
	// percent-encoded, and a suffix without vowels cannot pass for a file extension.
	slugSep = "."
)

var (
	urlRe      = regexp.MustCompile(`https?://\S+`)
	nonWord    = regexp.MustCompile(`[^a-z0-9]+`)
	scriptRe   = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)>`)
	tagRe      = regexp.MustCompile(`<[^>]*>`)
	mdLinkRe   = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	paraBreak  = regexp.MustCompile(`\n\s*\n`)
	sentenceRe = regexp.MustCompile(`[.!?](\s|$)`)
)

// firstSentence reduces markdown to the plain words of its opening sentence: markup, link targets
// and URLs are dropped, and only the first paragraph is considered. A sentence shorter than three
// words (an abbreviation, "Hi.") is not taken for the whole thought, so the text after it is kept.
func firstSentence(markdown string) string {
	t := scriptRe.ReplaceAllString(markdown, " ")
	t = tagRe.ReplaceAllString(t, " ")
	t = mdLinkRe.ReplaceAllString(t, "$1")
	t = urlRe.ReplaceAllString(t, " ")
	para := paraBreak.Split(strings.TrimSpace(t), 2)[0]
	para = strings.Join(strings.Fields(para), " ")
	if loc := sentenceRe.FindStringIndex(para); loc != nil {
		if first := para[:loc[0]]; len(strings.Fields(first)) >= 3 {
			return first
		}
	}
	return para
}

// Slug builds a URL path segment for a post: the words of its title or, for an untitled note,
// of its first sentence, then a short random suffix that makes it unique in the author's namespace.
// A slug is made once and never changes, even when the post is edited.
func Slug(title, markdown string) string { return SlugBase(title, markdown) + slugSep + Suffix() }

// SlugBase is the words of a slug, without the suffix: what the compose form suggests as the address.
func SlugBase(title, markdown string) string {
	src := title
	if strings.TrimSpace(src) == "" {
		src = firstSentence(markdown)
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
	if base := strings.Join(words, "-"); base != "" {
		return base
	}
	return "post"
}

// Suffix is a short random string that, after a full stop, makes an address unique among an author's.
func Suffix() string {
	var suf [suffixLen]byte
	rand.Read(suf[:])
	for i := range suf {
		suf[i] = suffixAlphabet[int(suf[i])%len(suffixAlphabet)]
	}
	return string(suf[:])
}

// maxCustomSlug is the longest address an author may choose.
const maxCustomSlug = 80

var ErrBadSlug = errors.New("an address needs letters or digits, and at most 80 characters")

// CleanSlug turns the words an author typed for the address into the words of a slug: lowercase letters
// and digits joined by single hyphens. Anything else (spaces, dots, underscores, accents, emoji) becomes a
// hyphen or is dropped, so "My trip to Lisbon!" is "my-trip-to-lisbon". The short code that makes the
// address unique is always added after it, by AddPost. An author may choose the words only when the post
// is made; after that the address never changes.
func CleanSlug(s string) (string, error) {
	s = strings.Trim(nonWord.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
	if s == "" || len(s) > maxCustomSlug {
		return "", ErrBadSlug
	}
	return s, nil
}
