package feed

import (
	"encoding/xml"
	"errors"
	"html"
	"regexp"
	"strings"
	"time"
)

// Parsed is the reader's view of an RSS 2.0 feed, textcast or not.
type Parsed struct {
	Title, Link, Description string
	Webmention, Following    string
	Items                    []ParsedItem
}

type ParsedItem struct {
	GUID, Link, Title string
	Text              string // description with markup removed; safe to show once escaped
	Markdown          string // source:markdown, if the feed has it
	Published         time.Time
	Updated           time.Time // zero if never edited
	InReplyTo         string    // tc:inReplyTo href, if any
}

type xmlParsed struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Title       string `xml:"title"`
		Link        string `xml:"link"`
		Description string `xml:"description"`
		Webmention  string `xml:"https://example.org/ns/textcast/0.1 webmention"`
		Following   string `xml:"https://example.org/ns/textcast/0.1 following"`
		Items       []struct {
			Title       string `xml:"title"`
			GUID        string `xml:"guid"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			PubDate     string `xml:"pubDate"`
			Markdown    string `xml:"http://source.scripting.com/ markdown"`
			Updated     string `xml:"https://example.org/ns/textcast/0.1 updated"`
			InReplyTo   struct {
				Href string `xml:"href,attr"`
			} `xml:"https://example.org/ns/textcast/0.1 inReplyTo"`
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
	if err := xml.Unmarshal(data, &x); err != nil {
		return nil, errors.New("not an RSS 2.0 feed: " + err.Error())
	}
	p := &Parsed{
		Title: strings.TrimSpace(x.Channel.Title), Link: strings.TrimSpace(x.Channel.Link),
		Description: strings.TrimSpace(x.Channel.Description),
		Webmention:  strings.TrimSpace(x.Channel.Webmention), Following: strings.TrimSpace(x.Channel.Following),
	}
	for _, it := range x.Channel.Items {
		pi := ParsedItem{
			GUID: strings.TrimSpace(it.GUID), Link: strings.TrimSpace(it.Link), Title: strings.TrimSpace(it.Title),
			Text: StripHTML(it.Description), Markdown: strings.TrimSpace(it.Markdown),
			Published: parseDate(it.PubDate), Updated: parseDate(it.Updated),
			InReplyTo: strings.TrimSpace(it.InReplyTo.Href),
		}
		if pi.GUID == "" {
			pi.GUID = pi.Link
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
