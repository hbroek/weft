package feed

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"time"
)

const (
	nsAtom   = "http://www.w3.org/2005/Atom"
	nsThread = "http://purl.org/syndication/thread/1.0"
)

// rootName is the namespace and local name of the document's root element.
func rootName(data []byte) (space, local string) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.CharsetReader = charsetReader
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Space, se.Name.Local
		}
	}
}

// atomText is an Atom text construct: plain text (the default), escaped HTML, or inline XHTML.
type atomText struct {
	Type  string `xml:"type,attr"`
	Text  string `xml:",chardata"`
	Inner string `xml:",innerxml"`
}

// plain is the text with markup removed.
func (t atomText) plain() string {
	switch strings.ToLower(strings.TrimSpace(t.Type)) {
	case "xhtml":
		return StripHTML(t.Inner)
	case "html", "text/html":
		return StripHTML(t.Text)
	}
	return strings.Join(strings.Fields(t.Text), " ")
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
	Type string `xml:"type,attr"`
}

// canonicalLink is the address a link with rel="canonical" gives: where the entry first appeared.
func canonicalLink(links []atomLink) string {
	for _, l := range links {
		if strings.EqualFold(strings.TrimSpace(l.Rel), "canonical") {
			return httpAddress(strings.TrimSpace(l.Href))
		}
	}
	return ""
}

// alternate picks the address of the page the entry or feed is about: the link whose rel is
// "alternate" (the default when there is none), preferring a web page over another media type.
func alternate(links []atomLink) string {
	best := ""
	for _, l := range links {
		if rel := strings.ToLower(strings.TrimSpace(l.Rel)); (rel == "" || rel == "alternate") && strings.TrimSpace(l.Href) != "" {
			if t := strings.ToLower(l.Type); t == "" || strings.Contains(t, "html") {
				return strings.TrimSpace(l.Href)
			}
			if best == "" {
				best = strings.TrimSpace(l.Href)
			}
		}
	}
	return best
}

// parseAtom reads an Atom 1.0 feed. Like the RSS reader, it takes what a timeline needs: the feed's
// title, link, logo and rel="next" page, and each entry's address, title, text, dates, categories and
// the post it replies to (RFC 4685).
func parseAtom(data []byte, now time.Time) (*Parsed, error) {
	var x struct {
		XMLName  xml.Name   `xml:"http://www.w3.org/2005/Atom feed"`
		Title    atomText   `xml:"title"`
		Subtitle atomText   `xml:"subtitle"`
		Links    []atomLink `xml:"link"`
		Logo     string     `xml:"logo"`
		Icon     string     `xml:"icon"`
		Entries  []struct {
			ID        string     `xml:"id"`
			Title     atomText   `xml:"title"`
			Summary   atomText   `xml:"summary"`
			Content   atomText   `xml:"content"`
			Links     []atomLink `xml:"link"`
			Published string     `xml:"published"`
			Updated   string     `xml:"updated"`
			Created   string     `xml:"created"`
			Category  []struct {
				Term  string `xml:"term,attr"`
				Label string `xml:"label,attr"`
			} `xml:"category"`
			InReplyTo struct {
				Ref  string `xml:"ref,attr"`
				Href string `xml:"href,attr"`
			} `xml:"http://purl.org/syndication/thread/1.0 in-reply-to"`
		} `xml:"entry"`
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.CharsetReader = charsetReader
	if err := dec.Decode(&x); err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New("not a readable Atom feed: " + err.Error())
	}
	p := &Parsed{Title: x.Title.plain(), Link: alternate(x.Links), Description: x.Subtitle.plain(),
		Image: httpAddress(orStr(x.Logo, x.Icon))}
	for _, l := range x.Links {
		if strings.EqualFold(strings.TrimSpace(l.Rel), "next") && strings.TrimSpace(l.Href) != "" {
			p.Next = strings.TrimSpace(l.Href)
		}
	}
	for _, e := range x.Entries {
		link := alternate(e.Links)
		guid := orStr(link, strings.TrimSpace(e.ID))
		if strings.TrimSpace(guid) == "" {
			continue // cannot identify the entry
		}
		body := e.Content.plain()
		if body == "" {
			body = e.Summary.plain()
		}
		pub := parseDate(orStr(e.Published, orStr(e.Created, e.Updated)))
		if pub.IsZero() {
			pub = now
		}
		it := ParsedItem{GUID: strings.TrimSpace(guid), UID: strings.TrimSpace(e.ID), Link: link, Title: e.Title.plain(),
			Text: body, Published: pub, RepliesCount: -1, Canonical: canonicalLink(e.Links)}
		if e.Published != "" {
			if u := parseDate(e.Updated); !u.IsZero() && u.After(pub) {
				it.Updated = u
			}
		}
		if r := strings.TrimSpace(orStr(e.InReplyTo.Href, e.InReplyTo.Ref)); httpAddress(r) != "" {
			it.InReplyTo = r
		}
		var cats []string
		for _, c := range e.Category {
			cats = append(cats, orStr(c.Label, c.Term))
		}
		it.Keywords = Keywords(cats)
		p.Items = append(p.Items, it)
	}
	return p, nil
}
