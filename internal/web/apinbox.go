package web

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/store"
)

// ActivityPub, phase 2: the inbox. Every request is signed by the actor that sends it, and an activity counts only if
// it comes from the actor that signed it. Follow adds a follower and is answered with a signed Accept; Undo of a Follow,
// and the Delete of an actor, take the follower away. Everything else is acknowledged and ignored until it is handled
// (phase 4).

const (
	maxInbox   = 256 << 10      // bytes of an activity; Mastodon's are a few kilobytes
	actorFresh = 24 * time.Hour // how long a cached actor is trusted without asking again

)

// actorRefetchIn is how soon a cached key that fails may be fetched again: a key that was just fetched is not stale.
var actorRefetchIn = time.Minute

const asAccept = `application/activity+json, application/ld+json; profile="https://www.w3.org/ns/activitystreams"`

// remoteActor is the part of an actor document that is read.
type remoteActor struct {
	ID                string `json:"id"`
	Type              string `json:"type"`
	Inbox             string `json:"inbox"`
	PreferredUsername string `json:"preferredUsername"`
	Name              string `json:"name"`
	Endpoints         struct {
		SharedInbox string `json:"sharedInbox"`
	} `json:"endpoints"`
	Icon      json.RawMessage `json:"icon"`
	PublicKey json.RawMessage `json:"publicKey"`
	// a key object, when the address of a key is asked for
	Owner        string `json:"owner"`
	PublicKeyPem string `json:"publicKeyPem"`
}

type remoteKey struct {
	ID           string `json:"id"`
	Owner        string `json:"owner"`
	PublicKeyPem string `json:"publicKeyPem"`
}

func httpsOrHTTP(u string) bool {
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "https" || p.Scheme == "http") && p.Host != ""
}

func sameHost(a, b string) bool {
	pa, err1 := url.Parse(a)
	pb, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && strings.EqualFold(pa.Host, pb.Host)
}

func withoutFragment(u string) string {
	if i := strings.IndexByte(u, '#'); i >= 0 {
		return u[:i]
	}
	return u
}

// fetchActor reads the actor at an address. wantKey, if not empty, is the key id the actor must have.
func (s *Server) fetchActor(ctx context.Context, as *store.APAccount, addr, wantKey string) (*store.APActor, error) {
	// signed with the account's key: many servers answer nothing else, and the ones that do not mind
	key, err := privateKey(as.PrivatePEM)
	if err != nil {
		return nil, err
	}
	res, err := s.web.GetSigned(ctx, addr, asAccept, func(r *http.Request) error { return signGet(r, as.Actor+"#main-key", key) })
	if err != nil {
		return nil, err
	}
	var d remoteActor
	if err := json.Unmarshal(res.Body, &d); err != nil {
		return nil, errors.New("the actor is not JSON")
	}
	if d.PublicKeyPem != "" && d.Owner != "" && d.PublicKey == nil { // the address was of a key: its owner is the actor
		if !sameHost(d.Owner, addr) || d.Owner == addr {
			return nil, errors.New("the key's owner is not on its server")
		}
		return s.fetchActor(ctx, as, d.Owner, wantKey)
	}
	if !httpsOrHTTP(d.ID) || !sameHost(d.ID, res.URL) || (withoutFragment(d.ID) != withoutFragment(addr) && withoutFragment(d.ID) != withoutFragment(res.URL)) {
		return nil, errors.New("the actor's id is not the address it was fetched from")
	}
	switch d.Type {
	case "Person", "Service", "Application", "Group", "Organization":
	default:
		return nil, errors.New("not an actor")
	}
	if !httpsOrHTTP(d.Inbox) {
		return nil, errors.New("the actor has no inbox")
	}
	if !sameHost(d.Inbox, d.ID) { // what we send goes to the actor's own server, never where an actor points us
		return nil, errors.New("the actor's inbox is on another server")
	}
	var k remoteKey
	if err := json.Unmarshal(d.PublicKey, &k); err != nil { // some servers give a list of keys
		var ks []remoteKey
		if json.Unmarshal(d.PublicKey, &ks) != nil || len(ks) == 0 {
			return nil, errors.New("the actor has no public key")
		}
		k = ks[0]
		for _, c := range ks {
			if c.ID == wantKey {
				k = c
			}
		}
	}
	if k.PublicKeyPem == "" || (k.Owner != "" && k.Owner != d.ID) {
		return nil, errors.New("the actor's key is not its own")
	}
	if wantKey != "" && k.ID != wantKey {
		return nil, errors.New("the actor does not have that key")
	}
	a := &store.APActor{ID: d.ID, KeyID: k.ID, PublicPEM: k.PublicKeyPem, Inbox: d.Inbox, Username: d.PreferredUsername, Name: d.Name, Fetched: time.Now()}
	if httpsOrHTTP(d.Endpoints.SharedInbox) && sameHost(d.Endpoints.SharedInbox, d.ID) {
		a.SharedInbox = d.Endpoints.SharedInbox
	}
	var icon struct{ URL string }
	if json.Unmarshal(d.Icon, &icon) == nil && httpsOrHTTP(icon.URL) {
		a.Icon = icon.URL
	}
	if err := s.st.PutAPActor(a); err != nil {
		return nil, err
	}
	return a, nil
}

