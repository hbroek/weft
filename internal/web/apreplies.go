package web

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"

	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/hbroek/weft/internal/store"
)

// ActivityPub, phase 4: what comes back. A public reply from the fediverse to one of the account's posts is kept as a
// reply that waits for the owner's approval, like a reply that came by Webmention; a Like is a like, and a boost counts
// with the likes (named "boosted" in the list of who). The sender's own edit (Update) and removal (Delete) of what it
// sent are followed, and Undo takes a like or boost back. Everything has to come from the actor that signed it, about
// an object that lives on that actor's server. A private reply (a direct message, or to followers only) is never kept:
// it was not written for the web.

const (
	maxReplyRunes   = 20000 // what is kept of a reply
	maxPendingReply = 500   // replies waiting for the owner; past it a stranger's reply is dropped
)

// noteIn is the part of a received Note that is read.
type noteIn struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	AttributedTo json.RawMessage `json:"attributedTo"`
	InReplyTo    json.RawMessage `json:"inReplyTo"`
	Content      string          `json:"content"`
	Published    string          `json:"published"`
	Updated      string          `json:"updated"`
	To           json.RawMessage `json:"to"`
	CC           json.RawMessage `json:"cc"`
}

// toList reads a field that is one address or a list of them.
func toList(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []json.RawMessage
	if json.Unmarshal(raw, &many) != nil {
		return nil
	}
	var out []string
	for _, m := range many {
		if id := idOf(m); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// isPublic is true when the audience includes everyone, in any of the three ways servers write it.
func isPublic(to, cc json.RawMessage) bool {
	for _, a := range append(toList(to), toList(cc)...) {
		switch a {
		case publicAudience, "as:Public", "Public":
			return true
		}
	}
	return false
}

// personName is how a sender is shown: its name and handle, or the handle alone.
func personName(who *store.APActor) string {
	handle := ""
	if h := hostPart(who.ID); h != "" && who.Username != "" {
		handle = "@" + who.Username + "@" + h
	}
	switch {
	case who.Name != "" && handle != "":
		return who.Name + " (" + handle + ")"
	case who.Name != "":
		return who.Name
	}
	return orStr(handle, who.ID)
}

func hostPart(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	return u
}

// postOf finds which of the account's posts an address is, or 0.
func (s *Server) apPostOf(sp *store.Space, a *store.APAccount, addr string) int64 {
	id, ok := s.localPostID(sp, strings.TrimSuffix(a.Actor, "/"), addr)
	if !ok {
		return 0
	}
	if p, err := sp.Post(sp.UID(), id); err != nil || p.Deleted {
		return 0
	}
	return id
}

// apInteraction handles a Create or Update of a reply, a Like or an Announce. It does nothing, quietly, for anything
// it does not take: the sender gets its 202 either way.
func (s *Server) apInteraction(q *req, a *store.APAccount, who *store.APActor, act activity) {
	sp := q.sp
	switch act.Type {
	case "Create", "Update":
		var n noteIn
		if json.Unmarshal(act.Object, &n) != nil || (n.Type != "Note" && n.Type != "Article" && n.Type != "Page") {
			return
		}
		// the object lives on the sender's server and is the sender's, and it answers one of our posts, in public
		if !httpsOrHTTP(n.ID) || !sameHost(n.ID, who.ID) || idOf(n.AttributedTo) != who.ID || !isPublic(n.To, n.CC) {
			return
		}
		postID := s.apPostOf(sp, a, idOf(n.InReplyTo))
		if postID == 0 {
			return
		}
		md := htmlToMarkdown(n.Content)
		md = stripMentionOf(md, q.host.Name)
		if strings.TrimSpace(md) == "" {
			return
		}
		if r := []rune(md); len(r) > maxReplyRunes {
			md = string(r[:maxReplyRunes])
		}
		if !sp.APClaim(n.ID, who.ID) { // another actor made what is under this address
			return
		}
		prior, err := sp.ReplyBySource(postID, n.ID)
		if err != nil && sp.PendingReplyCount(q.host.ID) >= maxPendingReply {
			return
		}
		published := parseTime(n.Published, time.Now())
		r := store.Reply{PostID: postID, Source: n.ID, Author: personName(who), Text: plainText(n.Content), Markdown: md, Published: published, Status: store.StatusPending}
		if act.Type == "Update" || prior != nil {
			u := parseTime(n.Updated, time.Now())
			r.Updated = &u
		}
		sp.UpsertReply(r)
	case "Like", "Announce":
		postID := s.apPostOf(sp, a, idOf(act.Object))
		if postID == 0 {
			return
		}
		id := act.ID
		if !httpsOrHTTP(id) || !sameHost(id, who.ID) { // an id of ours to give it, when the sender gave none
			id = who.ID + "#" + strings.ToLower(act.Type) + "-" + strings.TrimSuffix(a.Actor, "/") + "-" + idOf(act.Object)
		}
		if !sp.APClaim(id, who.ID) {
			return
		}
		name := personName(who)
		if act.Type == "Announce" {
			name += " (boosted)"
		}
		sp.UpsertLikeIn(store.LikeIn{PostID: postID, Source: id, Author: name})
	}
}

// apUndo takes back a Follow, a Like or an Announce. inner is what is undone: embedded, or its id alone.
func (s *Server) apUndo(q *req, a *store.APAccount, who *store.APActor, act activity) {
	var inner activity
	if json.Unmarshal(act.Object, &inner) != nil || inner.Type == "" {
		inner = activity{ID: idOf(act.Object)} // the id of a Follow, a Like or a boost, and nothing else is known
	}
	switch inner.Type {
	case "Follow":
		s.apUnfollow(q, a, who, act)
	case "Like", "Announce":
		s.dropLike(q, who, inner.ID)
	case "":
		s.apUnfollow(q, a, who, act) // by the id of the Follow
		s.dropLike(q, who, inner.ID)
	}
}

// dropLike removes a received like or boost that the sender made.
func (s *Server) dropLike(q *req, who *store.APActor, id string) {
	if id != "" && q.sp.APOwns(id, who.ID) {
		q.sp.DeleteLikeInBySource(id)
		q.sp.APRelease(id)
	}
}

// apRemoved handles the Delete of an object: a reply the sender wrote is removed.
func (s *Server) apRemoved(q *req, who *store.APActor, act activity) {
	id := idOf(act.Object)
	if id == "" || !q.sp.APOwns(id, who.ID) { // only the actor that wrote it may remove it, not another on the same server
		return
	}
	rs, _ := q.sp.RepliesBySource(id)
	for _, r := range rs {
		q.sp.DeleteReply(r.PostID, id)
	}
	q.sp.DeleteLikeInBySource(id)
	q.sp.APRelease(id)
}

func parseTime(s string, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return fallback
}

var leadingMention = regexp.MustCompile(`^(\s*@[A-Za-z0-9_.-]+(@[A-Za-z0-9.-]+)?(\s+|$))+`)

// stripMentionOf takes the mentions of the account off the start of a reply: the one that addressed it is not part of
// what was said.
func stripMentionOf(md, name string) string {
	loc := leadingMention.FindString(md)
	if loc == "" {
		return md
	}
	var keep []string
	for _, m := range strings.Fields(loc) {
		if !strings.EqualFold(strings.SplitN(strings.TrimPrefix(m, "@"), "@", 2)[0], name) {
			keep = append(keep, m)
		}
	}
	rest := md[len(loc):]
	if len(keep) > 0 {
		return strings.Join(keep, " ") + " " + rest
	}
	return rest
}

// mdSpecial escapes what Markdown would take for markup in the text of a stranger. (A "!" is escaped only where it
// stands right before a link, where it would make an image that the reader's browser loads from the stranger's server.)
var mdSpecial = strings.NewReplacer(`\`, `\\`, "`", "\\`", "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`, "<", `\<`, ">", `\>`,
	"#", `\#`, "~", `\~`, "|", `\|`, "=", `\=`, "+", `\+`, "-", `\-`)

var numberedLine = regexp.MustCompile(`(?m)^(\s*\d+)([.)])`)

// htmlToMarkdown makes Markdown of the HTML a Mastodon post carries: paragraphs, line breaks, links, emphasis, code,
// lists and quotes; mentions and hashtags become plain text, and what servers hide is left out. It is for reading
// what a stranger wrote, so everything in the text that Markdown would take for markup is escaped first.
func htmlToMarkdown(markup string) string {
	top, err := nethtml.ParseFragment(strings.NewReader(markup), &nethtml.Node{Type: nethtml.ElementNode, DataAtom: atom.Body, Data: "body"})
	if err != nil {
		return ""
	}
	attr := func(n *nethtml.Node, k string) string {
		for _, a := range n.Attr {
			if a.Key == k {
				return a.Val
			}
		}
		return ""
	}
	// text is the text of a node as it shows: what servers hide (class "invisible") is left out
	var text func(n *nethtml.Node, t *strings.Builder)
	text = func(n *nethtml.Node, t *strings.Builder) {
		if n.Type == nethtml.TextNode {
			t.WriteString(n.Data)
		}
		if n.Type == nethtml.ElementNode && strings.Contains(attr(n, "class"), "invisible") {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			text(c, t)
		}
	}
	plain := func(n *nethtml.Node) string {
		var t strings.Builder
		text(n, &t)
		return t.String()
	}
	var walk func(n *nethtml.Node, b *strings.Builder)
	kids := func(n *nethtml.Node, b *strings.Builder) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, b)
		}
	}
	walk = func(n *nethtml.Node, b *strings.Builder) {
		switch n.Type {
		case nethtml.TextNode:
			b.WriteString(mdSpecial.Replace(n.Data))
		case nethtml.ElementNode:
			switch n.Data {
			case "p":
				kids(n, b)
				b.WriteString("\n\n")
			case "br":
				b.WriteString("  \n")
			case "strong", "b":
				b.WriteString("**")
				kids(n, b)
				b.WriteString("**")
			case "em", "i":
				b.WriteString("*")
				kids(n, b)
				b.WriteString("*")
			case "code":
				// on one line: a blank line inside a code span would end the paragraph and free what follows
				b.WriteString("`" + strings.ReplaceAll(strings.Join(strings.Fields(plain(n)), " "), "`", "'") + "`")
			case "pre":
				code := strings.TrimSpace(plain(n))
				fence := strings.Repeat("`", max(3, longestRun(code, '`')+1)) // longer than any run inside, so the text cannot end it
				b.WriteString("\n" + fence + "\n" + code + "\n" + fence + "\n\n")
			case "blockquote":
				var inner strings.Builder
				kids(n, &inner)
				for _, l := range strings.Split(strings.TrimSpace(inner.String()), "\n") {
					b.WriteString("> " + l + "\n")
				}
				b.WriteString("\n")
			case "ul", "ol":
				kids(n, b)
				b.WriteString("\n")
			case "li":
				b.WriteString("- ")
				kids(n, b)
				b.WriteString("\n")
			case "a":
				cls, href := attr(n, "class"), attr(n, "href")
				if strings.Contains(cls, "hashtag") || strings.Contains(cls, "mention") || !httpsOrHTTP(href) {
					b.WriteString(mdSpecial.Replace(strings.Join(strings.Fields(plain(n)), " ")))
					return
				}
				if t := b.String(); strings.HasSuffix(t, "!") && !strings.HasSuffix(t, `\!`) {
					b.Reset()
					b.WriteString(t[:len(t)-1] + `\!`)
				}
				label := mdSpecial.Replace(strings.Join(strings.Fields(plain(n)), " "))
				dest := strings.NewReplacer("(", "%28", ")", "%29", " ", "%20", "<", "%3C", ">", "%3E", `\`, "%5C", "`", "%60", `"`, "%22", "'", "%27").Replace(href)
				if u, err := url.Parse(href); err == nil { // rebuilt from its parts, so that nothing odd survives in it
					dest = strings.NewReplacer("(", "%28", ")", "%29", "<", "%3C", ">", "%3E", `\`, "%5C", "`", "%60", `"`, "%22", "'", "%27").Replace(u.String())
				}
				b.WriteString("[" + label + "](" + dest + ")")
			case "span":
				if !strings.Contains(attr(n, "class"), "invisible") {
					kids(n, b)
				}
			case "script", "style":
			default:
				kids(n, b)
			}
		}
	}
	var b strings.Builder
	for _, n := range top {
		walk(n, &b)
	}
	out := numberedLine.ReplaceAllString(strings.ReplaceAll(b.String(), "\r", ""), `$1\$2`) // "1." at a line start is a list
	return strings.TrimSpace(out)
}

// longestRun is the length of the longest run of c in s.
func longestRun(s string, c rune) int {
	best, run := 0, 0
	for _, r := range s {
		if r == c {
			run++
			best = max(best, run)
		} else {
			run = 0
		}
	}
	return best
}
