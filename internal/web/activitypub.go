package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/hbroek/weft/internal/store"
)

// ActivityPub, phase 1: an account that has switched it on (on the profile page, with the operator's -activitypub)
// can be found and looked at from the fediverse, and nothing more. WebFinger names its actor, the actor is the home
// page asked for as ActivityStreams, and NodeInfo says what runs here. The inbox does not take activities yet and the
// outbox is empty, so nobody can follow the account yet and nothing is sent.
//
// The actor's id is the account's origin and "/", pinned when ActivityPub is first switched on, with the custom domain
// if the account has one. An account that is reached at two hosts has one identity: the instance's address redirects
// to the custom domain (see route), and an address that is no longer the pinned one answers nothing here, because a
// different id would be a different account to the servers that know this one.

const (
	activityJSON = "application/activity+json"
	jrdJSON      = "application/jrd+json"
	nodeInfoRel  = "http://nodeinfo.diaspora.software/ns/schema/2.1"
)

// apFor is the ActivityPub identity of the account at this address, if the operator allows ActivityPub, the account
// has it on, and the address is the pinned one.
func (s *Server) apFor(r *http.Request, q *req) (*store.APAccount, bool) {
	if !s.cfg.ActivityPub {
		return nil, false
	}
	a, err := q.sp.APAccount(q.host.ID)
	if err != nil || !a.Enabled || strings.TrimSuffix(a.Actor, "/") != s.originOf(q.host.Name, r) {
		return nil, false
	}
	return a, true
}

// wantsActivityJSON is true for a request that asks for ActivityStreams, the way Mastodon and the others do:
// application/activity+json, or application/ld+json with the ActivityStreams profile.
func wantsActivityJSON(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		mt, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		mt = strings.ToLower(strings.TrimSpace(mt))
		refused := false
		for _, p := range strings.Split(params, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(k, "q") {
				refused = strings.Trim(v, "0.") == "" // q=0, q=0.0: not acceptable
			}
		}
		switch {
		case refused:
		case mt == activityJSON:
			return true
		case mt == "application/ld+json" && strings.Contains(params, "https://www.w3.org/ns/activitystreams"):
			return true
		}
	}
	return false
}

func writeLD(w http.ResponseWriter, ct string, v any) {
	w.Header().Set("Content-Type", ct+"; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Add("Vary", "Accept")
	json.NewEncoder(w).Encode(v)
}

// actor answers the home page asked for as ActivityStreams. It reports false when the request is not one for the
// actor or the account has no actor here, and then the home page is what answers.
func (s *Server) actor(w http.ResponseWriter, r *http.Request, q *req) bool {
	if !wantsActivityJSON(r.Header.Get("Accept")) {
		return false
	}
	a, ok := s.apFor(r, q)
	if !ok {
		return false
	}
	id := a.Actor
	doc := map[string]any{
		"@context":                  []any{"https://www.w3.org/ns/activitystreams", "https://w3id.org/security/v1"},
		"id":                        id,
		"type":                      "Person",
		"preferredUsername":         q.host.Name,
		"name":                      orStr(q.host.Display, q.host.Name),
		"url":                       id,
		"inbox":                     id + "inbox",
		"outbox":                    id + "outbox",
		"manuallyApprovesFollowers": false,
		"publicKey":                 map[string]string{"id": id + "#main-key", "owner": id, "publicKeyPem": a.PublicPEM},
	}
	if q.host.Bio != "" {
		doc["summary"] = string(bioHTML(q.host.Bio))
	}
	if av := q.host.Avatar; av != "" {
		if strings.HasPrefix(av, "/") {
			av = strings.TrimSuffix(id, "/") + av
		}
		doc["icon"] = map[string]string{"type": "Image", "url": av}
	}
	writeLD(w, activityJSON, doc)
	return true
}

// outbox is empty until the account's posts are sent (phase 3), but it answers, so a server that looks finds a
// collection and not an error.
func (s *Server) outbox(w http.ResponseWriter, r *http.Request, q *req) {
	a, ok := s.apFor(r, q)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeLD(w, activityJSON, map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams", "id": a.Actor + "outbox", "type": "OrderedCollection",
		"totalItems": 0, "orderedItems": []any{},
	})
}

// inbox does not take activities yet (phase 2); saying so is better than a 404 that looks like a mistake.
func (s *Server) inbox(w http.ResponseWriter, r *http.Request, q *req) {
	if _, ok := s.apFor(r, q); !ok {
		http.NotFound(w, r)
		return
	}
	http.Error(w, "This account does not accept activities yet.", http.StatusNotImplemented)
}

// webfinger names the actor of the account for acct:name@host, where host is this account's address (with or without
// a port), or for the actor's own address. A vanity handle on another domain is that domain's own WebFinger file
// pointing at the actor.
func (s *Server) webfinger(w http.ResponseWriter, r *http.Request, q *req) {
	a, ok := s.apFor(r, q)
	res := r.URL.Query().Get("resource")
	if !ok || res == "" {
		if ok {
			http.Error(w, "give resource=acct:name@host", http.StatusBadRequest)
			return
		}
		http.NotFound(w, r)
		return
	}
	if !s.isThisAccount(res, a, q) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	subject := res
	if a.HandleHost != "" { // known as name@that-domain: say so, and the asking server checks that the domain agrees
		subject = "acct:" + q.host.Name + "@" + a.HandleHost
	}
	writeLD(w, jrdJSON, map[string]any{
		"subject": subject,
		"aliases": []string{a.Actor},
		"links": []map[string]string{
			{"rel": "self", "type": activityJSON, "href": a.Actor},
			{"rel": "http://webfinger.net/rel/profile-page", "type": "text/html", "href": a.Actor},
		},
	})
}

