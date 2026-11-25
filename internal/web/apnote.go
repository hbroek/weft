package web

import (
	"html"
	"mime"
	"net/url"
	"path"
	"strings"
	"unicode"

	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// A post as an ActivityPub Note. Mastodon keeps few HTML tags and drops the rest, so the post's HTML is rewritten to
// the tags it keeps: headings become bold paragraphs, pictures leave the text and go out as attachments, and
// anything it would strip is unwrapped. The post goes out as the text of a post on Mastodon: whole, with its title as
// a bold first line and no link back to the site (a link would give it a preview card repeating the text; the post's
// own address is the Note's url, which Mastodon offers as "open original page"). Only a post so long that other servers
// may refuse it goes as its first paragraphs and a link.

const (
	noteWholeLimit = 10000 // a post whose text is this long or shorter goes out whole, as it is
	excerptMin     = 240   // an excerpt takes blocks until it has this much text...
	excerptMax     = 400   // ...and never more than this
	maxAttachments = 4     // what Mastodon shows
	maxHashtags    = 10
)

type noteImage struct{ URL, Alt string }

// federateHTML rewrites the post's HTML for the fediverse: the blocks of it that are left, each as HTML, and the
// pictures it had. base resolves addresses that are relative to the site.
func federateHTML(markup, base string) (blocks []string, images []noteImage) {
	baseURL, _ := url.Parse(base + "/")
	top, err := nethtml.ParseFragment(strings.NewReader(markup), &nethtml.Node{Type: nethtml.ElementNode, DataAtom: atom.Body, Data: "body"})
	if err != nil {
		return nil, nil
	}
	resolve := func(ref string) string {
		u, err := url.Parse(strings.TrimSpace(ref))
		if err != nil || baseURL == nil {
			return ""
		}
		u = baseURL.ResolveReference(u)
		if u.Scheme != "http" && u.Scheme != "https" {
			return ""
		}
		return u.String()
	}
	var render func(n *nethtml.Node, b *strings.Builder)
	children := func(n *nethtml.Node, b *strings.Builder) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			render(c, b)
		}
	}
	attr := func(n *nethtml.Node, key string) string {
		for _, a := range n.Attr {
			if a.Key == key {
				return a.Val
			}
		}
		return ""
	}
	render = func(n *nethtml.Node, b *strings.Builder) {
		switch n.Type {
		case nethtml.TextNode:
			b.WriteString(html.EscapeString(n.Data))
		case nethtml.ElementNode:
			switch n.Data {
			case "p", "blockquote", "pre", "ul", "ol", "li", "code", "em", "i", "strong", "b", "del", "s", "u", "sub", "sup":
				b.WriteString("<" + n.Data + ">")
				children(n, b)
				b.WriteString("</" + n.Data + ">")
			case "br":
				b.WriteString("<br>")
			case "a":
				if href := resolve(attr(n, "href")); href != "" {
					b.WriteString(`<a href="` + html.EscapeString(href) + `" rel="nofollow noopener noreferrer">`)
					children(n, b)
					b.WriteString("</a>")
				} else {
					children(n, b)
				}
			case "h1", "h2", "h3", "h4", "h5", "h6":
				b.WriteString("<p><strong>")
				children(n, b)
				b.WriteString("</strong></p>")
			case "img":
				if src := resolve(attr(n, "src")); src != "" {
					images = append(images, noteImage{URL: src, Alt: strings.TrimSpace(attr(n, "alt"))})
				}
			case "tr":
				b.WriteString("<p>")
				children(n, b)
				b.WriteString("</p>")
			case "td", "th":
				children(n, b)
				b.WriteString(" ")
			case "hr", "script", "style", "iframe", "object", "embed", "svg", "video", "audio":
			default: // div, span, figure, table, thead and the rest: what is in them stays
				children(n, b)
			}
		}
	}
	for _, n := range top {
		var b strings.Builder
		switch {
		case n.Type == nethtml.TextNode:
			if strings.TrimSpace(n.Data) != "" {
				b.WriteString("<p>" + html.EscapeString(strings.TrimSpace(n.Data)) + "</p>")
			}
		default:
			render(n, &b)
		}
		out := strings.TrimSpace(b.String())
		if out != "" && out != "<p></p>" && plainText(out) != "" {
			blocks = append(blocks, out)
		}
	}
	return blocks, images
}