// actorForSignature finds the actor whose key signed the request, from the cache or from its server.
func (s *Server) actorForSignature(ctx context.Context, as *store.APAccount, r *http.Request, sig *signature) (*store.APActor, error) {
	cached, err := s.st.APActorByKey(sig.keyID)
	if err == nil && time.Since(cached.Fetched) < actorFresh {
		if sig.verify(r, cached.PublicPEM) == nil {
			return cached, nil
		}
		if time.Since(cached.Fetched) < actorRefetchIn { // just fetched: the key is not stale, the signature is wrong
			return nil, errors.New("the signature does not match")
		}
	}
	// not known, old, or its key did not fit (keys are changed): ask the actor's server
	a, err := s.fetchActor(ctx, as, withoutFragment(sig.keyID), sig.keyID)
	if err != nil {
		return nil, fmt.Errorf("the key %s cannot be fetched: %w", sig.keyID, err)
	}
	if err := sig.verify(r, a.PublicPEM); err != nil {
		return nil, err
	}
	return a, nil
}

// idOf is the id in a JSON value that is an address or an object with an id.
func idOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		ID string `json:"id"`
	}
	json.Unmarshal(raw, &o)
	return o.ID
}

type activity struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Actor  json.RawMessage `json:"actor"`
	Object json.RawMessage `json:"object"`
}

// inboxLog says what became of a request to an inbox. Refusals are always logged, with what the sender gave quoted: an
// operator whose followers do not arrive needs to see whether a request came and why it was not believed.
func (s *Server) inboxLog(r *http.Request, format string, args ...any) {
	log.Printf("activitypub inbox %s from %s: %s", r.Host, s.clientIP(r), fmt.Sprintf(format, args...))
}

