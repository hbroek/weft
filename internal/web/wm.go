package web

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/fetch"
	"github.com/hbroek/weft/internal/store"
)

const maxWMAttempts = 6

// resolveThread works out a new reply's parent feed and thread root.
//
// The parent may be one of the host user's own posts, an item in a feed this instance
// has cached, an item in the feed named by feedHint, or something unknown. A parent
// that carries no weft:root is itself the root, which is also how replies to plain RSS
// items and arbitrary pages work.
func (s *Server) resolveThread(ctx context.Context, base string, sp *store.Space, parent, feedHint string) (parentFeed, root, rootFeed string) {
	parentFeed, root, rootFeed = feedHint, parent, feedHint
	if id, ok := s.localPostID(sp, base, parent); ok {
		if p, err := sp.Post(sp.UID(), id); err == nil {
			own := base + "/feed.xml"
			if p.Root != "" {
				return own, p.Root, p.RootFeed
			}
			return own, parent, own
		}
	}
	if it, err := sp.ItemByGUID(sp.UID(), parent); err == nil {
		return it.FeedURL, rootOr(it.Root, parent), pick(it.Root != "", it.RootFeed, it.FeedURL)
	}
	// Replies to replies often arrive without a feed (the API only knows the post's URL). Without
	// the feed there is no way to learn the parent's own weft:root, and the parent would wrongly
	// become the root of the thread, so find the feed first.
	if feedHint == "" {
		if feedHint = s.discoverFeed(ctx, sp, parent); feedHint != "" {
			parentFeed, rootFeed = feedHint, feedHint
		}
	}
	if feedHint != "" {
		if res, err := s.web.Get(ctx, feedHint); err == nil {
			if p, err := feed.Parse(res.Body, time.Now()); err == nil {
				for _, it := range p.Items {
					if it.GUID == parent {
						return feedHint, rootOr(it.Root, parent), pick(it.Root != "", it.RootFeed, feedHint)
					}
				}
			}
		}
	}
	return
}

// discoverFeed finds the feed that publishes the post at u: from a reply this user received, or
// from the discovery link on the post's page.
func (s *Server) discoverFeed(ctx context.Context, sp *store.Space, u string) string {
	if rs, err := sp.RepliesForUser(sp.UID(), ""); err == nil {
		for _, r := range rs {
			if r.Source == u && r.FeedURL != "" {
				return r.FeedURL
			}
		}
	}
	res, err := s.web.Get(ctx, u)
	if err != nil || !res.LooksLikeHTML() {
		return ""
	}
	if link, ok := fetch.FindFeedLink(res.Body, res.URL); ok {
		return link
	}
	return ""
}

func rootOr(root, parent string) string { return pick(root != "", root, parent) }

func pick(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

// localPostID recognises base+"/posts/{slug}" as one of the user's own posts. The slug is the
// post's only public address.
func (s *Server) localPostID(sp *store.Space, base, raw string) (int64, bool) {
	slug, ok := strings.CutPrefix(raw, base+"/posts/")
	if !ok || slug == "" || strings.ContainsAny(slug, "/?#") {
		return 0, false
	}
	p, err := sp.PostBySlug(sp.UID(), slug)
	if err != nil {
		return 0, false
	}
	return p.ID, true
}

// notify queues an outgoing Webmention.
func (s *Server) notify(sp *store.Space, source, target string) {
	if err := sp.Enqueue(store.QueueOut, sp.UID(), source, target); err == nil {
		s.kick()
	}
}

func (s *Server) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// webmention is the receiver endpoint. It checks the request shape, queues the job and
// answers 202; verification happens in the background.
func (s *Server) webmention(w http.ResponseWriter, r *http.Request, q *req) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	source, target := httpURL(r.FormValue("source")), httpURL(r.FormValue("target"))
	if source == "" || target == "" || source == target {
		http.Error(w, "source and target must be different http(s) URLs", http.StatusBadRequest)
		return
	}
	tu, _ := url.Parse(target)
	id, ok := s.localPostID(q.sp, s.base(r), tu.Scheme+"://"+tu.Host+tu.Path)
	if !ok {
		http.Error(w, "target is not a post on this site", http.StatusBadRequest)
		return
	}
	if _, err := q.sp.Post(q.host.ID, id); err != nil {
		http.Error(w, "target post not found", http.StatusBadRequest)
		return
	}
	if err := q.sp.Enqueue(store.QueueIn, q.host.ID, source, target); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.kick()
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintln(w, "queued")
}