func runeLen(s string) int { return len([]rune(s)) }

// truncateWords cuts text to at most max runes, at a word, and says it was cut.
func truncateWords(text string, max int) string {
	r := []rune(text)
	if len(r) <= max {
		return text
	}
	cut := string(r[:max])
	if i := strings.LastIndexFunc(cut, unicode.IsSpace); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimRightFunc(cut, func(c rune) bool { return unicode.IsSpace(c) || strings.ContainsRune(",;:-", c) }) + "…"
}

// excerptOf takes the first blocks of a long post: whole ones while they add up to a little text, the last one cut
// if it would be too much.
func excerptOf(blocks []string) []string {
	var out []string
	acc := 0
	for _, b := range blocks {
		if acc >= excerptMin {
			break
		}
		t := plainText(b)
		if acc+runeLen(t) > excerptMax {
			out = append(out, "<p>"+html.EscapeString(truncateWords(t, excerptMax-acc))+"</p>")
			break
		}
		out = append(out, b)
		acc += runeLen(t)
	}
	return out
}

// hashtagOf makes a hashtag of a keyword: letters, digits and underscores, the rest taken out. It is "" when nothing
// usable is left, or only digits are (a hashtag of digits is not one).
func hashtagOf(keyword string) string {
	var b strings.Builder
	letters := false
	for _, c := range keyword {
		switch {
		case unicode.IsLetter(c):
			letters = true
			b.WriteRune(c)
		case unicode.IsDigit(c) || c == '_':
			b.WriteRune(c)
		}
	}
	if !letters {
		return ""
	}
	return b.String()
}

type noteTag struct{ Name, Href string }

// noteTags are the hashtags of a post's keywords, in order, without repeats.
func noteTags(keywords []string, site string) []noteTag {
	var out []noteTag
	seen := map[string]bool{}
	for _, k := range keywords {
		t := hashtagOf(k)
		if t == "" || seen[strings.ToLower(t)] || len(out) == maxHashtags {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, noteTag{Name: "#" + t, Href: site + "/?keyword=" + url.QueryEscape(k)})
	}
	return out
}

// noteContent is the HTML a Note carries, with the pictures that go as attachments.
func noteContent(title, markup, permalink string, tags []noteTag, site string) (string, []noteImage) {
	blocks, images := federateHTML(markup, site)
	total := 0
	for _, b := range blocks {
		total += runeLen(plainText(b))
	}
	link := func(label string) string {
		return `<a href="` + html.EscapeString(permalink) + `" rel="nofollow noopener noreferrer">` + label + `</a>`
	}
	var parts []string
	if title = strings.TrimSpace(title); title != "" {
		parts = append(parts, "<p><strong>"+html.EscapeString(title)+"</strong></p>")
	}
	if total <= noteWholeLimit {
		parts = append(parts, blocks...)
	} else {
		parts = append(parts, excerptOf(blocks)...)
		parts = append(parts, "<p>"+link("Read more")+"</p>")
	}
	if len(tags) > 0 {
		var ts []string
		for _, t := range tags {
			ts = append(ts, `<a href="`+html.EscapeString(t.Href)+`" class="mention hashtag" rel="tag">#<span>`+html.EscapeString(strings.TrimPrefix(t.Name, "#"))+`</span></a>`)
		}
		parts = append(parts, "<p>"+strings.Join(ts, " ")+"</p>")
	}
	if len(images) > maxAttachments {
		images = images[:maxAttachments]
	}
	return strings.Join(parts, ""), images
}

// imageType is the media type of a picture by its address, or "" if it is not one Mastodon takes.
func imageType(addr string) string {
	u, err := url.Parse(addr)
	if err != nil {
		return ""
	}
	t := mime.TypeByExtension(strings.ToLower(path.Ext(u.Path)))
	if !strings.HasPrefix(t, "image/") || t == "image/svg+xml" {
		return ""
	}
	return t
}
