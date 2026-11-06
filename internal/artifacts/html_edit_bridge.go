package artifacts

// htmlEditBridgeJS runs inside the sandboxed edit-mode page. It makes leaf
// text blocks (numbered elements holding only inline markup) editable when the
// parent asks, and answers "collect" with the blocks the user typed into. It
// trusts messages only from window.parent. No backticks: it is a Go raw string.
const htmlEditBridgeJS = `(function(){
if(window.parent===window)return;
var INLINE={A:1,ABBR:1,B:1,BDI:1,BDO:1,BR:1,CITE:1,CODE:1,DATA:1,DEL:1,DFN:1,EM:1,FONT:1,I:1,INS:1,KBD:1,MARK:1,Q:1,S:1,SAMP:1,SMALL:1,SPAN:1,STRONG:1,SUB:1,SUP:1,TIME:1,U:1,VAR:1,WBR:1};
var NOTBLOCK={HTML:1,HEAD:1,BODY:1,TITLE:1,SCRIPT:1,STYLE:1,TEXTAREA:1,NOSCRIPT:1,TEMPLATE:1,IFRAME:1,XMP:1,PLAINTEXT:1,NOEMBED:1,NOFRAMES:1,BUTTON:1,SELECT:1,OPTION:1};
var XHTML="http://www.w3.org/1999/xhtml";
var blocks=null,editing=false;
function post(m){m.source="arti-html-edit";try{window.parent.postMessage(m,"*")}catch(e){}}
function scan(){
  var all=document.querySelectorAll("[data-arti-eid]"),seen={},dup={},i;
  for(i=0;i<all.length;i++){var k=all[i].getAttribute("data-arti-eid");if(seen[k])dup[k]=1;seen[k]=1}
  var memo=new Map();
  function tagged(el){var k=el.getAttribute("data-arti-eid");return k!==null&&!dup[k]}
  function inlineOnly(el){
    if(memo.has(el))return memo.get(el);
    var ok=true,c=el.firstElementChild;
    for(;c;c=c.nextElementSibling){if(c.namespaceURI!==XHTML||!INLINE[c.tagName]||!tagged(c)||!inlineOnly(c)){ok=false;break}}
    memo.set(el,ok);return ok}
  function qualifies(el){return !!el&&el.nodeType===1&&el.namespaceURI===XHTML&&!NOTBLOCK[el.tagName]&&tagged(el)&&inlineOnly(el)}
  var out=[];
  for(i=0;i<all.length;i++){
    var el=all[i];
    if(!qualifies(el)||qualifies(el.parentElement))continue;
    if(!/\S/.test(el.textContent||""))continue;
    el.setAttribute("data-arti-editable","");
    out.push({el:el,eid:+el.getAttribute("data-arti-eid"),before:el.textContent,orig:el.innerHTML});
  }
  return out}
var style=document.createElement("style");
style.textContent="html.arti-editing [data-arti-editable]{outline:1px dashed rgba(37,99,235,.6);outline-offset:2px;cursor:text}html.arti-editing [data-arti-editable]:hover{background:rgba(219,234,254,.35)}html.arti-editing [data-arti-editable]:focus{outline:2px solid #2563eb;background:rgba(219,234,254,.45)}html.arti-editing [data-arti-editable][data-arti-dirty]{outline-color:#d97706}[data-arti-editable][data-arti-rejected]{outline:2px solid #dc2626!important}";
function blockOf(t){var el=t&&(t.nodeType===1?t:t.parentElement);return el&&el.closest?el.closest("[data-arti-editable]"):null}
function changed(b){return b.touched&&b.el.innerHTML!==b.orig}
function dirtyCount(){var n=0;for(var i=0;i<blocks.length;i++)if(changed(blocks[i]))n++;return n}
function setMode(on){
  if(!blocks)blocks=scan();
  editing=on;
  if(!style.parentNode)(document.head||document.documentElement).appendChild(style);
  document.documentElement.classList.toggle("arti-editing",on);
  for(var i=0;i<blocks.length;i++){var el=blocks[i].el;if(on){el.setAttribute("contenteditable","plaintext-only");if(el.contentEditable!=="plaintext-only")el.setAttribute("contenteditable","true");el.spellcheck=true}else el.removeAttribute("contenteditable")}
  if(!on&&document.activeElement&&blockOf(document.activeElement))document.activeElement.blur();
  post({type:"mode",editing:on,editable:blocks.length})}
function guard(e){if(editing&&blockOf(e.target)){if(e.type==="keydown"&&e.key==="Enter")e.preventDefault();e.stopPropagation()}}
["keydown","keyup","keypress"].forEach(function(t){window.addEventListener(t,guard,true)});
["mousedown","mouseup","click","dblclick","pointerdown","pointerup","touchstart","touchend","wheel"].forEach(function(t){window.addEventListener(t,function(e){if(!editing)return;var b=blockOf(e.target);if(!b)return;if(t==="click"&&e.target.closest&&e.target.closest("a[href]"))e.preventDefault();e.stopPropagation()},true)});
window.addEventListener("beforeinput",function(e){if(!editing||!blockOf(e.target))return;var it=e.inputType||"";if(it.indexOf("format")===0||it==="insertParagraph"||it==="insertLineBreak"||it==="insertFromDrop")e.preventDefault()},true);
window.addEventListener("paste",function(e){if(!editing||!blockOf(e.target))return;e.preventDefault();var t=(e.clipboardData&&e.clipboardData.getData("text/plain"))||"";t=t.replace(/\s*\n\s*/g," ");try{document.execCommand("insertText",false,t)}catch(_){}},true);
window.addEventListener("input",function(e){var b=blockOf(e.target);if(!editing||!b||!blocks)return;var d=false;for(var i=0;i<blocks.length;i++)if(blocks[i].el===b){blocks[i].touched=true;d=changed(blocks[i]);break}if(d)b.setAttribute("data-arti-dirty","");else b.removeAttribute("data-arti-dirty");post({type:"changed",count:dirtyCount()})},true);
window.addEventListener("message",function(e){
  if(e.source!==window.parent)return;var d=e.data;if(!d||d.source!=="arti-html-edit-host")return;
  if(d.type==="hello"){if(document.readyState==="complete")ready()}
  else if(d.type==="mode")setMode(!!d.editing);
  else if(d.type==="collect"){if(!blocks)blocks=scan();var out=[];for(var i=0;i<blocks.length;i++){var b=blocks[i];if(changed(b))out.push({eid:b.eid,before:b.before,html:b.el.innerHTML})}post({type:"edits",requestId:d.requestId,edits:out})}
  else if(d.type==="rejected"){var set={};(d.eids||[]).forEach(function(x){set[x]=1});for(var j=0;j<blocks.length;j++)if(set[blocks[j].eid])blocks[j].el.setAttribute("data-arti-rejected","")}
});
function ready(){post({type:"ready"})}
if(document.readyState==="complete")ready();else window.addEventListener("load",ready);
})();`
