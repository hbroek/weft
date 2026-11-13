// weftFit(file, {side, max, dim}) makes a picture small enough before it is sent: nothing leaves the browser until it
// has been through here. It answers with the picture to send, which is the original when nothing needed doing and
// when this browser cannot do it. A HEIC picture is only accepted where the browser can read it (Safari), and is
// made a JPEG. GIFs are never touched (a canvas would keep one frame).
//   side  the longest side wanted, in pixels
//   dim   true: a picture longer than side is scaled to it; false: only a picture over max bytes is made smaller
//   max   the most bytes the server takes (0: no limit)
(function(){
 // the size a picture is made, as [width, height]; never larger than it is
 function fitSize(w,h,side){var s=Math.min(1,side/Math.max(w,h));return [Math.max(1,Math.round(w*s)),Math.max(1,Math.round(h*s))]}
 // the tries, in order, for a picture that is too large in bytes: [longest side, quality]
 function tries(long,side){var t=[[Math.min(long,side),.85],[Math.min(long,side),.7]];[1200,800].forEach(function(x){if(x<Math.min(long,side))t.push([x,.7])});return t}
 function name(n,ext){return (n||'picture').replace(/\.[A-Za-z0-9]+$/,'')+'.'+ext}
 function toBlob(w,h,bm,type,q){
  return new Promise(function(ok,no){
   var c=document.createElement('canvas');c.width=w;c.height=h;
   var x=c.getContext('2d');if(!x){no(new Error('no canvas'));return}
   x.imageSmoothingQuality='high';
   if(type==='image/jpeg'){x.fillStyle='#fff';x.fillRect(0,0,w,h)} // no transparency in a JPEG: white, not black
   x.drawImage(bm,0,0,w,h);
   c.toBlob(function(b){b?ok(b):no(new Error('no blob'))},type,q);
  });
 }
 function hasAlpha(bm){
  try{var w=Math.min(bm.width,512),h=Math.min(bm.height,512),c=document.createElement('canvas');c.width=w;c.height=h;
   var x=c.getContext('2d');x.drawImage(bm,0,0,w,h);var d=x.getImageData(0,0,w,h).data;
   for(var i=3;i<d.length;i+=4)if(d[i]<255)return true}catch(e){}
  return false;
 }
 function fit(f,o){
  var heic=/^image\/(heic|heif)$/.test(f.type);
  if(!/^image\/(jpeg|png)$/.test(f.type)&&!heic)return Promise.resolve(f);
  if(!window.createImageBitmap)return heic?Promise.reject(new Error('This browser cannot read HEIC pictures. Choose a JPEG or PNG.')):Promise.resolve(f);
  var big=o.max&&f.size>o.max;
  return createImageBitmap(f,{imageOrientation:'from-image'}).then(function(bm){
   var long=Math.max(bm.width,bm.height),need=heic||big||(o.dim&&long>o.side);
   if(!need){bm.close();return f}
   var png=f.type==='image/png'&&hasAlpha(bm),type=png?'image/png':'image/jpeg',ext=png?'png':'jpg';
   var plan=big||heic?tries(long,o.side):[[Math.min(long,o.side),.85]],best=null,i=0;
   function next(){
    var p=plan[i++],sz=fitSize(bm.width,bm.height,p[0]);
    return toBlob(sz[0],sz[1],bm,type,p[1]).then(function(b){
     if(!best||b.size<best.size)best=b;
     if(o.max&&b.size>o.max&&i<plan.length)return next();
    });
   }
   return next().then(function(){
    bm.close();
    if(!heic&&best.size>=f.size&&long<=o.side)return f; // not smaller: keep what was chosen
    return new File([best],name(f.name,ext),{type:type});
   });
  },function(){
   if(heic)throw new Error('This browser cannot read HEIC pictures. Choose a JPEG or PNG.');
   return f; // cannot be read here: the server will say what is wrong with it
  });
 }
 window.weftFit=fit;window.weftFitSize=fitSize;window.weftFitTries=tries;
})();