// ProcessQueues runs all due Webmention jobs once, for the users the directory says have some.
func (s *Server) ProcessQueues(ctx context.Context) {
	names, err := s.st.UsersWithWork()
	if err != nil {
		return
	}
	for _, name := range names {
		if ctx.Err() != nil {
			return
		}
		s.processUser(ctx, name)
	}
}

func (s *Server) processUser(ctx context.Context, name string) {
	sp, release, err := s.st.Space(name)
	if err != nil {
		return
	}
	defer release()
	for _, dir := range []string{store.QueueOut, store.QueueIn} {
		items, err := sp.DueQueue(dir, 50)
		if err != nil {
			return
		}
		for _, it := range items {
			if ctx.Err() != nil {
				return
			}
			var note string
			var retry bool
			if dir == store.QueueOut {
				note, retry = s.sendMention(ctx, it)
			} else {
				note, retry = s.receiveMention(ctx, sp, it)
			}
			if retry {
				sp.RetryQueue(it.ID, time.Duration(1<<it.Attempts)*time.Minute, maxWMAttempts, note)
			} else {
				sp.FinishQueue(it.ID, note)
			}
		}
	}
}

// sendMention discovers the target's endpoint and notifies it. retry asks for another attempt.
func (s *Server) sendMention(ctx context.Context, it store.QueueItem) (note string, retry bool) {
	res, err := s.web.Get(ctx, it.Target)
	if err != nil {
		if fetch.IsGone(err) {
			return "target is gone", false
		}
		return err.Error(), true
	}
	endpoint, ok := res.FindRel("webmention")
	if !ok {
		return "target has no webmention endpoint", false
	}
	code, err := s.web.PostForm(ctx, endpoint, url.Values{"source": {it.Source}, "target": {it.Target}})
	switch {
	case err != nil:
		return err.Error(), true
	case code >= 200 && code < 300:
		return fmt.Sprintf("sent to %s (%d)", endpoint, code), false
	case code >= 500 || code == http.StatusTooManyRequests:
		return fmt.Sprintf("endpoint answered %d", code), true
	default:
		return fmt.Sprintf("endpoint refused: %d", code), false
	}
}

// receiveMention verifies a received Webmention and records or removes the reply.
func (s *Server) receiveMention(ctx context.Context, sp *store.Space, it store.QueueItem) (note string, retry bool) {
	host, err := sp.User()
	if err != nil {
		return "unknown user", false
	}
	tu, err := url.Parse(it.Target)
	if err != nil {
		return "bad target", false
	}
	postID, ok := s.localPostID(sp, tu.Scheme+"://"+tu.Host, tu.Scheme+"://"+tu.Host+tu.Path)
	if !ok {
		return "target is not a post", false
	}
	if p, err := sp.Post(host.ID, postID); err != nil || p.Deleted {
		return "target post is gone", false
	}

	page, err := s.web.Get(ctx, it.Source)
	if err != nil {
		if fetch.IsGone(err) { // the sender deleted their reply or like
			s.dropMention(sp, postID, it.Target, it.Source)
			return "source gone; removed", false
		}
		return err.Error(), true
	}
	if body := string(page.Body); !strings.Contains(body, it.Target) && !strings.Contains(body, html.EscapeString(it.Target)) {
		s.dropMention(sp, postID, it.Target, it.Source)
		return "source does not link to target", false
	}
	feedURL, ok := fetch.FindFeedLink(page.Body, page.URL)
	if !ok {
		return "plain mention: source advertises no feed", false
	}
	fr, err := s.web.Get(ctx, feedURL)
	if err != nil {
		return err.Error(), true
	}
	parsed, err := feed.Parse(fr.Body, time.Now())
	if err != nil {
		return err.Error(), false
	}
	var item *feed.ParsedItem
	for i := range parsed.Items {
		if parsed.Items[i].GUID == it.Source || parsed.Items[i].Link == it.Source {
			item = &parsed.Items[i]
		}
	}
	trusted, _ := sp.IsFollowingSite(host.ID, fr.URL, parsed.Link)
	if su, err := url.Parse(it.Source); err == nil && su.Scheme == tu.Scheme && strings.EqualFold(su.Host, tu.Host) {
		trusted = true // the owner answering a reply under their own post needs no one's approval, not even their own
	}
	switch {
	case item != nil && item.InReplyTo == it.Target:
		status := store.StatusPending
		if trusted {
			status = store.StatusApproved
		}
		r := store.Reply{PostID: postID, Source: it.Source, FeedURL: fr.URL, Author: parsed.Title, Title: item.Title,
			Text: item.Text, Markdown: item.Markdown, Published: item.Published, Status: status,
			Root: item.Root, RepliesHref: item.RepliesHref}
		if !item.Updated.IsZero() {
			r.Updated = &item.Updated
		}
		sp.DeleteLikeIn(postID, it.Source)
		changed, err := sp.UpsertReply(r)
		if err != nil {
			return err.Error(), true
		}
		subChanged := s.refreshSubtree(ctx, sp, postID, it.Source, item.RepliesHref)
		if cur, err := sp.ReplyBySource(postID, it.Source); err == nil && cur.Status == store.StatusApproved && (changed || subChanged) {
			s.bubble(sp, postID, it.Target)
		}
		return "reply " + status, false
	case item != nil && item.LikeOf == it.Target:
		s.dropReply(sp, postID, it.Target, it.Source)
		if err := sp.UpsertLikeIn(store.LikeIn{PostID: postID, Source: it.Source, FeedURL: orStr(httpURL(item.FeedHref), fr.URL), // the liker's own feed, so the owner can follow it
			Author: orStr(item.FeedName, parsed.Title), Trusted: trusted}); err != nil {
			return err.Error(), true
		}
		return "like recorded", false
	default:
		s.dropMention(sp, postID, it.Target, it.Source)
		return "plain mention: source is neither a reply nor a like of target", false
	}
}

