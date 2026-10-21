package feed

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"time"
)

// parseRDF reads an RSS 1.0 document (an rdf:RDF root with the items beside the channel, as Slashdot
// publishes). Only what a timeline needs is read: the channel's title and link, and each item's
// address, title, description and dc:date.
func parseRDF(data []byte, now time.Time) (*Parsed, error) {
	var x struct {
		XMLName xml.Name `xml:"RDF"`
		Channel struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
		} `xml:"channel"`
		Items []struct {
			About       string `xml:"about,attr"`
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			Date        string `xml:"date"` // dc:date
		} `xml:"item"`
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.CharsetReader = charsetReader
	if err := dec.Decode(&x); err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New("not a readable RSS 1.0 feed: " + err.Error())
	}
	p := &Parsed{Title: strings.TrimSpace(x.Channel.Title), Link: strings.TrimSpace(x.Channel.Link), Description: strings.TrimSpace(x.Channel.Description)}
	for _, it := range x.Items {
		link := strings.TrimSpace(orStr(it.Link, it.About))
		if link == "" {
			continue // cannot identify the item
		}
		pub := parseDate(it.Date)
		if pub.IsZero() {
			pub = now
		}
		p.Items = append(p.Items, ParsedItem{GUID: link, UID: link, Link: link, Title: strings.TrimSpace(it.Title),
			Text: StripHTML(it.Description), Published: pub, RepliesCount: -1})
	}
	return p, nil
}
