package feed

import (
	"bytes"
	"encoding/xml"
	"errors"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Parsed is the reader's view of an RSS 2.0 feed, Weft or not.
type Parsed struct {
	Title, Link, Description string
	Webmention, Following    string
	LikesFeed                string
	Image                    string // the channel image address (an http(s) URL), the author's avatar
	// Next is the rel="next" link of a paged feed (RFC 5005): the page of older items.
	Next  string
	Items []ParsedItem
}

type ParsedItem struct {
	// GUID is the key the reader files the item under: the item's permalink when it has one,
	// otherwise its guid. UID is the item's own guid, its identity if its URL changes (a Weft
	// post's is urn:uuid:…).
	GUID, UID, Link, Title string
	Parent                 ParentInfo // weft:context title, description and image
	Text                   string     // description with markup removed; safe to show once escaped
	Markdown               string     // source:markdown, if the feed has it
	Published              time.Time
	Updated                time.Time // zero if never edited
	InReplyTo              string    // weft:inReplyTo href, if any
	InReplyToFeed          string
	Root, RootFeed         string // weft:root
	// weft:likeOf: set when the item is a like rather than a post.
	LikeOf             string
	Likes              int      // weft:likes count
	RepliesHref        string   // weft:replies href: where this item's approved replies are published
	RepliesCount       int      // weft:replies count; -1 when the item does not say
	FeedHref, FeedName string   // weft:feed: the author's feed and display name (replies feeds)
	Keywords           []string // the item's <category> elements, cleaned
}

// plainLinks collects <link> elements. encoding/xml matches an unqualified name in any
// namespace, so atom:link (rel="self") would otherwise overwrite the real link.
type plainLinks []struct {
	XMLName xml.Name
	Text    string `xml:",chardata"`
}

func (l plainLinks) first() string {
	for _, e := range l {
		if e.XMLName.Space == "" && strings.TrimSpace(e.Text) != "" {
			return strings.TrimSpace(e.Text)
		}
	}
	return ""
}

type xmlParsed struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Image struct {
			URL string `xml:"url"`
		} `xml:"image"`
		Title     string `xml:"title"`
		AtomLinks []struct {
			Rel  string `xml:"rel,attr"`
			Href string `xml:"href,attr"`
		} `xml:"http://www.w3.org/2005/Atom link"`
		Link         plainLinks `xml:"link"`
		Description  string     `xml:"description"`
		Webmention   string     `xml:"https://weftseed.com/ns/weft/0.1 webmention"`
		Following    string     `xml:"https://weftseed.com/ns/weft/0.1 following"`
		LikesFeed    string     `xml:"https://weftseed.com/ns/weft/0.1 likesFeed"`
		OldReactions string     `xml:"https://weftseed.com/ns/weft/0.1 reactionsFeed"` // before likes: read as the likes feed
		Items        []struct {
			Title       string     `xml:"title"`
			GUID        string     `xml:"guid"`
			Link        plainLinks `xml:"link"`
			Description string     `xml:"description"`
			PubDate     string     `xml:"pubDate"`
			Categories  []string   `xml:"category"`
			Markdown    string     `xml:"http://source.scripting.com/ markdown"`
			Updated     string     `xml:"https://weftseed.com/ns/weft/0.1 updated"`
			InReplyTo   struct {
				Href string `xml:"href,attr"`
				Feed string `xml:"feed,attr"`
			} `xml:"https://weftseed.com/ns/weft/0.1 inReplyTo"`
			Root struct {
				Href string `xml:"href,attr"`
				Feed string `xml:"feed,attr"`
			} `xml:"https://weftseed.com/ns/weft/0.1 root"`
			LikeOf struct {
				Title       string `xml:"title,attr"`
				Description string `xml:"description,attr"`
				Image       string `xml:"image,attr"`
				Href        string `xml:"href,attr"`
			} `xml:"https://weftseed.com/ns/weft/0.1 likeOf"`
			// Before there were only likes, an item could be a reaction with a polarity; a "+" is a like.
			ReactionOf struct {
				Href     string `xml:"href,attr"`
				Polarity string `xml:"polarity,attr"`
			} `xml:"https://weftseed.com/ns/weft/0.1 reactionOf"`
			Replies struct {
				Href  string `xml:"href,attr"`
				Count *int   `xml:"count,attr"`
			} `xml:"https://weftseed.com/ns/weft/0.1 replies"`
			Feed struct {
				Href string `xml:"href,attr"`
				Name string `xml:",chardata"`
			} `xml:"https://weftseed.com/ns/weft/0.1 feed"`
			Context struct {
				Title       string `xml:"title,attr"`
				Description string `xml:"description,attr"`
				Image       string `xml:"image,attr"`
			} `xml:"https://weftseed.com/ns/weft/0.1 context"`
			Likes struct {
				Count int `xml:"count,attr"`
			} `xml:"https://weftseed.com/ns/weft/0.1 likes"`
			OldReactions struct {
				Plus int `xml:"plus,attr"`
			} `xml:"https://weftseed.com/ns/weft/0.1 reactions"`
		} `xml:"item"`
	} `xml:"channel"`
}

