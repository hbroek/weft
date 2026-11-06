// Package extract turns a web page into the Markdown of its article, for a repost: the text of the post
// without the page around it. It is a best effort. Pages that build their text with scripts give nothing.
package extract

import (
	"bytes"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// maxBody is the most HTML that is read; maxText is the most Markdown that is kept.
const (
	maxBody = 4 << 20
	maxText = 200 << 10
)

// Article is what was found on a page.
type Article struct {
	Title     string
	Markdown  string
	Canonical string // the page's own <link rel="canonical">, as an absolute address, or ""
}

// Page reads the page at base (its address, for relative links and pictures).
func Page(page []byte, base string) Article {
	if len(page) > maxBody {
		page = page[:maxBody]
	}
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return Article{}
	}
	b, _ := url.Parse(base)
	a := Article{Canonical: canonicalOf(doc, b)}
	a.Title = titleOf(doc)
	root := articleRoot(doc)
	if root == nil {
		return a
	}
	c := &conv{base: b}
	c.block(root)
	a.Markdown = strings.TrimSpace(c.out.String())
	if len(a.Markdown) > maxText {
		a.Markdown = strings.TrimSpace(a.Markdown[:maxText])
	}
	// the page's own heading is the title, so the text does not start with it again
	if h := strings.TrimSpace(a.Title); h != "" {
		first, rest, _ := strings.Cut(a.Markdown, "\n")
		if strings.TrimLeft(first, "# ") == h {
			a.Markdown = strings.TrimSpace(rest)
		}
	}
	return a
}

func walk(n *html.Node, f func(*html.Node) bool) {
	if !f(n) {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, f)
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return strings.TrimSpace(a.Val)
		}
	}
	return ""
}

func text(n *html.Node) string {
	var b strings.Builder
	walk(n, func(x *html.Node) bool {
		if x.Type == html.ElementNode && (x.DataAtom == atom.Script || x.DataAtom == atom.Style) {
			return false
		}
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		return true
	})
	return strings.Join(strings.Fields(b.String()), " ")
}

func titleOf(doc *html.Node) string {
	var h1, title, og string
	walk(doc, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return true
		}
		switch n.DataAtom {
		case atom.H1:
			if h1 == "" {
				h1 = text(n)
			}
		case atom.Title:
			if title == "" {
				title = text(n)
			}
		case atom.Meta:
			if attr(n, "property") == "og:title" && og == "" {
				og = attr(n, "content")
			}
		}
		return true
	})
	for _, t := range []string{og, h1, title} {
		if t = strings.Join(strings.Fields(t), " "); t != "" {
			return t
		}
	}
	return ""
}

func canonicalOf(doc *html.Node, base *url.URL) string {
	var out string
	walk(doc, func(n *html.Node) bool {
		if out == "" && n.Type == html.ElementNode && n.DataAtom == atom.Link && strings.EqualFold(attr(n, "rel"), "canonical") {
			out = resolve(base, attr(n, "href"))
		}
		return out == ""
	})
	return out
}

// resolve makes an absolute http(s) address of ref, or "" for any other kind.
func resolve(base *url.URL, ref string) string {
	if ref == "" || base == nil {
		return ""
	}
	u, err := base.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}

// skipped are elements that are the page around the article, not part of it.
func skipped(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Noscript, atom.Nav, atom.Header, atom.Footer, atom.Aside, atom.Form,
		atom.Iframe, atom.Svg, atom.Button, atom.Select, atom.Template, atom.Head, atom.Dialog, atom.Canvas, atom.Object, atom.Embed:
		return true
	}
	return false
}

// articleRoot picks the element that holds the post: an <article>, else <main>, else the element with the most
// paragraph text.
func articleRoot(doc *html.Node) *html.Node {
	var article, main *html.Node
	walk(doc, func(n *html.Node) bool {
		if n.Type != html.ElementNode {
			return true
		}
		switch {
		case n.DataAtom == atom.Article && article == nil && len(text(n)) > 200:
			article = n
		case n.DataAtom == atom.Main && main == nil:
			main = n
		case attr(n, "itemprop") == "articleBody" && article == nil:
			article = n
		}
		return true
	})
	if article != nil {
		return article
	}
	if main != nil && len(text(main)) > 200 {
		return main
	}
	// the container whose own paragraphs hold the most text
	var best *html.Node
	bestLen := 0
	walk(doc, func(n *html.Node) bool {
		if n.Type == html.ElementNode && skipped(n) {
			return false
		}
		if n.Type != html.ElementNode || (n.DataAtom != atom.Div && n.DataAtom != atom.Section && n.DataAtom != atom.Body) {
			return true
		}
		l := 0
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.DataAtom == atom.P {
				l += len(text(c))
			}
		}
		if l > bestLen {
			best, bestLen = n, l
		}
		return true
	})
	if bestLen < 200 {
		return nil
	}
	return best
}

type conv struct {
	base *url.URL
	out  strings.Builder
	list []int // per open list: 0 for a bullet list, else the next number
}

func (c *conv) blank() {
	s := c.out.String()
	if s == "" || strings.HasSuffix(s, "\n\n") {
		return
	}
	if strings.HasSuffix(s, "\n") {
		c.out.WriteString("\n")
	} else {
		c.out.WriteString("\n\n")
	}
}

