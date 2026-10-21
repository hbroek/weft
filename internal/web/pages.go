package web

import (
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hbroek/weft/internal/feed"
	"github.com/hbroek/weft/internal/store"
)

var layout = template.Must(template.New("layout").Parse(`{{define "head"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
<link rel="alternate" type="application/rss+xml" href="{{.FeedURL}}">
<link rel="webmention" href="{{.WebmentionURL}}">
{{range .Links}}<link rel="{{.Rel}}"{{if .Type}} type="{{.Type}}"{{end}} href="{{.Href}}">
{{end}}<style>body{font:16px/1.5 system-ui,sans-serif;max-width:40rem;margin:2rem auto;padding:0 1rem}
article{margin:2rem 0;border-bottom:1px solid #8884;padding-bottom:1rem}time{color:#888;font-size:.85em}
nav a,nav button{margin-right:1rem}nav form{display:inline}
nav button,.linkbtn{background:none;border:0;padding:0;font:inherit;color:LinkText;cursor:pointer;text-decoration:underline}
input[type=text],input[type=password],textarea{width:100%;box-sizing:border-box;font:inherit;padding:.4rem}
ul.thread,ul.thread ul{list-style:none;padding-left:1rem;border-left:2px solid #8883}.node{margin:.75rem 0}.note{display:block;color:#a60}.focus{scroll-margin-top:4rem;background:#8881;padding:.25rem .5rem;border-radius:4px}
textarea{min-height:14rem;font-family:ui-monospace,monospace}.err{color:#b00}label{display:block;margin-top:1rem}</style>
</head><body>
<header><h1><a href="/">{{.Author}}</a></h1><p>{{.Bio}}</p>
<nav>{{if .Owner}}<a href="/compose">Compose</a><a href="/timeline">Timeline</a><a href="/following">Following</a><a href="/replies">Replies</a>
<form method="post" action="/logout"><button>Log out</button></form>{{else}}<a href="/login">Log in</a>{{end}}</nav></header>
<main>{{end}}
{{define "foot"}}</main></body></html>{{end}}
{{define "posts"}}{{template "head" .}}
{{range .Posts}}<article class="h-entry">
{{if .Title}}<h2 class="p-name"><a class="u-url" href="{{.URL}}">{{.Title}}</a></h2>{{end}}
{{if .InReplyTo}}<p><small>In reply to <a class="u-in-reply-to" href="{{.InReplyTo}}">{{.InReplyTo}}</a></small></p>{{end}}
<div class="e-content">{{.HTML}}</div>
<a class="u-url" href="{{.URL}}"><time class="dt-published" datetime="{{.ISO}}">{{.Date}}</time></a>
{{if .Edited}} <time>(edited)</time>{{end}}
{{if $.Owner}} <a href="{{.URL}}/edit">Edit</a>{{if .HasThread}} <a href="{{.ThreadHref}}">Thread</a>{{end}}{{end}}
{{if .Tally}}<p class="tally" title="Reactions from people you follow">{{.Tally}}</p>{{end}}
</article>{{else}}<p>No posts yet.</p>{{end}}
{{if .Replies}}<section><h3>Replies</h3>{{range .Replies}}<div class="reply">
<p>{{if .AuthorHref}}<a href="{{.AuthorHref}}"><strong>{{.Author}}</strong></a>{{else}}<strong>{{.Author}}</strong>{{end}} <a href="{{.Source}}"><time>{{.Date}}</time></a></p>{{if .Title}}<h4>{{.Title}}</h4>{{end}}{{.HTML}}</div>{{end}}</section>{{end}}
{{if and .Owner .Pending}}<p><a href="/replies">{{.Pending}} reply(ies) awaiting your approval</a></p>{{end}}
{{template "foot"}}{{end}}
{{define "reaction"}}{{template "head" .}}<article class="h-entry">
<p class="p-content">{{.React.Author}} reacted {{.React.Emoji}} ({{.React.Polarity}}) to
<a class="u-like-of" href="{{.React.Target}}">{{.React.Target}}</a></p></article>
{{template "foot"}}{{end}}
{{define "tombstone"}}{{template "head" .}}<p>This post was removed by its author.</p>{{template "foot"}}{{end}}
{{define "login"}}{{template "head" .}}<form method="post" action="/login">
<h2>Log in to {{.Author}}</h2>{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<label>Password <input type="password" name="password" autofocus required></label>
<p><button>Log in</button></p></form>{{template "foot"}}{{end}}
{{define "timeline"}}{{template "head" .}}<h2>{{if .Only}}{{.Heading}}</h2><p><small>{{.Only}} · <a href="/timeline">All feeds</a> · <a href="/following">Following</a></small></p>{{else}}Timeline</h2>{{end}}
{{range .Timeline}}<article>
<p>{{if .AuthorHref}}<a href="{{.AuthorHref}}"><strong>{{.Author}}</strong></a>{{else}}<strong>{{.Author}}</strong>{{end}}{{if .InReplyTo}} replying to <a href="{{.InReplyTo}}">a post</a>{{end}}</p>
{{if .Title}}<h3>{{if .Link}}<a href="{{.Link}}">{{.Title}}</a>{{else}}{{.Title}}{{end}}</h3>{{end}}
<div>{{.HTML}}</div>
<p><a href="{{.ReplyHref}}">Reply</a>{{if .HasThread}} <a href="{{.ThreadHref}}">Thread</a>{{end}}</p>
{{if .Target}}<form method="post" action="/react" class="react">
<input type="hidden" name="target" value="{{.Target}}"><input type="hidden" name="feed" value="{{.FeedURL}}">
<button name="polarity" value="+" title="Positive">&#x1F44D;</button> <button name="polarity" value="-" title="Negative">&#x1F44E;</button>
<input type="text" name="emoji" size="4" maxlength="16" placeholder="emoji" aria-label="Optional emoji"></form>
{{if .Mine}}<small>You reacted {{.Mine}}</small>
<form method="post" action="/unreact" style="display:inline"><input type="hidden" name="target" value="{{.Target}}"><button class="linkbtn">undo</button></form>{{end}}
{{if .Tally}}<small>{{.Tally}}</small>{{end}}{{end}}
{{if .Link}}<a href="{{.Link}}"><time>{{.Date}}</time></a>{{else}}<time>{{.Date}}</time>{{end}}{{if .Edited}} <time>(edited)</time>{{end}}
</article>{{else}}<p>Nothing yet. <a href="/following">Follow a feed</a>.</p>{{end}}
{{template "foot"}}{{end}}
{{define "following"}}{{template "head" .}}<h2>Following</h2>{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/following"><label>Feed or site URL <input type="text" name="url" placeholder="http://bob.localhost:8080/" required></label>
<p><button>Follow</button></p></form>
<ul>{{range .Follows}}<li><a href="/timeline?feed={{.URL | urlquery}}">{{if .Title}}{{.Title}}{{else}}{{.URL}}{{end}}</a> <small>{{.URL}}{{if .Err}} <span class="err">{{.Err}}</span>{{end}}</small>
<form method="post" action="/following/remove" style="display:inline"><input type="hidden" name="url" value="{{.URL}}"><button class="linkbtn">Unfollow</button></form></li>{{else}}<li>Not following anyone yet.</li>{{end}}</ul>
<form method="post" action="/following/refresh"><button>Refresh now</button></form>
<p><small>Your follow list is public: <a href="/following.opml">following.opml</a></small></p>
{{template "foot"}}{{end}}
{{define "thread"}}{{template "head" .}}<h2>Thread</h2>
<p>{{if eq .Thread.View "focus"}}<strong>This branch</strong>{{else}}<a href="{{.Thread.FocusHref}}">This branch</a>{{end}} |
{{if eq .Thread.View "curated"}}<strong>Whole thread</strong>{{else}}<a href="{{.Thread.CuratedHref}}">Whole thread</a>{{end}} |
{{if eq .Thread.View "raw"}}<strong>Raw</strong>{{else}}<a href="{{.Thread.RawHref}}">Raw</a>{{end}}
<small>({{.Thread.Count}} posts)</small></p>
<p><small>{{if eq .Thread.View "focus"}}The post, the posts above it back to the root, and its replies. Use “replies →” on any post to open that branch.{{else if eq .Thread.View "curated"}}Only the replies each author approved, read down from the root.{{else}}Everything your reader knows about this conversation, including replies that were not approved.{{end}}</small></p>
<ul class="thread">{{template "node" .Thread.Root}}</ul>
{{template "foot"}}{{end}}
{{define "node"}}<li><div{{if .Focus}} id="focus"{{end}} class="node{{if .Focus}} focus{{end}}">
{{if .Tombstone}}<em>original post removed</em> <small>{{.URL}}</small>
{{else if .Missing}}<em>{{.Note}}</em> <a href="{{.URL}}">{{.URL}}</a>
{{else}}{{if .AuthorHref}}<a href="{{.AuthorHref}}"><strong>{{.Author}}</strong></a>{{else}}<strong>{{.Author}}</strong>{{end}} <a href="{{.URL}}"><time>{{.Date}}</time></a>{{if .Title}} <em>{{.Title}}</em>{{end}}
{{.HTML}}{{if .Note}}<small class="note">{{.Note}}</small>{{end}}{{end}}
{{if .ReplyHref}}<a href="{{.ReplyHref}}">Reply</a>{{end}}
{{if .DrillHref}} <a href="{{.DrillHref}}">{{if gt .Replies 0}}{{.Replies}} {{if .Other}}other {{end}}{{if eq .Replies 1}}reply{{else}}replies{{end}} &rarr;{{else if .Other}}other replies &rarr;{{else}}replies &rarr;{{end}}</a>{{end}}</div>
{{if .Children}}<ul>{{range .Children}}{{template "node" .}}{{end}}</ul>{{end}}</li>{{end}}
{{define "moderation"}}{{template "head" .}}<h2>Replies to your posts</h2>
{{range .Moderation}}<article>
<p><strong>{{.Author}}</strong> on <a href="{{.PostURL}}">your post {{.PostID}}</a> <small>({{.Status}})</small></p>
{{if .Title}}<h4>{{.Title}}</h4>{{end}}{{.HTML}}
<p><a href="{{.Source}}">{{.Source}}</a></p>
<form method="post" action="/replies/{{.ID}}/status">
{{if ne .Status "approved"}}<button name="status" value="approved">Approve</button>{{end}}
{{if ne .Status "rejected"}}<button name="status" value="rejected">Reject</button>{{end}}</form>
</article>{{else}}<p>No replies yet.</p>{{end}}
{{template "foot"}}{{end}}
{{define "editor"}}{{template "head" .}}<form method="post" action="{{.Action}}">
<h2>{{.Heading}}</h2>
{{if .ReplyTo}}<p>Replying to <a href="{{.ReplyTo}}">{{.ReplyTo}}</a></p><input type="hidden" name="reply_to" value="{{.ReplyTo}}"><input type="hidden" name="reply_feed" value="{{.ReplyFeed}}">{{end}}
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<label>Title (optional) <input type="text" name="title" value="{{.Form.Title}}"></label>
<label>Markdown <textarea name="markdown" required>{{.Form.Markdown}}</textarea></label>
<p><button>{{.Button}}</button></p></form>
{{if .Delete}}<form method="post" action="{{.Delete}}" onsubmit="return confirm('Delete this post?')"><button class="linkbtn">Delete post</button></form>{{end}}
{{template "foot"}}{{end}}`))

