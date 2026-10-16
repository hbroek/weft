package web

import (
	"net/url"
	"sort"

	"github.com/hbroek/text-thread-casting/internal/feed"
)

// The focused thread view shows one post in its conversation: the chain of ancestors from the
// root down to the post, the siblings at every level, and the post's own replies. Branches off
// that path are left closed, each with its reply count and a link that makes it the new focus.
// It costs about one fetch per level of depth, not one per post in the whole thread.

// nodeFor loads the post at u from whatever this reader already has, and fetches its author's feed
// only when nothing local knows it. hint is that feed's URL, if the caller has it.
func (b *threadBuilder) nodeFor(u, hint string) *tnode {
	host := b.q.host.ID
	if id, ok := b.s.localPostID(host, b.base, u); ok {
		if p, err := b.s.st.Post(host, id); err == nil {
			if p.Deleted {
				return &tnode{URL: u, Tombstone: true, Author: u, repliesCount: -1}
			}
			n := b.localNode(p)
			n.parentFeed = p.InReplyToFeed
			return n
		}
	}
	if it, err := b.s.st.ItemByTarget(host, u); err == nil {
		n := b.itemNode(*it)
		n.parentFeed = it.InReplyToFeed
		return n
	}
	if rs, err := b.s.st.RepliesForUser(host, ""); err == nil {
		for _, rp := range rs {
			if rp.Source == u {
				n := b.receivedNode(rp, b.s.postURL(b.r, rp.PostID))
				n.parentFeed = b.own
				return n
			}
		}
	}
	if hint == "" {
		hint = b.s.discoverFeed(b.ctx, host, u)
	}
	if hint != "" {
		if p := b.fetchFeed(hint); p != nil {
			for _, it := range p.Items {
				if it.GUID == u {
					return &tnode{URL: u, Author: orStr(p.Title, hint), Title: it.Title, Date: it.Published.Format("2 Jan 2006 15:04"),
						HTML: renderRemote(it.Markdown, it.Text), parent: it.InReplyTo, parentFeed: it.InReplyToFeed, published: it.Published,
						feedHint: hint, repliesHref: it.RepliesHref, repliesCount: it.RepliesCount,
						ReplyHref: b.reply(u) + "&reply_feed=" + url.QueryEscape(hint)}
				}
			}
		}
	}
	return b.missing(u, hint)
}

// feedChild turns an item of a replies feed into a closed node.
func (b *threadBuilder) feedChild(it feed.ParsedItem) *tnode {
	name := it.FeedName
	if name == "" {
		name = it.FeedHref
	}
	return &tnode{URL: it.GUID, Author: name, Title: it.Title, Date: it.Published.Format("2 Jan 2006 15:04"),
		HTML: renderRemote("", it.Text), parent: it.InReplyTo, published: it.Published, feedHint: it.FeedHref,
		repliesHref: it.RepliesHref, repliesCount: it.RepliesCount,
		ReplyHref: b.reply(it.GUID) + "&reply_feed=" + url.QueryEscape(it.FeedHref)}
}

// childIndex reads n's approved replies and their approved replies (a replies feed holds two
// levels) and returns them grouped by the post they answer. Every key is a post whose replies are
// now completely known; n itself is always a key when the read worked. nil means it did not.
func (b *threadBuilder) childIndex(n *tnode) map[string][]*tnode {
	idx := map[string][]*tnode{n.URL: nil}
	if id, ok := b.s.localPostID(b.q.host.ID, b.base, n.URL); ok {
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

	// A replies feed covers the children of its post and of those children, so reading every
	// second ancestor (1, 3, 5, …) gives the siblings at every level of the path.
	index := map[string][]*tnode{}
	known := map[string]bool{}
	read := func(i int) {
		for k, v := range b.childIndex(chain[i]) {
			index[k], known[k] = v, true
		}
	}
	for i := 1; i < len(chain); i += 2 {
		read(i)
	}
	if last := len(chain) - 1; last%2 == 0 {
		read(last)
	}
	for i, n := range chain { // a reply that was not approved hides what is under it: ask directly
		if !known[n.URL] {
			read(i)
		}
	}

	for k := len(chain) - 1; k >= 0; k-- {
		x := chain[k]
		x.OnPath, x.expanded = true, true
		var kids []*tnode
		for _, c := range index[x.URL] {
			if k > 0 && c.URL == chain[k-1].URL {
				continue
			}
			c.Replies = -1
			if known[c.URL] {
				c.Replies = len(index[c.URL])
			} else if c.repliesCount >= 0 {
				c.Replies = c.repliesCount
			}
			if c.Replies != 0 {
				c.DrillHref = threadHref(c.URL, "")
			}
			kids = append(kids, c)
		}
		if k > 0 {
			kids = append(kids, chain[k-1])
		}
		sort.SliceStable(kids, func(i, j int) bool { return kids[i].published.Before(kids[j].published) })
		x.Children = kids
	}
	focus.Focus = true
	return chain[len(chain)-1], focus
}
