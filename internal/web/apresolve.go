package web

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/store"
)

// An apTarget is a post on the fediverse that something of the account's answers or likes: its id as an ActivityPub
// object, and who wrote it.
type apTarget struct {
	ObjectID string
	ActorID  string
	Handle   string // @name@server, as a mention shows it
	Inbox    string // where to deliver to its author: the shared inbox of its server if there is one, else its own
}

func targetOf(objectID string, who *store.APActor) *apTarget {
	t := &apTarget{ObjectID: objectID, ActorID: who.ID, Inbox: orStr(who.SharedInbox, who.Inbox)}
	if who.Username != "" {
		t.Handle = "@" + who.Username + "@" + hostPart(who.ID)
	}
	return t
}

// resolveObject finds what an address of a web page is on the fediverse: a post, with its author. It asks the address
// for ActivityStreams (a Mastodon post's page answers that), signed, and then asks the author's server about its
// author. It fails for a page that is not a post of the fediverse, and then the reply or like stays on Weft.
func (s *Server) resolveObject(ctx context.Context, as *store.APAccount, sp *store.Space, addr string) (*apTarget, error) {
	if !httpsOrHTTP(addr) {
		return nil, errors.New("not an address")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// a reply that came from the fediverse is already known by its object id and its author
	if owner := sp.APOwnerOf(addr); owner != "" {
		who, err := s.cachedActor(ctx, as, owner)
		if err != nil {
			return nil, err
		}
		return targetOf(addr, who), nil
	}
	res, err := s.getSigned(ctx, as, addr)
	if err != nil {
		return nil, err
	}
	var obj struct {
		ID           string          `json:"id"`
		Type         string          `json:"type"`
		AttributedTo json.RawMessage `json:"attributedTo"`
	}
	if err := json.Unmarshal(res.Body, &obj); err != nil || obj.ID == "" {
		return nil, errors.New("that address is not a post on the fediverse")
	}
	switch obj.Type {
	case "Note", "Article", "Page", "Question", "Video", "Image", "Audio":
	default:
		return nil, errors.New("that address is not a post on the fediverse")
	}
	if !httpsOrHTTP(obj.ID) || !sameHost(obj.ID, res.URL) {
		return nil, errors.New("the post's id is not on the server that gave it")
	}
	authors := toList(obj.AttributedTo)
	if len(authors) == 0 {
		return nil, errors.New("the post has no author")
	}
	who, err := s.cachedActor(ctx, as, authors[0])
	if err != nil {
		return nil, err
	}
	return targetOf(obj.ID, who), nil
}

// cachedActor is the actor at an address, from the cache if it is not old, else from its server.
func (s *Server) cachedActor(ctx context.Context, as *store.APAccount, id string) (*store.APActor, error) {
	if a, err := s.st.APActorByID(id); err == nil && time.Since(a.Fetched) < actorFresh {
		return a, nil
	}
	return s.fetchActor(ctx, as, withoutFragment(id), "")
}

// replyMention is the mention that starts a reply to somebody: a link to the actor, as Mastodon writes it.
func replyMention(t *apTarget) string {
	name := strings.TrimPrefix(t.Handle, "@")
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	return `<span class="h-card"><a href="` + html.EscapeString(t.ActorID) + `" class="u-url mention" rel="nofollow noopener noreferrer">@<span>` + html.EscapeString(name) + `</span></a></span> `
}
