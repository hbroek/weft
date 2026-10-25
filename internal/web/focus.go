package web

import (
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/feed"
)

// The focused thread view shows one post in its conversation: the chain of ancestors from the
// root down to the post, and the post's own replies. Nothing beside the path is shown; every post on
// it links to its own branch.
// It costs about one fetch per level of depth, not one per post in the whole thread.

// nodeFor loads the post at u from whatever this reader already has, and fetches its author's feed
// only when nothing local knows it. hint is that feed's URL, if the caller has it.
func (b *threadBuilder) nodeFor(u, hint string) *tnode {
	host := b.q.host.ID
	sp := b.q.sp
	if id, ok := b.s.localPostID(sp, b.base, u); ok {
		if p, err := b.q.sp.Post(host, id); err == nil {
			if p.Deleted {
				return &tnode{URL: u, Tombstone: true, Author: u, repliesCount: -1}
			}
			n := b.localNode(p)
			n.parentFeed = p.InReplyToFeed
			return n
		}
	}
	if it, err := b.q.sp.ItemByTarget(host, u); err == nil {
		n := b.itemNode(*it)
		n.parentFeed = it.InReplyToFeed
		return n
	}
	if rs, err := b.q.sp.RepliesForUser(host, ""); err == nil {
		for _, rp := range rs {
			if rp.Source == u {
				n := b.receivedNode(rp, b.s.postURL(b.r, rp.PostID))
				n.parentFeed = b.own
				return n
			}
		}
	}
	// The post's own URL is the source of truth: asked for as a one-item feed it gives the post, its
	// parent and root, and where its replies are. A feed is only the fallback, for posts from
	// software that does not answer that way.
	if n := b.fromFeed(u, "", b.fetchItem(u)); n != nil {
		return n
	}
	if hint != "" {
		if n := b.fromFeed(u, hint, b.fetchFeed(hint)); n != nil {
			return n
		}
	}
	if hint == "" {
		if hint = b.s.discoverFeed(b.ctx, sp, u); hint != "" {
			if n := b.fromFeed(u, hint, b.fetchFeed(hint)); n != nil {
				return n
			}
		}
	}
	return b.missing(u, hint)
}

// fromFeed builds the node for the item at u in a parsed feed, or returns nil if it is not there.
// hint is the feed's URL; it defaults to the channel's own link when the feed is a one-item answer.
func (b *threadBuilder) fromFeed(u, hint string, p *feed.Parsed) *tnode {
	if p == nil {
		return nil
	}
	for _, it := range p.Items {
		if it.GUID != u {
			continue
		}
		if hint == "" {
			hint = strings.TrimRight(p.Link, "/") + "/feed.xml"
		}
		return &tnode{URL: u, Author: orStr(p.Title, hint), Title: it.Title, Date: stamp(it.Published),
			HTML: renderRemote(it.Markdown, it.Text), md: orStr(it.Markdown, it.Text), parent: it.InReplyTo, parentFeed: it.InReplyToFeed, published: it.Published,
			feedHint: hint, repliesHref: it.RepliesHref, repliesCount: it.RepliesCount,
			ReplyHref: b.reply(u) + "&reply_feed=" + url.QueryEscape(hint)}
	}
	return nil
}

// fetchItem asks the post's own URL for itself as a one-item feed. Servers that do not offer that
// answer with a page, which does not parse, and the caller falls back to the author's feed.
func (b *threadBuilder) fetchItem(u string) *feed.Parsed {
	key := "item:" + u
	if p, ok := b.feeds[key]; ok {
		return p
	}
	if b.fetches >= maxThreadFetches || b.ctx.Err() != nil {
		return nil
	}
	b.fetches++
	var parsed *feed.Parsed
	if res, err := b.s.web.GetAs(b.ctx, u, "application/rss+xml"); err == nil {
		if p, err := feed.Parse(res.Body, time.Now()); err == nil {
			parsed = p
		}
	}
	b.feeds[key] = parsed
	return parsed
}

