package web

import (
	"html/template"
	"io"
	"net/http"
	"net/url"
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
{{end}}<style>body{font:16px/1.5 system-ui,sans-serif;max-width:40rem;margin:2rem auto;padding:0 1rem;background:#f3f4f6;color:#1c1c1e}
article,.node{background:#fff;border:1px solid #0000000f;border-radius:12px;box-shadow:0 1px 2px #0000001a,0 4px 14px #0000000d;padding:.9rem 1.1rem}
article{margin:1rem 0}time{color:#888;font-size:.85em}
nav a,nav button{margin-right:1rem}nav form{display:inline}
nav button,.linkbtn{background:none;border:0;padding:0;font:inherit;color:LinkText;cursor:pointer;text-decoration:underline}
input[type=text],input[type=password],textarea{width:100%;box-sizing:border-box;font:inherit;padding:.4rem}
ul.thread,ul.thread ul{list-style:none;padding-left:1rem;border-left:2px solid #0002}.node{margin:.75rem 0}
article img,.node img,#preview img{max-width:100%;max-height:28rem;height:auto;object-fit:contain;border-radius:8px}
article,.node{overflow-wrap:anywhere}.note{display:block;color:#a60}.focus{scroll-margin-top:4rem;background:#f4f8ff;box-shadow:0 0 0 2px #1a6fe055,0 4px 14px #0000000d}
.react{display:flex;align-items:center;gap:.25rem;margin:.25rem 0}
.likebtn{display:inline-flex;align-items:center;background:none;border:0;padding:.15rem .25rem;color:inherit;cursor:pointer}
.likebtn svg{width:1.4rem;height:1.4rem;fill:none;stroke:currentColor;stroke-width:2;stroke-linejoin:round}
.likebtn[aria-pressed=true]{color:#1a6fe0}.likebtn[aria-pressed=true] svg{fill:currentColor}
article.likeevent{color:GrayText;font-size:.9rem;background:none;border:0;box-shadow:none;padding:0 1.1rem;margin:.6rem 0}
.kw{display:flex;flex-wrap:wrap;gap:.35rem;margin:.5rem 0 0}.kw span,.kw a{text-decoration:none;background:#0000000a;border-radius:999px;padding:.05rem .65rem;font-size:.85em;color:#444}.kw a:hover{background:#0000001a}.kwhead small{color:#555}
.tally{cursor:help;text-decoration:underline dotted;text-underline-offset:3px}
#preview .empty{color:GrayText}
.sr{position:absolute;left:-9999px}
textarea{min-height:14rem;font-family:ui-monospace,monospace}.err{color:#b00}label{display:block;margin-top:1rem}</style>
</head><body>
<header><h1><a href="/">{{.Author}}</a></h1><p>{{.Bio}}</p>
<nav>{{if .Owner}}<a href="/compose">Compose</a><a href="/timeline">Timeline</a><a href="/following">Following</a><a href="/replies">Replies</a>
<form method="post" action="/logout"><button>Log out</button></form>{{else}}<a href="/login">Log in</a>{{end}}</nav></header>
<main>{{end}}
{{define "foot"}}</main></body></html>{{end}}
{{define "posts"}}{{template "head" .}}
{{if .Keyword}}<p class="kwhead">Posts tagged <strong>{{.Keyword}}</strong> <small>· <a href="/">All posts</a> · <a href="{{.TopicFeed}}">Feed for this topic</a></small></p>{{end}}
{{if .Bookmarklet}}<p class="bm"><small>Bookmark card: drag <a href="{{.Bookmarklet}}">Weft it</a> to your bookmarks bar, then click it on any page to compose a post from that page.</small></p>{{end}}
{{range .Posts}}<article class="h-entry">
{{if .Title}}<h2 class="p-name"><a class="u-url" href="{{.URL}}">{{.Title}}</a></h2>{{end}}
{{if .InReplyTo}}<p><small>In reply to <a class="u-in-reply-to" href="{{.InReplyTo}}">{{.InReplyTo}}</a></small></p>{{end}}
<div class="e-content">{{.HTML}}</div>
{{if .Keywords}}<p class="kw">{{range .Keywords}}<a class="p-category" href="/?keyword={{.}}">{{.}}</a>{{end}}</p>{{end}}
<a class="u-url" href="{{.URL}}"><time class="dt-published" datetime="{{.ISO}}">{{.Date}}</time></a>
{{if .Edited}} <time>(edited)</time>{{end}}
{{if $.Owner}} <a href="{{.URL}}/edit">Edit</a>{{if .HasThread}} <a href="{{.ThreadHref}}">Thread</a>{{end}}{{end}}
{{if .Tally}}<p class="tally" title="{{.TallyTip}}">{{.Tally}}</p>{{end}}
</article>{{else}}<p>No posts yet.</p>{{end}}
{{if .Replies}}<section><h3>Replies</h3>{{range .Replies}}<div class="reply">
<p>{{if .AuthorHref}}<a href="{{.AuthorHref}}"><strong>{{.Author}}</strong></a>{{else}}<strong>{{.Author}}</strong>{{end}} <a href="{{.Source}}"><time>{{.Date}}</time></a></p>{{if .Title}}<h4>{{.Title}}</h4>{{end}}{{.HTML}}</div>{{end}}</section>{{end}}
{{if and .Owner .Pending}}<p><a href="/replies">{{.Pending}} reply(ies) awaiting your approval</a></p>{{end}}
{{template "foot"}}{{end}}
{{define "like"}}{{template "head" .}}<article class="h-entry">
<p class="p-content">{{.Like.Author}} liked
<a class="u-like-of" href="{{.Like.Target}}">{{.Like.Target}}</a></p></article>
{{template "foot"}}{{end}}
{{define "tombstone"}}{{template "head" .}}<p>This post was removed by its author.</p>{{template "foot"}}{{end}}
{{define "login"}}{{template "head" .}}<form method="post" action="/login">
<h2>Log in to {{.Author}}</h2>{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<label>Password <input type="password" name="password" autofocus required></label>
<p><button>Log in</button></p></form>{{template "foot"}}{{end}}
{{define "timeline"}}{{template "head" .}}{{if .Keyword}}<p class="kwhead">Tagged <strong>{{.Keyword}}</strong>{{if .Only}} from <strong>{{.Heading}}</strong>{{end}} <small>· <a href="{{.ClearKeyword}}">Clear filter</a></small></p>{{end}}<h2>{{if .Only}}{{.Heading}}</h2><p><small>{{.Only}} · <a href="/timeline">All feeds</a> · <a href="/following">Following</a></small></p>{{else}}Timeline</h2>{{end}}
{{if not .Keyword}}<p><small><a href="{{.LikesToggle}}">{{if .ShowLikes}}Hide likes{{else}}Show likes{{end}}</a></small></p>{{end}}
{{range .Timeline}}{{if .IsLike}}<article class="likeevent"><p>{{if .AuthorHref}}<a href="{{.AuthorHref}}"><strong>{{.Author}}</strong></a>{{else}}<strong>{{.Author}}</strong>{{end}} liked {{if .LikedHref}}<a href="{{.LikedHref}}">{{.LikedAs}}</a>{{else}}{{.LikedAs}}{{end}} <small><time>{{.Date}}</time></small></p></article>
{{else}}<article>
<p>{{if .AuthorHref}}<a href="{{.AuthorHref}}"><strong>{{.Author}}</strong></a>{{else}}<strong>{{.Author}}</strong>{{end}}{{if .InReplyTo}} replying to <a href="{{.InReplyTo}}">a post</a>{{end}}</p>
{{if .Title}}<h3>{{if .Link}}<a href="{{.Link}}">{{.Title}}</a>{{else}}{{.Title}}{{end}}</h3>{{end}}
<div>{{.HTML}}</div>
{{if .Keywords}}<p class="kw">{{range .Keywords}}{{if $.Only}}<a href="/timeline?feed={{$.Only}}&amp;keyword={{.}}" title="Posts with this keyword in this feed">{{.}}</a>{{else}}<a href="/timeline?keyword={{.}}" title="Posts with this keyword from everyone you follow">{{.}}</a>{{end}}{{end}}</p>{{end}}
<p><a href="{{.ReplyHref}}">Reply</a>{{if .HasThread}} <a href="{{.ThreadHref}}">Thread</a>{{end}}</p>
{{if .Target}}<form method="post" action="{{if .Mine}}/unlike{{else}}/like{{end}}" class="react"><input type="hidden" name="target" value="{{.Target}}"><input type="hidden" name="feed" value="{{.FeedURL}}">
<button class="likebtn" aria-pressed="{{if .Mine}}true{{else}}false{{end}}" title="{{if .Mine}}Unlike{{else}}Like{{end}}"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 2.5 21.5 12 12 21.5 2.5 12z"/></svg><span class="sr">{{if .Mine}}Unlike{{else}}Like{{end}}</span></button>{{if .Tally}} <small class="tally" title="{{.TallyTip}}">{{.Tally}}</small>{{end}}</form>{{end}}
{{if .Link}}<a href="{{.Link}}"><time>{{.Date}}</time></a>{{else}}<time>{{.Date}}</time>{{end}}{{if .Edited}} <time>(edited)</time>{{end}}
</article>{{end}}{{else}}<p>Nothing yet. <a href="/following">Follow a feed</a>.</p>{{end}}
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
<label>Keywords (optional, separated by commas) <input type="text" name="keywords" value="{{.Form.Keywords}}" placeholder="pickleball, open web, Dave Winer"></label>
<p><button>{{.Button}}</button></p></form>
<h3>Preview</h3>
<article id="preview" class="h-entry"><h3 id="preview-title"{{if not .Form.Title}} hidden{{end}}>{{.Form.Title}}</h3><div id="preview-body">{{if .Preview}}{{.Preview}}{{else}}<p class="empty">Nothing to preview yet.</p>{{end}}</div>
<p class="kw" id="preview-kw"{{if not .PreviewKeywords}} hidden{{end}}>{{range .PreviewKeywords}}<span>{{.}}</span>{{end}}</p></article>
<script>
(function(){
var md=document.querySelector('textarea[name=markdown]'),ti=document.querySelector('input[name=title]'),kw=document.querySelector('input[name=keywords]'),kwbox=document.getElementById('preview-kw'),
 body=document.getElementById('preview-body'),head=document.getElementById('preview-title'),timer,seq=0,shown=0;
if(!md||!body)return;
function title(){head.textContent=ti.value.trim();head.hidden=!ti.value.trim()}
function chips(){
 var seen={},out=[];
 kw.value.split(',').forEach(function(k){k=k.replace(/\s+/g,' ').trim();if(k&&!seen[k.toLowerCase()]&&out.length<10){seen[k.toLowerCase()]=1;out.push(k.slice(0,50))}});
 kwbox.textContent='';out.forEach(function(k){var s=document.createElement('span');s.textContent=k;kwbox.appendChild(s)});kwbox.hidden=!out.length;
}
function render(){
 var n=++seq;
 fetch('/preview',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'markdown='+encodeURIComponent(md.value),credentials:'same-origin'})
  .then(function(r){return r.ok?r.text():null})
  .then(function(h){if(h===null||n<shown)return;shown=n;body.innerHTML=h.trim()?h:'<p class="empty">Nothing to preview yet.</p>'})
  .catch(function(){});
}
md.addEventListener('input',function(){clearTimeout(timer);timer=setTimeout(render,200)});
ti.addEventListener('input',title);
if(kw&&kwbox)kw.addEventListener('input',chips);
})();
</script>
{{if .Delete}}<form method="post" action="{{.Delete}}" onsubmit="return confirm('Delete this post?')"><button class="linkbtn">Delete post</button></form>{{end}}
{{template "foot"}}{{end}}`))

type pagePost struct {
	Title, URL, ISO, Date string
	HTML                  template.HTML
	Edited                bool
	InReplyTo             string
	Keywords              []string
	Tally, TallyTip       string
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
	Like                                       *likeData
	Thread                                     *threadPage
	ReplyTo, ReplyFeed                         string
	Follows                                    []followRow
	Links                                      []feed.AtomLink // extra <link>s in the page head
	// forms
	Error, Action, Heading, Button, Delete, Only string
	Preview                                      template.HTML // the editor's rendered Markdown
	PreviewKeywords                              []string      // the editor's keywords as chips
	ShowLikes                                    bool
	ClearKeyword                                 string
	Keyword, TopicFeed                           string       // a keyword filter on the home page, and the feed of just those posts
	Bookmarklet                                  template.URL // javascript: link that sends the current page to /compose; owner's home page only
	LikesToggle                                  string
	Form                                         struct{ Title, Markdown, Keywords string }
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

func (s *Server) renderPosts(w http.ResponseWriter, r *http.Request, q *req, title string, posts []*store.Post, tweak ...func(*pageData)) {
	d := s.data(r, q, title)
	tallies, _ := s.st.Tallies(q.host.ID)
	likers, _ := s.st.Likers(q.host.ID)
	counts, _ := s.st.ApprovedCounts(q.host.ID)
	for _, p := range posts {
		d.Posts = append(d.Posts, pagePost{
			Title: p.Title, URL: s.postURL(r, p.ID), HTML: template.HTML(p.HTML), // rendered by goldmark in safe mode
			ISO: p.Created.UTC().Format("2006-01-02T15:04:05Z"), Date: p.Created.Format("2 Jan 2006 15:04"),
			Edited: p.Updated != nil, InReplyTo: p.InReplyTo, Keywords: p.Keywords, Tally: likesText(tallies[p.ID]), TallyTip: likedBy(likers[p.ID], tallies[p.ID]), ThreadHref: threadHref(s.postURL(r, p.ID), ""),
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
	for _, f := range tweak {
		f(&d)
	}
	s.show(w, "posts", http.StatusOK, d)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request, q *req) {
	kw := feed.Keyword(r.FormValue("keyword"))
	var posts []*store.Post
	var err error
	if kw != "" {
		posts, err = s.st.PostsWithKeyword(q.host.ID, kw, 50, 0)
	} else {
		posts, err = s.st.RecentPosts(q.host.ID, 50)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.renderPosts(w, r, q, q.host.Display, posts, func(d *pageData) {
		if kw != "" {
			// the page's feed is the topic feed, so following this address follows just these posts
			d.Keyword, d.TopicFeed = kw, s.topicFeed(r, kw)
			d.FeedURL = d.TopicFeed
			d.Title = q.host.Display + ": " + kw
		} else if q.owner {
			d.Bookmarklet = bookmarklet(s.base(r) + "/compose")
		}
	})
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
		s.writeFeed(w, r, q, []*store.Post{p}, "") // this one post, as a feed item with all its weft: links
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
	// ?category=kw is a topic feed: only the posts with that keyword, paged the same way
	kw := feed.Keyword(r.URL.Query().Get("category"))
	var posts []*store.Post
	var err error
	if kw != "" {
		posts, err = s.st.PostsWithKeyword(q.host.ID, kw, feedPageSize+1, (page-1)*feedPageSize)
	} else {
		posts, err = s.st.PostsPage(q.host.ID, feedPageSize+1, (page-1)*feedPageSize)
	}
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
		v := url.Values{}
		if kw != "" {
			v.Set("category", kw)
		}
		if n > 1 {
			v.Set("page", strconv.Itoa(n))
		}
		if len(v) == 0 {
			return s.base(r) + "/feed.xml"
		}
		return s.base(r) + "/feed.xml?" + v.Encode()
	}
	links := []feed.AtomLink{{Href: at(page), Rel: "self", Type: "application/rss+xml"}}
	if page > 1 {
		links = append(links, feed.AtomLink{Href: at(1), Rel: "first", Type: "application/rss+xml"},
			feed.AtomLink{Href: at(page - 1), Rel: "previous", Type: "application/rss+xml"})
	}
	if more {
		links = append(links, feed.AtomLink{Href: at(page + 1), Rel: "next", Type: "application/rss+xml"})
	}
	title := ""
	if kw != "" {
		title = q.host.Display + ": " + kw
	}
	s.writeFeed(w, r, q, posts, title, links)
}

// topicFeed is the address of the feed of this host's posts with the keyword.
func (s *Server) topicFeed(r *http.Request, kw string) string {
	return s.base(r) + "/feed.xml?category=" + url.QueryEscape(kw)
}

// writeFeed writes the host user's feed channel holding the given posts. links default to rel="self".
func (s *Server) writeFeed(w http.ResponseWriter, r *http.Request, q *req, posts []*store.Post, title string, links ...[]feed.AtomLink) {
	if title == "" {
		title = q.host.Display
	}
	selfLinks := []feed.AtomLink{{Href: s.base(r) + "/feed.xml", Rel: "self", Type: "application/rss+xml"}}
	if len(links) > 0 {
		selfLinks = links[0]
	}
	ch := feed.Channel{
		Title: title, Link: s.base(r) + "/", Description: q.host.Bio,
		Links:      selfLinks,
		Webmention: s.base(r) + "/webmention",
		Following:  s.base(r) + "/following.opml", LikesFeed: s.base(r) + "/likes.xml",
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
		it.Likes = &feed.Likes{Count: tallies[p.ID]}
		it.Categories = p.Keywords
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
