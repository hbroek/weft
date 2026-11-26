package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/store"
)

// ActivityPub, phase 5: Weft follows accounts on the fediverse. Following @name@server finds the actor with WebFinger,
// sends it a signed Follow, and keeps the account as a feed whose address is the actor's. Its posts reach the timeline
// two ways: the server delivers them to the inbox once it has accepted the follow, and the outbox is read on the
// timeline's own schedule, which also fills the timeline with what was posted before. A boost is the original post in
// the booster's feed, with "canonical" naming where it first appeared, which is what a repost is on Weft. Only public
// posts are read.

// apScheme is how a handle's server is reached. Tests change it.
var apScheme = "https"

const (
	maxOutboxItems = 20 // activities read from an outbox at a time
	maxBoostFetch  = 8  // boosted posts fetched at a time on one refresh: each is a request to another server
)

var handleRe = regexp.MustCompile(`^@?([A-Za-z0-9_.-]{1,64})@([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+|localhost|127\.0\.0\.1)(:[0-9]{1,5})?$`)

// fediHandle reads "@name@server" (the @ in front is optional).
func fediHandle(raw string) (name, host string, ok bool) {
	m := handleRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2] + m[3], true
}

// webfingerActor finds the actor address of name@host.
func (s *Server) webfingerActor(ctx context.Context, name, host string) (string, error) {
	u := apScheme + "://" + host + "/.well-known/webfinger?resource=" + url.QueryEscape("acct:"+name+"@"+host)
	res, err := s.web.Get(ctx, u)
	if err != nil {
		return "", errors.New("Could not look up " + name + "@" + host + ": " + err.Error())
	}
	var d struct {
		Links []struct{ Rel, Type, Href string }
	}
	if json.Unmarshal(res.Body, &d) != nil {
		return "", errors.New(host + " did not answer like a fediverse server.")
	}
	for _, l := range d.Links {
		if l.Rel == "self" && (l.Type == activityJSON || strings.Contains(l.Type, "ld+json")) && httpsOrHTTP(l.Href) {
			return l.Href, nil
		}
	}
	return "", errors.New("There is no fediverse account " + name + "@" + host + ".")
}

// actorDoc is the actor at an address, read fresh (signed as the account).
func (s *Server) actorDoc(ctx context.Context, as *store.APAccount, id string) (*remoteActor, error) {
	res, err := s.getSigned(ctx, as, id)
	if err != nil {
		return nil, err
	}
	var d remoteActor
	if json.Unmarshal(res.Body, &d) != nil || !httpsOrHTTP(d.ID) || !sameHost(d.ID, res.URL) || withoutFragment(d.ID) != withoutFragment(id) {
		return nil, errors.New("the actor's id is not the address it was fetched from")
	}
	return &d, nil
}

func idHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:8])
}

// followActor follows name@host: Follow sent, feed made, first posts read.
func (s *Server) followActor(ctx context.Context, sp *store.Space, name, host string) (string, error) {
	u, err := sp.User()
	if err != nil {
		return "", err
	}
	a, err := sp.APAccount(u.ID)
	if !s.cfg.ActivityPub || err != nil || !a.Enabled {
		return "", errors.New("Switch ActivityPub on, on your profile page, to follow people on the fediverse.")
	}
	addr, err := s.webfingerActor(ctx, name, host)
	if err != nil {
		return "", err
	}
	who, err := s.fetchActor(ctx, a, withoutFragment(addr), "")
	if err != nil {
		return "", errors.New("Could not read that account: " + err.Error())
	}
	if isUs(who.ID, a) {
		return "", errors.New("That is you.")
	}
	if _, err := sp.APFollowingGet(u.ID, who.ID); err == nil {
		return who.ID, nil // already following
	}
	if err := s.st.APFeedMark(who.ID); err != nil {
		return "", err
	}
	s.readActor(ctx, a, who.ID) // the posts so far; a server that will not show them yet is not a reason to refuse
	if err := sp.Follow(u.ID, who.ID); err != nil {
		return "", err
	}
	followID := a.Actor + "#follows/" + idHash(who.ID, fmt.Sprint(time.Now().UnixNano()))
	if err := sp.APFollowingAdd(u.ID, store.APFollowing{Actor: who.ID, FollowID: followID, Inbox: who.Inbox}); err != nil {
		return "", err
	}
	err = s.deliverTo(sp, u.ID, followID, "Follow", who.Inbox, map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams", "id": followID, "type": "Follow", "actor": a.Actor, "object": who.ID,
	})
	if err != nil {
		return "", err
	}
	s.retrustLikes(sp)
	return who.ID, nil
}

