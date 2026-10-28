// Package feed generates RSS 2.0 feeds with the weft: namespace.
package feed

import (
	"encoding/xml"
	"time"
)

const (
	NSWeft = "https://weftprotocol.org/ns/weft/0.1"
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

// Context is weft:context: the parent's URL, when it was retrieved, and optionally what the parent
// looked like then (its title, a short description and an image address). The description is for
// replies to pages that publish no feed, which a reader cannot look up.
type Context struct {
	Href        string `xml:"href,attr"`
	Retrieved   string `xml:"retrieved,attr,omitempty"`
	Title       string `xml:"title,attr,omitempty"`
	Description string `xml:"description,attr,omitempty"`
	Image       string `xml:"image,attr,omitempty"`
}

// FeedRef is weft:feed: who wrote an item, by feed URL, with an optional display name as text.
type FeedRef struct {
	Href string `xml:"href,attr"`
	Name string `xml:",chardata"`
}

// LikeRef is weft:likeOf: the item is a like of the post at Href.
// LikeRef is weft:likeOf: the address of the liked page, and optionally what it looked like (its
// title, a short description and an image), for a like of a page that publishes no feed.
type LikeRef struct {
	Href        string `xml:"href,attr"`
	Title       string `xml:"title,attr,omitempty"`
	Description string `xml:"description,attr,omitempty"`
	Image       string `xml:"image,attr,omitempty"`
}

// Likes is weft:likes: a post's count of verified likes from trusted senders, and where all the likes are listed.
type Likes struct {
	Count int    `xml:"count,attr"`
	Href  string `xml:"href,attr,omitempty"` // the post's liked-by feed: everyone whose like it received
}

// Replies is weft:replies: the URL of a post's replies feed and the approved count.
type Replies struct {
	Href  string `xml:"href,attr"`
	Count int    `xml:"count,attr"`
}

type Channel struct {
	Title         string        `xml:"title"`
	Link          string        `xml:"link"`
	Description   string        `xml:"description"`
	LastBuildDate string        `xml:"lastBuildDate"`
	Image         *ChannelImage `xml:"image,omitempty"` // the author's avatar (RSS 2.0 channel image)
	Links         []AtomLink    `xml:"atom:link"`       // rel="self", and rel="next"/"previous"/"first" for paged feeds
	Webmention    string        `xml:"weft:webmention,omitempty"`
	Following     string        `xml:"weft:following,omitempty"`
	LikesFeed     string        `xml:"weft:likesFeed,omitempty"`
	RepliesTo     *Ref          `xml:"weft:repliesTo"`
	Depth         int           `xml:"weft:depth,omitempty"`
	Items         []Item        `xml:"item"`
}

// ChannelImage is the RSS 2.0 <image> of a channel; Weft uses it as the author's avatar.
type ChannelImage struct {
	URL   string `xml:"url"`
	Title string `xml:"title"`
	Link  string `xml:"link"`
}

type GUID struct {
	IsPermaLink bool   `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type Item struct {
	Title       string   `xml:"title,omitempty"`
	GUID        GUID     `xml:"guid"`
	Link        string   `xml:"link"`
	Description string   `xml:"description"`
	Markdown    string   `xml:"source:markdown,omitempty"`
	PubDate     string   `xml:"pubDate"`
	Categories  []string `xml:"category"` // the keywords
	Updated     string   `xml:"weft:updated,omitempty"`
	InReplyTo   *Ref     `xml:"weft:inReplyTo"`
	Root        *Ref     `xml:"weft:root"`
	Context     *Context `xml:"weft:context"`
	Replies     *Replies `xml:"weft:replies"`
	LikeOf      *LikeRef `xml:"weft:likeOf"`
	Likes       *Likes   `xml:"weft:likes"`
	Feed        *FeedRef `xml:"weft:feed"`
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
