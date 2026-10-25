package web

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/feed"
)

// The pages a person navigates can also be asked for as Markdown (Accept: text/markdown): the same
// content as the page shows, as one document, with links to the other pages. It is not the user
// interface rebuilt: no menus, forms or buttons, only what the page is about. Remote posts are passed
// through as their authors wrote them, so a reader of the document treats them as untrusted.

// markdown renders the content of the named page as Markdown, or reports that the page has none.
func (d pageData) markdown(name string) (string, bool) {
	var b strings.Builder
	switch name {
	case "posts":
		d.mdPosts(&b)
	case "timeline":
		d.mdTimeline(&b)
	case "thread":
		d.mdThread(&b)
	case "following":
		d.mdFollowing(&b)
	case "moderation":
		d.mdModeration(&b)
	case "likers":
		d.mdLikers(&b)
	case "like":
		fmt.Fprintf(&b, "# %s liked\n\n%s\n", mdLine(d.Like.Author), mdLink(d.Like.Target, d.Like.Target))
		if c := d.Like.Card; c != nil {
			d.mdCard(&b, c)
		}
	case "tombstone":
		b.WriteString("# Post removed\n\nThis post was removed by its author.\n")
	default:
		return "", false
	}
	return b.String(), true
}

// abs makes a path on this site a full address, so a document keeps working when it is copied.
func (d pageData) abs(path string) string { return strings.TrimSuffix(d.SiteURL, "/") + path }

