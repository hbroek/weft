package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/store"
)

// ActivityPub, phase 3: an account's posts go out to its followers. A new post is a Create of a Note, an edit an Update
// and a removal a Delete; each is kept in the account's file by its id and delivered to every follower's server (once
// to a shared inbox) from the delivery queue, which survives restarts and tries again with growing waits. The
// Note's id is the post's own address, which answers as the Note when asked for ActivityStreams, so servers that look
// the post up find it.
//
// Only a post of the author's own goes out: not a reply and not a repost. A reply names a page, and the fediverse
// can only be replied to there if that page is one of its posts; replies come with phase 4. A post made before
// ActivityPub was switched on is not sent either, nor is its edit; only what the fediverse has gets edited or removed.

const publicAudience = "https://www.w3.org/ns/activitystreams#Public"

// federatable is true for a post that goes to the fediverse.
func federatable(p *store.Post) bool { return p.InReplyTo == "" && p.Canonical == "" }

// noteObject is the post as a Note. Its id is its address on the actor's own host.
func noteObject(a *store.APAccount, p *store.Post) map[string]any {
	site := strings.TrimSuffix(a.Actor, "/")
	permalink := site + "/posts/" + p.Slug
	tags := noteTags(p.Keywords, site)
	content, images := noteContent(p.Title, p.HTML, permalink, tags, site)
	n := map[string]any{
		"id": permalink, "type": "Note", "attributedTo": a.Actor, "url": permalink,
		"to": []string{publicAudience}, "cc": []string{a.Actor + "followers"},
		"published": p.Created.UTC().Format(time.RFC3339), "content": content, "mediaType": "text/html", "sensitive": false,
	}
	if p.Title != "" {
		n["name"] = p.Title
	}
	if p.Updated != nil {
		n["updated"] = p.Updated.UTC().Format(time.RFC3339)
	}
	var tagList []map[string]string
	for _, t := range tags {
		tagList = append(tagList, map[string]string{"type": "Hashtag", "href": t.Href, "name": t.Name})
	}
	if len(tagList) > 0 {
		n["tag"] = tagList
	}
	var att []map[string]string
	for _, im := range images {
		if mt := imageType(im.URL); mt != "" {
			att = append(att, map[string]string{"type": "Document", "mediaType": mt, "url": im.URL, "name": im.Alt})
		}
	}
	if len(att) > 0 {
		n["attachment"] = att
	}
	return n
}

// activityOf wraps an object in an activity from the actor, to the same audience as the post.
func activityOf(a *store.APAccount, typ, id string, object any, published time.Time) map[string]any {
	return map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams", "id": id, "type": typ, "actor": a.Actor, "object": object,
		"published": published.UTC().Format(time.RFC3339),
		"to":        []string{publicAudience}, "cc": []string{a.Actor + "followers"},
	}
}

// federate sends what happened to a post (kind: create, update or delete) to the account's followers, if the account
// takes part in ActivityPub and the fediverse has, or should have, the post.
func (s *Server) federate(r *http.Request, p *store.Post, kind string) {
	q := reqOf(r)
	a, ok := s.apFor(r, q)
	if !ok {
		return
	}
	sp, uid := q.sp, q.host.ID
	switch kind {
	case "create":
		if !federatable(p) {
			return
		}
	default:
		if !sp.APPostSent(uid, p.ID) {
			return
		}
	}
	site := strings.TrimSuffix(a.Actor, "/")
	permalink := site + "/posts/" + p.Slug
	var act map[string]any
	switch kind {
	case "create":
		act = activityOf(a, "Create", permalink+"#create", noteObject(a, p), p.Created)
	case "update":
		when := time.Now()
		if p.Updated != nil {
			when = *p.Updated
		}
		act = activityOf(a, "Update", permalink+"#update-"+strconv.FormatInt(when.Unix(), 10), noteObject(a, p), when)
	case "delete":
		act = activityOf(a, "Delete", permalink+"#delete", map[string]any{"id": permalink, "type": "Tombstone"}, time.Now())
	}
	body, err := json.Marshal(act)
	if err != nil {
		return
	}
	id := act["id"].(string)
	typ := act["type"].(string)
	if err := sp.APPutActivity(uid, id, p.ID, typ, string(body)); err != nil {
		return
	}
	if kind == "delete" { // nothing of the post stays, and an earlier send that is still waiting has nothing left to send
		sp.APWithdrawPost(uid, p.ID, id)
	}
	s.deliverToFollowers(sp, uid, id)
}

// deliverToFollowers queues an activity for every follower's server, once for a server with a shared inbox.
func (s *Server) deliverToFollowers(sp *store.Space, uid int64, activityID string) {
	fs, err := sp.APFollowers(uid, 0)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, f := range fs {
		target := f.Inbox
		if f.SharedInbox != "" {
			target = f.SharedInbox
		}
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		sp.Enqueue(store.QueueAP, uid, activityID, target)
	}
	s.kick()
}