func (s *Server) inbox(w http.ResponseWriter, r *http.Request, q *req) {
	a, ok := s.apFor(r, q)
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInbox))
	if err != nil {
		http.Error(w, "the activity is too large", http.StatusRequestEntityTooLarge)
		return
	}
	sig, err := checkSignedRequest(r, body, time.Now())
	if err != nil {
		s.inboxLog(r, "refused: %q", err.Error())
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	signer, err := s.actorForSignature(r.Context(), a, r, sig)
	if err != nil {
		s.inboxLog(r, "refused: %q", err.Error())
		http.Error(w, "the signature cannot be verified", http.StatusUnauthorized)
		return
	}
	var act activity
	if err := json.Unmarshal(body, &act); err != nil || act.Type == "" {
		s.inboxLog(r, "refused: not an activity")
		http.Error(w, "not an activity", http.StatusBadRequest)
		return
	}
	if idOf(act.Actor) != signer.ID {
		s.inboxLog(r, "refused: %q says %q, signed by %q", act.Type, idOf(act.Actor), signer.ID)
		http.Error(w, "the activity is not from the actor that signed it", http.StatusForbidden)
		return
	}
	switch act.Type {
	case "Follow":
		if !isUs(idOf(act.Object), a) {
			break // a follow of somebody else, or of nobody
		}
		if err := s.apFollow(q, a, signer, act); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.inboxLog(r, "follow from %q accepted", signer.ID)
	case "Undo":
		s.apUnfollow(q, a, signer, act)
	case "Delete":
		if idOf(act.Object) == signer.ID { // the actor is gone
			q.sp.APRemoveFollower(q.host.ID, signer.ID, "")
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

// isUs is true when an address is the account's actor.
func isUs(id string, a *store.APAccount) bool {
	return id != "" && strings.TrimSuffix(id, "/") == strings.TrimSuffix(a.Actor, "/")
}

func (s *Server) apFollow(q *req, a *store.APAccount, who *store.APActor, act activity) error {
	f := store.APFollower{Actor: who.ID, FollowID: act.ID, Inbox: who.Inbox, SharedInbox: who.SharedInbox, Name: who.Name}
	if u, err := url.Parse(who.ID); err == nil {
		f.Handle = orStr(who.Username, "?") + "@" + u.Host
	}
	if _, err := q.sp.APAddFollower(q.host.ID, f); err != nil {
		return err
	}
	// answered every time, so that a follower whose first Accept was lost can ask again
	sum := sha256.Sum256([]byte(act.ID + who.ID))
	s.send(a, who.Inbox, map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       a.Actor + "#accepts/" + hex.EncodeToString(sum[:8]),
		"type":     "Accept",
		"actor":    a.Actor,
		"object":   map[string]any{"id": act.ID, "type": "Follow", "actor": who.ID, "object": a.Actor},
	})
	return nil
}

func (s *Server) apUnfollow(q *req, a *store.APAccount, who *store.APActor, act activity) {
	var inner activity
	if json.Unmarshal(act.Object, &inner) == nil && inner.Type != "" {
		if inner.Type == "Follow" && idOf(inner.Actor) == who.ID && isUs(idOf(inner.Object), a) {
			q.sp.APRemoveFollower(q.host.ID, who.ID, "")
		}
		return
	}
	if id := idOf(act.Object); id != "" { // the id of the Follow
		var f store.APFollower
		fs, _ := q.sp.APFollowers(q.host.ID, 0)
		for _, c := range fs {
			if c.FollowID == id && c.Actor == who.ID {
				f = c
			}
		}
		if f.Actor != "" {
			q.sp.APRemoveFollower(q.host.ID, f.Actor, "")
		}
	}
}

// backoff is how long to wait before each new try of a delivery.
func (s *Server) backoff() []time.Duration {
	if s.apBackoff != nil {
		return s.apBackoff
	}
	return []time.Duration{10 * time.Second, 2 * time.Minute, 20 * time.Minute}
}

// send delivers an activity to an inbox, signed as the account, and tries again later if the server cannot be reached
// or is busy. The tries are kept in memory: an Accept that is lost with a restart is asked for again when the follower
// follows again. (Posts will use a queue that survives restarts, in phase 3.)
func (s *Server) send(a *store.APAccount, inbox string, act map[string]any) {
	body, err := json.Marshal(act)
	key, kerr := privateKey(a.PrivatePEM)
	if err != nil || kerr != nil || !httpsOrHTTP(inbox) {
		log.Printf("activitypub: cannot send %v to %s: %v %v", act["type"], inbox, err, kerr)
		return
	}
	go func() {
		wait := s.backoff()
		for try := 0; ; try++ {
			status, err := s.post(inbox, body, a.Actor+"#main-key", key)
			if err == nil && status >= 200 && status < 300 {
				return
			}
			retry := err != nil || status == 429 || status == 408 || status >= 500
			if !retry || try >= len(wait) {
				log.Printf("activitypub: gave up sending %v to %s (status %d, %v)", act["type"], inbox, status, err)
				return
			}
			time.Sleep(wait[try])
		}
	}()
}

// post sends one signed POST to an inbox.
func (s *Server) post(inbox string, body []byte, keyID string, key *rsa.PrivateKey) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", inbox, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", activityJSON)
	req.Header.Set("User-Agent", "Weft/0.1 (+https://weftseed.com)")
	if err := signRequest(req, body, keyID, key); err != nil {
		return 0, err
	}
	return s.web.Send(req)
}