// mdLine makes text safe on one line of Markdown.
func mdLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.NewReplacer(`\`, `\\`, "[", `\[`, "]", `\]`, "*", `\*`, "_", `\_`, "`", "\\`", "<", "&lt;").Replace(s)
}

func mdLink(text, href string) string {
	href = strings.NewReplacer(" ", "%20", "(", "%28", ")", "%29").Replace(href)
	return "[" + mdLine(text) + "](" + href + ")"
}

// mdCard is a link card for a page the post is about: "> **Title**: description (host)".
func (d pageData) mdCard(b *strings.Builder, c *parentCard) {
	b.WriteString("\n> " + cardLine(c) + "\n")
}

func cardLine(c *parentCard) string {
	s := "**" + mdLink(c.Title, c.Href) + "**"
	if c.Description != "" {
		s += ": " + mdLine(c.Description)
	}
	return s + " (" + c.Host + ")"
}

func (d pageData) mdKeywords(keywords []string, path string) string {
	var out []string
	for _, k := range keywords {
		out = append(out, mdLink(k, d.abs(path+url.PathEscape(k))))
	}
	return strings.Join(out, ", ")
}

func (d pageData) mdPosts(b *strings.Builder) {
	fmt.Fprintf(b, "# %s\n", mdLine(d.Author))
	if d.Bio != "" {
		b.WriteString("\n" + mdLine(d.Bio) + "\n")
	}
	if d.Keyword != "" {
		fmt.Fprintf(b, "\nPosts tagged **%s**. %s · %s\n", mdLine(d.Keyword), mdLink("All posts", d.abs("/")), mdLink("Feed for this topic", d.TopicFeed))
	}
	if len(d.Posts) == 0 {
		b.WriteString("\nNo posts yet.\n")
	}
	for _, p := range d.Posts {
		b.WriteString("\n---\n\n")
		when := p.Date
		if p.Edited {
			when += " · edited"
		}
		if p.Title != "" {
			fmt.Fprintf(b, "## %s\n\n%s\n", mdLink(p.Title, p.URL), when)
		} else {
			fmt.Fprintf(b, "%s · %s\n", when, mdLink("permalink", p.URL))
		}
		if p.InReplyTo != "" {
			fmt.Fprintf(b, "\nIn reply to %s\n", mdLink(siteHost(p.InReplyTo), p.InReplyTo))
			if p.Parent != nil {
				d.mdCard(b, p.Parent)
			}
		}
		b.WriteString("\n" + strings.TrimSpace(p.Markdown) + "\n")
		var meta []string
		if len(p.Keywords) > 0 {
			meta = append(meta, "Keywords: "+d.mdKeywords(p.Keywords, "/?keyword="))
		}
		if p.Tally != "" {
			meta = append(meta, mdLink(p.Tally, d.abs("/liked-by?target="+url.QueryEscape(p.URL))))
		}
		if len(meta) > 0 {
			b.WriteString("\n" + strings.Join(meta, " · ") + "\n")
		}
	}
}

func (d pageData) mdTimeline(b *strings.Builder) {
	b.WriteString("# Timeline\n")
	if d.Keyword != "" {
		fmt.Fprintf(b, "\nTagged **%s**", mdLine(d.Keyword))
		if d.Only != "" {
			fmt.Fprintf(b, " from **%s**", mdLine(d.Heading))
		}
		fmt.Fprintf(b, ". %s\n", mdLink("Clear filter", d.abs(d.ClearKeyword)))
	} else if d.Only != "" {
		fmt.Fprintf(b, "\nFrom **%s** (%s). %s\n", mdLine(d.Heading), d.Only, mdLink("All feeds", d.abs("/timeline")))
	}
	if len(d.Timeline) == 0 {
		b.WriteString("\nNothing yet.\n")
	}
	kwPath := "/timeline?keyword="
	for _, r := range d.Timeline {
		who := mdLine(r.Author)
		if r.AuthorHref != "" {
			who = mdLink(r.Author, d.abs(r.AuthorHref))
		}
		if r.IsLike {
			what := r.LikedAs
			if r.LikedHref != "" {
				what = mdLink(r.LikedAs, r.LikedHref)
			}
			fmt.Fprintf(b, "\n- **%s** liked %s (%s)\n", who, what, r.Date)
			continue
		}
		b.WriteString("\n---\n\n")
		when := r.Date
		if r.Edited {
			when += " · edited"
		}
		if r.Link != "" {
			when = mdLink(when, r.Link)
		}
		head := "**" + who + "** · " + when
		if r.InReplyTo != "" {
			head += " · replying to " + mdLink(siteHost(r.InReplyTo), r.InReplyTo)
		}
		if r.Title != "" {
			fmt.Fprintf(b, "## %s\n\n", mdLine(r.Title))
		}
		b.WriteString(head + "\n")
		if r.Parent != nil {
			d.mdCard(b, r.Parent)
		}
		b.WriteString("\n" + strings.TrimSpace(r.MD) + "\n")
		var meta []string
		if len(r.Keywords) > 0 {
			meta = append(meta, "Keywords: "+d.mdKeywords(r.Keywords, kwPath))
		}
		if r.Tally != "" && r.Target != "" || r.Own && r.Tally != "" {
			t := r.Target
			if r.Own {
				t = r.Link
			}
			meta = append(meta, mdLink(r.Tally, d.abs("/liked-by?target="+url.QueryEscape(t))))
		}
		if r.HasThread && r.ThreadHref != "" {
			meta = append(meta, mdLink("Thread", d.abs(r.ThreadHref)))
		}
		if len(meta) > 0 {
			b.WriteString("\n" + strings.Join(meta, " · ") + "\n")
		}
	}
}

func (d pageData) mdThread(b *strings.Builder) {
	t := d.Thread
	names := map[string]string{"focus": "This branch", "curated": "Whole thread", "raw": "Raw"}
	fmt.Fprintf(b, "# Thread\n\n%s (%s). Views: %s · %s · %s\n", names[t.View], postsText(t.Count),
		viewLink(d, t, "focus", t.FocusHref), viewLink(d, t, "curated", t.CuratedHref), viewLink(d, t, "raw", t.RawHref))
	d.mdNode(b, t.Root, 0)
}

func viewLink(d pageData, t *threadPage, view, href string) string {
	if t.View == view {
		return names3[view]
	}
	return mdLink(names3[view], d.abs(href))
}

var names3 = map[string]string{"focus": "This branch", "curated": "Whole thread", "raw": "Raw"}

func (d pageData) mdNode(b *strings.Builder, n *tnode, depth int) {
	var lines []string
	switch {
	case n.Tombstone:
		lines = append(lines, "*original post removed* ("+n.URL+")")
	case n.Missing:
		if n.Parent != nil {
			lines = append(lines, cardLine(n.Parent))
		} else {
			lines = append(lines, mdLink(n.URL, n.URL)+": "+n.Note)
		}
	default:
		who := "**" + mdLine(n.Author) + "**"
		head := who + " · " + mdLink(n.Date, n.URL)
		if n.Title != "" {
			head += " · *" + mdLine(n.Title) + "*"
		}
		lines = append(lines, head, "")
		lines = append(lines, strings.Split(strings.TrimSpace(n.md), "\n")...)
		if n.Note != "" {
			lines = append(lines, "", "*"+mdLine(n.Note)+"*")
		}
	}
	if n.DrillHref != "" {
		text := "Replies"
		switch {
		case n.Replies > 0 && n.Other:
			text = repliesText(n.Replies, "other ")
		case n.Replies > 0:
			text = repliesText(n.Replies, "")
		case n.Other:
			text = "Other replies"
		}
		lines = append(lines, "", mdLink(text, d.abs(n.DrillHref)))
	}
	prefix := strings.Repeat("> ", depth)
	b.WriteString("\n")
	for _, l := range lines {
		b.WriteString(strings.TrimRight(prefix+l, " ") + "\n")
	}
	for _, c := range n.Children {
		d.mdNode(b, c, depth+1)
	}
}

func (d pageData) mdFollowing(b *strings.Builder) {
	b.WriteString("# Following\n\n")
	if len(d.Follows) == 0 {
		b.WriteString("Not following anyone yet.\n")
	}
	for _, f := range d.Follows {
		line := "- " + mdLink(orStr(f.Title, f.URL), d.abs("/timeline?feed="+url.QueryEscape(f.URL))) + " (" + f.URL + ")"
		if f.Err != "" {
			line += " — error: " + mdLine(f.Err)
		}
		b.WriteString(line + "\n")
	}
	fmt.Fprintf(b, "\nThe list is public: %s\n", mdLink("following.opml", d.abs("/following.opml")))
}

func (d pageData) mdModeration(b *strings.Builder) {
	b.WriteString("# Replies to your posts\n")
	if len(d.Moderation) == 0 {
		b.WriteString("\nNo replies yet.\n")
	}
	for _, m := range d.Moderation {
		fmt.Fprintf(b, "\n---\n\n## %s on %s (%s)\n", mdLine(m.Author), mdLink(fmt.Sprintf("your post %d", m.PostID), m.PostURL), m.Status)
		if m.Title != "" {
			fmt.Fprintf(b, "\n*%s*\n", mdLine(m.Title))
		}
		fmt.Fprintf(b, "\n%s\n\nSource: %s\n", strings.TrimSpace(m.MD), mdLink(m.Source, m.Source))
	}
}

func (d pageData) mdLikers(b *strings.Builder) {
	fmt.Fprintf(b, "# Likes\n\nof %s\n\n", mdLink(d.LikeTitle, d.LikeTarget))
	if len(d.Likers) == 0 && len(d.Others) == 0 {
		b.WriteString("No likes to show.\n")
	}
	for _, l := range d.Likers {
		line := "- " + mdLine(l.Name)
		if l.FeedURL != "" {
			line = "- " + mdLink(l.Name, d.abs("/timeline?feed="+url.QueryEscape(l.FeedURL))) + " (" + l.FeedURL + ")"
		}
		if l.Tag != "" {
			line += " — " + l.Tag
		}
		b.WriteString(line + "\n")
	}
	if len(d.Others) > 0 {
		b.WriteString("\n## Also liked by\n\nPeople you don't follow.\n\n")
		for _, l := range d.Others {
			b.WriteString("- " + mdLink(l.Name, l.Site) + " (" + l.FeedURL + ")\n")
		}
	}
	if d.LikeNote != "" {
		b.WriteString("\n" + mdLine(d.LikeNote) + "\n")
	}
}

// ---- feeds as Markdown ----

// wantsMarkdown reports whether the request asks for Markdown and for no HTML-like type (a feed reader
// that says */* still gets the feed).
func wantsMarkdown(r *http.Request) bool { return negotiate(r.Header.Get("Accept")) == "markdown" }

// writeChannel answers with the feed, or with the same content as a Markdown document when that is
// what the request asks for.
func (s *Server) writeChannel(w http.ResponseWriter, r *http.Request, ch feed.Channel) {
	w.Header().Add("Vary", "Accept")
	if wantsMarkdown(r) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		io.WriteString(w, markdownChannel(ch))
		return
	}
	b, err := feed.New(ch).Marshal()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write(b)
}

func feedDate(s string) string {
	if t, err := time.Parse(time.RFC1123Z, s); err == nil {
		return t.Local().Format("2 Jan 2006 at 15:04")
	}
	return s
}

// markdownChannel is a feed's content as one document: the channel, its items, and the links to
// the neighbouring pages of the feed.
func markdownChannel(ch feed.Channel) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", mdLine(ch.Title))
	if ch.Description != "" {
		b.WriteString("\n" + mdLine(ch.Description) + "\n")
	}
	if ch.Link != "" {
		b.WriteString("\n" + mdLink(ch.Link, ch.Link) + "\n")
	}
	var also []string
	if ch.LikesFeed != "" {
		also = append(also, mdLink("Likes", ch.LikesFeed))
	}
	if ch.Following != "" {
		also = append(also, mdLink("Following", ch.Following))
	}
	if len(also) > 0 {
		b.WriteString("\nAlso: " + strings.Join(also, " · ") + "\n")
	}
	if len(ch.Items) == 0 {
		b.WriteString("\nNothing here yet.\n")
	}
	site := strings.TrimSuffix(ch.Link, "/")
	for _, it := range ch.Items {
		who := ""
		if it.Feed != nil && it.Feed.Name != "" {
			who = "**" + mdLine(it.Feed.Name) + "** · "
		}
		if it.LikeOf != nil {
			what := orStr(it.LikeOf.Title, it.LikeOf.Href)
			line := "\n- " + who + "liked " + mdLink(what, it.LikeOf.Href)
			if it.LikeOf.Description != "" {
				line += ": " + mdLine(it.LikeOf.Description)
			}
			b.WriteString(line + " (" + feedDate(it.PubDate) + ")\n")
			continue
		}
		b.WriteString("\n---\n\n")
		when := feedDate(it.PubDate)
		if it.Updated != "" {
			when += " · edited"
		}
		if it.Title != "" {
			fmt.Fprintf(&b, "## %s\n\n%s%s\n", mdLink(it.Title, it.Link), who, when)
		} else {
			fmt.Fprintf(&b, "%s%s · %s\n", who, when, mdLink("permalink", it.Link))
		}
		if it.InReplyTo != nil && it.InReplyTo.Href != "" {
			fmt.Fprintf(&b, "\nIn reply to %s\n", mdLink(siteHost(it.InReplyTo.Href), it.InReplyTo.Href))
			if c := it.Context; c != nil && (c.Title != "" || c.Description != "") {
				b.WriteString("\n> " + cardLine(&parentCard{Href: it.InReplyTo.Href, Title: orStr(c.Title, siteHost(it.InReplyTo.Href)), Description: c.Description, Host: siteHost(it.InReplyTo.Href)}) + "\n")
			}
		}
		b.WriteString("\n" + strings.TrimSpace(orStr(it.Markdown, it.Description)) + "\n")
		var meta []string
		if len(it.Categories) > 0 {
			var ks []string
			for _, k := range it.Categories {
				if site != "" {
					ks = append(ks, mdLink(k, site+"/?keyword="+url.PathEscape(k)))
				} else {
					ks = append(ks, mdLine(k))
				}
			}
			meta = append(meta, "Keywords: "+strings.Join(ks, ", "))
		}
		if it.Likes != nil && it.Likes.Count > 0 {
			meta = append(meta, likesText(it.Likes.Count))
		}
		if it.Replies != nil && it.Replies.Href != "" {
			text := "Replies"
			if it.Replies.Count > 0 {
				text = repliesText(it.Replies.Count, "")
			}
			meta = append(meta, mdLink(text, it.Replies.Href))
		}
		if len(meta) > 0 {
			b.WriteString("\n" + strings.Join(meta, " · ") + "\n")
		}
	}
	var nav []string
	for _, l := range ch.Links {
		switch l.Rel {
		case "first":
			nav = append(nav, mdLink("First page", l.Href))
		case "previous":
			nav = append(nav, mdLink("Newer posts", l.Href))
		case "next":
			nav = append(nav, mdLink("Older posts", l.Href))
		}
	}
	if len(nav) > 0 {
		b.WriteString("\n---\n\n" + strings.Join(nav, " · ") + "\n")
	}
	return b.String()
}