// block writes the children of n as Markdown blocks.
func (c *conv) block(n *html.Node) {
	var run []*html.Node // inline content waiting to become a paragraph
	flush := func() {
		if len(run) == 0 {
			return
		}
		var b strings.Builder
		for _, r := range run {
			c.inline(&b, r)
		}
		if t := strings.TrimSpace(strings.Join(strings.Fields(b.String()), " ")); t != "" {
			c.blank()
			c.out.WriteString(t + "\n\n")
		}
		run = nil
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.TextNode {
			run = append(run, ch)
			continue
		}
		if ch.Type != html.ElementNode || skipped(ch) {
			continue
		}
		switch ch.DataAtom {
		case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
			flush()
			var b strings.Builder
			c.inline(&b, ch)
			if t := strings.TrimSpace(strings.Join(strings.Fields(b.String()), " ")); t != "" {
				level := int(ch.Data[1] - '0')
				if level < 2 { // the post's own title is the form's title; headings inside start at the second level
					level = 2
				}
				c.blank()
				c.out.WriteString(strings.Repeat("#", level) + " " + t + "\n\n")
			}
		case atom.P:
			run = append(run, ch)
			flush()
		case atom.Ul, atom.Ol:
			flush()
			next := 0 // a bullet list
			if ch.DataAtom == atom.Ol {
				next = 1
			}
			c.list = append(c.list, next)
			c.listItems(ch)
			c.list = c.list[:len(c.list)-1]
			c.blank()
		case atom.Blockquote:
			flush()
			sub := &conv{base: c.base}
			sub.block(ch)
			if t := strings.TrimSpace(sub.out.String()); t != "" {
				c.blank()
				for _, line := range strings.Split(t, "\n") {
					c.out.WriteString("> " + line + "\n")
				}
				c.out.WriteString("\n")
			}
		case atom.Pre:
			flush()
			c.blank()
			c.out.WriteString("```\n" + strings.Trim(rawText(ch), "\n") + "\n```\n\n")
		case atom.Hr:
			flush()
			c.blank()
			c.out.WriteString("---\n\n")
		case atom.Figure, atom.Div, atom.Section, atom.Article, atom.Main, atom.Figcaption, atom.Details, atom.Summary, atom.Table,
			atom.Tbody, atom.Thead, atom.Tr, atom.Td, atom.Th, atom.Dl, atom.Dd, atom.Dt, atom.Address:
			flush()
			c.block(ch)
		default: // inline element: a link, emphasis, a picture…
			run = append(run, ch)
		}
	}
	flush()
}

func (c *conv) listItems(l *html.Node) {
	depth := len(c.list) - 1
	for li := l.FirstChild; li != nil; li = li.NextSibling {
		if li.Type != html.ElementNode || li.DataAtom != atom.Li {
			continue
		}
		sub := &conv{base: c.base, list: c.list}
		sub.block(li)
		body := strings.TrimSpace(sub.out.String())
		if body == "" {
			continue
		}
		marker := "- "
		if n := c.list[depth]; n > 0 {
			marker = strconv.Itoa(n) + ". "
			c.list[depth]++
		}
		pad := strings.Repeat(" ", len(marker))
		lines := strings.Split(body, "\n")
		for i, line := range lines {
			switch {
			case i == 0:
				c.out.WriteString(strings.Repeat("  ", depth) + marker + line + "\n")
			case line == "":
				c.out.WriteString("\n")
			default:
				c.out.WriteString(strings.Repeat("  ", depth) + pad + line + "\n")
			}
		}
	}
}

func rawText(n *html.Node) string {
	var b strings.Builder
	walk(n, func(x *html.Node) bool {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		if x.Type == html.ElementNode && x.DataAtom == atom.Br {
			b.WriteString("\n")
		}
		return true
	})
	return b.String()
}

// inline writes n and what is inside it as one line of Markdown.
func (c *conv) inline(b *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(escape(n.Data))
		return
	case html.ElementNode:
	default:
		return
	}
	if skipped(n) {
		return
	}
	kids := func() string {
		var k strings.Builder
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			c.inline(&k, ch)
		}
		return k.String()
	}
	switch n.DataAtom {
	case atom.A:
		inner := strings.TrimSpace(kids())
		if href := resolve(c.base, attr(n, "href")); href != "" && inner != "" {
			b.WriteString("[" + inner + "](" + strings.ReplaceAll(href, ")", "%29") + ")")
		} else {
			b.WriteString(inner)
		}
	case atom.Img:
		if src := resolve(c.base, attr(n, "src")); src != "" {
			alt := strings.NewReplacer("[", "", "]", "", "\n", " ").Replace(attr(n, "alt"))
			b.WriteString("![" + alt + "](" + strings.ReplaceAll(src, ")", "%29") + ")")
		}
	case atom.Em, atom.I:
		if t := strings.TrimSpace(kids()); t != "" {
			b.WriteString("*" + t + "*")
		}
	case atom.Strong, atom.B:
		if t := strings.TrimSpace(kids()); t != "" {
			b.WriteString("**" + t + "**")
		}
	case atom.Code, atom.Kbd, atom.Samp:
		if t := strings.TrimSpace(rawText(n)); t != "" && !strings.Contains(t, "`") {
			b.WriteString("`" + t + "`")
		}
	case atom.Br:
		b.WriteString("  \n")
	default:
		b.WriteString(kids())
	}
}

// escape keeps characters that Markdown would take for markup out of the text.
func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "<", `\<`).Replace(s)
}