type pagePost struct {
	Title, URL, ISO, Date string
	HTML                  template.HTML
	Edited                bool
	InReplyTo             string
	Tally                 string
	ThreadHref            string
	HasThread             bool // it replies to something or has approved replies
}

type pageData struct {
	Title, FeedURL, WebmentionURL, Author, Bio string
	Owner                                      bool
	Posts                                      []pagePost
	Timeline                                   []tlRow
	Replies                                    []replyRow
	Moderation                                 []modRow
	Pending                                    int
	React                                      *reactData
	Thread                                     *threadPage
	ReplyTo, ReplyFeed                         string
	Follows                                    []followRow
	Links                                      []feed.AtomLink // extra <link>s in the page head
	// forms
	Error, Action, Heading, Button, Delete, Only string
	Form                                         struct{ Title, Markdown string }
}

func (s *Server) data(r *http.Request, q *req, title string) pageData {
	return pageData{Title: title, FeedURL: s.base(r) + "/feed.xml", WebmentionURL: s.base(r) + "/webmention",
		Author: q.host.Display, Bio: q.host.Bio, Owner: q.owner}
}

func (s *Server) show(w http.ResponseWriter, name string, status int, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := layout.ExecuteTemplate(w, name, d); err != nil {
		// headers are sent; nothing more to do than log through the body
		w.Write([]byte("template error"))
	}
}