// isThisAccount is true when the WebFinger resource names the account: acct:name@host or the actor's address.
func (s *Server) isThisAccount(res string, a *store.APAccount, q *req) bool {
	if res == a.Actor || res == strings.TrimSuffix(a.Actor, "/") {
		return true
	}
	acct, ok := strings.CutPrefix(res, "acct:")
	if !ok {
		return false
	}
	name, host, ok := strings.Cut(acct, "@")
	if !ok || !strings.EqualFold(name, q.host.Name) {
		return false
	}
	u, err := url.Parse(a.Actor)
	if err != nil {
		return false
	}
	return strings.EqualFold(host, u.Host) || strings.EqualFold(host, u.Hostname()) || (a.HandleHost != "" && strings.EqualFold(host, a.HandleHost))
}

// nodeInfoLinks and nodeInfo say what runs here (NodeInfo 2.1), so servers that ask can tell Weft from the rest.
func (s *Server) nodeInfoLinks(w http.ResponseWriter, r *http.Request, q *req) {
	a, ok := s.apFor(r, q)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeLD(w, "application/json", map[string]any{"links": []map[string]string{{"rel": nodeInfoRel, "href": strings.TrimSuffix(a.Actor, "/") + "/nodeinfo/2.1"}}})
}

func (s *Server) nodeInfo(w http.ResponseWriter, r *http.Request, q *req) {
	if _, ok := s.apFor(r, q); !ok {
		http.NotFound(w, r)
		return
	}
	writeLD(w, `application/json; profile="`+nodeInfoRel+`#"`, map[string]any{
		"version":           "2.1",
		"software":          map[string]string{"name": "weft", "version": store.Version(), "repository": "https://github.com/hbroek/weft", "homepage": "https://weftseed.com"},
		"protocols":         []string{"activitypub"},
		"services":          map[string]any{"inbound": []string{}, "outbound": []string{"rss2.0"}},
		"openRegistrations": false,
		"usage":             map[string]any{"users": map[string]int{"total": 1}, "localPosts": q.sp.PostCount(q.host.ID)},
		"metadata":          map[string]any{},
	})
}

// apView is the Fediverse section of the profile page.
type apView struct {
	Enabled    bool
	Handle     string // @name@host: how someone finds the account
	Actor      string
	Moved      bool   // the account's address is no longer the one its identity was made for
	Pinned     string // that address
	HandleHost string // the domain the handle is shown with, if the owner set one
}

func (s *Server) apView(r *http.Request, q *req) *apView {
	if !s.cfg.ActivityPub {
		return nil
	}
	v := &apView{}
	if a, err := q.sp.APAccount(q.host.ID); err == nil {
		v.Actor, v.Enabled = a.Actor, a.Enabled
		v.HandleHost = a.HandleHost
		if u, err := url.Parse(a.Actor); err == nil {
			v.Handle = "@" + q.host.Name + "@" + orStr(a.HandleHost, u.Hostname())
		}
		v.Moved = a.Enabled && strings.TrimSuffix(a.Actor, "/") != s.originOf(q.host.Name, r)
		v.Pinned = strings.TrimSuffix(a.Actor, "/")
	}
	return v
}

// apSwitch turns ActivityPub on or off for the account. Turning it on pins the actor address.
func (s *Server) apSwitch(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.cfg.ActivityPub {
		http.NotFound(w, r)
		return
	}
	if !s.requireOwner(w, r, q) {
		return
	}
	var err error
	if r.FormValue("enable") == "1" {
		_, err = q.sp.APEnable(q.host.ID, s.originOf(q.host.Name, r)+"/")
	} else {
		err = q.sp.APDisable(q.host.ID)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/profile#fediverse", http.StatusSeeOther)
}

// apHandle sets the domain the handle is shown with. It does not check the domain: the WebFinger file there is the
// owner's to write, and the profile page says what it must say.
func (s *Server) apHandle(w http.ResponseWriter, r *http.Request, q *req) {
	if !s.cfg.ActivityPub {
		http.NotFound(w, r)
		return
	}
	if !s.requireOwner(w, r, q) {
		return
	}
	if err := q.sp.APSetHandleHost(q.host.ID, strings.TrimSpace(r.FormValue("handle_host"))); err != nil {
		if err == store.ErrNotFound {
			http.Error(w, "Switch ActivityPub on first.", http.StatusConflict)
			return
		}
		s.renderProfile(w, r, q, http.StatusBadRequest, "That is not a domain name you can use for your handle. Give a name such as example.com, without https://.", q.host.Display, q.host.Bio, q.host.Avatar)
		return
	}
	http.Redirect(w, r, "/profile#fediverse", http.StatusSeeOther)
}
