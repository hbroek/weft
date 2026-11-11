package web

import (
	"bytes"
	"html"
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

var layout = template.Must(template.New("layout").Funcs(template.FuncMap{"day": func(t time.Time) string { return t.Format("2 January 2006") }, "copyScript": copyHTML}).Parse(`{{define "head"}}<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
{{if .Desc}}<meta name="description" content="{{.Desc}}">
{{end}}<meta property="og:title" content="{{.Title}}">
<meta property="og:type" content="{{.OGType}}">
<meta property="og:url" content="{{.OGURL}}">
<meta property="og:site_name" content="{{.Author}}">
{{if .Desc}}<meta property="og:description" content="{{.Desc}}">
{{end}}{{if .OGImage}}<meta property="og:image" content="{{.OGImage}}">
<meta name="twitter:card" content="summary_large_image">
{{else}}<meta name="twitter:card" content="summary">
{{end}}<link rel="alternate" type="application/rss+xml" href="{{.FeedURL}}">
<link rel="icon" href="/static/weft-icon.svg" type="image/svg+xml">
<link rel="icon" href="/favicon.ico" sizes="any">
<link rel="apple-touch-icon" href="/static/apple-touch-icon.png">
<link rel="manifest" href="/static/site.webmanifest">
<meta name="theme-color" content="#26337a">
<link rel="webmention" href="{{.WebmentionURL}}">
{{range .Links}}<link rel="{{.Rel}}"{{if .Type}} type="{{.Type}}"{{end}} href="{{.Href}}">
{{end}}<link rel="stylesheet" href="/static/weft.css">
<script>try{var t=localStorage.getItem('weft-theme');if(t==='light'||t==='dark')document.documentElement.setAttribute('data-theme',t)}catch(e){}</script>
</head><body>
<header class="top"><div class="top-in">
<div class="top-l"><a class="brand" href="/" title="{{.Author}}"><img src="/static/weft-icon.svg" alt="" width="36" height="36"><span>Weft</span></a></div>
{{if and .Owner (not .Forced)}}<nav class="menu"><a href="/compose"{{if eq .Nav "compose"}} class="on" aria-current="page"{{end}}>Compose</a><a href="/timeline"{{if eq .Nav "timeline"}} class="on" aria-current="page"{{end}}>Timeline</a><a href="/following"{{if eq .Nav "following"}} class="on" aria-current="page"{{end}}>Following</a><a href="/replies"{{if eq .Nav "replies"}} class="on" aria-current="page"{{end}}>Replies</a></nav>{{end}}
<div class="top-r"><a class="site" href="/">{{template "avatar" .}}<span>{{.Author}}</span></a>
<button type="button" class="th" id="theme" title="Theme: auto" aria-label="Theme: auto"><svg class="t-auto" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 3v18" /><path d="M12 3a9 9 0 0 1 0 18z" fill="currentColor"/></svg><svg class="t-light" viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg><svg class="t-dark" viewBox="0 0 24 24" aria-hidden="true"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg></button>
{{if .Owner}}<form method="post" action="/logout"><button class="btn">Log out</button></form>{{else}}<a class="btn" href="/login">Log in</a>{{end}}</div></div></header>
<script>(function(){var b=document.getElementById('theme'),r=document.documentElement,o=['auto','light','dark'];function cur(){return r.getAttribute('data-theme')||'auto'}function label(){b.title='Theme: '+cur()+' (click to change)';b.setAttribute('aria-label',b.title)}label();b.addEventListener('click',function(){var n=o[(o.indexOf(cur())+1)%3];if(n==='auto')r.removeAttribute('data-theme');else r.setAttribute('data-theme',n);try{if(n==='auto')localStorage.removeItem('weft-theme');else localStorage.setItem('weft-theme',n)}catch(e){}label()})})()</script>
<div class="wrap">
{{if .DomainWarn}}<p class="err" role="alert">{{.DomainWarn}} <a href="/profile">Fix it on your profile page</a></p>{{end}}
{{if .Home}}{{if or .Bio .Owner}}<p class="bio">{{.Bio}}{{if .Owner}} <small><a href="/profile">Edit profile</a></small>{{end}}</p>{{end}}{{end}}
<main>{{end}}
{{define "foot"}}</main><footer><small><a href="https://weftseed.com"><img src="/static/weft-icon.svg" alt="" width="18" height="18"> Published with Weft</a></small></footer></div>
<script>
(function(){
// a post in a list that is longer than about 20 lines is folded, with a button to open it.
// Kept cheap on purpose: one pass reads every height, then one pass changes what needs it, and a picture that
// loads later is looked at on its own. (Checking every post again for every picture froze the page.)
var posts=[].slice.call(document.querySelectorAll('.clip'));
if(!posts.length)return;
var limit=(parseFloat(getComputedStyle(posts[0]).fontSize)||16)*33;
function toggle(c,b){var o=c.classList.toggle('open');b.textContent=o?'Show less':'Show more';b.setAttribute('aria-expanded',o?'true':'false')}
function fold(c){
 c.dataset.fold='1';c.classList.add('clamped');
 var b=document.createElement('button');b.type='button';b.className='clipbtn';b.textContent='Show more';b.setAttribute('aria-expanded','false');
 b.addEventListener('click',function(){toggle(c,b)});
 c.after(b);
}
function check(list){
 var long=list.filter(function(c){return c.dataset.fold!=='1'&&c.scrollHeight>limit});
 long.forEach(fold);
}
check(posts);
window.addEventListener('load',function(){check(posts)});
document.addEventListener('load',function(e){var c=e.target.closest&&e.target.closest('.clip');if(c)check([c])},true);
})();
</script>{{copyScript}}</body></html>{{end}}
{{define "modes"}}{{if .Modes}}<p class="modes">{{range .Modes}}{{if .Current}}<strong>{{.Label}}</strong>{{else}}<a href="{{.Href}}">{{.Label}}</a>{{end}}{{end}}</p>{{end}}{{end}}
{{define "pagefeeds"}}{{if .PageFeeds}}<div class="pfeeds"><p><small>{{if gt (len .PageFeeds) 1}}This page offers these feeds{{else}}This page has a feed{{end}}</small></p>
{{range .PageFeeds}}<p class="pfeed"><span class="pf-t"><strong>{{if .Title}}{{.Title}}{{else}}{{.Host}}{{end}}</strong><small>{{.URL}}</small></span>{{if .Following}}<span class="following">Following ✓</span>{{else}}<form method="post" action="/following" class="followform"><input type="hidden" name="url" value="{{.URL}}"><button>Follow</button></form>{{end}}</p>
{{end}}<script>
(function(){
document.querySelectorAll('form.followform').forEach(function(f){
 f.addEventListener('submit',function(e){
  e.preventDefault();var b=f.querySelector('button'),old=b.textContent,m=f.querySelector('.err');if(m)m.remove();b.disabled=true;b.textContent='Following…';
  fetch('/following',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'url='+encodeURIComponent(f.elements.url.value),credentials:'same-origin'})
   .then(function(r){if(!r.ok)throw 0;var s=document.createElement('span');s.className='following';s.textContent='Following ✓';f.replaceWith(s)})
   .catch(function(){b.disabled=false;b.textContent=old;f.insertAdjacentHTML('beforeend',' <small class="err">Could not follow that feed.</small>')})
 })})
})();
</script></div>{{end}}{{end}}
{{define "likepage"}}{{template "head" .}}<h2>Like this page</h2>
{{template "modes" .}}{{template "pagefeeds" .}}
{{if .Parent}}{{template "parentcard" .Parent}}{{else}}<p><a href="{{.ReplyTo}}">{{.ReplyTo}}</a></p>{{end}}
<form method="post" action="/like"><input type="hidden" name="target" value="{{.ReplyTo}}"><input type="hidden" name="from" value="page">{{if not .PInfo.Empty}}<input type="hidden" name="ptitle" value="{{.PInfo.Title}}"><input type="hidden" name="pdesc" value="{{.PInfo.Description}}"><input type="hidden" name="pimg" value="{{.PInfo.Image}}">{{end}}
<p><button>Like this page</button></p></form>
<p><small>Your like goes in your likes feed, and the page is told with a Webmention if it can take one.</small></p>
{{template "foot"}}{{end}}
{{define "parentcard"}}<a class="pcard" href="{{.Href}}">{{if .Image}}<img src="{{.Image}}" alt="" width="96" height="96" loading="lazy" referrerpolicy="no-referrer" onerror="this.remove()">{{end}}<span class="pc-t"><strong>{{.Title}}</strong>{{if .Description}}<span class="pc-d">{{.Description}}</span>{{end}}<small>{{.Host}}</small></span></a>{{end}}
{{define "authorcard"}}<span class="p-author h-card" hidden><a class="u-url p-name" href="{{.SiteURL}}">{{.Author}}</a>{{if .Avatar}}<img class="u-photo" src="{{.Avatar}}" alt="" width="64" height="64" loading="lazy" referrerpolicy="no-referrer">{{end}}</span>{{end}}
{{define "avatar"}}<span class="av">{{if .Avatar}}<img src="{{.Avatar}}" alt="" width="32" height="32" loading="lazy" referrerpolicy="no-referrer" onerror="this.hidden=true;this.nextElementSibling.hidden=false"><span hidden>{{.Initial}}</span>{{else}}<span>{{.Initial}}</span>{{end}}</span>{{end}}
{{define "password"}}{{template "head" .}}<h2>{{if .Forced}}Choose a new password{{else}}Change password{{end}}</h2>
{{if .Forced}}<p>You signed in with a temporary password. Choose your own to go on.</p>{{end}}
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/password">
<label>{{if .Forced}}Temporary password{{else}}Current password{{end}} <input type="password" name="current" autocomplete="current-password" required autofocus></label>
<label>New password (at least 8 characters) <input type="password" name="new" autocomplete="new-password" minlength="8" required></label>
<label>New password again <input type="password" name="again" autocomplete="new-password" minlength="8" required></label>
<p><button>Change password</button></p></form>
{{template "foot"}}{{end}}
{{define "profile"}}{{template "head" .}}<h2>Profile</h2>{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/profile">
<label>Name <input type="text" name="display" value="{{.Profile.Display}}" maxlength="80"></label>
<label>Bio <input type="text" name="bio" value="{{.Profile.Bio}}" maxlength="500"></label>
<label>Picture from a link (optional, an http:// or https:// address{{if .Profile.Local}}; you have uploaded a picture, and a link here replaces it{{end}}) <input type="text" name="avatar" value="{{.Profile.Avatar}}" placeholder="https://example.com/me.jpg" inputmode="url"></label>
{{if .Profile.Local}}<input type="hidden" name="keep_avatar" value="1">{{end}}
<p><button>Save</button></p></form>
<hr>
<h3>Picture</h3>
<p>{{template "avatar" .}}<small> {{if .Profile.Local}}an uploaded picture{{else if .Avatar}}a picture from a link{{else}}no picture: the first letter of your name is shown{{end}}</small></p>
<form method="post" action="/profile/avatar" enctype="multipart/form-data"><label>Upload a picture <input type="file" name="file" accept="image/jpeg,image/png,image/gif" data-max="{{.UploadMax}}" required></label>
<p class="err" id="avmsg" role="alert"></p>
<p><button>Upload</button> <small>It is cropped to a square, made at most 256 pixels wide, and kept with your account. Photos from a phone are fine.</small></p></form>
{{if .Avatar}}<form method="post" action="/profile/avatar/remove"><button class="linkbtn">Remove picture</button></form>{{end}}
<script>
(function(){
var f=document.querySelector('input[type=file][name=file]'),m=document.getElementById('avmsg');
if(!f||!m)return;
f.addEventListener('change',function(){
 m.textContent='';var x=f.files[0];if(!x)return;
 if(!/^image\/(jpeg|png|gif)$/.test(x.type)){m.textContent='Only JPEG, PNG and GIF pictures can be added.';f.value='';return}
 if(x.size>+f.dataset.max){m.textContent='That file is too large: the limit is '+Math.round(f.dataset.max/1048576)+' MB.';f.value=''}
});
})();
</script>
<hr>
<p><a href="/password">Change password</a></p>
<hr>
<h3>Your data</h3>
<p><small>Everything you have here in one file: posts, replies, likes, who you follow and your login. It holds no sessions and no API token. Whoever runs a Weft server can import it to bring you there.</small></p>
<form method="post" action="/export"><button>Download my data</button></form>
{{with .Domain}}<hr>
<h3>Your address</h3>
<p>Your site is at <strong data-copy>{{.Address}}</strong>.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}{{if .Notice}}<p>{{.Notice}}</p>{{end}}
{{if .Domain}}<p>Your own domain: <strong>{{.Domain}}</strong> {{if .Active}}(verified; your old address redirects to it){{else if .Verified}}(verified, but it does not reach this server yet){{else}}(not verified yet){{end}}</p>
{{if not .Verified}}<p><small>To show that the domain is yours, add this DNS record. It can take a while to appear.</small></p>
<p><code data-copy>{{.TXTName}}</code> TXT <code data-copy>{{.TXTValue}}</code></p>{{end}}
{{if not .Active}}{{if .Target}}<p><small>{{if .Verified}}The domain is yours. Now point it at this server{{else}}To have visitors reach your site, point the domain at us too{{end}}: <code data-copy>{{.Domain}}</code> CNAME <code data-copy>{{.Target}}</code> {{if .Points}}(found){{else}}(not found yet; behind a proxy such as Cloudflare it stays hidden, which is fine){{end}}. Use a subdomain such as <code>weft.example.com</code>: a bare domain cannot be a CNAME.</small></p>{{else}}<p><small>{{if .Verified}}The domain is yours. Now point it at this server{{else}}To have visitors reach your site, point the domain at this server too{{end}} with a CNAME record whose value is this server's host name, for example <code>weft.example.net</code>: a host name only, without <code>http://</code> or a port.
{{if .Dev}}This server is in development mode and can only be reached from this computer, so a real domain cannot point at it.{{else}}Ask whoever runs this server which name to use.{{end}}</small></p>{{end}}
<p><small>Until the domain reaches this server your site stays at {{.Address}}, and nothing is redirected.</small></p>{{end}}
<form method="post" action="/domain/check">{{if not .Active}}<p><small>If the domain works, you are taken to it at once and have to sign in again there, because a sign-in belongs to one address.</small></p>{{end}}<p><button>{{if .Active}}Check that it still works{{else if .Verified}}Check again{{else}}Check now{{end}}</button></p></form>
{{if .Bound}}<p><small>Your account belongs to this domain, so it cannot be changed or removed here. To leave it, move your account to another server, or download your data.</small></p>
{{else}}<form method="post" action="/domain/remove"><p><button>Remove this domain</button></p></form>{{end}}
{{with .Lapse}}{{if .Lapsed}}<p class="err">This domain stopped verifying on {{day .Since}}. If it does not verify again by <strong>{{day .Deadline}}</strong>, your account is removed: it waits 30 days in the trash, and your posts can still be downloaded or moved until then.</p>
{{else}}<p class="err">The last {{.Fails}} check{{if gt .Fails 1}}s{{end}} of this domain failed. After {{.Left}} more, a grace period of {{.Grace}} days starts.</p>{{end}}{{end}}
{{else}}<form method="post" action="/domain"><label>Use your own domain <input type="text" name="domain" placeholder="weft.example.com" inputmode="url"></label>
<p><button>Add</button></p></form>
<p><small>Anyone who already follows you is moved to the new address by their reader. Once the domain works, you sign in again there.</small></p>{{end}}{{end}}
{{with .Move}}<hr>
<h3>Moving between servers</h3>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}{{if .Notice}}<p>{{.Notice}}</p>{{end}}
{{if .Code}}<p>Your move code. It is shown once and works for an hour:</p><p><code id="movecode">{{.Code}}</code> <button type="button" id="copycode">Copy</button> <small id="copymsg" role="status"></small></p>
<script>(function(){var b=document.getElementById('copycode'),c=document.getElementById('movecode'),m=document.getElementById('copymsg');
function done(t){m.textContent=t}
function old(){var r=document.createRange();r.selectNodeContents(c);var s=getSelection();s.removeAllRanges();s.addRange(r);try{done(document.execCommand('copy')?'Copied.':'Press Ctrl/Cmd+C to copy.')}catch(e){done('Press Ctrl/Cmd+C to copy.')}}
b.addEventListener('click',function(){if(navigator.clipboard&&navigator.clipboard.writeText)navigator.clipboard.writeText(c.textContent.trim()).then(function(){done('Copied.')},old);else old()})})()</script>
<p><small>On the new server, sign in to the account you made there (it must be empty, with the same name), open Profile, and under "Move here from another server" give <strong>{{.This}}</strong> and this code. Your account will then be at <strong>{{.Dest}}</strong>, and this address will redirect there. Until you use the code nothing changes.</small></p>{{end}}
{{if .State}}<p>A move is under way{{if eq .State "received"}}: the new server has your account, and nothing can be changed here{{end}}.</p>
<form method="post" action="/move/cancel"><p><button>Cancel the move</button></p></form>
{{if eq .State "received"}}<form method="post" action="/move/finish"><p><button>Finish the move</button> <small>Only if the new server says your account is there but this server was not told.</small></p></form>{{end}}
{{else if .Waiting}}<p>A move code was made for <strong>{{.Dest}}</strong> and has not been used yet.</p>
<form method="post" action="/move/cancel"><p><button>Cancel the move code</button></p></form>
{{else}}<h4>Move to another server</h4>
<form method="post" action="/move/code"><label>New server <input type="text" name="server" placeholder="weftbig.com" inputmode="url" required></label>
<label>Your password <input type="password" name="password" autocomplete="current-password" required></label>
<p><button>Get a move code</button> <small>Make an account with the same name on the new server first.</small></p></form>{{end}}
{{if .Receive}}<h4>Move here from another server</h4>
<form method="post" action="/move/receive"><label>The old server <input type="text" name="server" placeholder="weftseed.com" inputmode="url" required></label>
<label>Move code <input type="text" name="code" autocomplete="off" autocapitalize="off" spellcheck="false" required></label>
<p><button>Move my account here</button> <small>It replaces this empty account with the one you had there.</small></p></form>
{{else}}<p><small>To move an account here from another server, this account has to be empty; it holds {{.Why}}.</small></p>{{end}}
{{if .Pulling}}<p><a href="/move/status">See how the move is going</a></p>{{end}}{{end}}
{{template "foot"}}{{end}}
{{define "movestatus"}}{{template "head" .}}<h2>Moving your account here</h2>
{{with .Pull}}{{if .Err}}<p class="err">{{.Err}}</p>{{if not .Stuck}}<p>Nothing was changed here or on the old server. <a href="/profile">Back to your profile</a></p>{{end}}
{{else if .Done}}<p>Done. Your account is here now, and the old address redirects to it.</p><p><a href="/">Go to your site</a></p>
{{else}}<p>{{.Step}}…</p><p><small>This page refreshes by itself. Please do not post anything on the old server meanwhile.</small></p><script>setTimeout(function(){location.reload()},2000)</script>{{end}}{{end}}
{{template "foot"}}{{end}}
{{define "ic-reply"}}<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 14 4 9l5-5"/><path d="M4 9h10a6 6 0 0 1 6 6v3"/></svg>{{end}}
{{define "ic-thread"}}<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21 15a2 2 0 0 1-2 2H8l-5 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>{{end}}
{{define "ic-edit"}}<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/></svg>{{end}}
{{define "comment"}}<li class="p-comment h-cite"><div class="comment">
<div class="card-top"><p class="who">{{template "avatar" .}}{{if .AuthorHref}}<a class="p-author h-card" href="{{.AuthorHref}}"><strong class="p-name">{{.Author}}</strong></a>{{else}}<span class="p-author h-card"><strong class="p-name">{{.Author}}</strong></span>{{end}}</p>
<span class="when">{{if .Source}}<a class="u-url" href="{{.Source}}"><time class="dt-published" datetime="{{.ISO}}" title="{{.Date}}">{{.Ago}}</time></a>{{else}}<time class="dt-published" datetime="{{.ISO}}" title="{{.Date}}">{{.Ago}}</time>{{end}}{{if .Edited}} · edited{{end}}</span></div>
{{if .Title}}<h4 class="p-name">{{.Title}}</h4>{{end}}<div class="p-content">{{.HTML}}</div>
{{if .ReplyHref}}<div class="meta"><div class="m-l"></div><div class="m-r"><a class="ico" href="{{.ReplyHref}}" title="Reply" aria-label="Reply">{{template "ic-reply"}}</a></div></div>{{end}}</div>
{{if .Children}}<ol class="comments">{{range .Children}}{{template "comment" .}}{{end}}</ol>{{end}}</li>{{end}}
{{define "posts"}}{{template "head" .}}
{{if .Keyword}}<p class="kwhead">Posts tagged <strong>{{.Keyword}}</strong> <small>· <a href="/">All posts</a> · <a href="{{.TopicFeed}}">Feed for this topic</a></small></p>{{end}}
{{if .Bookmarklet}}<p class="bm"><small>Bookmark card: drag <a href="{{.Bookmarklet}}">Weft it</a> to your bookmarks bar, then click it on any page to compose a post from that page.</small></p>{{end}}
{{range .Posts}}<article class="h-entry">
{{template "authorcard" $}}
<div class="card-top">{{if .Title}}<h2 class="p-name"><a class="u-url" href="{{.URL}}">{{.Title}}</a></h2>{{else}}<span></span>{{end}}<span class="when"><a class="u-url" href="{{.URL}}"><time class="dt-published" datetime="{{.ISO}}" title="{{.Date}}">{{.Ago}}</time></a>{{if .Edited}} · edited{{end}}</span></div>
{{if .InReplyTo}}{{if .Parent}}<p><small>In reply to</small></p>{{template "parentcard" .Parent}}<a class="u-in-reply-to" href="{{.InReplyTo}}" hidden></a>{{else}}<p><small>In reply to <a class="u-in-reply-to" href="{{.InReplyTo}}">{{.InReplyTo}}</a></small></p>{{end}}{{end}}
<div class="e-content{{if $.Clip}} clip{{end}}">{{.HTML}}</div>
{{if .Canonical}}<p class="repost"><small>Originally published at <a class="u-repost-of" href="{{.Canonical}}">{{.CanonicalHost}}</a></small></p>{{end}}
{{if .Keywords}}<p class="kw">{{range .Keywords}}<a class="p-category" href="/?keyword={{.}}">{{.}}</a>{{end}}</p>{{end}}
<div class="meta"><div class="m-l">{{if .Tally}}<small class="tally" title="{{.TallyTip}}"><a href="/liked-by?target={{.URL}}">{{.Tally}}</a></small>{{end}}</div>
<div class="m-r">{{if $.Owner}}{{if .HasThread}}<a class="ico" href="{{.ThreadHref}}" title="Thread" aria-label="Thread">{{template "ic-thread"}}</a>{{end}}<a class="ico" href="{{.URL}}/edit" title="Edit" aria-label="Edit">{{template "ic-edit"}}</a>{{end}}</div></div>
</article>{{else}}<p>No posts yet.</p>{{end}}
{{if .Replies}}<section class="replies" id="replies"><h3>{{.ReplyTotal}} {{if eq .ReplyTotal 1}}reply{{else}}replies{{end}}</h3>
<ol class="comments">{{range .Replies}}{{template "comment" .}}{{end}}</ol></section>{{end}}
{{if and .Owner .Pending}}<p><a href="/replies">{{.Pending}} {{if eq .Pending 1}}reply awaits{{else}}replies await{{end}} your approval</a></p>{{end}}
{{template "foot"}}{{end}}
{{define "like"}}{{template "head" .}}<article class="h-entry">
{{template "authorcard" .}}
<p class="p-content">{{.Like.Author}} liked
<a class="u-like-of" href="{{.Like.Target}}">{{.Like.Target}}</a></p>{{if .Like.Card}}{{template "parentcard" .Like.Card}}{{end}}</article>
{{template "foot"}}{{end}}
{{define "tombstone"}}{{template "head" .}}<p>This post was removed by its author.</p>{{template "foot"}}{{end}}
{{define "login"}}{{template "head" .}}<form method="post" action="/login">
<h2>Log in to {{.Author}}</h2>{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<label>Password <input type="password" name="password" autofocus required></label>
<p><button>Log in</button></p></form>{{template "foot"}}{{end}}
{{define "timeline"}}{{template "head" .}}{{if .Keyword}}<p class="kwhead">Tagged <strong>{{.Keyword}}</strong>{{if .Only}} from <strong>{{.Heading}}</strong>{{end}} <small>· <a href="{{.ClearKeyword}}">Clear filter</a></small></p>{{end}}<h2>{{if .Only}}{{.Heading}}</h2><p><small>{{.Only}} · <a href="/timeline">All feeds</a> · <a href="/following">Following</a></small></p>{{else}}Timeline</h2>{{end}}
<p><small>{{if not .Keyword}}<a href="{{.LikesToggle}}">{{if .ShowLikes}}Hide likes{{else}}Show likes{{end}}</a>{{end}}{{if not .Only}}{{if not .Keyword}} · {{end}}<a href="{{.MineToggle}}">{{if .ShowMine}}Hide my posts{{else}}Show my posts{{end}}</a>{{end}}</small></p>
{{range .Timeline}}{{if .IsLike}}<article class="likeevent"><p>{{if .AuthorHref}}<a href="{{.AuthorHref}}"><strong>{{.Author}}</strong></a>{{else}}<strong>{{.Author}}</strong>{{end}} liked {{if .LikedHref}}<a href="{{.LikedHref}}">{{.LikedAs}}</a>{{else}}{{.LikedAs}}{{end}} <small><time>{{.Date}}</time></small>{{if .Target}} <small><a href="/liked-by?target={{.Target | urlquery}}">Who liked</a></small>{{end}}</p>{{if .Mine}}<form method="post" action="/unlike"><input type="hidden" name="target" value="{{.Target}}"><button class="linkbtn">Unlike</button></form>{{end}}</article>
{{else}}<article{{if .Anchor}} id="{{.Anchor}}"{{end}}>
<div class="card-top"><p class="who">{{template "avatar" .}}{{if .AuthorHref}}<a href="{{.AuthorHref}}"><strong>{{.Author}}</strong></a>{{else}}<strong>{{.Author}}</strong>{{end}}{{if .InReplyTo}} replying to <a href="{{.InReplyTo}}">{{if .ReplyWho}}{{.ReplyWho}}{{else if .Parent}}{{.Parent.Host}}{{else}}a post{{end}}</a>{{end}}</p>
<span class="when">{{if .Link}}<a href="{{.Link}}"><time datetime="{{.ISO}}" title="{{.Date}}">{{.Ago}}</time></a>{{else}}<time datetime="{{.ISO}}" title="{{.Date}}">{{.Ago}}</time>{{end}}{{if .Edited}} · edited{{end}}</span></div>
{{if .Parent}}{{template "parentcard" .Parent}}{{end}}
{{if .Title}}<h3>{{if .Link}}<a href="{{.Link}}">{{.Title}}</a>{{else}}{{.Title}}{{end}}</h3>{{end}}
<div class="clip">{{.HTML}}</div>
{{if .Canonical}}<p class="repost"><small>Originally published at <a href="{{.Canonical}}">{{.CanonicalHost}}</a></small></p>{{end}}
{{if .Keywords}}<p class="kw">{{range .Keywords}}{{if $.Only}}<a href="/timeline?feed={{$.Only}}&amp;keyword={{.}}" title="Posts with this keyword in this feed">{{.}}</a>{{else}}<a href="/timeline?keyword={{.}}" title="Posts with this keyword from everyone you follow">{{.}}</a>{{end}}{{end}}</p>{{end}}
<div class="meta"><div class="m-l">{{if .Own}}{{if .Tally}}<small class="tally" title="{{.TallyTip}}"><a href="/liked-by?target={{.Link}}">{{.Tally}}</a></small>{{end}}{{else if .Target}}<form method="post" action="{{if .Mine}}/unlike{{else}}/like{{end}}" class="react"><input type="hidden" name="target" value="{{.Target}}"><input type="hidden" name="feed" value="{{.FeedURL}}">
<button class="likebtn" aria-pressed="{{if .Mine}}true{{else}}false{{end}}" title="{{if .Mine}}Unlike{{else}}Like{{end}}"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 2.5 21.5 12 12 21.5 2.5 12z"/></svg><span class="sr">{{if .Mine}}Unlike{{else}}Like{{end}}</span></button>{{if .Tally}} <small class="tally" title="{{.TallyTip}}"><a href="/liked-by?target={{.Target}}">{{.Tally}}</a></small>{{end}}</form>{{end}}</div>
<div class="m-r"><a class="ico" href="{{.ReplyHref}}" title="Reply" aria-label="Reply">{{template "ic-reply"}}</a>{{if .HasThread}}<a class="ico" href="{{.ThreadHref}}" title="Thread" aria-label="Thread">{{template "ic-thread"}}</a>{{end}}{{if .Own}}<a class="ico" href="{{.EditHref}}" title="Edit" aria-label="Edit">{{template "ic-edit"}}</a>{{end}}</div></div>
</article>{{end}}{{else}}<p>{{if .Newest}}No older posts.{{else}}Nothing yet. <a href="/following">Follow a feed</a>.{{end}}</p>{{end}}
{{if or .Older .Newest}}<nav class="pager">{{if .Older}}<a href="{{.Older}}" rel="next">Older posts</a>{{end}}{{if .Newest}}<a href="{{.Newest}}">Back to the newest</a>{{end}}</nav>{{end}}
{{template "foot"}}{{end}}
{{define "likers"}}{{template "head" .}}<h2>Likes</h2>
<p><small>of <a href="{{.LikeTarget}}">{{.LikeTitle}}</a></small></p>
{{if .Likers}}<ul>{{range .Likers}}<li>{{if .FeedURL}}<a href="/timeline?feed={{.FeedURL | urlquery}}">{{.Name}}</a>{{else}}<strong>{{.Name}}</strong>{{end}}{{if .FeedURL}} <small>{{.FeedURL}}</small>{{end}}{{if .Tag}} <small>{{.Tag}}</small>{{end}}
{{if eq .State "unfollow"}}<form method="post" action="/following/remove" style="display:inline"><input type="hidden" name="url" value="{{.FeedURL}}"><button class="linkbtn">Unfollow</button></form>
{{else if eq .State "follow"}}<form method="post" action="/following" style="display:inline"><input type="hidden" name="url" value="{{.FeedURL}}"><button class="linkbtn">Follow</button></form>
{{else if eq .State "following"}}<small>Following</small>{{end}}</li>{{end}}</ul>{{else}}<p>No likes to show.</p>{{end}}
{{if .Others}}<h3>Also liked by</h3>
<p><small>People you don't follow.</small></p>
<ul>{{range .Others}}<li><a href="{{.Site}}">{{.Name}}</a> <small>{{.FeedURL}}</small>
<form method="post" action="/following" style="display:inline"><input type="hidden" name="url" value="{{.FeedURL}}"><button class="linkbtn">Follow</button></form></li>{{end}}</ul>{{end}}
{{if .LikeNote}}<p><small>{{.LikeNote}}</small></p>{{end}}
{{template "foot"}}{{end}}
{{define "images"}}{{template "head" .}}<h2>Images</h2>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<p>{{.MediaUsed}} used{{if .MediaQuota}} of {{.MediaQuota}}{{end}}. Add pictures in the <a href="/compose">editor</a>.</p>
{{if .Images}}<ul class="imgs">{{range .Images}}<li><a href="{{.Path}}"><img src="{{.Path}}?w=240" alt="" loading="lazy" width="120"></a>
<small>{{.Dims}} · {{.Size}} · {{.When}}{{if .Origin}} · copied from <a href="{{.Origin}}" rel="noreferrer">{{.Origin}}</a>{{end}}<br>
<code>{{.Path}}</code> · {{if .Used}}in {{.Used}} post{{if ne .Used 1}}s{{end}}{{else}}not used{{end}}</small>
<form method="post" action="/images/delete" style="display:inline"><input type="hidden" name="name" value="{{.Name}}">{{if .Used}}<input type="hidden" name="force" value="1"><button class="linkbtn">Delete anyway</button>{{else}}<button class="linkbtn">Delete</button>{{end}}</form></li>{{end}}</ul>{{else}}<p>No pictures yet.</p>{{end}}
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
<small>({{.Thread.Count}} {{if eq .Thread.Count 1}}post{{else}}posts{{end}})</small></p>
<p><small>{{if eq .Thread.View "focus"}}The post, the posts above it back to the root, and its replies. Use the replies icon on any post to open that branch.{{else if eq .Thread.View "curated"}}Only the replies each author approved, read down from the root.{{else}}Everything your reader knows about this conversation, including replies that were not approved.{{end}}</small></p>
<ul class="thread">{{template "node" .Thread.Root}}</ul>
{{template "foot"}}{{end}}
{{define "drilltext"}}{{if gt .Replies 0}}{{.Replies}} {{if .Other}}other {{end}}{{if eq .Replies 1}}reply{{else}}replies{{end}}{{else if .Other}}Other replies{{else}}Replies{{end}}{{end}}
{{define "ic-replies"}}<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 9a2 2 0 0 1-2 2H6l-4 4V4a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2z"/><path d="M18 9h2a2 2 0 0 1 2 2v11l-4-4h-6a2 2 0 0 1-2-2v-1"/></svg>{{end}}
{{define "node"}}<li><div{{if .Focus}} id="focus"{{end}} class="node{{if .Focus}} focus{{end}}">
{{if .Tombstone}}<em>original post removed</em> <small>{{.URL}}</small>
{{else if .Missing}}{{if .Parent}}{{template "parentcard" .Parent}}{{else}}<em>{{.Note}}</em> <a href="{{.URL}}">{{.URL}}</a>{{end}}
{{else}}{{if .AuthorHref}}<a href="{{.AuthorHref}}"><strong>{{.Author}}</strong></a>{{else}}<strong>{{.Author}}</strong>{{end}} <a href="{{.URL}}"><time>{{.Date}}</time></a>{{if .Title}} <em>{{.Title}}</em>{{end}}
{{.HTML}}{{if .Note}}<small class="note">{{.Note}}</small>{{end}}{{end}}
{{if or .ReplyHref .DrillHref}}<div class="node-actions">{{if .DrillHref}}<a class="ico" href="{{.DrillHref}}" title="{{template "drilltext" .}}" aria-label="{{template "drilltext" .}}">{{template "ic-replies"}}{{if gt .Replies 0}}<span class="cnt">{{.Replies}}</span>{{end}}</a>{{end}}{{if .ReplyHref}}<a class="ico" href="{{.ReplyHref}}" title="Reply" aria-label="Reply">{{template "ic-reply"}}</a>{{end}}</div>{{end}}</div>
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
{{define "cheatrows"}}<dl>{{range .}}<dt><code>{{.Syntax}}</code></dt><dd>{{.Shown}}</dd>{{end}}</dl>{{end}}
{{define "cheats"}}{{if .}}<div class="cheats">
<aside class="cheat cheat-l" aria-label="Markdown: text"><h3>Text</h3>{{template "cheatrows" .Left}}<p><small>A blank line starts a new paragraph. End a line with two spaces to break it there.</small></p></aside>
<aside class="cheat cheat-r" aria-label="Markdown: links, pictures and code"><h3>Links, pictures, code</h3>{{template "cheatrows" .Right}}<p><small>Web addresses become links by themselves. HTML you type is not shown.</small></p></aside>
</div>{{end}}{{end}}
{{define "editor"}}{{template "head" .}}<h2>{{.Heading}}</h2>
{{template "modes" .}}{{template "pagefeeds" .}}<div class="ed"><form method="post" action="{{.Action}}">
{{if .ReplyTo}}{{if .Parent}}<p>Replying to</p>{{template "parentcard" .Parent}}{{else}}<p>Replying to <a href="{{.ReplyTo}}">{{.ReplyTo}}</a></p>{{end}}<input type="hidden" name="reply_to" value="{{.ReplyTo}}"><input type="hidden" name="reply_feed" value="{{.ReplyFeed}}">{{if .Back}}<input type="hidden" name="back" value="{{.Back}}">{{end}}{{if not .PInfo.Empty}}<input type="hidden" name="ptitle" value="{{.PInfo.Title}}"><input type="hidden" name="pdesc" value="{{.PInfo.Description}}"><input type="hidden" name="pimg" value="{{.PInfo.Image}}">{{end}}{{end}}

{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<label class="adv">Title (optional) <input type="text" name="title" value="{{.Form.Title}}"></label>
<label><span class="adv">Markdown</span> <textarea name="markdown" aria-label="Markdown" required>{{.Form.Markdown}}</textarea></label>
<p class="imgbar"><input type="file" id="imgfile" accept="image/jpeg,image/png,image/gif" data-max="{{.ImageMax}}" data-max-compact="{{.ImageMaxCompact}}" hidden><button type="button" id="addimg">Add image</button>
<label>Size <select id="imgsize"><option value="original">Original</option><option value="compact">Compact</option></select></label>
<button type="button" id="keepimgs" title="Fetch the pictures this post shows from other sites and keep them here" hidden>Copy images</button> <small><a href="/images">Your images</a></small> <small id="imgmsg" role="status"></small></p>
<label class="adv">Keywords (optional, separated by commas) <input type="text" name="keywords" value="{{.Form.Keywords}}" autocapitalize="off" autocorrect="off" spellcheck="false" placeholder="travel, open source, weekend project"></label>
{{if not .Delete}}<label class="adv">Address <input type="text" name="slug" id="slug" value="{{.Form.Slug}}" maxlength="80"{{if .SlugAuto}} data-auto="1"{{end}} autocapitalize="off" autocorrect="off" spellcheck="false"></label>
<p class="adv"><small>The end of the post's link. It follows your title or first sentence until you change it, and cannot be changed once the post is published. A short code is added to make it yours alone.</small></p>{{end}}
<p class="adv"><label><input type="checkbox" id="repost" name="repost" value="1"{{if .Form.Canonical}} checked{{end}}> This is a repost (it first appeared somewhere else)</label></p>
<label class="adv repostfield">Canonical link <input type="text" name="canonical" value="{{.Form.Canonical}}" inputmode="url" maxlength="2000" autocapitalize="off" autocorrect="off" spellcheck="false" placeholder="https://example.com/the-original-post"></label>
<p class="adv repostfield"><small>The address of the original. This post will say where it first appeared, and its feed and page will point at it.</small></p>
<p class="advrow"><label><input type="checkbox" id="adv"{{if .Advanced}} checked{{end}}> Advanced</label> <small>title, keywords, address, repost link and Markdown help</small></p>
<p><button>{{.Button}}</button></p></form>
<h3>Preview</h3>
<article id="preview" class="h-entry"><h3 id="preview-title"{{if not .Form.Title}} hidden{{end}}>{{.Form.Title}}</h3><div id="preview-body">{{if .Preview}}{{.Preview}}{{else}}<p class="empty">Nothing to preview yet.</p>{{end}}</div>
<p class="kw" id="preview-kw"{{if not .PreviewKeywords}} hidden{{end}}>{{range .PreviewKeywords}}<span>{{.}}</span>{{end}}</p></article>
{{template "cheats" .Cheat}}</div>
<script>
(function(){
var md=document.querySelector('textarea[name=markdown]'),ti=document.querySelector('input[name=title]'),kw=document.querySelector('input[name=keywords]'),kwbox=document.getElementById('preview-kw'),
 body=document.getElementById('preview-body'),head=document.getElementById('preview-title'),sl=document.getElementById('slug'),adv=document.getElementById('adv'),timer,seq=0,shown=0,auto=!!(sl&&sl.dataset.auto);
if(!md||!body)return;
if(adv){try{if(!adv.checked&&localStorage.getItem('weft-advanced')==='1')adv.checked=true}catch(e){}
 adv.addEventListener('change',function(){try{localStorage.setItem('weft-advanced',adv.checked?'1':'0')}catch(e){}})}
function title(){head.textContent=ti.value.trim();head.hidden=!ti.value.trim()}
function chips(){
 var seen={},out=[];
 kw.value.split(',').forEach(function(k){k=k.replace(/\s+/g,' ').trim();if(k&&!seen[k.toLowerCase()]&&out.length<10){seen[k.toLowerCase()]=1;out.push(k.slice(0,50))}});
 kwbox.textContent='';out.forEach(function(k){var s=document.createElement('span');s.textContent=k;kwbox.appendChild(s)});kwbox.hidden=!out.length;
}
function render(){
 var n=++seq;
 fetch('/preview',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:'markdown='+encodeURIComponent(md.value)+'&title='+encodeURIComponent(ti.value),credentials:'same-origin'})
  .then(function(r){if(r.ok&&auto&&sl&&n>=shown){var b=r.headers.get('X-Slug-Base');if(b)sl.value=b}return r.ok?r.text():null})
  .then(function(h){if(h===null||n<shown)return;shown=n;body.innerHTML=h.trim()?h:'<p class="empty">Nothing to preview yet.</p>'})
  .catch(function(){});
}
md.addEventListener('input',function(){clearTimeout(timer);timer=setTimeout(render,200)});
ti.addEventListener('input',function(){title();clearTimeout(timer);timer=setTimeout(render,200)});
if(sl)sl.addEventListener('input',function(){auto=sl.value.trim()===''});
if(kw&&kwbox)kw.addEventListener('input',chips);
var file=document.getElementById('imgfile'),add=document.getElementById('addimg'),size=document.getElementById('imgsize'),keep=document.getElementById('keepimgs'),msg=document.getElementById('imgmsg');
if(file&&add){
 var sv=null;try{sv=localStorage.getItem('weft-imgsize')}catch(e){}
 if(sv==='compact'||sv==='original')size.value=sv;else if(window.matchMedia&&matchMedia('(pointer: coarse)').matches)size.value='compact'; // a phone: its photos are large
 size.addEventListener('change',function(){try{localStorage.setItem('weft-imgsize',size.value)}catch(e){}});
 function say(t){msg.textContent=t}
 function put(text){var a=md.selectionStart||0,b=md.selectionEnd||0;md.setRangeText(text,a,b,'end');var s=a+2;md.setSelectionRange(s,s+5);md.focus();md.dispatchEvent(new Event('input'))}
 function send(f){
  if(!/^image\/(jpeg|png|gif)$/.test(f.type)){say('Only JPEG, PNG and GIF pictures can be added.');return}
  var lim=+(size.value==='compact'?file.dataset.maxCompact:file.dataset.max);
  if(lim&&f.size>lim){say('That file is too large: the limit is '+Math.round(lim/1048576)+' MB'+(size.value==='compact'?'.':'. The Compact size accepts larger files.'));return}
  var fd=new FormData();fd.append('file',f);fd.append('size',size.value);say('Adding '+(f.name||'picture')+'…');
  fetch('/media',{method:'POST',body:fd,credentials:'same-origin'}).then(function(r){return r.ok?r.json():r.text().then(function(t){throw new Error(t.trim())})})
   .then(function(j){put('![image]('+j.path+')');say('Added: '+j.width+' × '+j.height+', '+Math.round(j.size/1024)+' KB. Type a description in place of "image".')})
   .catch(function(e){say(e.message||'The picture could not be added.')});
 }
 add.addEventListener('click',function(){file.click()});
 file.addEventListener('change',function(){if(file.files[0])send(file.files[0]);file.value=''});
 md.addEventListener('paste',function(e){var fs=e.clipboardData&&e.clipboardData.files;if(fs&&fs.length&&/^image\//.test(fs[0].type)){e.preventDefault();send(fs[0])}});
 md.addEventListener('dragover',function(e){if(e.dataTransfer&&Array.prototype.indexOf.call(e.dataTransfer.types,'Files')>=0)e.preventDefault()});
 md.addEventListener('drop',function(e){var fs=e.dataTransfer&&e.dataTransfer.files;if(fs&&fs.length){e.preventDefault();send(fs[0])}});
 function other(){keep.hidden=!/!\[[^\]]*\]\(https?:\/\//.test(md.value)}
 md.addEventListener('input',other);other();
 keep.addEventListener('click',function(){
  say('Copying…');
  fetch('/media/keep',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},credentials:'same-origin',body:'markdown='+encodeURIComponent(md.value)+'&size='+encodeURIComponent(size.value)})
   .then(function(r){return r.ok?r.json():r.text().then(function(t){throw new Error(t.trim())})})
   .then(function(j){md.value=j.markdown;md.dispatchEvent(new Event('input'));
    var t=j.kept?('Kept '+j.kept+' cop'+(j.kept==1?'y':'ies')+'.'):'Nothing was copied.';
    if(j.failed&&j.failed.length)t+=' Not copied: '+j.failed.map(function(f){return f.url+' ('+f.error+')'}).join('; ');say(t)})
   .catch(function(e){say(e.message||'The copies could not be made.')});
 });
}
})();
</script>
{{if .Delete}}<form method="post" action="{{.Delete}}" onsubmit="return confirm('Delete this post?')"><button class="linkbtn">Delete post</button></form>{{end}}
{{template "foot"}}{{end}}`))

type pagePost struct {
	Title, URL, ISO, Date, Ago string
	Markdown                   string // the post as its author wrote it, for the Markdown form of the page
	HTML                       template.HTML
	Edited                     bool
	InReplyTo                  string
	Canonical, CanonicalHost   string // where a reposted post first appeared
	Keywords                   []string
	Tally, TallyTip            string
	Parent                     *parentCard // the page this post replies to, when the reply carries a snapshot of it
	ThreadHref                 string
	HasThread                  bool // it replies to something or has approved replies
}

type pageData struct {
	Title, FeedURL, WebmentionURL, Author, Bio string
	Owner                                      bool
	Posts                                      []pagePost
	Timeline                                   []tlRow
	Replies                                    []replyRow
	ReplyTotal                                 int          // replies in all, with the ones to replies
	PageFeeds                                  []pageFeed   // feeds the bookmarked page advertises
	Back                                       string       // the timeline view a reply returns to
	Desc                                       string       // the meta description of the page
	OGType, OGURL, OGImage                     string       // Open Graph: what the page is, its address, and a picture for a link preview
	sp                                         *store.Space // the owner of the page, for the sizes of its pictures
	Older, Newest                              string       // timeline: the next page of older posts, and the way back to the newest
	Cheat                                      *cheatSheet  // the Markdown help beside the editor
	Advanced                                   bool         // the editor opens with title, keywords, address and the Markdown help
	Forced                                     bool         // the owner signed in with a temporary password and must replace it
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
	ShowLikes, ShowMine                          bool
	MineToggle                                   string
	Nav                                          string          // which menu entry the page belongs to
	WantMD                                       bool            // the request asked for the page as Markdown
	Home                                         bool            // the site's home page: it shows the bio
	SiteURL                                      string          // this site's home address, for the author's h-card
	PInfo                                        feed.ParentInfo // the snapshot of the page being replied to, kept in hidden fields
	Parent                                       *parentCard
	Modes                                        []modeLink // Bookmark, Reply and Like links for the page the bookmarklet sent
	Avatar, Initial                              string     // the site owner's avatar address and fallback letter, for the header
	Profile                                      struct {
		Display, Bio, Avatar string
		Local                bool // the avatar is an uploaded picture
	}
	Domain                               *domainView
	DomainWarn                           string    // for the owner: the account's domain has lapsed
	Move                                 *moveView // the profile page: moving to or from another server
	Pull                                 *pullView // the page that shows a move to this server
	Likers, Others                       []likerRow
	Images                               []imageRow
	UploadMax, ImageMax, ImageMaxCompact int  // the largest picture accepted: for a profile picture, at the Original size, at the Compact size
	Clip                                 bool // long posts are folded, with a button to open them (a list of posts, not one)
	MediaUsed, MediaQuota                string
	LikeTarget, LikeTitle, LikeNote      string
	ClearKeyword                         string
	Keyword, TopicFeed                   string       // a keyword filter on the home page, and the feed of just those posts
	Bookmarklet                          template.URL // javascript: link that sends the current page to /compose; owner's home page only
	LikesToggle                          string
	Form                                 struct{ Title, Markdown, Keywords, Slug, Canonical string }
	SlugAuto                             bool // the address is the suggestion, to follow the title and text until it is reworded
}

func (s *Server) data(r *http.Request, q *req, title string) pageData {
	d := s.pageBase(r, q, title)
	if q.owner {
		d.DomainWarn = s.lapseWarning(q.host.Name)
	}
	return d
}

func (s *Server) pageBase(r *http.Request, q *req, title string) pageData {
	return pageData{Title: title, FeedURL: s.base(r) + "/feed.xml", WebmentionURL: s.base(r) + "/webmention",
		Author: q.host.Display, Bio: q.host.Bio, Owner: q.owner, SiteURL: s.base(r) + "/", WantMD: negotiate(r.Header.Get("Accept")) == "markdown", Nav: navOf(r.URL.Path), Avatar: q.host.Avatar, Initial: initial(orStr(q.host.Display, q.host.Name)), sp: q.sp, Desc: describe(orStr(q.host.Bio, "Posts by "+orStr(q.host.Display, q.host.Name)+".")),
		OGType: "website", OGURL: s.base(r) + r.URL.Path, OGImage: s.previewImage(r, q.host.Avatar)}
}

// navOf names the header menu entry a path belongs to, or "".
func navOf(path string) string {
	for _, k := range []string{"compose", "timeline", "following", "replies"} {
		if path == "/"+k || strings.HasPrefix(path, "/"+k+"/") {
			return k
		}
	}
	return ""
}

func (s *Server) show(w http.ResponseWriter, name string, status int, d pageData) {
	w.Header().Add("Vary", "Accept")
	if d.WantMD {
		if doc, ok := d.markdown(name); ok {
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(status)
			io.WriteString(w, doc)
			return
		}
	}
	var page bytes.Buffer
	err := layout.ExecuteTemplate(&page, name, d)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err != nil {
		w.Write([]byte("template error"))
		return
	}
	w.Write(sizePictures(page.Bytes(), d.sp))
}

func (s *Server) renderPosts(w http.ResponseWriter, r *http.Request, q *req, title string, posts []*store.Post, tweak ...func(*pageData)) {
	d := s.data(r, q, title)
	tallies, _ := q.sp.Tallies(q.host.ID)
	likers, _ := q.sp.Likers(q.host.ID)
	counts, _ := q.sp.ApprovedCounts(q.host.ID)
	for _, p := range posts {
		d.Posts = append(d.Posts, pagePost{
			Title: p.Title, URL: s.postURL(r, p.ID), HTML: template.HTML(p.HTML), // rendered by goldmark in safe mode
			ISO: p.Created.UTC().Format("2006-01-02T15:04:05Z"), Date: stampAt(p.Created), Ago: ago(p.Created), Markdown: p.Markdown,
			Edited: p.Updated != nil, InReplyTo: p.InReplyTo, Parent: newParentCard(p.InReplyTo, p.Parent), Keywords: p.Keywords, Tally: likesText(tallies[p.ID]), TallyTip: likedBy(likers[p.ID], tallies[p.ID]), ThreadHref: threadHref(s.postURL(r, p.ID), ""),
			HasThread: p.InReplyTo != "" || counts[p.ID] > 0, Canonical: p.Canonical, CanonicalHost: siteHost(p.Canonical),
		})
		if pp := &d.Posts[len(d.Posts)-1]; p.InReplyTo != "" && pp.Parent == nil {
			_, pp.Parent = s.replyContext(r, q, p.InReplyTo) // no snapshot: what this server knows of the post
		}
	}
	d.Clip = len(posts) > 1
	if len(posts) == 1 && !posts[0].Deleted {
		d.Replies, d.ReplyTotal, d.Pending = s.repliesFor(r, q, posts[0].ID)
		// the same links the one-item feed carries, for readers that start from the page: the post
		// as a feed item (same URL, Accept: application/rss+xml), its parent and its replies feed
		u := s.postURL(r, posts[0].ID)
		d.Links = append(d.Links, feed.AtomLink{Rel: "alternate", Type: "application/rss+xml", Href: u})
		if posts[0].InReplyTo != "" {
			d.Links = append(d.Links, feed.AtomLink{Rel: "in-reply-to", Href: posts[0].InReplyTo})
		}
		d.Links = append(d.Links, feed.AtomLink{Rel: "replies", Type: "application/rss+xml", Href: u + "/replies.xml"})
		if posts[0].Canonical != "" { // a repost names the page it first appeared at; an original names nothing
			d.Links = append(d.Links, feed.AtomLink{Rel: "canonical", Href: posts[0].Canonical})
		}
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
		posts, err = q.sp.PostsWithKeyword(q.host.ID, kw, 50, 0)
	} else {
		posts, err = q.sp.RecentPosts(q.host.ID, 50)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.renderPosts(w, r, q, q.host.Display, posts, func(d *pageData) {
		d.Home = true
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
	p, err := q.sp.PostBySlug(q.host.ID, r.PathValue("slug"))
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
		io.WriteString(w, absMedia(p.Markdown, s.base(r)))
		return
	case "rss":
		s.writeFeed(w, r, q, []*store.Post{p}, "") // this one post, as a feed item with all its weft: links
		return
	}
	title := p.Title
	if title == "" {
		title = q.host.Display
	}
	s.renderPosts(w, r, q, title, []*store.Post{p}, func(d *pageData) {
		if text := describe(p.HTML); text != "" {
			d.Desc = text
		}
		d.OGType = "article"
		if m := bodyImage.FindStringSubmatch(absMedia(p.HTML, s.base(r))); m != nil {
			d.OGImage = s.previewImage(r, html.UnescapeString(m[1]))
		}
	})
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
		posts, err = q.sp.PostsWithKeyword(q.host.ID, kw, feedPageSize+1, (page-1)*feedPageSize)
	} else {
		posts, err = q.sp.PostsPage(q.host.ID, feedPageSize+1, (page-1)*feedPageSize)
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
	if q.host.Avatar != "" {
		ch.Image = &feed.ChannelImage{URL: absPath(q.host.Avatar, s.base(r)), Title: q.host.Display, Link: s.base(r) + "/"}
	}
	counts, _ := q.sp.ApprovedCounts(q.host.ID)
	tallies, _ := q.sp.Tallies(q.host.ID)
	var last time.Time
	for _, p := range posts {
		if p.Created.After(last) {
			last = p.Created
		}
		url := s.postURL(r, p.ID)
		it := feed.Item{Title: p.Title, GUID: feed.GUID{Value: "urn:uuid:" + p.UID}, Link: url,
			Description: absMedia(p.HTML, s.base(r)), Markdown: absMedia(p.Markdown, s.base(r)), PubDate: feed.RFC822(p.Created)}
		it.Replies = &feed.Replies{Href: url + "/replies.xml", Count: counts[p.ID]}
		it.Likes = &feed.Likes{Count: tallies[p.ID], Href: url + "/likes.xml"}
		if p.Canonical != "" {
			it.Canonical = &feed.Ref{Href: p.Canonical}
		}
		it.Categories = p.Keywords
		if p.InReplyTo != "" {
			it.InReplyTo = &feed.Ref{Href: p.InReplyTo, Feed: p.InReplyToFeed}
			it.Root = &feed.Ref{Href: p.Root, Feed: p.RootFeed}
			it.Context = &feed.Context{Href: p.InReplyTo, Retrieved: feed.RFC822(p.Created), Title: p.Parent.Title, Description: p.Parent.Description, Image: p.Parent.Image}
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
	s.writeChannel(w, r, ch)
}

// previewImage is the address of a picture for a link preview: absolute, and for an uploaded picture the
// 800-pixel copy. It is "" when there is no usable picture.
func (s *Server) previewImage(r *http.Request, src string) string {
	if p := localAvatar(src); p != "" {
		return s.base(r) + p + "?w=800"
	}
	if rest, ok := strings.CutPrefix(src, s.base(r)+"/media/"); ok && mediaName.MatchString(rest) {
		return src + "?w=800"
	}
	if httpURL(src) != "" {
		return src
	}
	return ""
}
