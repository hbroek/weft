package web

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

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

// postCard makes the card of a post from its title and Markdown (plain: text already without markup). A post
// without a title is named by its first words, then by the alt text of its first image, then "a photo"; a
// post with nothing to show gets no card.
func postCard(target, host, title, markdown string, plain bool) *parentCard {
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

// parentSnapshot is what a reply to target carries of the post it answers (weft:context), for a reply sent
// without one: what this server knows of the post, or else the post itself, asked for as a one-item feed,
// so a reply reads the same whether the post is on this server or another one. A page that is not a Weft
// post, or does not answer in time, gives nothing, and the reply keeps only the address.
func (s *Server) parentSnapshot(r *http.Request, q *req, target string) feed.ParentInfo {
	if _, card := s.replyContext(r, q, target); card != nil {
		return feed.ParentInfo{Title: card.Title, Description: card.Description, Image: card.Image}.Clean()
	}
	it := s.fetchPost(r.Context(), target)
	if it == nil {
		return feed.ParentInfo{}
	}
	card := postCard(target, siteHost(target), it.Title, orStr(it.Markdown, it.Text), it.Markdown == "")
	if card == nil {
		return feed.ParentInfo{}
	}
	return feed.ParentInfo{Title: card.Title, Description: card.Description, Image: card.Image}.Clean()
}

// fetchPost asks a Weft post's own server for it as a one-item feed (Accept: application/rss+xml), for a
// post this reader does not follow. It returns nil for a page that is not a Weft post or does not answer in
// five seconds.
func (s *Server) fetchPost(ctx context.Context, target string) *feed.ParsedItem {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := s.web.GetAs(ctx, target, "application/rss+xml")
	if err != nil {
		return nil
	}
	p, err := feed.Parse(res.Body, time.Now())
	if err != nil || len(p.Items) == 0 {
		return nil
	}
	it := p.Items[0]
	for _, x := range p.Items {
		if sameAddress(x.Link, target) {
			it = x
			break
		}
	}
	return &it
}

// replyContext says what the post at target is, for the "replying to …" line of a timeline row: whose post
// it is and a card of its first lines. It looks only at what this server already has: one of the reader's
// own posts, a post of another user on this server (the host names the user), a post cached from a
// followed feed ("a post from NYT > Top Stories": a feed is as often a publication as a person). Anything else is just its host, and the card is left to the snapshot the reply carries.
func (s *Server) replyContext(r *http.Request, q *req, target string) (who string, card *parentCard) {
	target = httpURL(target)
	if target == "" {
		return "", nil
	}
	host := siteHost(target)
	who = host
	cardOf := func(title, markdown string, plain bool) *parentCard {
		return postCard(target, host, title, markdown, plain)
	}
	if id, ok := s.localPostID(q.sp, s.base(r), target); ok {
		if p, err := q.sp.Post(q.host.ID, id); err == nil && !p.Deleted {
			return "your post", cardOf(p.Title, p.Markdown, false)
		}
	}
	if name := s.hostUser(host); name != "" && name != q.host.Name {
		// the host looks like a user here; if there is no such user (removed, or moved away), the post is
		// treated like any other and found in the feeds the reader follows
		if sp, release, err := s.st.Space(name); err == nil {
			defer release()
			who = name + "’s post"
			if u, err := url.Parse(target); err == nil {
				if slug, ok := strings.CutPrefix(u.Path, "/posts/"); ok && slug != "" && !strings.Contains(slug, "/") {
					if p, err := sp.PostBySlug(sp.UID(), slug); err == nil && !p.Deleted {
						if usr, err := sp.User(); err == nil {
							who = orStr(usr.Display, name) + "’s post"
						}
						return who, cardOf(p.Title, p.Markdown, false)
					}
				}
			}
			return who, nil
		}
	}
	if it, err := q.sp.ItemByTarget(q.host.ID, target); err == nil {
		return "a post from " + orStr(it.FeedTitle, it.FeedURL), cardOf(it.Title, orStr(it.Markdown, it.Text), it.Markdown == "")
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

// pageFeed is a feed the bookmarked page advertises, and whether the reader already follows it.
type pageFeed struct {
	URL, Title, Host string
	Following        bool
}

// sameAddress compares two addresses as a reader would: not by scheme, case of the host, a trailing slash or
// a fragment.
func sameAddress(a, b string) bool { return addressKey(a) == addressKey(b) }

func addressKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw
	}
	return strings.ToLower(u.Host) + strings.TrimSuffix(u.EscapedPath(), "/") + "?" + u.RawQuery
}

// pageFeeds reads the feeds the bookmarklet found on page (the feed and ftitle parameters, in order), keeping
// the first five that are web addresses, and marks those the reader follows. A feed counts as followed when its
// address is one of the reader's, or, when the page offers only that one feed, when the page is the home page
// of a feed the reader follows (a follow keeps the address the feed answered from, which can differ).
func pageFeeds(q *req, page string, urls, titles []string) []pageFeed {
	var out []pageFeed
	for i, raw := range urls {
		u := httpURL(raw)
		if u == "" || len(out) == 5 {
			continue
		}
		dup := false
		for _, f := range out {
			dup = dup || sameAddress(f.URL, u)
		}
		if dup {
			continue
		}
		title := ""
		if i < len(titles) {
			title = oneLine(titles[i], 80)
		}
		out = append(out, pageFeed{URL: u, Title: title, Host: siteHost(u)})
	}
	if len(out) == 0 {
		return nil
	}
	follows, _ := q.sp.Follows(q.host.ID)
	for i := range out {
		for _, f := range follows {
			if sameAddress(f.URL, out[i].URL) || (len(out) == 1 && f.Link != "" && sameAddress(f.Link, page)) {
				out[i].Following = true
			}
		}
	}
	return out
}
