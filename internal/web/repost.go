package web

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/extract"
)

// repostText is what a repost copies from the page at an address.
type repostText struct {
	Title, Markdown string
	Canonical       string // where the content first appeared: the address to publish as the canonical link
}

// repostSource reads the page a repost copies. It asks for the best form first: a Weft post (or any feed
// item) that has its Markdown, then a page that answers with Markdown, then the article text of an HTML page.
// A page that gives nothing yields an empty Markdown, and the caller starts from a card with its link.
func (s *Server) repostSource(ctx context.Context, page string) repostText {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out := repostText{Canonical: untracked(page)}
	if it := s.fetchPost(ctx, page); it != nil && (it.Markdown != "" || it.Text != "") {
		out.Title, out.Markdown = it.Title, strings.TrimSpace(orStr(it.Markdown, it.Text))
		if c := httpURL(it.Canonical); c != "" { // a repost of a repost points at the first
			out.Canonical = untracked(c)
		}
		return out
	}
	if res, err := s.web.GetAs(ctx, page, "text/markdown, text/html;q=0.5"); err == nil {
		if strings.HasPrefix(strings.ToLower(res.ContentType), "text/markdown") {
			out.Markdown = strings.TrimSpace(string(res.Body))
			return out
		}
		a := extract.Page(res.Body, res.URL)
		out.Title, out.Markdown = a.Title, a.Markdown
		if c := httpURL(a.Canonical); c != "" {
			out.Canonical = untracked(c)
		}
	}
	return out
}

// repostLine is the quoted line at the top of a repost that says where it comes from, followed by a blank line.
func repostLine(canonical string) string {
	return "> Repost from [" + siteHost(canonical) + "](" + strings.ReplaceAll(canonical, ")", "%29") + ")\n\n"
}

// untracked drops the parts of an address that only say where a visitor came from: the fragment and the usual
// campaign parameters.
func untracked(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Fragment = ""
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "utm_") || lk == "fbclid" || lk == "gclid" || lk == "mc_cid" || lk == "mc_eid" || lk == "igshid" {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}
