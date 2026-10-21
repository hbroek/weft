package web

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/fetch"
	"github.com/hbroek/weft/internal/store"
)

// Limits that keep a thread view from turning into a crawl of the web.
const (
	maxThreadNodes   = 300
	maxThreadFetches = 24
	maxProbes        = 8
	maxCrawlDepth    = 6
	threadTimeout    = 10 * time.Second
)

// AuthorHref links the author's name to their site.
func (n *tnode) AuthorHref() string { return siteOf(n.feedHint) }

type tnode struct {
	URL, Author, Title, Date, Note, ReplyHref string
	HTML                                      template.HTML
	Local, Tombstone, Missing, Unapproved     bool
	// OnPath marks the chain from the root to the focused post; Focus marks the post itself.
	OnPath, Focus bool
	// Replies is how many approved replies a closed node has (-1 when not known), and DrillHref
	// opens the thread focused on it.
	Replies   int
	Other     bool // Replies counts only the replies not already shown (a post on the path)
	DrillHref string
	Children  []*tnode
	Parent    *parentCard // for a post the reader cannot read: what a reply says about it

	pinfo                                     feed.ParentInfo // what this post says about the page it replies to
	md                                        string          // the post as written, for the Markdown form of the page
	parent, parentFeed, feedHint, repliesHref string
	repliesCount                              int // -1 unknown
	published                                 time.Time
	expanded                                  bool
}

// threadView names the view a request asks for: "focus" (the default), "curated" or "raw".
func threadView(v string) string {
	switch v {
	case "curated", "raw":
		return v
	}
	return "focus"
}

type threadPage struct {
	Root        *tnode
	View        string // "focus", "curated" (the whole approved tree) or "raw"
	RootURL     string
	FocusHref   string
	CuratedHref string
	RawHref     string
	Count       int
}

func threadHref(target, view string) string {
	h := "/thread?url=" + url.QueryEscape(target)
	if view != "" {
		return h + "&view=" + view
	}
	return h + "#focus" // the focused view opens scrolled to the post
}

type threadBuilder struct {
	s       *Server
	r       *http.Request
	q       *req
	ctx     context.Context
	base    string
	own     string // own feed URL
	feeds   map[string]*feed.Parsed
	fetches int
	probes  int
}

func (s *Server) thread(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	target := httpURL(r.URL.Query().Get("url"))
	if target == "" {
		http.Error(w, "url parameter required", http.StatusBadRequest)
		return
	}
	view := threadView(r.URL.Query().Get("view"))
	tree, root := s.buildThread(r, q, target, view)
	d := s.data(r, q, "Thread")
	// all three views keep the post you came from as the target (each finds the root itself), so
	// "This branch" always comes back to the same branch
	d.Thread = &threadPage{Root: tree, View: view, RootURL: root, FocusHref: threadHref(target, ""), CuratedHref: threadHref(target, "curated"),
		RawHref: threadHref(target, "raw"), Count: countNodes(tree)}
	s.show(w, "thread", http.StatusOK, d)
}

// buildThread resolves the thread that target belongs to and returns its tree and root URL.
func (s *Server) buildThread(r *http.Request, q *req, target, view string) (*tnode, string) {
	ctx, cancel := context.WithTimeout(r.Context(), threadTimeout)
	defer cancel()
	b := &threadBuilder{s: s, r: r, q: q, ctx: ctx, base: s.base(r), own: s.base(r) + "/feed.xml", feeds: map[string]*feed.Parsed{}}
	if view == "focus" {
		root, _ := b.focus(target)
		attachParentCards(root)
		return root, root.URL
	}
	root, rootFeed := b.resolveRoot(target)
	var tree *tnode
	if view == "raw" {
		tree = b.raw(root, rootFeed)
	} else {
		tree = b.curated(root, rootFeed)
	}
	attachParentCards(tree)
	return tree, root
}

// attachParentCards gives each post the reader cannot read (an ordinary web page, say) the card its
// first reply with a snapshot says about it.
func attachParentCards(n *tnode) {
	for _, c := range n.Children {
		attachParentCards(c)
		if n.Missing && n.Parent == nil && !c.pinfo.Empty() {
			n.Parent = newParentCard(n.URL, c.pinfo)
		}
	}
}

func countNodes(n *tnode) int {
	c := 1
	for _, ch := range n.Children {
		c += countNodes(ch)
	}
	return c
}

