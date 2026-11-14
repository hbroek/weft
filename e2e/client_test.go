package e2e

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

// client is a small script-style user of the public REST API: one user's instance and bearer token,
// nothing else. It is all the end-to-end tests need to act as a person or a bot.
type client struct {
	Base  string // e.g. http://dana.localhost:8080
	Token string
	HTTP  *http.Client
}

// newClient makes *.localhost names resolve to loopback (RFC 6761), since the Go resolver does not
// do that everywhere.
func newClient(base, token string) *client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err == nil && strings.HasSuffix(host, ".localhost") {
			addr = net.JoinHostPort("127.0.0.1", port)
		}
		return d.DialContext(ctx, network, addr)
	}}
	return &client{Base: strings.TrimRight(base, "/"), Token: token, HTTP: &http.Client{Transport: tr, Timeout: 30 * time.Second}}
}

func (c *client) FeedURL() string { return c.Base + "/feed.xml" }

func (c *client) do(ctx context.Context, method, path string, in, out any) error {
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

// reply is a reply someone sent to one of the user's posts.
type reply struct {
	ID     int64  `json:"id"`
	Source string `json:"source"`
	Author string `json:"author"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

type posted struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}

// threadNode is one post of a thread, depth-first from the root.
type threadNode struct {
	URL    string `json:"url"`
	Author string `json:"author"`
	Text   string `json:"text"`
	Depth  int    `json:"depth"`
}

func (c *client) Replies(ctx context.Context, status string) (out []reply, err error) {
	err = c.do(ctx, "GET", "/api/replies?status="+status, nil, &out)
	return
}

func (c *client) Thread(ctx context.Context, target string) (out []threadNode, err error) {
	err = c.do(ctx, "GET", "/api/thread?url="+url.QueryEscape(target), nil, &out)
	return
}

func (c *client) Follow(ctx context.Context, feedURL string) error {
	return c.do(ctx, "POST", "/api/following", map[string]string{"url": feedURL}, nil)
}

func (c *client) Post(ctx context.Context, title, markdown, inReplyTo, inReplyToFeed string) (p posted, err error) {
	err = c.do(ctx, "POST", "/api/posts", map[string]string{"title": title, "markdown": markdown,
		"in_reply_to": inReplyTo, "in_reply_to_feed": inReplyToFeed}, &p)
	return
}

func (c *client) Like(ctx context.Context, target, targetFeed string) error {
	return c.do(ctx, "POST", "/api/likes", map[string]string{"target": target, "target_feed": targetFeed}, nil)
}
