package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/hbroek/weft/internal/store"
)

// Search looks through one account's posts: the account the address names, never the rest of the server.
// /search?q=words shows the posts that hold every word, as a page or, for Accept: text/markdown, as a list of
// links with a line of context each.

const (
	maxSearchLen = 200 // runes of a query
	maxWords     = 8
	maxResults   = 50
	snippetLen   = 160 // runes of context around the first word found
)

// searchWords splits a query into the words that are searched for (lowercase, at most maxWords).
func searchWords(query string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(query)) {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
		if len(out) == maxWords {
			break
		}
	}
	return out
}

func (s *Server) search(w http.ResponseWriter, r *http.Request, q *req) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if r := []rune(query); len(r) > maxSearchLen {
		query = string(r[:maxSearchLen])
	}
	var posts []*store.Post
	if words := searchWords(query); len(words) > 0 {
		var err error
		if posts, err = q.sp.SearchPosts(q.host.ID, words, maxResults); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	w.Header().Add("Vary", "Authorization")
	title := "Search " + q.host.Display
	if query != "" {
		title = query + " · " + q.host.Display
	}
	s.renderPosts(w, r, q, title, posts, func(d *pageData) {
		d.Searching, d.Query = true, query
		d.Clip = true
		// the match list is not a page worth indexing, and its feed is the account's own
		d.Desc = ""
		for i := range d.Posts {
			d.Posts[i].Snippet = snippet(plainText(posts[i].HTML), searchWords(query))
		}
	})
}

// snippet is about snippetLen runes of text around the first of the words that is found in it.
func snippet(text string, words []string) string {
	r := []rune(text)
	if len(r) <= snippetLen {
		return text
	}
	lower := []rune(strings.ToLower(text))
	at := -1
	if len(lower) == len(r) { // lowercasing can change a length; then the start is as good a place as any
		for _, w := range words {
			if i := indexRunes(lower, []rune(w)); i >= 0 && (at < 0 || i < at) {
				at = i
			}
		}
	}
	start := 0
	if at > snippetLen/3 {
		start = at - snippetLen/3
		for start > 0 && !unicode.IsSpace(r[start]) { // begin at a word
			start--
		}
	}
	end := start + snippetLen
	if end > len(r) {
		end = len(r)
	} else {
		for end > start+snippetLen/2 && !unicode.IsSpace(r[end]) {
			end--
		}
	}
	out := strings.TrimSpace(string(r[start:end]))
	if start > 0 {
		out = "…" + out
	}
	if end < len(r) {
		out += "…"
	}
	return out
}

func indexRunes(s, sub []rune) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if string(s[i:i+len(sub)]) == string(sub) {
			return i
		}
	}
	return -1
}

// mdSearch lists the matches as Markdown links, each with a line of what the post says.
func (d pageData) mdSearch(b *strings.Builder) {
	fmt.Fprintf(b, "# Search %s\n", mdLine(d.Author))
	if d.Query == "" {
		b.WriteString("\nGive the words to look for as `?q=`.\n")
		return
	}
	fmt.Fprintf(b, "\n%d %s for **%s**.\n", len(d.Posts), map[bool]string{true: "match", false: "matches"}[len(d.Posts) == 1], mdLine(d.Query))
	for _, p := range d.Posts {
		name := p.Title
		if name == "" {
			name = p.Date
		}
		fmt.Fprintf(b, "\n- %s", mdLink(name, p.URL))
		if p.Title != "" {
			fmt.Fprintf(b, " · %s", p.Date)
		}
		if p.Snippet != "" {
			fmt.Fprintf(b, "\n  %s", mdLine(p.Snippet))
		}
	}
	b.WriteString("\n")
}

// bonbonTemplate is the draft an editor starts a new post from: the keys of a post of one's own (not the reply keys), empty, with the
// cursor going on the title.
const bonbonTemplate = "---\ntitle: \nslug: \nkeywords: []\ncanonical: \n---\n\n"

// bonbon tells an editor how to search this site and what a new post starts from. It is only labels and
// addresses, so it is public; the Allow header of the home page stays what says who may add a post.
func (s *Server) bonbon(w http.ResponseWriter, r *http.Request, q *req) {
	doc := map[string]any{
		"urlSearch": s.base(r) + "/search",
		"mardo": map[string]any{
			"newPages": []map[string]string{{"name": "post", "template": bonbonTemplate}},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300")
	json.NewEncoder(w).Encode(doc)
}
