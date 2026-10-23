package web

import (
	"encoding/json"
	"html/template"
	"net/url"
	"strings"
)

// bookmarkletJS runs on the page being bookmarked. It reads the social meta tags (Open Graph, then
// Twitter), falls back to the page itself for the title, description and image, and opens the
// compose form with them. It also looks for a Webmention endpoint (a <link> or <a rel="webmention">,
// or a Link header on the page's own response): with one the form starts as a reply to the page,
// without one as a bookmark card. It also lists the RSS and Atom feeds the page advertises (at most five), so
// the form can offer to follow them. COMPOSE is replaced by the compose address.
const bookmarkletJS = `(function(){
var d=document,L=location,C=COMPOSE;
function m(k){var e=d.querySelector('meta[property="'+k+'"],meta[name="'+k+'"]');return e&&e.content?e.content.trim():''}
function t(s){return(s||'').replace(/\s+/g,' ').trim()}
function abs(u){try{return u?new URL(u,L.href).href:''}catch(e){return''}}
var title=t(m('og:title')||m('twitter:title')||d.title||(d.querySelector('h1')||{}).textContent)||L.hostname;
var desc=t(m('og:description')||m('twitter:description')||m('description'));
if(!desc){var ps=d.querySelectorAll('article p,main p,p');for(var i=0;i<ps.length;i++){var x=t(ps[i].textContent);if(x.length>80){desc=x;break}}}
if(desc.length>300)desc=desc.slice(0,297).replace(/\s+\S*$/,'')+'...';
var img=abs(m('og:image:secure_url')||m('og:image')||m('twitter:image')||m('twitter:image:src'));
if(!img){var l=d.querySelector('link[rel="image_src"]');img=l?abs(l.href):''}
if(!img){var best=0;for(var j=0;j<d.images.length;j++){var g=d.images[j],a=g.naturalWidth*g.naturalHeight;if(g.naturalWidth>=200&&g.naturalHeight>=100&&a>best){best=a;img=abs(g.currentSrc||g.src)}}}
var kw=[m('keywords')].concat([].map.call(d.querySelectorAll('meta[property="article:tag"]'),function(e){return e.content}));
kw=kw.join(',').split(',').map(t).filter(Boolean).slice(0,5).join(', ');
var fl=[],seen={};[].forEach.call(d.querySelectorAll('link[rel~="alternate"][type]'),function(e){var h=abs(e.getAttribute('href'));if(fl.length<5&&h&&!seen[h]&&/^application\/(rss|atom)\+xml$/i.test((e.getAttribute('type')||'').trim())){seen[h]=1;fl.push('&feed='+encodeURIComponent(h)+'&ftitle='+encodeURIComponent(t(e.getAttribute('title'))))}});
var q='?page='+encodeURIComponent(L.href)+'&ptitle='+encodeURIComponent(title)+'&pdesc='+encodeURIComponent(desc)+'&pimg='+encodeURIComponent(img)+'&keywords='+encodeURIComponent(kw)+fl.join('');
var w=window.open('','_blank');
function go(wm){var u=C+q+'&wm='+wm;if(w)w.location.href=u;else L.href=u}
if(d.querySelector('link[rel~="webmention"],a[rel~="webmention"]')){go(1)}
else{var done=false,tm=setTimeout(function(){if(!done){done=true;go(0)}},1500);
fetch(L.href,{method:'HEAD',credentials:'same-origin'}).then(function(r){return /rel="?[^";,]*\bwebmention\b/i.test(r.headers.get('Link')||'')}).catch(function(){return false}).then(function(x){if(done)return;done=true;clearTimeout(tm);go(x?1:0)})}
})();`

// bookmarklet returns the javascript: link that sends the current page to the compose form at
// composeURL. It is percent-encoded so it survives as an href and browsers decode it before running.
func bookmarklet(composeURL string) template.URL {
	to, _ := json.Marshal(composeURL)
	js := strings.ReplaceAll(bookmarkletJS, "COMPOSE", string(to))
	js = strings.Join(strings.Fields(strings.ReplaceAll(js, "\n", " ")), " ")
	return template.URL("javascript:" + url.PathEscape(js))
}
