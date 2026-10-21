// Package feed generates RSS 2.0 feeds with the weft: namespace.
package feed

import (
	"encoding/xml"
	"time"
)

const (
	NSWeft = "https://weftseed.com/ns/weft/0.1"
	// NSLegacy is the placeholder URI used before the project was renamed; the parser still reads it.
	NSLegacy = "https://example.org/ns/textcast/0.1"
	NSSource = "http://source.scripting.com/"
	NSAtom   = "http://www.w3.org/2005/Atom"
)

type RSS struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	NSWeft  string   `xml:"xmlns:weft,attr"`
	NSSrc   string   `xml:"xmlns:source,attr"`
	NSAtom  string   `xml:"xmlns:atom,attr"`
	Channel Channel  `xml:"channel"`
}

type AtomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

// Ref is an element that points at a post or feed: weft:inReplyTo, weft:root, weft:feed, weft:repliesTo.
type Ref struct {
	Href string `xml:"href,attr"`
	Feed string `xml:"feed,attr,omitempty"`
}

// Context is weft:context. In this version it carries only the parent's URL.
type Context struct {
	Href      string `xml:"href,attr"`
	Retrieved string `xml:"retrieved,attr,omitempty"`
}

// FeedRef is weft:feed: who wrote an item, by feed URL, with an optional display name as text.
type FeedRef struct {
	Href string `xml:"href,attr"`
	Name string `xml:",chardata"`
}

// ReactionRef is weft:reactionOf: the item is a reaction to the post at Href. Polarity is "+" or "-";
// Emoji is optional nuance.
type ReactionRef struct {
	Href     string `xml:"href,attr"`
	Polarity string `xml:"polarity,attr"`
	Emoji    string `xml:"emoji,attr,omitempty"`
}

// EmojiCount is one entry of a reaction tally breakdown.
type EmojiCount struct {
	Value string `xml:"value,attr"`
	Count int    `xml:"count,attr"`
}

// Reactions is weft:reactions: a post's tally of verified reactions from trusted senders.
type Reactions struct {
	Plus  int          `xml:"plus,attr"`
	Minus int          `xml:"minus,attr"`
	Emoji []EmojiCount `xml:"weft:emoji"`
}

// Replies is weft:replies: the URL of a post's replies feed and the approved count.
type Replies struct {
	Href  string `xml:"href,attr"`
	Count int    `xml:"count,attr"`
}

type Channel struct {
	Title         string     `xml:"title"`
	Link          string     `xml:"link"`
	Description   string     `xml:"description"`
	LastBuildDate string     `xml:"lastBuildDate"`
	Links         []AtomLink `xml:"atom:link"` // rel="self", and rel="next"/"previous"/"first" for paged feeds
	Webmention    string     `xml:"weft:webmention,omitempty"`
	Following     string     `xml:"weft:following,omitempty"`
	ReactionsFeed string     `xml:"weft:reactionsFeed,omitempty"`
	RepliesTo     *Ref       `xml:"weft:repliesTo"`
	Depth         int        `xml:"weft:depth,omitempty"`
	Items         []Item     `xml:"item"`
}

type GUID struct {
	IsPermaLink bool   `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type Item struct {
	Title       string       `xml:"title,omitempty"`
	GUID        GUID         `xml:"guid"`
	Link        string       `xml:"link"`
	Description string       `xml:"description"`
	Markdown    string       `xml:"source:markdown,omitempty"`
	PubDate     string       `xml:"pubDate"`
	Updated     string       `xml:"weft:updated,omitempty"`
	InReplyTo   *Ref         `xml:"weft:inReplyTo"`
	Root        *Ref         `xml:"weft:root"`
	Context     *Context     `xml:"weft:context"`
	Replies     *Replies     `xml:"weft:replies"`
	ReactionOf  *ReactionRef `xml:"weft:reactionOf"`
	Reactions   *Reactions   `xml:"weft:reactions"`
	Feed        *FeedRef     `xml:"weft:feed"`
}

func New(c Channel) *RSS {
	return &RSS{Version: "2.0", NSWeft: NSWeft, NSSrc: NSSource, NSAtom: NSAtom, Channel: c}
}

func RFC822(t time.Time) string { return t.UTC().Format(time.RFC1123Z) }

func (r *RSS) Marshal() ([]byte, error) {
	b, err := xml.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), b...), nil
}