// deliverTo queues an activity for one inbox.
func (s *Server) deliverTo(sp *store.Space, uid int64, activityID, typ, inbox string, act map[string]any) error {
	body, err := json.Marshal(act)
	if err != nil {
		return err
	}
	if err := sp.APPutActivity(uid, activityID, 0, typ, string(body)); err != nil {
		return err
	}
	if err := sp.Enqueue(store.QueueAP, uid, activityID, inbox); err != nil {
		return err
	}
	s.kick()
	return nil
}

// maxAPAttempts is how many times a delivery is tried. The waits double from a minute to six hours, so the last try is
// a day or two after the first, which is about as long as other servers keep trying too.
const maxAPAttempts = 12

// sendActivity is one try of a queued delivery. retry asks for another.
func (s *Server) sendActivity(ctx context.Context, sp *store.Space, it store.QueueItem) (note string, retry bool) {
	u, err := sp.User()
	if err != nil {
		return "unknown user", false
	}
	a, err := sp.APAccount(u.ID)
	if err != nil || !a.Enabled {
		return "the account is not on the fediverse", false
	}
	body := sp.APActivityBody(u.ID, it.Source)
	if body == "" {
		return "nothing left to send", false // it was withdrawn, for a post that was removed
	}
	key, err := privateKey(a.PrivatePEM)
	if err != nil {
		return err.Error(), false
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	status, err := s.post(ctx, it.Target, []byte(body), a.Actor+"#main-key", key)
	switch {
	case err != nil:
		return err.Error(), true
	case status >= 200 && status < 300:
		return fmt.Sprintf("delivered (%d)", status), false
	case status == http.StatusGone: // that server says the inbox is gone for good
		sp.APRemoveFollowersAt(u.ID, it.Target)
		return "the inbox is gone (410): its followers are removed", false
	case status >= 500 || status == http.StatusTooManyRequests || status == http.StatusRequestTimeout:
		return fmt.Sprintf("the server answered %d", status), true
	default:
		return fmt.Sprintf("the server refused it: %d", status), false
	}
}

// apRetryDelay is the wait before try number attempts+1.
func apRetryDelay(attempts int64) time.Duration {
	if attempts > 12 {
		attempts = 12
	}
	d := time.Duration(1<<attempts) * time.Minute
	if d > 6*time.Hour {
		d = 6 * time.Hour
	}
	return d
}

// serveNote answers the address of a post asked for as ActivityStreams: the Note, or for a post that was removed a
// Tombstone with 410. It reports false when the fediverse does not have the post, and the page answers.
func (s *Server) serveNote(w http.ResponseWriter, r *http.Request, q *req, p *store.Post) bool {
	if !wantsActivityJSON(r.Header.Get("Accept")) {
		return false
	}
	a, ok := s.apFor(r, q)
	if !ok || !q.sp.APPostSent(q.host.ID, p.ID) {
		return false
	}
	if p.Deleted {
		w.Header().Set("Content-Type", activityJSON+"; charset=utf-8")
		w.Header().Add("Vary", "Accept")
		w.WriteHeader(http.StatusGone)
		json.NewEncoder(w).Encode(map[string]any{"@context": "https://www.w3.org/ns/activitystreams", "id": strings.TrimSuffix(a.Actor, "/") + "/posts/" + p.Slug, "type": "Tombstone"})
		return true
	}
	note := noteObject(a, p)
	note["@context"] = "https://www.w3.org/ns/activitystreams"
	writeLD(w, activityJSON, note)
	return true
}

// outbox lists what the account has sent, newest first, in a first page of 20 posts.
func (s *Server) outbox(w http.ResponseWriter, r *http.Request, q *req) {
	a, ok := s.apFor(r, q)
	if !ok {
		http.NotFound(w, r)
		return
	}
	id := a.Actor + "outbox"
	if r.URL.Query().Get("page") != "true" {
		writeLD(w, activityJSON, map[string]any{
			"@context": "https://www.w3.org/ns/activitystreams", "id": id, "type": "OrderedCollection",
			"totalItems": q.sp.APCreateCount(q.host.ID), "first": id + "?page=true",
		})
		return
	}
	bodies, _ := q.sp.APCreates(q.host.ID, 20)
	items := make([]json.RawMessage, 0, len(bodies))
	for _, b := range bodies {
		items = append(items, json.RawMessage(b))
	}
	writeLD(w, activityJSON, map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams", "id": id + "?page=true", "type": "OrderedCollectionPage", "partOf": id,
		"orderedItems": items,
	})
}