// feedChild turns an item of a replies feed into a closed node.
func (b *threadBuilder) feedChild(it feed.ParsedItem) *tnode {
	name := it.FeedName
	if name == "" {
		name = it.FeedHref
	}
	return &tnode{URL: it.GUID, Author: name, Title: it.Title, Date: stamp(it.Published),
		HTML: renderRemote("", it.Text), md: it.Text, parent: it.InReplyTo, published: it.Published, feedHint: it.FeedHref,
		repliesHref: it.RepliesHref, repliesCount: it.RepliesCount,
		ReplyHref: b.reply(it.GUID) + "&reply_feed=" + url.QueryEscape(it.FeedHref)}
}

// childIndex reads n's approved replies and their approved replies (a replies feed holds two
// levels) and returns them grouped by the post they answer. Every key is a post whose replies are
// now completely known; n itself is always a key when the read worked. nil means it did not.
func (b *threadBuilder) childIndex(n *tnode) map[string][]*tnode {
	idx := map[string][]*tnode{n.URL: nil}
	if id, ok := b.s.localPostID(b.q.sp, b.base, n.URL); ok {
		for _, c := range b.localChildren(id) {
			idx[c.parent] = append(idx[c.parent], c)
		}
		return idx
	}
	if n.Tombstone || n.Missing || n.repliesCount == 0 {
		return idx
	}
	href := n.repliesHref
	if href == "" {
		href = b.findRepliesHref(n)
	}
	if href == "" {
		return nil
	}
	p := b.fetchFeed(href)
	if p == nil {
		return nil
	}
	for _, it := range p.Items {
		if it.InReplyTo == "" || it.GUID == "" {
			continue
		}
		idx[it.InReplyTo] = append(idx[it.InReplyTo], b.feedChild(it))
	}
	return idx
}

const maxChain = 40

// focus builds the focused view of target and returns the root of the path and the focused node.
func (b *threadBuilder) focus(target string) (root, focus *tnode) {
	focus = b.nodeFor(target, "")
	chain := []*tnode{focus}
	seen := map[string]bool{focus.URL: true}
	for cur := focus; cur.parent != "" && !cur.Missing && !cur.Tombstone && len(chain) < maxChain && b.ctx.Err() == nil; {
		if seen[cur.parent] {
			break
		}
		p := b.nodeFor(cur.parent, cur.parentFeed)
		seen[p.URL] = true
		chain = append(chain, p)
		cur = p
	}

	// Context is the path to the root and nothing beside it, so only the post's own replies are
	// read (a replies feed holds two levels: its replies and theirs). Each ancestor offers a link
	// to open its branch.
	index := map[string][]*tnode{}
	known := map[string]bool{}
	for k, v := range b.childIndex(focus) {
		index[k], known[k] = v, true
	}
	for k := len(chain) - 1; k >= 1; k-- {
		x := chain[k]
		x.OnPath, x.expanded = true, true
		x.Children = []*tnode{chain[k-1]}
		// the next post down the path is one of its replies and is already on the page, unless it
		// is not approved, in which case the count never included it
		x.Other, x.Replies = true, x.repliesCount
		if x.Replies > 0 && !chain[k-1].Unapproved {
			x.Replies--
		}
		if x.Replies != 0 {
			x.DrillHref = threadHref(x.URL, "")
		}
	}
	focus.OnPath, focus.expanded = true, true
	for _, c := range index[focus.URL] {
		c.Replies = -1
		if known[c.URL] {
			c.Replies = len(index[c.URL])
		} else if c.repliesCount >= 0 {
			c.Replies = c.repliesCount
		}
		if c.Replies != 0 {
			c.DrillHref = threadHref(c.URL, "")
		}
		focus.Children = append(focus.Children, c)
	}
	sort.SliceStable(focus.Children, func(i, j int) bool { return focus.Children[i].published.Before(focus.Children[j].published) })
	focus.Focus = true
	return chain[len(chain)-1], focus
}
