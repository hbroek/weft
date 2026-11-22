package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/store"
)

// A post as a document: YAML-like frontmatter between two lines of "---", then the Markdown, exactly as
// stored. An editor opens one, changes it and sends it back with PUT.
//
//	---
//	id: 42
//	url: https://alice.example/posts/hello.k3x9
//	title: Hello
//	keywords: [weft, reading]
//	canonical: https://example.org/original
//	---
//	The body.
//
// The module has no YAML library, and this needs little of one: single values (plain or quoted), lists
// (inline or one "- item" per line) and the one nested map, context. Anything else in a known key is an
// error that names the line; keys that are not known are skipped, so an editor can keep its own.

// maxFrontmatter is the most frontmatter a document may have, in bytes.
const maxFrontmatter = 64 << 10

type docError struct {
	line int
	msg  string
}

func (e docError) Error() string {
	if e.line == 0 {
		return e.msg
	}
	return fmt.Sprintf("frontmatter line %d: %s", e.line, e.msg)
}

// parseDoc reads a document. id is the post it says it is (0: none). A document that does not start with
// a line of "---" is all body.
func parseDoc(text string) (in postReq, id int64, err error) {
	text = strings.TrimPrefix(text, "\xEF\xBB\xBF") // a byte order mark
	first, rest, _ := strings.Cut(text, "\n")
	if strings.TrimRight(first, " \t\r") != "---" {
		in.Markdown = text
		return
	}
	var lines []string
	closed := false
	for {
		if len(text)-len(rest) > maxFrontmatter {
			return in, 0, docError{msg: "the frontmatter is too long"}
		}
		line, after, more := strings.Cut(rest, "\n")
		if t := strings.TrimRight(line, " \t\r"); t == "---" || t == "..." {
			in.Markdown, closed = after, true
			break
		}
		lines = append(lines, strings.TrimRight(line, "\r"))
		if !more {
			break
		}
		rest = after
	}
	if !closed {
		return in, 0, docError{msg: "the frontmatter is not closed by a line of ---"}
	}
	err = fillDoc(&in, &id, lines)
	return
}

type frontEntry struct {
	val  string
	line int
	sub  []string // the indented lines and "- " items under the key
}

var keyRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*):(?:[ \t]+(.*))?$`)

func fillDoc(in *postReq, id *int64, lines []string) error {
	entries := map[string]*frontEntry{}
	var cur *frontEntry
	for i, line := range lines {
		n := i + 2 // the first line is the opening ---
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' && !strings.HasPrefix(line, "- ") && line != "-" {
			m := keyRe.FindStringSubmatch(strings.TrimRight(line, " \t"))
			if m == nil {
				return docError{n, "expected key: value"}
			}
			if _, dup := entries[m[1]]; dup {
				return docError{n, m[1] + " appears twice"}
			}
			cur = &frontEntry{val: strings.TrimSpace(m[2]), line: n}
			entries[m[1]] = cur
			continue
		}
		if cur == nil {
			return docError{n, "expected key: value"}
		}
		cur.sub = append(cur.sub, t)
		if len(cur.sub) > 200 {
			return docError{n, "too many lines under one key"}
		}
	}
	one := func(key string) (string, error) {
		e := entries[key]
		if e == nil {
			return "", nil
		}
		v, err := scalar(e.val)
		if err != nil {
			return "", docError{e.line, key + ": " + err.Error()}
		}
		if len(e.sub) > 0 {
			return "", docError{e.line, key + " must be a single value"}
		}
		return v, nil
	}
	var err error
	if in.Title, err = one("title"); err != nil {
		return err
	}
	if in.Slug, err = one("slug"); err != nil {
		return err
	}
	if in.Canonical, err = one("canonical"); err != nil {
		return err
	}
	if in.InReplyTo, err = one("in_reply_to"); err != nil {
		return err
	}
	if in.InReplyToFeed, err = one("in_reply_to_feed"); err != nil {
		return err
	}
	in.Title = strings.TrimSpace(in.Title)
	if e := entries["id"]; e != nil {
		v, err := one("id")
		if err != nil {
			return err
		}
		if n, perr := strconv.ParseInt(v, 10, 64); perr == nil && n > 0 {
			*id = n
		} else if v != "" {
			return docError{e.line, "id must be a number"}
		}
	}
	if e := entries["keywords"]; e != nil {
		k, err := keywordList(e)
		if err != nil {
			return docError{e.line, "keywords: " + err.Error()}
		}
		in.Keywords = k
	}
	if e := entries["context"]; e != nil {
		if e.val != "" && e.val != "{}" && e.val != "~" && e.val != "null" {
			return docError{e.line, "context must list title, description and image on indented lines"}
		}
		c := &postContext{}
		for _, l := range e.sub {
			m := keyRe.FindStringSubmatch(l)
			if m == nil {
				return docError{e.line, "context: expected key: value"}
			}
			v, err := scalar(strings.TrimSpace(m[2]))
			if err != nil {
				return docError{e.line, "context " + m[1] + ": " + err.Error()}
			}
			switch m[1] {
			case "title":
				c.Title = v
			case "description":
				c.Description = v
			case "image":
				c.Image = v
			}
		}
		in.Context = c
	}
	return nil
}

// keywordList reads keywords written as [a, b], as a comma-separated line, or one "- item" a line.
func keywordList(e *frontEntry) ([]string, error) {
	switch {
	case e.val == "" && len(e.sub) > 0:
		var out []string
		for _, l := range e.sub {
			item, ok := strings.CutPrefix(l, "-")
			if !ok || (item != "" && item[0] != ' ' && item[0] != '\t') {
				return nil, errors.New("expected a list of - items")
			}
			v, err := scalar(strings.TrimSpace(item))
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return feed.Keywords(out), nil
	case len(e.sub) > 0:
		return nil, errors.New("must be a list")
	case strings.HasPrefix(e.val, "["):
		items, err := flowList(e.val)
		if err != nil {
			return nil, err
		}
		return feed.Keywords(items), nil
	}
	v, err := scalar(e.val)
	if err != nil {
		return nil, err
	}
	return feed.SplitKeywords(v), nil
}

// flowList reads "[a, "b, c", 'd']".
func flowList(v string) ([]string, error) {
	var out []string
	i := 1
	for {
		for i < len(v) && (v[i] == ' ' || v[i] == '\t') {
			i++
		}
		if i >= len(v) {
			return nil, errors.New("the list is not closed by ]")
		}
		if v[i] == ']' {
			break
		}
		end := i
		if v[i] == '"' || v[i] == '\'' {
			e, err := quotedEnd(v, i)
			if err != nil {
				return nil, err
			}
			end = e
		} else {
			for end < len(v) && v[end] != ',' && v[end] != ']' {
				end++
			}
		}
		item, err := scalar(strings.TrimSpace(v[i:end]))
		if err != nil {
			return nil, err
		}
		out = append(out, item)
		i = end
		for i < len(v) && (v[i] == ' ' || v[i] == '\t') {
			i++
		}
		if i < len(v) && v[i] == ',' {
			i++
		}
	}
	if rest := strings.TrimSpace(v[i+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
		return nil, errors.New("unexpected text after the list")
	}
	return out, nil
}

// quotedEnd is the index just past the quote that closes the one at v[i].
func quotedEnd(v string, i int) (int, error) {
	q := v[i]
	for j := i + 1; j < len(v); j++ {
		switch {
		case q == '"' && v[j] == '\\':
			j++
		case v[j] == q && q == '\'' && j+1 < len(v) && v[j+1] == '\'':
			j++
		case v[j] == q:
			return j + 1, nil
		}
	}
	return 0, errors.New("a quote is not closed")
}

// scalar reads one value: "double quoted" (with \" \\ \n \t \uXXXX), 'single quoted' (” for '), or plain
// text, which ends at " #". null and ~ are empty.
func scalar(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	switch v[0] {
	case '"', '\'':
		end, err := quotedEnd(v, 0)
		if err != nil {
			return "", err
		}
		if rest := strings.TrimSpace(v[end:]); rest != "" && !strings.HasPrefix(rest, "#") {
			return "", errors.New("unexpected text after the closing quote")
		}
		if v[0] == '\'' {
			return strings.ReplaceAll(v[1:end-1], "''", "'"), nil
		}
		var s string
		if err := json.Unmarshal([]byte(v[:end]), &s); err != nil {
			return "", errors.New("a quoted value has an escape that is not supported")
		}
		return s, nil
	case '|', '>':
		return "", errors.New("multi-line values are not supported; use a quoted string")
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	if v == "~" || v == "null" {
		return "", nil
	}
	return v, nil
}

// --- writing ---

var (
	yamlWords = regexp.MustCompile(`(?i)^(null|true|false|yes|no|on|off|y|n)$`)
	yamlDate  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)
)

func needsQuote(s string, flow bool) bool {
	if s == "" || s != strings.TrimSpace(s) || yamlWords.MatchString(s) || yamlDate.MatchString(s) || s == "~" || s == "-" {
		return true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	if strings.ContainsAny(s[:1], "\"'[]{}&*!|>%@`#,?") || strings.HasPrefix(s, "- ") {
		return true
	}
	if strings.Contains(s, ": ") || strings.Contains(s, " #") || strings.HasSuffix(s, ":") {
		return true
	}
	if flow && strings.ContainsAny(s, ",[]{}") {
		return true
	}
	for _, r := range s {
		if r < ' ' || r == 0x7f || r == ' ' || r == ' ' {
			return true
		}
	}
	return false
}

func quoted(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func out(s string, flow bool) string {
	if needsQuote(s, flow) {
		return quoted(s)
	}
	return s
}

// postDoc is a post as a document. Media paths stay as stored (relative): an editor that saved the
// permalink's absolute ones back would change the post.
func postDoc(p *store.Post, url string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %d\nurl: %s\n", p.ID, out(url, false))
	if p.Title != "" {
		fmt.Fprintf(&b, "title: %s\n", out(p.Title, false))
	}
	if len(p.Keywords) > 0 {
		items := make([]string, len(p.Keywords))
		for i, k := range p.Keywords {
			items[i] = out(k, true)
		}
		fmt.Fprintf(&b, "keywords: [%s]\n", strings.Join(items, ", "))
	}
	for _, f := range []struct{ key, val string }{{"canonical", p.Canonical}, {"in_reply_to", p.InReplyTo}, {"in_reply_to_feed", p.InReplyToFeed}} {
		if f.val != "" {
			fmt.Fprintf(&b, "%s: %s\n", f.key, out(f.val, false))
		}
	}
	if !p.Parent.Empty() {
		b.WriteString("context:\n")
		for _, f := range []struct{ key, val string }{{"title", p.Parent.Title}, {"description", p.Parent.Description}, {"image", p.Parent.Image}} {
			if f.val != "" {
				fmt.Fprintf(&b, "  %s: %s\n", f.key, out(f.val, false))
			}
		}
	}
	fmt.Fprintf(&b, "published: %s\n", p.Created.UTC().Format(time.RFC3339))
	if p.Updated != nil {
		fmt.Fprintf(&b, "updated: %s\n", p.Updated.UTC().Format(time.RFC3339))
	}
	b.WriteString("---\n")
	b.WriteString(p.Markdown)
	return b.String()
}