// unfollowActor takes back the Follow of an account on the fediverse, if the feed is one the user followed that way.
func (s *Server) unfollowActor(sp *store.Space, actor string) {
	u, err := sp.User()
	if err != nil {
		return
	}
	f, err := sp.APFollowingGet(u.ID, actor)
	if err != nil {
		return
	}
	sp.APFollowingRemove(u.ID, actor)
	a, err := sp.APAccount(u.ID)
	if err != nil || !a.Enabled {
		return
	}
	id := a.Actor + "#undo-follows/" + idHash(f.FollowID)
	s.deliverTo(sp, u.ID, id, "Undo", f.Inbox, map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams", "id": id, "type": "Undo", "actor": a.Actor,
		"object": map[string]any{"id": f.FollowID, "type": "Follow", "actor": a.Actor, "object": actor},
	})
}

// refreshActor is Refresh for the feed of an account on the fediverse: its outbox is read, signed as one of the users
// who follow it.
func (s *Server) refreshActor(ctx context.Context, feedURL string) error {
	names, _ := s.st.FollowersOf(feedURL)
	for _, n := range names {
		sp, release, err := s.st.Space(n)
		if err != nil {
			continue
		}
		u, uerr := sp.User()
		var a *store.APAccount
		if uerr == nil {
			a, err = sp.APAccount(u.ID)
		}
		release()
		if uerr == nil && err == nil && a.Enabled {
			return s.readActor(ctx, a, feedURL)
		}
	}
	return errors.New("no account that follows it has ActivityPub on")
}

// readActor reads the actor and its newest posts into the feed.
func (s *Server) readActor(ctx context.Context, a *store.APAccount, actorID string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	d, err := s.actorDoc(ctx, a, actorID)
	if err != nil {
		return err
	}
	title := orStr(d.Name, d.PreferredUsername)
	link := idOf(d.URL)
	if !httpsOrHTTP(link) {
		link = d.ID
	}
	var icon struct{ URL string }
	json.Unmarshal(d.Icon, &icon)
	if !httpsOrHTTP(icon.URL) {
		icon.URL = ""
	}
	var items []feed.ParsedItem
	if httpsOrHTTP(d.Outbox) && sameHost(d.Outbox, d.ID) {
		items = s.readOutbox(ctx, a, d, actorID)
	}
	return s.st.SaveRemoteItems(actorID, title, link, icon.URL, items)
}

// readOutbox returns the newest public posts and boosts of an actor, newest first.
func (s *Server) readOutbox(ctx context.Context, a *store.APAccount, d *remoteActor, feedURL string) []feed.ParsedItem {
	var page struct {
		First        json.RawMessage   `json:"first"`
		OrderedItems []json.RawMessage `json:"orderedItems"`
	}
	get := func(addr string) bool {
		res, err := s.getSigned(ctx, a, addr)
		page.First, page.OrderedItems = nil, nil
		return err == nil && json.Unmarshal(res.Body, &page) == nil
	}
	if !get(d.Outbox) {
		return nil
	}
	if len(page.OrderedItems) == 0 && len(page.First) > 0 {
		var inline struct {
			OrderedItems []json.RawMessage `json:"orderedItems"`
		}
		if json.Unmarshal(page.First, &inline) == nil && len(inline.OrderedItems) > 0 {
			page.OrderedItems = inline.OrderedItems
		} else if first := idOf(page.First); httpsOrHTTP(first) && sameHost(first, d.ID) && !get(first) {
			return nil
		}
	}
	var out []feed.ParsedItem
	fetched := 0
	for i, raw := range page.OrderedItems {
		if i >= maxOutboxItems {
			break
		}
		var act struct {
			Type      string          `json:"type"`
			Actor     json.RawMessage `json:"actor"`
			Published string          `json:"published"`
			Object    json.RawMessage `json:"object"`
			To        json.RawMessage `json:"to"`
			CC        json.RawMessage `json:"cc"`
		}
		if json.Unmarshal(raw, &act) != nil || (idOf(act.Actor) != "" && idOf(act.Actor) != d.ID) {
			continue
		}
		switch act.Type {
		case "Create":
			var n noteIn
			if json.Unmarshal(act.Object, &n) == nil {
				if it, ok := s.itemOf(ctx, a, &n, d.ID, ""); ok {
					out = append(out, it)
				}
			}
		case "Announce":
			if !isPublic(act.To, act.CC) {
				continue
			}
			id := idOf(act.Object)
			if !httpsOrHTTP(id) || s.st.HasItem(feedURL, id) || fetched >= maxBoostFetch {
				continue
			}
			fetched++
			if it, ok := s.boostOf(ctx, a, id, d.ID, parseTime(act.Published, time.Now())); ok {
				out = append(out, it)
			}
		}
	}
	return out
}

