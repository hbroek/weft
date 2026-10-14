// Package feed generates RSS 2.0 feeds with the tc: namespace.
package feed

import (
	"encoding/xml"
	"time"
)

const (
	NSTextcast = "https://example.org/ns/textcast/0.1"
	NSSource   = "http://source.scripting.com/"
	NSAtom     = "http://www.w3.org/2005/Atom"
)

type RSS struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	NSTC    string   `xml:"xmlns:tc,attr"`
	NSSrc   string   `xml:"xmlns:source,attr"`
	NSAtom  string   `xml:"xmlns:atom,attr"`
	Channel Channel  `xml:"channel"`
}

type AtomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

// Ref is an element that points at a post or feed: tc:inReplyTo, tc:root, tc:feed, tc:repliesTo.
type Ref struct {
	Href string `xml:"href,attr"`
	Feed string `xml:"feed,attr,omitempty"`
}

// Context is tc:context. In this version it carries only the parent's URL.
type Context struct {
	Href      string `xml:"href,attr"`
	Retrieved string `xml:"retrieved,attr,omitempty"`
}

// Replies is tc:replies: the URL of a post's replies feed and the approved count.
type Replies struct {
	Href  string `xml:"href,attr"`
	Count int    `xml:"count,attr"`
}

type Channel struct {
	Title         string   `xml:"title"`
	Link          string   `xml:"link"`
	Description   string   `xml:"description"`
	LastBuildDate string   `xml:"lastBuildDate"`
	Self          AtomLink `xml:"atom:link"`
	Webmention    string   `xml:"tc:webmention,omitempty"`
	Following     string   `xml:"tc:following,omitempty"`
	RepliesTo     *Ref     `xml:"tc:repliesTo"`
	Depth         int      `xml:"tc:depth,omitempty"`
	Items         []Item   `xml:"item"`
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
	Updated     string   `xml:"tc:updated,omitempty"`
	InReplyTo   *Ref     `xml:"tc:inReplyTo"`
	Root        *Ref     `xml:"tc:root"`
	Context     *Context `xml:"tc:context"`
	Replies     *Replies `xml:"tc:replies"`
	Feed        *Ref     `xml:"tc:feed"`
}

func New(c Channel) *RSS {
	return &RSS{Version: "2.0", NSTC: NSTextcast, NSSrc: NSSource, NSAtom: NSAtom, Channel: c}
}

func RFC822(t time.Time) string { return t.UTC().Format(time.RFC1123Z) }

func (r *RSS) Marshal() ([]byte, error) {
	b, err := xml.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), b...), nil
}