func (s *Server) renderPosts(w http.ResponseWriter, r *http.Request, q *req, title string, posts []*store.Post) {
	d := s.data(r, q, title)
	tallies, _ := s.st.Tallies(q.host.ID)
	counts, _ := s.st.ApprovedCounts(q.host.ID)
	for _, p := range posts {
		d.Posts = append(d.Posts, pagePost{
			Title: p.Title, URL: s.postURL(r, p.ID), HTML: template.HTML(p.HTML), // rendered by goldmark in safe mode
			ISO: p.Created.UTC().Format("2006-01-02T15:04:05Z"), Date: p.Created.Format("2 Jan 2006 15:04"),
			Edited: p.Updated != nil, InReplyTo: p.InReplyTo, Tally: tallyText(tallies[p.ID]), ThreadHref: threadHref(s.postURL(r, p.ID), ""),
			HasThread: p.InReplyTo != "" || counts[p.ID] > 0,
		})
	}
	if len(posts) == 1 && !posts[0].Deleted {
		d.Replies, d.Pending = s.repliesFor(q, posts[0].ID)
		// the same links the one-item feed carries, for readers that start from the page: the post
		// as a feed item (same URL, Accept: application/rss+xml), its parent and its replies feed
		u := s.postURL(r, posts[0].ID)
		d.Links = append(d.Links, feed.AtomLink{Rel: "alternate", Type: "application/rss+xml", Href: u})
		if posts[0].InReplyTo != "" {
			d.Links = append(d.Links, feed.AtomLink{Rel: "in-reply-to", Href: posts[0].InReplyTo})
		}
		d.Links = append(d.Links, feed.AtomLink{Rel: "replies", Type: "application/rss+xml", Href: u + "/replies.xml"})
	}
	s.show(w, "posts", http.StatusOK, d)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request, q *req) {
	posts, err := s.st.RecentPosts(q.host.ID, 50)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.renderPosts(w, r, q, q.host.Display, posts)
}