// resolveRoot finds the thread a post belongs to: a post that carries no weft:root is its own root.
func (b *threadBuilder) resolveRoot(target string) (root, rootFeed string) {
	if id, ok := b.s.localPostID(b.q.host.ID, b.base, target); ok {
		if p, err := b.s.st.Post(b.q.host.ID, id); err == nil && p.Root != "" {
			return p.Root, p.RootFeed
		}
		return target, b.own
	}
	if it, err := b.s.st.ItemByGUID(target); err == nil && it.Root != "" {
		return it.Root, it.RootFeed
	}
	if rs, err := b.s.st.RepliesForUser(b.q.host.ID, ""); err == nil {
		for _, rp := range rs {
			if rp.Source == target && rp.Root != "" {
				return rp.Root, ""
			}
		}
	}
	return target, ""
}

// ---- nodes from the different places a post can be known from ----

func (b *threadBuilder) reply(u string) string {
	return "/compose?reply_to=" + url.QueryEscape(u)
}

func (b *threadBuilder) localNode(p *store.Post) *tnode {
	u := b.s.postURL(b.r, p.ID)
	return &tnode{URL: u, Author: b.q.host.Display, Title: p.Title, Date: p.Created.Format("2 Jan 2006 15:04"), Local: true,
		HTML: template.HTML(p.HTML), md: p.Markdown, parent: p.InReplyTo, published: p.Created, feedHint: b.own, repliesHref: u + "/replies.xml",
		repliesCount: -1, pinfo: p.Parent, ReplyHref: b.reply(u) + "&reply_feed=" + url.QueryEscape(b.own)}
}

func (b *threadBuilder) itemNode(it store.TimelineItem) *tnode {
	u := itemTarget(it)
	return &tnode{URL: u, Author: orStr(it.FeedTitle, it.FeedURL), Title: it.Title, Date: it.Published.Format("2 Jan 2006 15:04"),
		HTML: renderRemote(it.Markdown, it.Text), md: orStr(it.Markdown, it.Text), parent: it.InReplyTo, published: it.Published, feedHint: it.FeedURL,
		repliesHref: it.RepliesHref, repliesCount: -1, pinfo: it.Parent, ReplyHref: b.reply(u) + "&reply_feed=" + url.QueryEscape(it.FeedURL)}
}

func (b *threadBuilder) receivedNode(rp store.Reply, parent string) *tnode {
	n := &tnode{URL: rp.Source, Author: orStr(rp.Author, rp.FeedURL), Title: rp.Title, Date: rp.Published.Format("2 Jan 2006 15:04"),
		HTML: renderRemote(rp.Markdown, rp.Text), md: orStr(rp.Markdown, rp.Text), parent: parent, published: rp.Published, feedHint: rp.FeedURL,
		repliesHref: rp.RepliesHref, repliesCount: -1, ReplyHref: b.reply(rp.Source) + "&reply_feed=" + url.QueryEscape(rp.FeedURL)}
	if rp.Status != store.StatusApproved {
		n.Unapproved = true
		n.Note = "not approved by the author of the post it replies to (" + rp.Status + ")"
	}
	return n
}

func (b *threadBuilder) subNode(sr store.Subreply) *tnode {
	return &tnode{URL: sr.Source, Author: orStr(sr.Author, sr.FeedURL), Title: sr.Title, Date: sr.Published.Format("2 Jan 2006 15:04"),
		HTML: renderRemote("", sr.Text), md: sr.Text, parent: sr.Parent, published: sr.Published, feedHint: sr.FeedURL, repliesCount: -1,
		ReplyHref: b.reply(sr.Source) + "&reply_feed=" + url.QueryEscape(sr.FeedURL)}
}

// ---- raw view: everything this reader knows ----

func (b *threadBuilder) raw(root, rootFeed string) *tnode {
	nodes := map[string]*tnode{}
	var order []*tnode
	add := func(n *tnode) {
		if _, dup := nodes[n.URL]; !dup && len(nodes) < maxThreadNodes {
			nodes[n.URL], order = n, append(order, n)
		}
	}
	host := b.q.host.ID
	var localIDs []int64
	if id, ok := b.s.localPostID(b.q.host.ID, b.base, root); ok {
		if p, err := b.s.st.Post(host, id); err == nil && !p.Deleted {
			add(b.localNode(p))
			localIDs = append(localIDs, p.ID)
		}
	}
	if ps, err := b.s.st.PostsInThread(host, root); err == nil {
		for _, p := range ps {
			add(b.localNode(p))
			localIDs = append(localIDs, p.ID)
		}
	}
	if items, err := b.s.st.ItemsInThread(host, root); err == nil {
		for _, it := range items {
			if itemTarget(it) != "" {
				add(b.itemNode(it))
			}
		}
	}
	thread := map[int64]bool{}
	for _, id := range localIDs {
		thread[id] = true
	}
	if rs, err := b.s.st.RepliesForUser(host, ""); err == nil {
		sort.Slice(rs, func(i, j int) bool { return rs[i].Published.Before(rs[j].Published) })
		for _, rp := range rs {
			if thread[rp.PostID] {
				add(b.receivedNode(rp, b.s.postURL(b.r, rp.PostID)))
			}
		}
	}
	for _, id := range localIDs {
		if subs, err := b.s.st.SubrepliesFor(id); err == nil {
			for _, sr := range subs {
				add(b.subNode(sr))
			}
		}
	}

	rootNode := nodes[root]
	if rootNode == nil {
		rootNode = b.missing(root, rootFeed)
		nodes[root] = rootNode
	}
	orphans := map[string][]*tnode{}
	for _, n := range order {
		if n == rootNode {
			continue
		}
		if p := nodes[n.parent]; p != nil && n.parent != n.URL {
			p.Children = append(p.Children, n)
		} else {
			orphans[n.parent] = append(orphans[n.parent], n)
		}
	}
	b.placeOrphans(rootNode, nodes, orphans)
	b.rescueUnreachable(rootNode, order)
	sortTree(rootNode)
	return rootNode
}

