// Runs the bookmarklet's script against fake pages; used by bookmarklet_test.go (needs node). Prints one JSON object.
const fs=require('fs'),vm=require('vm');
let js=fs.readFileSync(process.argv[2],'utf8').replace('COMPOSE','"http://x/compose"');
function run(page){return new Promise(res=>{
 const out={};
 const doc={title:page.title,images:[],
  querySelector(sel){const m=sel.match(/^meta\[property="([^"]+)"\],meta\[name="([^"]+)"\]$/);if(m){const v=page.meta[m[1]];return v?{content:v}:null}return null},
  querySelectorAll(sel){if(sel.startsWith('script[type="application/ld+json"]'))return (page.ld||[]).map(x=>({textContent:x}));if(sel==='article p,main p,p')return (page.ps||[]).map(x=>({textContent:x}));return []}};
 const u=new URL(page.url);
 const w={location:{}};
 const ctx={document:doc,location:{href:u.href,hostname:u.hostname},window:{open:()=>w},fetch:()=>Promise.reject(new Error('x')),setTimeout,clearTimeout,URL,encodeURIComponent,console};
 vm.runInNewContext(js,ctx);
 setTimeout(()=>{const q=new URL(w.location.href).searchParams;res({title:q.get('ptitle'),desc:q.get('pdesc'),img:q.get('pimg'),kw:q.get('keywords')})},1800)})}
(async()=>{
 const P_yt=run({url:'https://www.youtube.com/watch?v=n7hesOgzRdg',title:'2026 West Hill Shop M60+ - YouTube',meta:{'og:title':'YouTube','og:image':'https://www.youtube.com/img/desktop/yt_1200.png','keywords':'video, sharing, camera phone, video phone, free','description':'Enjoy the videos and music you love, upload original content'},
  ld:[JSON.stringify({'@context':'https://schema.org','@type':'VideoObject',name:'2026 West Hill Shop M60+',description:'West Hill Shop M60+ race - 2nd of 21\nHad my shoes with toe spikes',thumbnailUrl:['https://i.ytimg.com/vi/n7hesOgzRdg/maxresdefault.jpg']})]});
 
 const P_art=run({url:'https://blog.example/p/1',title:'The web of links - Example Blog',meta:{'keywords':'web, links','og:title':'The web of links','og:description':'About links.','og:image':'https://blog.example/i.png'},ld:[JSON.stringify({'@type':'Article',headline:'Other headline',description:'ld desc'})]});
 
 const P_news=run({url:'https://www.wsj.com/articles/x',title:'Big story - WSJ',meta:{},ld:[JSON.stringify({'@graph':[{'@type':'WebSite',name:'WSJ'},{'@type':'NewsArticle',headline:'Big story',description:'The details.',image:{'@type':'ImageObject',url:'https://s.wsj.net/i.jpg'}}]})]});
 
 const P_bare=run({url:'https://x.example/a',title:'Plain page',meta:{},ld:['{bad json'],ps:['A paragraph that is long enough to be used as the description of a page that has no tags at all, really.']});
 const [yt,art,news,bare]=await Promise.all([P_yt,P_art,P_news,P_bare]);const all={};all.youtube=yt;all.article=art;all.news=news;all.bare=bare;console.log(JSON.stringify(all));
})();
