package web

import (
	"encoding/json"
	"html/template"
	"net/url"
	"strings"
)

// bookmarkletJS runs on the page being bookmarked. It reads the social meta tags (Open Graph, then
// Twitter), falls back to the page itself for the title, description and image, and opens the
// compose form prefilled with a bookmark card. COMPOSE is replaced by the compose address.
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
var host=L.hostname.replace(/^www\./,'');
var body=(img?'!['+title.replace(/[\[\]]/g,'')+']('+img+')\n\n':'')+(desc?'> '+desc+'\n\n':'')+'['+host+']('+L.href.replace(/\)/g,'%29')+')\n';
var u=C+'?title='+encodeURIComponent(title)+'&body='+encodeURIComponent(body)+'&keywords='+encodeURIComponent(kw);
if(!window.open(u,'_blank'))L.href=u;
})();`

// bookmarklet returns the javascript: link that sends the current page to the compose form at
// composeURL. It is percent-encoded so it survives as an href and browsers decode it before running.
func bookmarklet(composeURL string) template.URL {
	to, _ := json.Marshal(composeURL)
	js := strings.ReplaceAll(bookmarkletJS, "COMPOSE", string(to))
	js = strings.Join(strings.Fields(strings.ReplaceAll(js, "\n", " ")), " ")
	return template.URL("javascript:" + url.PathEscape(js))
}