func (b *threadBuilder) missing(u, feedHint string) *tnode {
	n := &tnode{URL: u, Missing: true, Author: u, Note: "This post is not known to your reader.", feedHint: feedHint, repliesCount: -1}
	if b.probe(u) == probeGone {
		n.Tombstone, n.Missing, n.Note = true, false, ""
	}
	return n
}

type probeResult int

const (
	probeOK probeResult = iota
	probeGone
	probeDown
)

// probe asks whether a post that nobody here has still exists.
func (b *threadBuilder) probe(u string) probeResult {
	if b.probes >= maxProbes {
		return probeDown
	}
	b.probes++
	_, err := b.s.web.Get(b.ctx, u)
	var se *fetch.StatusError
	switch {
	case err == nil:
		return probeOK
	case errors.As(err, &se) && se.Code == http.StatusGone:
		return probeGone
	default:
		return probeDown
	}
}

// placeOrphans attaches replies whose parent is not known here, following the spec's table:
// a deleted parent leaves a tombstone, an offline one leaves the reply under the root with
// its parent's URL as context, and a live one that this reader has not seen gets a placeholder.
func (b *threadBuilder) placeOrphans(root *tnode, nodes map[string]*tnode, orphans map[string][]*tnode) {
	parents := make([]string, 0, len(orphans))
	for p := range orphans {
		parents = append(parents, p)
	}
	sort.Strings(parents)
	for _, p := range parents {
		kids := orphans[p]
		if p == "" { // a post that replies to nothing, yet claims this root: keep it under the root
			for _, k := range kids {
				k.Note = joinNote(k.Note, "no parent given")
			}
			root.Children = append(root.Children, kids...)
			continue
		}
		switch b.probe(p) {
		case probeGone:
			ts := &tnode{URL: p, Tombstone: true, Author: p, repliesCount: -1}
			ts.Children = kids
			root.Children = append(root.Children, ts)
		case probeOK:
			ph := &tnode{URL: p, Missing: true, Author: p, Note: "This post exists but is not known to your reader.", repliesCount: -1,
				ReplyHref: b.reply(p)}
			ph.Children = kids
			root.Children = append(root.Children, ph)
		default:
			for _, k := range kids {
				k.Note = joinNote(k.Note, "parent unavailable: "+p)
			}
			root.Children = append(root.Children, kids...)
		}
	}
}