var dateLayouts = []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC3339}

func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

var (
	breakTags = regexp.MustCompile(`(?i)<\s*(br\s*/?|/p|/div|/li|/h[1-6])\s*>`)
	anyTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	manyNL    = regexp.MustCompile(`\n{3,}`)
)

const maxText = 5000

// StripHTML reduces an HTML fragment to plain text. The result still needs escaping on output.
func StripHTML(s string) string {
	s = breakTags.ReplaceAllString(s, "\n\n")
	s = anyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = manyNL.ReplaceAllString(strings.TrimSpace(s), "\n\n")
	if r := []rune(s); len(r) > maxText {
		s = string(r[:maxText]) + "…"
	}
	return s
}

// Parse reads an RSS 2.0 document. now stamps items that carry no usable date.
func Parse(data []byte, now time.Time) (*Parsed, error) {
	var x xmlParsed
	// feeds written before the rename declare the old namespace URI; read them as the new one
	data = bytes.ReplaceAll(data, []byte(NSLegacy), []byte(NSWeft))
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, errors.New("not an RSS 2.0 feed: " + err.Error())
	}
	p := &Parsed{
		Title: strings.TrimSpace(x.Channel.Title), Link: x.Channel.Link.first(),
		Description: strings.TrimSpace(x.Channel.Description), Image: httpAddress(x.Channel.Image.URL),
		Webmention: strings.TrimSpace(x.Channel.Webmention), Following: strings.TrimSpace(x.Channel.Following),
		LikesFeed: strings.TrimSpace(orStr(x.Channel.LikesFeed, x.Channel.OldReactions)),
	}
	for _, l := range x.Channel.AtomLinks {
		if l.Rel == "next" && strings.TrimSpace(l.Href) != "" {
			p.Next = strings.TrimSpace(l.Href)
		}
	}
	for _, it := range x.Channel.Items {
		pi := ParsedItem{
			UID: strings.TrimSpace(it.GUID), Link: it.Link.first(), Title: strings.TrimSpace(it.Title),
			Text: StripHTML(it.Description), Markdown: strings.TrimSpace(it.Markdown),
			Published: parseDate(it.PubDate), Updated: parseDate(it.Updated),
			Parent:    ParentInfo{Title: it.Context.Title, Description: it.Context.Description, Image: it.Context.Image}.Clean(),
			InReplyTo: strings.TrimSpace(it.InReplyTo.Href), InReplyToFeed: strings.TrimSpace(it.InReplyTo.Feed),
			Root: strings.TrimSpace(it.Root.Href), RootFeed: strings.TrimSpace(it.Root.Feed),
			LikeOf: strings.TrimSpace(it.LikeOf.Href), RepliesHref: strings.TrimSpace(it.Replies.Href),
			FeedHref: strings.TrimSpace(it.Feed.Href), FeedName: strings.TrimSpace(it.Feed.Name),
			Likes: max(it.Likes.Count, it.OldReactions.Plus, 0), Keywords: Keywords(it.Categories),
		}
		if pi.LikeOf != "" && pi.Parent.Empty() {
			// a like carries what the liked page looked like, as the reply's weft:context does
			pi.Parent = ParentInfo{Title: it.LikeOf.Title, Description: it.LikeOf.Description, Image: it.LikeOf.Image}.Clean()
		}
		if pi.LikeOf == "" && strings.TrimSpace(it.ReactionOf.Polarity) != "-" {
			pi.LikeOf = strings.TrimSpace(it.ReactionOf.Href)
		}
		pi.RepliesCount = -1
		if it.Replies.Count != nil {
			pi.RepliesCount = *it.Replies.Count
		}
		pi.GUID = pi.Link
		if pi.GUID == "" {
			pi.GUID = pi.UID
		}
		if pi.GUID == "" {
			continue // cannot identify the item
		}
		if pi.Published.IsZero() {
			pi.Published = now
		}
		p.Items = append(p.Items, pi)
	}
	return p, nil
}

func orStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// httpAddress returns s if it is an absolute http(s) URL of sensible length, else "".
func httpAddress(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2048 {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}
