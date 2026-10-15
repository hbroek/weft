// Package agent is an AI participant that uses only the public REST API of a textcast instance.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one user's instance with that user's bearer token.
type Client struct {
	Base  string // e.g. http://dana.localhost:8080
	Token string
	HTTP  *http.Client
}

// NewClient returns a client whose *.localhost names resolve to loopback (RFC 6761), since the
// Go resolver does not do that everywhere.
func NewClient(base, token string) *Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err == nil && strings.HasSuffix(host, ".localhost") {
			addr = net.JoinHostPort("127.0.0.1", port)
		}
		return d.DialContext(ctx, network, addr)
	}}
	return &Client{Base: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Transport: tr, Timeout: 30 * time.Second}}
}

// FeedURL is the agent user's own feed, used to avoid replying to itself.
func (c *Client) FeedURL() string { return c.Base + "/feed.xml" }

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, path, res.Status, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Item is one entry of the agent's timeline.
type Item struct {
	FeedURL   string    `json:"feed_url"`
	FeedTitle string    `json:"feed_title"`
	GUID      string    `json:"guid"`
	Link      string    `json:"link"`
	Title     string    `json:"title"`
	Text      string    `json:"text"`
	Markdown  string    `json:"markdown"`
	Published time.Time `json:"published"`
	InReplyTo string    `json:"in_reply_to"`
}

// Reply is a reply someone sent to one of the agent's posts.
type Reply struct {
	ID     int64  `json:"id"`
	PostID int64  `json:"post_id"`
	Source string `json:"source"`
	Author string `json:"author"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

type Posted struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}

func (c *Client) Timeline(ctx context.Context) (out []Item, err error) {
	err = c.do(ctx, "GET", "/api/timeline", nil, &out)
	return
}

// ThreadNode is one post of a thread, depth-first from the root.
type ThreadNode struct {
	URL        string `json:"url"`
	Author     string `json:"author"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	Depth      int    `json:"depth"`
	Parent     string `json:"parent"`
	Note       string `json:"note"`
	Unapproved bool   `json:"unapproved"`
	Tombstone  bool   `json:"tombstone"`
	Missing    bool   `json:"missing"`
}

// Thread returns the whole thread that the post at target belongs to (the curated view).
func (c *Client) Thread(ctx context.Context, target string) (out []ThreadNode, err error) {
	err = c.do(ctx, "GET", "/api/thread?url="+url.QueryEscape(target), nil, &out)
	return
}

func (c *Client) Replies(ctx context.Context, status string) (out []Reply, err error) {
	err = c.do(ctx, "GET", "/api/replies?status="+status, nil, &out)
	return
}

func (c *Client) Follow(ctx context.Context, feedURL string) error {
	return c.do(ctx, "POST", "/api/following", map[string]string{"url": feedURL}, nil)
}

func (c *Client) Post(ctx context.Context, title, markdown, inReplyTo, inReplyToFeed string) (p Posted, err error) {
	err = c.do(ctx, "POST", "/api/posts", map[string]string{"title": title, "markdown": markdown,
		"in_reply_to": inReplyTo, "in_reply_to_feed": inReplyToFeed}, &p)
	return
}

func (c *Client) React(ctx context.Context, target, targetFeed, polarity, emoji string) error {
	return c.do(ctx, "POST", "/api/reactions", map[string]string{"target": target, "target_feed": targetFeed,
		"polarity": polarity, "emoji": emoji}, nil)
}
