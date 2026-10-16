package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/hbroek/text-thread-casting/internal/feed"
	"github.com/hbroek/text-thread-casting/internal/store"
)

// validEmoji accepts a short run of symbol-like runes. Letters, digits, spaces and
// control characters are refused so the field cannot carry text or markup.
func validEmoji(e string) bool {
	n := 0
	for _, r := range e {
		n++
		if n > 8 || r < 0x80 || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return n > 0
}

func (s *Server) reactionURL(r *http.Request, id int64) string {
	return fmt.Sprintf("%s/reactions/%d", s.base(r), id)
}

// setReaction publishes (or changes) the user's reaction to target and notifies the target's site.
func (s *Server) setReaction(r *http.Request, u *store.User, target, feedHint, polarity, emoji string) (*store.Reaction, string) {
	target, feedHint = httpURL(target), httpURL(feedHint)
	emoji = strings.TrimSpace(emoji)
	switch {
	case target == "":
		return nil, "target must be an http(s) URL"
	case polarity != store.Plus && polarity != store.Minus:
		return nil, `polarity must be "+" or "-"`
	case emoji != "" && !validEmoji(emoji):
		return nil, "emoji must be a short run of emoji characters"
	}
	x, err := s.st.SetReaction(u.ID, target, feedHint, polarity, emoji)
	if err != nil {
		return nil, err.Error()
	}
	s.notify(s.reactionURL(r, x.ID), target)
	return x, ""
}

func (s *Server) removeReaction(r *http.Request, u *store.User, target string) error {
	x, err := s.st.RemoveReaction(u.ID, httpURL(target))
	if err != nil {
		return err
	}
	s.notify(s.reactionURL(r, x.ID), x.Target)
	return nil
}

func (s *Server) reactionsFeed(w http.ResponseWriter, r *http.Request, q *req) {
	xs, err := s.st.ActiveReactions(q.host.ID, 500)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	ch := feed.Channel{
		Title: q.host.Display + " reactions", Link: s.base(r) + "/", Description: "Reactions published by " + q.host.Display,
		Links: []feed.AtomLink{{Href: s.base(r) + "/reactions.xml", Rel: "self", Type: "application/rss+xml"}},
	}
	var last time.Time
	for _, x := range xs {
		u := s.reactionURL(r, x.ID)
		it := feed.Item{GUID: feed.GUID{IsPermaLink: true, Value: u}, Link: u, PubDate: feed.RFC822(x.Created),
			ReactionOf: &feed.ReactionRef{Href: x.Target, Polarity: x.Polarity, Emoji: x.Emoji}}
		if x.Created.After(last) {
			last = x.Created
		}
		if x.Updated != nil {
			it.Updated = feed.RFC822(*x.Updated)
			if x.Updated.After(last) {
				last = *x.Updated
			}
		}
		ch.Items = append(ch.Items, it)
	}
	if last.IsZero() {
		last = time.Now()
	}
	ch.LastBuildDate = feed.RFC822(last)
	b, err := feed.New(ch).Marshal()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write(b)
}

// reactionPage is the HTML permalink of a reaction. Webmention receivers check that it links
// to the target, and its feed link leads them to the reactions feed.
func (s *Server) reactionPage(w http.ResponseWriter, r *http.Request, q *req) {
	id, ok := postID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	x, err := s.st.Reaction(q.host.ID, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	d := s.data(r, q, q.host.Display+" reacted")
	d.FeedURL = s.base(r) + "/reactions.xml"
	if x.Deleted {
		s.show(w, "tombstone", http.StatusGone, d)
		return
	}
	d.React = &reactData{Author: q.host.Display, Target: x.Target, Polarity: x.Polarity, Emoji: orStr(x.Emoji, store.DefaultEmoji(x.Polarity))}
	s.show(w, "reaction", http.StatusOK, d)
}

type reactData struct{ Author, Target, Polarity, Emoji string }

func (s *Server) reactForm(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	if _, msg := s.setReaction(r, q.host, r.FormValue("target"), r.FormValue("feed"), r.FormValue("polarity"), r.FormValue("emoji")); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/timeline", http.StatusSeeOther)
}

func (s *Server) unreactForm(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.requireOwner(w, r, q) {
		return
	}
	if err := s.removeReaction(r, q.host, r.FormValue("target")); err != nil && err != store.ErrNotFound {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/timeline", http.StatusSeeOther)
}

type reactReq struct {
	Target     string `json:"target"`
	TargetFeed string `json:"target_feed"`
	Polarity   string `json:"polarity"`
	Emoji      string `json:"emoji"`
}

func (s *Server) apiReact(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	var in reactReq
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	x, msg := s.setReaction(r, q.host, in.Target, in.TargetFeed, in.Polarity, in.Emoji)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, postResp{ID: x.ID, URL: s.reactionURL(r, x.ID)})
}

func (s *Server) apiUnreact(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	var in reactReq
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in) != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	switch err := s.removeReaction(r, q.host, in.Target); err {
	case nil:
		w.WriteHeader(http.StatusNoContent)
	case store.ErrNotFound:
		http.NotFound(w, r)
	default:
		http.Error(w, err.Error(), 500)
	}
}

type receivedJSON struct {
	Source   string `json:"source"`
	Author   string `json:"author"`
	Polarity string `json:"polarity"`
	Emoji    string `json:"emoji"`
	Trusted  bool   `json:"trusted"`
}

// apiReactionsReceived lists every verified reaction to one of the user's posts, with a flag
// for senders the user follows (only those count in the public tally).
func (s *Server) apiReactionsReceived(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.apiAuth(w, r, q) {
		return
	}
	var id int64
	if _, err := fmt.Sscan(r.URL.Query().Get("post_id"), &id); err != nil {
		http.Error(w, "post_id required", http.StatusBadRequest)
		return
	}
	if _, err := s.st.Post(q.host.ID, id); err != nil {
		http.NotFound(w, r)
		return
	}
	xs, err := s.st.ReactionsInFor(id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out := make([]receivedJSON, 0, len(xs))
	for _, x := range xs {
		out = append(out, receivedJSON{Source: x.Source, Author: x.Author, Polarity: x.Polarity, Emoji: x.Emoji, Trusted: x.Trusted})
	}
	writeJSON(w, http.StatusOK, out)
}

// tallyText renders a tally for display: each emoji with its count, most used first.
func tallyText(t *store.Tally) string {
	if t == nil {
		return ""
	}
	parts := make([]string, 0, len(t.Emoji))
	for _, e := range t.Emoji {
		parts = append(parts, fmt.Sprintf("%s %d", e.Emoji, e.Count))
	}
	return strings.Join(parts, "  ")
}

// remoteTallyText renders a tally published in someone else's feed (plus/minus counts only).
func remoteTallyText(plus, minus int) string {
	var parts []string
	if plus > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", store.DefaultEmoji(store.Plus), plus))
	}
	if minus > 0 {
		parts = append(parts, fmt.Sprintf("%s %d", store.DefaultEmoji(store.Minus), minus))
	}
	return strings.Join(parts, "  ")
}