// dropMention forgets any reply or like previously recorded from source.
func (s *Server) dropMention(sp *store.Space, postID int64, target, source string) {
	s.dropReply(sp, postID, target, source)
	sp.DeleteLikeIn(postID, source)
}

// dropReply removes a stored reply and, if it had been visible, tells the post's own parent.
func (s *Server) dropReply(sp *store.Space, postID int64, target, source string) {
	if prior, _ := sp.DeleteReply(postID, source); prior != nil && prior.Status == store.StatusApproved {
		s.bubble(sp, postID, target)
	}
}

// bubble is the spec's "bubble up": when the replies under a post that is itself a reply
// change, re-send that post's Webmention to its parent so the parent refreshes its cached
// subtree. It stops by itself at the root (a post that replies to nothing) and, because it
// only fires on real changes, any cycle of replies settles.
func (s *Server) bubble(sp *store.Space, postID int64, postURL string) {
	if p, err := sp.Post(sp.UID(), postID); err == nil && p.InReplyTo != "" && !p.Deleted {
		s.notify(sp, postURL, p.InReplyTo)
	}
}

// maxSubreplies bounds how much of a child's replies feed is cached.
const maxSubreplies = 50

// refreshSubtree re-reads a direct reply's own replies feed and caches the approved replies
// to it (the second level of this post's replies feed). It reports whether the cache changed.
// A feed that cannot be fetched leaves the cache as it was.
func (s *Server) refreshSubtree(ctx context.Context, sp *store.Space, postID int64, via, repliesHref string) bool {
	if repliesHref == "" {
		changed, _ := sp.ReplaceSubreplies(postID, via, nil)
		return changed
	}
	res, err := s.web.Get(ctx, repliesHref)
	if err != nil {
		return false
	}
	parsed, err := feed.Parse(res.Body, time.Now())
	if err != nil {
		return false
	}
	var rows []store.Subreply
	for _, it := range parsed.Items {
		if it.InReplyTo != via || len(rows) >= maxSubreplies {
			continue // only the direct children of via: two levels in total
		}
		author := it.FeedName
		if author == "" {
			author = it.FeedHref
		}
		rows = append(rows, store.Subreply{PostID: postID, Via: via, Parent: via, Source: it.GUID, FeedURL: it.FeedHref,
			Author: author, Title: it.Title, Text: it.Text, Published: it.Published})
	}
	changed, _ := sp.ReplaceSubreplies(postID, via, rows)
	return changed
}

// Pending counts Webmention jobs waiting to be sent or processed.
func (s *Server) Pending() int {
	names, err := s.st.UsersWithWork()
	if err != nil {
		return 0
	}
	n := 0
	for _, name := range names {
		sp, release, err := s.st.Space(name)
		if err != nil {
			continue
		}
		for _, dir := range []string{store.QueueOut, store.QueueIn} {
			jobs, _ := sp.DueQueue(dir, 1000)
			n += len(jobs)
		}
		release()
	}
	return n
}
