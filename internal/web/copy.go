package web

import (
	_ "embed"
	"html/template"
)

// copyScript puts a Copy button after every element marked data-copy: the values a person has to carry somewhere
// else, such as the DNS records of a domain. It uses the clipboard where the page may, and a selection and the
// browser's own copy otherwise. Without scripts nothing changes: the value is still there to select.
const copyScript = `<script>(function(){document.querySelectorAll('[data-copy]').forEach(function(e){
var b=document.createElement('button');b.type='button';b.className='copy';b.textContent='Copy';b.setAttribute('aria-label','Copy to the clipboard');
function done(t){b.textContent=t;setTimeout(function(){b.textContent='Copy'},1500)}
function old(){var r=document.createRange();r.selectNodeContents(e);var s=getSelection();s.removeAllRanges();s.addRange(r);try{done(document.execCommand('copy')?'Copied':'Select and copy')}catch(x){done('Select and copy')}}
b.addEventListener('click',function(){var t=e.textContent.trim();if(navigator.clipboard&&navigator.clipboard.writeText)navigator.clipboard.writeText(t).then(function(){done('Copied')},old);else old()});
e.after(b)})})()</script>`

// copyHTML is copyScript for the templates.
func copyHTML() template.HTML { return template.HTML(copyScript) }

// fitJS makes a picture small in the browser before it is sent (fit.js).
//
//go:embed fit.js
var fitJS string

// fitHTML is the script that defines weftFit, for the pages that send pictures.
func fitHTML() template.HTML { return template.HTML("<script>" + fitJS + "</script>") }