// fetchNote reads one post at an address, as the server serves it.
func (s *Server) fetchNote(ctx context.Context, a *store.APAccount, id string) (*noteIn, error) {
	res, err := s.getSigned(ctx, a, id)
	if err != nil {
		return nil, err
	}
	var n noteIn
	if json.Unmarshal(res.Body, &n) != nil || !httpsOrHTTP(n.ID) || !sameHost(n.ID, res.URL) ||
		(withoutFragment(n.ID) != withoutFragment(id) && withoutFragment(n.ID) != withoutFragment(res.URL)) {
		return nil, errors.New("the post's id is not the address it was fetched from")
	}
	if au := idOf(n.AttributedTo); !httpsOrHTTP(au) || !sameHost(au, n.ID) { // the author is on the server that has the post
		return nil, errors.New("the post's author is not on its server")
	}
	return &n, nil
}

// boostOf is the item for a post boosted at time when: the original post, in the booster's feed.
func (s *Server) boostOf(ctx context.Context, a *store.APAccount, id, booster string, when time.Time) (feed.ParsedItem, bool) {
	n, err := s.fetchNote(ctx, a, id)
	if err != nil {
		return feed.ParsedItem{}, false
	}
	it, ok := s.itemOf(ctx, a, n, idOf(n.AttributedTo), booster)
	if !ok {
		return it, false
	}
	it.Canonical = it.Link // a boost is a repost: it first appeared where the author posted it
	it.Published, it.Updated = when, time.Time{}
	by := idOf(n.AttributedTo)
	who := hostPart(by)
	if au, err := s.cachedActor(ctx, a, by); err == nil {
		who = "@" + orStr(au.Username, "?") + "@" + hostPart(au.ID)
	}
	line := "Boosted from [" + mdSpecial.Replace(who) + "](" + mdDest(by) + ")\n\n"
	it.Markdown = line + it.Markdown
	it.Text = line + it.Text
	return it, true
}

// itemOf makes a timeline item of a post. author is the actor whose post it must be (the post says so itself and
// agrees, or it is not shown); booster is set for a boosted post.
func (s *Server) itemOf(ctx context.Context, a *store.APAccount, n *noteIn, author, booster string) (feed.ParsedItem, bool) {
	var it feed.ParsedItem
	if (n.Type != "Note" && n.Type != "Article" && n.Type != "Page") || !httpsOrHTTP(n.ID) || !isPublic(n.To, n.CC) {
		return it, false
	}
	if idOf(n.AttributedTo) != author || !sameHost(n.ID, author) {
		return it, false
	}
	link := idOf(n.URL)
	if !httpsOrHTTP(link) {
		link = n.ID
	}
	md := htmlToMarkdown(n.Content)
	if sum := strings.TrimSpace(n.Summary); sum != "" { // a content warning
		md = "Content warning: " + mdSpecial.Replace(oneLineOf(sum)) + "\n\n" + md
	}
	for _, at := range attachmentsOf(n.Attachment) {
		md = strings.TrimRight(md, "\n") + "\n\n![" + mdSpecial.Replace(oneLineOf(at.Name)) + "](" + mdDest(at.URL) + ")\n"
	}
	if r := []rune(md); len(r) > maxReplyRunes {
		md = string(r[:maxReplyRunes])
	}
	if strings.TrimSpace(md) == "" {
		return it, false
	}
	it = feed.ParsedItem{
		GUID: n.ID, UID: n.ID, Link: link, Title: oneLineOf(n.Name), Text: md, Markdown: md,
		Published: parseTime(n.Published, time.Now()), RepliesCount: -1, Keywords: hashtagsOf(n.Tag),
	}
	if n.Updated != "" {
		it.Updated = parseTime(n.Updated, time.Time{})
	}
	if r := idOf(n.InReplyTo); httpsOrHTTP(r) {
		it.InReplyTo = r
	}
	return it, true
}