func postID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil
}

func (s *Server) permalink(w http.ResponseWriter, r *http.Request, q *req) {
	p, err := s.st.PostBySlug(q.host.ID, r.PathValue("slug"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if p.Deleted {
		s.show(w, "tombstone", http.StatusGone, s.data(r, q, "Post removed"))
		return
	}
	w.Header().Add("Vary", "Accept")
	switch negotiate(r.Header.Get("Accept")) {
	case "markdown":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		io.WriteString(w, p.Markdown)
		return
	case "rss":
		s.writeFeed(w, r, q, []*store.Post{p}) // this one post, as a feed item with all its weft: links
		return
	}
	title := p.Title
	if title == "" {
		title = q.host.Display
	}
	s.renderPosts(w, r, q, title, []*store.Post{p})
}

// negotiate picks the representation of a post that the Accept header asks for: "markdown",
// "rss" (the post as a one-item feed) or "html". A client that also accepts HTML or anything at
// all gets HTML, so browsers and plain page fetches are unaffected.
func negotiate(accept string) string {
	want, anyHTML := "", false
	for _, part := range strings.Split(accept, ",") {
		mt, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		switch strings.ToLower(mt) {
		case "text/html", "application/xhtml+xml", "*/*", "text/*":
			anyHTML = true
		case "text/markdown":
			if want == "" && !anyHTML {
				want = "markdown"
			}
		case "application/rss+xml":
			if want == "" {
				want = "rss"
			}
		}
	}
	if want == "rss" && anyHTML {
		return "html"
	}
	if want == "" {
		return "html"
	}
	return want
}

// feedPageSize is how many posts one page of feed.xml holds.
const feedPageSize = 100

// feed serves the newest posts, and older ones as pages (/feed.xml?page=2, …) that are linked
// with rel="next" and rel="previous", as in RFC 5005 paged feeds.
func (s *Server) feed(w http.ResponseWriter, r *http.Request, q *req) {
	page := 1
	if v := r.URL.Query().Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			http.NotFound(w, r)
			return
		}
		page = n
	}
	posts, err := s.st.PostsPage(q.host.ID, feedPageSize+1, (page-1)*feedPageSize)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if page > 1 && len(posts) == 0 {
		http.NotFound(w, r)
		return
	}
	more := len(posts) > feedPageSize
	if more {
		posts = posts[:feedPageSize]
	}
	at := func(n int) string {
		if n == 1 {
			return s.base(r) + "/feed.xml"
		}
		return s.base(r) + "/feed.xml?page=" + strconv.Itoa(n)
	}
	links := []feed.AtomLink{{Href: at(page), Rel: "self", Type: "application/rss+xml"}}
	if page > 1 {
		links = append(links, feed.AtomLink{Href: at(1), Rel: "first", Type: "application/rss+xml"},
			feed.AtomLink{Href: at(page - 1), Rel: "previous", Type: "application/rss+xml"})
	}
	if more {
		links = append(links, feed.AtomLink{Href: at(page + 1), Rel: "next", Type: "application/rss+xml"})
	}
	s.writeFeed(w, r, q, posts, links)
}

