package web

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hbroek/text-thread-casting/internal/feed"
	"github.com/hbroek/text-thread-casting/internal/fetch"
	"github.com/hbroek/text-thread-casting/internal/store"
)

const maxWMAttempts = 6

// resolveThread works out a new reply's parent feed and thread root.
//
// The parent may be one of the host user's own posts, an item in a feed this instance
// has cached, an item in the feed named by feedHint, or something unknown. A parent
// that carries no tc:root is itself the root, which is also how replies to plain RSS
// items and arbitrary pages work.
func (s *Server) resolveThread(ctx context.Context, base string, u *store.User, parent, feedHint string) (parentFeed, root, rootFeed string) {
	parentFeed, root, rootFeed = feedHint, parent, feedHint
	if id, ok := localPostID(base, parent); ok {
		if p, err := s.st.Post(u.ID, id); err == nil {
			own := base + "/feed.xml"
			if p.Root != "" {
				return own, p.Root, p.RootFeed
			}
			return own, parent, own
		}
	}
	if it, err := s.st.ItemByGUID(parent); err == nil {
		return it.FeedURL, rootOr(it.Root, parent), pick(it.Root != "", it.RootFeed, it.FeedURL)
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

func rootOr(root, parent string) string { return pick(root != "", root, parent) }

func pick(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}

// localPostID recognises base+"/posts/{id}".
func localPostID(base, raw string) (int64, bool) {
	rest, ok := strings.CutPrefix(raw, base+"/posts/")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	return id, err == nil
}

// notify queues an outgoing Webmention.
func (s *Server) notify(source, target string) {
	if err := s.st.Enqueue(store.QueueOut, 0, source, target); err == nil {
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
	id, ok := localPostID(s.base(r), tu.Scheme+"://"+tu.Host+tu.Path)
	if !ok {
		http.Error(w, "target is not a post on this site", http.StatusBadRequest)
		return
	}
	if _, err := s.st.Post(q.host.ID, id); err != nil {
		http.Error(w, "target post not found", http.StatusBadRequest)
		return
	}
	if err := s.st.Enqueue(store.QueueIn, q.host.ID, source, target); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.kick()
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintln(w, "queued")
}

// ProcessQueues runs all due Webmention jobs once.
func (s *Server) ProcessQueues(ctx context.Context) {
	for _, dir := range []string{store.QueueOut, store.QueueIn} {
		items, err := s.st.DueQueue(dir, 50)
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
				note, retry = s.receiveMention(ctx, it)
			}
			if retry {
				s.st.RetryQueue(it.ID, time.Duration(1<<it.Attempts)*time.Minute, maxWMAttempts, note)
			} else {
				s.st.FinishQueue(it.ID, note)
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
func (s *Server) receiveMention(ctx context.Context, it store.QueueItem) (note string, retry bool) {
	host, err := s.st.UserByID(it.UserID)
	if err != nil {
		return "unknown user", false
	}
	tu, err := url.Parse(it.Target)
	if err != nil {
		return "bad target", false
	}
	postID, ok := localPostID(tu.Scheme+"://"+tu.Host, tu.Scheme+"://"+tu.Host+tu.Path)
	if !ok {
		return "target is not a post", false
	}
	if p, err := s.st.Post(host.ID, postID); err != nil || p.Deleted {
		return "target post is gone", false
	}

	page, err := s.web.Get(ctx, it.Source)
	if err != nil {
		if fetch.IsGone(err) { // the sender deleted their reply or reaction
			s.dropMention(postID, it.Source)
			return "source gone; removed", false
		}
		return err.Error(), true
	}
	if body := string(page.Body); !strings.Contains(body, it.Target) && !strings.Contains(body, html.EscapeString(it.Target)) {
		s.dropMention(postID, it.Source)
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
	trusted, _ := s.st.IsFollowingSite(host.ID, fr.URL, parsed.Link)
	switch {
	case item != nil && item.InReplyTo == it.Target:
		status := store.StatusPending
		if trusted {
			status = store.StatusApproved
		}
		r := store.Reply{PostID: postID, Source: it.Source, FeedURL: fr.URL, Author: parsed.Title, Title: item.Title,
			Text: item.Text, Markdown: item.Markdown, Published: item.Published, Status: status}
		if !item.Updated.IsZero() {
			r.Updated = &item.Updated
		}
		s.st.DeleteReactionIn(postID, it.Source)
		if err := s.st.UpsertReply(r); err != nil {
			return err.Error(), true
		}
		return "reply " + status, false
	case item != nil && item.ReactionOf == it.Target && (item.Polarity == store.Plus || item.Polarity == store.Minus):
		emoji := item.Emoji
		if emoji == "" || !validEmoji(emoji) {
			emoji = store.DefaultEmoji(item.Polarity)
		}
		s.st.DeleteReply(postID, it.Source)
		if err := s.st.UpsertReactionIn(store.ReactionIn{PostID: postID, Source: it.Source, FeedURL: fr.URL,
			Author: parsed.Title, Polarity: item.Polarity, Emoji: emoji, Trusted: trusted}); err != nil {
			return err.Error(), true
		}
		return "reaction recorded", false
	default:
		s.dropMention(postID, it.Source)
		return "plain mention: source is neither a reply nor a reaction to target", false
	}
}

// dropMention forgets any reply or reaction previously recorded from source.
func (s *Server) dropMention(postID int64, source string) {
	s.st.DeleteReply(postID, source)
	s.st.DeleteReactionIn(postID, source)
}
