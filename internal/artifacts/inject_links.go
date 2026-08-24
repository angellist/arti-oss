package artifacts

import (
	"bytes"
	"encoding/json"
	"strings"
)

// injectExternalLinkHandler keeps target-less links from navigating a
// sandboxed served document to a cross-origin page, which would replace the
// frame with a browser blocking page. In-content artifact links and
// same-document anchors are left to the browser.
func injectExternalLinkHandler(body []byte, ct, contentRoot string) []byte {
	if !strings.HasPrefix(strings.ToLower(ct), "text/html") {
		return body
	}
	rootJSON, _ := json.Marshal(contentRoot)
	snippet := []byte(`<script>try{document.addEventListener("click",function(e){if(e.defaultPrevented||e.button!==0||e.ctrlKey||e.metaKey||e.shiftKey||e.altKey)return;var a=e.target&&e.target.closest&&e.target.closest("a[href]");if(!a||a.hasAttribute("download"))return;var t=a.getAttribute("target");if(t&&t!=="_self")return;var u=new URL(a.getAttribute("href"),document.baseURI);if(u.protocol!=="http:"&&u.protocol!=="https:")return;var d=new URL(document.URL);if(u.origin===d.origin&&u.pathname===d.pathname&&u.search===d.search)return;var r=` + string(rootJSON) + `;if(r&&u.origin===d.origin&&(u.pathname===r||r.charAt(r.length-1)==="/"&&u.pathname.indexOf(r)===0))return;e.preventDefault();try{var w=window.open(u.href,"_blank");if(w){try{w.opener=null}catch(_){}return}}catch(_){}try{window.parent.postMessage({source:"arti-open-external",url:u.href},"*")}catch(_){}},true)}catch(e){}</script>`)
	if i := bytes.LastIndex(bytes.ToLower(body), []byte("</body>")); i >= 0 {
		out := make([]byte, 0, len(body)+len(snippet))
		out = append(out, body[:i]...)
		out = append(out, snippet...)
		return append(out, body[i:]...)
	}
	return append(body, snippet...)
}