func oneLineOf(s string) string { return strings.Join(strings.Fields(s), " ") }

type attachment struct{ URL, Name string }

// attachmentsOf lists the images of a post (at most the first four).
func attachmentsOf(raw json.RawMessage) []attachment {
	var list []struct {
		Type      string `json:"type"`
		MediaType string `json:"mediaType"`
		URL       string `json:"url"`
		Name      string `json:"name"`
	}
	if json.Unmarshal(raw, &list) != nil {
		var one struct {
			Type, MediaType, URL, Name string
		}
		if json.Unmarshal(raw, &one) != nil {
			return nil
		}
		list = append(list, struct {
			Type      string `json:"type"`
			MediaType string `json:"mediaType"`
			URL       string `json:"url"`
			Name      string `json:"name"`
		}{one.Type, one.MediaType, one.URL, one.Name})
	}
	var out []attachment
	for _, a := range list {
		if strings.HasPrefix(a.MediaType, "image/") && httpsOrHTTP(a.URL) && len(out) < maxAttachments {
			out = append(out, attachment{a.URL, a.Name})
		}
	}
	return out
}

// hashtagsOf lists the hashtags of a post, without the #.
func hashtagsOf(raw json.RawMessage) []string {
	var tags []struct{ Type, Name string }
	if json.Unmarshal(raw, &tags) != nil {
		return nil
	}
	var out []string
	for _, t := range tags {
		if t.Type == "Hashtag" {
			if k := strings.TrimPrefix(strings.TrimSpace(t.Name), "#"); k != "" && len(out) < maxHashtags {
				out = append(out, k)
			}
		}
	}
	return out
}

// mdDest is an address that can stand in the parentheses of a Markdown link or image, or "" if it is not an http(s) one.
func mdDest(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return strings.NewReplacer("(", "%28", ")", "%29", `"`, "%22", "'", "%27", "`", "%60", `\`, "%5C", "<", "%3C", ">", "%3E", " ", "%20").Replace(u.String())
}

// apFeedPush takes what an account that the user follows has sent: a post, or the boost of one, for the timeline.
func (s *Server) apFeedPush(q *req, a *store.APAccount, who *store.APActor, act activity) {
	if _, err := q.sp.APFollowingGet(q.host.ID, who.ID); err != nil || !s.st.IsAPFeed(who.ID) {
		return
	}
	switch act.Type {
	case "Create", "Update":
		var n noteIn
		if json.Unmarshal(act.Object, &n) != nil {
			return
		}
		if it, ok := s.itemOf(context.Background(), a, &n, who.ID, ""); ok {
			s.st.SaveRemoteItems(who.ID, "", "", "", []feed.ParsedItem{it})
		}
	case "Announce":
		id := idOf(act.Object)
		if !httpsOrHTTP(id) || sameHost(id, a.Actor) || !isPublic(act.To, act.CC) { // ours go to apInteraction
			return
		}
		s.apWG.Add(1)
		go func() {
			defer s.apWG.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if it, ok := s.boostOf(ctx, a, id, who.ID, time.Now()); ok {
				s.st.SaveRemoteItems(who.ID, "", "", "", []feed.ParsedItem{it})
			}
		}()
	}
}
