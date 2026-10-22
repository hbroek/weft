package web

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/md"
)

// parentCard is a link card for the page a post replies to: shown from the snapshot the reply
// carries (weft:context), since a plain web page cannot be looked up like a Weft post.
type parentCard struct{ Href, Title, Description, Image, Host string }

func newParentCard(href string, p feed.ParentInfo) *parentCard {
	href = httpURL(href)
	if href == "" || p.Empty() {
		return nil
	}
	host := siteHost(href)
	return &parentCard{Href: href, Title: orStr(p.Title, host), Description: p.Description, Image: p.Image, Host: host}
}

// parentFromForm reads the parent snapshot the compose form carries in hidden fields.
func parentFromForm(r *http.Request) feed.ParentInfo {
	return feed.ParentInfo{Title: r.FormValue("ptitle"), Description: r.FormValue("pdesc"), Image: r.FormValue("pimg")}.Clean()
}

// modeLink is one of the ways to use the page the bookmarklet sent: Bookmark, Reply or Like.
type modeLink struct {
	Label, Href string
	Current     bool
}

// replyContext says what the post at target is, for the "replying to …" line of a timeline row: whose post
// it is and a card of its first lines. It looks only at what this server already has: one of the reader's
// own posts, a post of another user on this server (the host names the user), a post cached from a
// followed feed. Anything else is just its host, and the card is left to the snapshot the reply carries.
func (s *Server) replyContext(r *http.Request, q *req, target string) (who string, card *parentCard) {
	target = httpURL(target)
	if target == "" {
		return "", nil
	}
	host := siteHost(target)
	who = host
	// cardOf makes the card of a post from its title and Markdown. A post without a title is named by
	// its first words, then by the alt text of its first image, then "a photo"; a post with nothing to
	// show gets no card.
	cardOf := func(title, markdown string, plain bool) *parentCard {
		text, img, alt := markdown, "", ""
		if !plain {
			text, img, alt = readMarkdown(markdown)
		}
		desc := oneLine(text, 140)
		if title == "" {
			switch {
			case text != "":
				title, desc = oneLine(text, 90), ""
			case alt != "":
				title = alt
			case img != "":
				title = "a photo"
			default:
				return nil
			}
		}
		return &parentCard{Href: target, Title: oneLine(title, 90), Description: desc, Image: img, Host: host}
	}
	if id, ok := s.localPostID(q.sp, s.base(r), target); ok {
		if p, err := q.sp.Post(q.host.ID, id); err == nil && !p.Deleted {
			return "your post", cardOf(p.Title, p.Markdown, false)
		}
	}
	if name := s.hostUser(host); name != "" && name != q.host.Name {
		who = name + "’s post"
		if u, err := url.Parse(target); err == nil {
			if slug, ok := strings.CutPrefix(u.Path, "/posts/"); ok && slug != "" && !strings.Contains(slug, "/") {
				if sp, release, err := s.st.Space(name); err == nil {
					defer release()
					if p, err := sp.PostBySlug(sp.UID(), slug); err == nil && !p.Deleted {
						if usr, err := sp.User(); err == nil {
							who = orStr(usr.Display, name) + "’s post"
						}
						return who, cardOf(p.Title, p.Markdown, false)
					}
				}
			}
		}
		return who, nil
	}
	if it, err := q.sp.ItemByTarget(q.host.ID, target); err == nil {
		return orStr(it.FeedTitle, it.FeedURL) + "’s post", cardOf(it.Title, orStr(it.Markdown, it.Text), it.Markdown == "")
	}
	return who, nil
}

var (
	tagRun   = regexp.MustCompile(`<[^>]*>`)
	blockEnd = regexp.MustCompile(`(?i)</(p|li|h[1-6]|blockquote|pre)>|<br ?/?>`)
)

var imgTag = regexp.MustCompile(`(?i)<img\b[^>]*>`)
var imgAttr = regexp.MustCompile(`(?i)\b(src|alt)="([^"]*)"`)

// readMarkdown reads a post as a card does: its text without the markup, and the address and alt text of
// its first image.
func readMarkdown(markdown string) (text, img, alt string) {
	h, err := md.Render(markdown)
	if err != nil {
		return strings.Join(strings.Fields(markdown), " "), "", ""
	}
	if tag := imgTag.FindString(h); tag != "" {
		for _, m := range imgAttr.FindAllStringSubmatch(tag, -1) {
			v := html.UnescapeString(m[2])
			if strings.EqualFold(m[1], "src") {
				img = httpURL(v)
			} else {
				alt = strings.Join(strings.Fields(v), " ")
			}
		}
	}
	text = strings.Join(strings.Fields(html.UnescapeString(tagRun.ReplaceAllString(blockEnd.ReplaceAllString(h, " "), ""))), " ")
	return text, img, alt
}
