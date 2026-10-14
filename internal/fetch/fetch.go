// Package fetch retrieves remote feeds and pages safely: bounded size, bounded time,
// and (unless allowed) no connections to private or loopback addresses.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const maxBody = 5 << 20

// StatusError is returned when a server answers with a non-200 status.
type StatusError struct {
	Code       int
	Op, Status string
}

func (e *StatusError) Error() string { return e.Op + ": " + e.Status }

// IsGone reports whether err means the resource is gone (404 or 410).
func IsGone(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && (se.Code == http.StatusGone || se.Code == http.StatusNotFound)
}

type Client struct{ http *http.Client }

// New returns a client. With allowPrivate false, connections to loopback, private and
// link-local addresses are refused (SSRF guard); dev setups pass true.
func New(allowPrivate bool) *Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	tr := &http.Transport{
		Proxy: nil, // a proxy would bypass the address guard
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			var ips []net.IP
			if host == "localhost" || strings.HasSuffix(host, ".localhost") { // RFC 6761
				ips = []net.IP{net.IPv4(127, 0, 0, 1)}
			} else if ip := net.ParseIP(host); ip != nil {
				ips = []net.IP{ip}
			} else {
				as, err := net.DefaultResolver.LookupIPAddr(ctx, host)
				if err != nil {
					return nil, err
				}
				for _, a := range as {
					ips = append(ips, a.IP)
				}
			}
			for _, ip := range ips {
				if !allowPrivate && isPrivate(ip) {
					continue
				}
				return d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			}
			return nil, fmt.Errorf("refusing to connect to %s: private address", host)
		},
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        20,
		IdleConnTimeout:     60 * time.Second,
	}
	return &Client{http: &http.Client{
		Transport: tr,
		Timeout:   20 * time.Second,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}}
}

func isPrivate(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast()
}

// Result is a fetched document.
type Result struct {
	Header      http.Header
	Body        []byte
	ContentType string
	URL         string // final URL after redirects
}

func (c *Client) Get(ctx context.Context, rawurl string) (*Result, error) {
	u, err := url.Parse(rawurl)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("not a http(s) URL: %q", rawurl)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "text-thread-casting/0.1")
	req.Header.Set("Accept", "application/rss+xml, application/xml;q=0.9, text/html;q=0.8, */*;q=0.5")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: resp.StatusCode, Op: "GET " + rawurl, Status: resp.Status}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, errors.New("response too large")
	}
	return &Result{Header: resp.Header, Body: body, ContentType: resp.Header.Get("Content-Type"), URL: resp.Request.URL.String()}, nil
}

var (
	linkTag = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	attrRE  = regexp.MustCompile(`(?is)([a-z-]+)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
)

// FindFeedLink returns the RSS feed URL advertised by an HTML page, resolved against base.
func FindFeedLink(page []byte, base string) (string, bool) {
	b, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	for _, tag := range linkTag.FindAll(page, -1) {
		attrs := map[string]string{}
		for _, m := range attrRE.FindAllSubmatch(tag, -1) {
			v := string(m[2])
			if v == "" {
				v = string(m[3])
			}
			attrs[strings.ToLower(string(m[1]))] = v
		}
		if strings.EqualFold(attrs["type"], "application/rss+xml") && strings.Contains(strings.ToLower(attrs["rel"]), "alternate") && attrs["href"] != "" {
			if h, err := b.Parse(htmlUnescape(attrs["href"])); err == nil {
				return h.String(), true
			}
		}
	}
	return "", false
}

func htmlUnescape(s string) string {
	return strings.NewReplacer("&amp;", "&", "&#38;", "&", "&quot;", `"`, "&#39;", "'").Replace(s)
}

// LooksLikeHTML reports whether the document is an HTML page rather than a feed.
func (r *Result) LooksLikeHTML() bool {
	if strings.Contains(strings.ToLower(r.ContentType), "html") {
		return true
	}
	head := strings.ToLower(string(r.Body[:min(len(r.Body), 512)]))
	return strings.Contains(head, "<html") || strings.Contains(head, "<!doctype html")
}

// PostForm sends an application/x-www-form-urlencoded POST and returns the status code.
// Any 2xx status counts as success to the caller; the body is discarded.
func (c *Client) PostForm(ctx context.Context, rawurl string, form url.Values) (int, error) {
	u, err := url.Parse(rawurl)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return 0, fmt.Errorf("not a http(s) URL: %q", rawurl)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "text-thread-casting/0.1")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, nil
}

var linkHeader = regexp.MustCompile(`<([^>]*)>([^,<]*)`)
var relParam = regexp.MustCompile(`(?i)rel\s*=\s*(?:"([^"]*)"|([^\s;,]+))`)

func hasRel(rels, want string) bool {
	for _, r := range strings.Fields(strings.ToLower(rels)) {
		if r == want {
			return true
		}
	}
	return false
}

// FindRel returns the first URL with the given rel, looking at Link headers first, then at
// <link> and <a> tags in the page. The result is resolved against base.
func (r *Result) FindRel(rel string) (string, bool) {
	b, err := url.Parse(r.URL)
	if err != nil {
		return "", false
	}
	for _, h := range r.Header.Values("Link") {
		for _, m := range linkHeader.FindAllStringSubmatch(h, -1) {
			pm := relParam.FindStringSubmatch(m[2])
			if pm == nil {
				continue
			}
			rels := pm[1]
			if rels == "" {
				rels = pm[2]
			}
			if hasRel(rels, rel) {
				if u, err := b.Parse(m[1]); err == nil {
					return u.String(), true
				}
			}
		}
	}
	for _, tag := range tagRE.FindAll(r.Body, -1) {
		attrs := map[string]string{}
		for _, m := range attrRE.FindAllSubmatch(tag, -1) {
			v := string(m[2])
			if v == "" {
				v = string(m[3])
			}
			attrs[strings.ToLower(string(m[1]))] = v
		}
		if hasRel(attrs["rel"], rel) {
			if href, ok := attrs["href"]; ok {
				if u, err := b.Parse(htmlUnescape(href)); err == nil {
					return u.String(), true
				}
			}
		}
	}
	return "", false
}

var tagRE = regexp.MustCompile(`(?is)<(?:link|a)\b[^>]*>`)