// writeFeed writes the host user's feed channel holding the given posts. links default to rel="self".
func (s *Server) writeFeed(w http.ResponseWriter, r *http.Request, q *req, posts []*store.Post, links ...[]feed.AtomLink) {
	selfLinks := []feed.AtomLink{{Href: s.base(r) + "/feed.xml", Rel: "self", Type: "application/rss+xml"}}
	if len(links) > 0 {
		selfLinks = links[0]
	}
	ch := feed.Channel{
		Title: q.host.Display, Link: s.base(r) + "/", Description: q.host.Bio,
		Links:      selfLinks,
		Webmention: s.base(r) + "/webmention",
		Following:  s.base(r) + "/following.opml", ReactionsFeed: s.base(r) + "/reactions.xml",
	}
	counts, _ := s.st.ApprovedCounts(q.host.ID)
	tallies, _ := s.st.Tallies(q.host.ID)
	var last time.Time
	for _, p := range posts {
		if p.Created.After(last) {
			last = p.Created
		}
		url := s.postURL(r, p.ID)
		it := feed.Item{Title: p.Title, GUID: feed.GUID{Value: "urn:uuid:" + p.UID}, Link: url,
			Description: p.HTML, Markdown: p.Markdown, PubDate: feed.RFC822(p.Created)}
		it.Replies = &feed.Replies{Href: url + "/replies.xml", Count: counts[p.ID]}
		it.Reactions = &feed.Reactions{}
		if t := tallies[p.ID]; t != nil {
			it.Reactions.Plus, it.Reactions.Minus = t.Plus, t.Minus
			for _, e := range t.Emoji {
				it.Reactions.Emoji = append(it.Reactions.Emoji, feed.EmojiCount{Value: e.Emoji, Count: e.Count})
			}
		}
		if p.InReplyTo != "" {
			it.InReplyTo = &feed.Ref{Href: p.InReplyTo, Feed: p.InReplyToFeed}
			it.Root = &feed.Ref{Href: p.Root, Feed: p.RootFeed}
			it.Context = &feed.Context{Href: p.InReplyTo, Retrieved: feed.RFC822(p.Created)}
		}
		if p.Updated != nil {
			it.Updated = feed.RFC822(*p.Updated)
			if p.Updated.After(last) {
				last = *p.Updated
			}
		}
		ch.Items = append(ch.Items, it)
	}
	if last.IsZero() {
		last = time.Now()
	}
	ch.LastBuildDate = feed.RFC822(last)
	b, err := feed.New(ch).Marshal()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write(b)
}