// rescueUnreachable keeps replies that point at each other in a cycle from vanishing.
func (b *threadBuilder) rescueUnreachable(root *tnode, all []*tnode) {
	seen := map[*tnode]bool{}
	var walk func(*tnode)
	walk = func(n *tnode) {
		if seen[n] {
			return
		}
		seen[n] = true
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	for _, n := range all {
		if !seen[n] && n != root {
			n.Note = joinNote(n.Note, "part of a reply loop")
			n.Children = nil
			root.Children = append(root.Children, n)
			seen[n] = true
		}
	}
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

func sortTree(n *tnode) {
	sort.SliceStable(n.Children, func(i, j int) bool { return n.Children[i].published.Before(n.Children[j].published) })
	for _, c := range n.Children {
		sortTree(c)
	}
}

// ---- curated view: only what each author approved, read downward from the root ----

func (b *threadBuilder) curated(root, rootFeed string) *tnode {
	var rn *tnode
	if id, ok := b.s.localPostID(b.q.host.ID, b.base, root); ok {
		if p, err := b.s.st.Post(b.q.host.ID, id); err == nil && !p.Deleted {
			rn = b.localNode(p)
		}
	}
	if rn == nil {
		if items, err := b.s.st.ItemsInThread(b.q.host.ID, root); err == nil {
			for _, it := range items {
				if itemTarget(it) == root {
					rn = b.itemNode(it)
				}
			}
		}
	}
	if rn == nil {
		rn = b.missingCurated(root, rootFeed)
	}
	nodes := map[string]*tnode{root: rn}
	frontier := []*tnode{rn}
	for depth := 0; depth < maxCrawlDepth && len(frontier) > 0 && b.ctx.Err() == nil; depth++ {
		var next []*tnode
		for _, n := range frontier {
			if len(nodes) >= maxThreadNodes {
				break
			}
			for _, c := range b.childrenOf(n) {
				if _, dup := nodes[c.URL]; dup {
					continue
				}
				// a reply feed may report grandchildren too: place each under its stated parent
				p := nodes[c.parent]
				if p == nil {
					p = n
				}
				p.Children = append(p.Children, c)
				nodes[c.URL] = c
				next = append(next, c)
			}
			n.expanded = true
		}
		frontier = next
	}
	sortTree(rn)
	return rn
}

func (b *threadBuilder) missingCurated(root, rootFeed string) *tnode {
	return &tnode{URL: root, Missing: true, Author: root, Note: "This post is not known to your reader.", feedHint: rootFeed, repliesCount: -1}
}

// childrenOf returns the approved replies to n, from the database for own posts and from the
// replies feed (found through the post's own feed) for everyone else.
func (b *threadBuilder) childrenOf(n *tnode) []*tnode {
	if n.expanded || n.Tombstone {
		return nil
	}
	if id, ok := b.s.localPostID(b.q.host.ID, b.base, n.URL); ok {
		return b.localChildren(id)
	}
	href := n.repliesHref
	if href == "" && n.repliesCount != 0 {
		href = b.findRepliesHref(n)
	}
	if href == "" && n.repliesCount != 0 {
		// a post that publishes no replies feed (a plain page or RSS item) cannot approve replies, so
		// the replies this reader knows of stand
		return b.knownReplies(n.URL)
	}
	if href == "" || n.repliesCount == 0 {
		return nil
	}
	p := b.fetchFeed(href)
	if p == nil {
		return nil
	}
	var out []*tnode
	for _, it := range p.Items {
		if it.InReplyTo == "" || it.GUID == "" {
			continue
		}
		out = append(out, b.feedChild(it))
	}
	return out
}

// knownReplies are the direct replies to u that the reader knows of: the host's own posts and posts
// in the feeds the host follows.
func (b *threadBuilder) knownReplies(u string) []*tnode {
	var out []*tnode
	host := b.q.host.ID
	if ps, err := b.s.st.PostsInThread(host, u); err == nil {
		for _, p := range ps {
			if p.InReplyTo == u && !p.Deleted {
				out = append(out, b.localNode(p))
			}
		}
	}
	if items, err := b.s.st.ItemsInThread(host, u); err == nil {
		for _, it := range items {
			if it.InReplyTo == u && itemTarget(it) != "" {
				out = append(out, b.itemNode(it))
			}
		}
	}
	return out
}

// localChildren reads one of the host's own posts' approved replies, and the second level cached under them.
func (b *threadBuilder) localChildren(postID int64) []*tnode {
	var out []*tnode
	purl := b.s.postURL(b.r, postID)
	approved := map[string]bool{}
	if rs, err := b.s.st.RepliesFor(postID, store.StatusApproved); err == nil {
		for _, rp := range rs {
			approved[rp.Source] = true
			n := b.receivedNode(rp, purl)
			out = append(out, n)
		}
	}
	if subs, err := b.s.st.SubrepliesFor(postID); err == nil {
		for _, sr := range subs {
			if approved[sr.Via] {
				out = append(out, b.subNode(sr))
			}
		}
	}
	return out
}

// findRepliesHref reads a post's own feed to learn where its replies are published.
func (b *threadBuilder) findRepliesHref(n *tnode) string {
	if n.feedHint == "" {
		return ""
	}
	p := b.fetchFeed(n.feedHint)
	if p == nil {
		return ""
	}
	for _, it := range p.Items {
		if it.GUID == n.URL {
			n.repliesCount = it.RepliesCount
			return it.RepliesHref
		}
	}
	return ""
}

// fetchFeed fetches and parses a feed at most once per request, within the fetch budget.
func (b *threadBuilder) fetchFeed(u string) *feed.Parsed {
	if p, ok := b.feeds[u]; ok {
		return p
	}
	if b.fetches >= maxThreadFetches || b.ctx.Err() != nil {
		return nil
	}
	b.fetches++
	var parsed *feed.Parsed
	if res, err := b.s.web.Get(b.ctx, u); err == nil {
		if p, err := feed.Parse(res.Body, time.Now()); err == nil {
			parsed = p
		}
	}
	b.feeds[u] = parsed
	return parsed
}
