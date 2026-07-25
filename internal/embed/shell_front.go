package embed

import (
	"net/http"
	"strings"
)

// serveFrontShell serves the Front side-panel adapter — the one Front-coupled
// piece. Front's side-panel URL is static (it never injects the conversation id),
// so this page runs Front's Plugin SDK, reads the open conversation, builds the
// slug from the surface's shell_slug, and frames the generic doc route.
//
// It is the only response that opts into being framed by Front: it overrides the
// global X-Frame-Options: SAMEORIGIN with frame-ancestors = the surface origin.
// The nested doc iframe is same-origin, so the doc route's own 'self' covers it.
//
// auth_secret is read from this page's own URL (Front appends it to the
// configured side-panel URL) and forwarded to the doc route.
func serveFrontShell(w http.ResponseWriter, surface string, surf Surface) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "frame-ancestors "+strings.Join(surf.Origin, " "))
	w.Header().Del("X-Frame-Options")
	w.Header().Set("Cache-Control", "no-store")

	// Inject the surface name and shell_slug as JS string literals. Both are
	// operator-configured (not user input); JSON-encode defensively anyway.
	page := frontShellHTML
	page = strings.ReplaceAll(page, "__SURFACE__", jsString(surface))
	page = strings.ReplaceAll(page, "__SHELL_SLUG__", jsString(surf.ShellSlug))
	_, _ = w.Write([]byte(page))
}

// jsString renders s as a safe single-quoted JS string literal.
func jsString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", `\r`, "<", `\x3c`)
	return "'" + r.Replace(s) + "'"
}

// frontShellHTML is the Front adapter page. __SURFACE__ / __SHELL_SLUG__ are
// replaced with JS string literals at serve time. It frames a nested iframe and
// swaps its src as the user moves between conversations, keeping the SDK
// subscription alive (a top-level redirect would lose it).
const frontShellHTML = `<!doctype html>
<html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  html,body{margin:0;height:100%;width:100%}
  #doc{border:0;width:100%;height:100vh;display:none}
  #msg{padding:24px;font:13px/1.5 -apple-system,system-ui,sans-serif;color:#666}
  /* Subtle, textless refresh. The shell only swaps the doc when the thread
     changes; this lets a user pull a fresh copy when the SAME thread's doc was
     regenerated (the agent re-ran). Deliberately quiet — low-opacity gray. */
  #rf{position:fixed;top:7px;right:9px;z-index:2;display:none;border:0;background:transparent;
      color:#a8a8a8;font-size:14px;line-height:1;cursor:pointer;padding:4px;opacity:.45;
      -webkit-font-smoothing:antialiased}
  #rf:hover{opacity:.9;color:#6b6b6b}
</style>
<script src="//dl.frontapp.com/libs/plugin-sdk-1.0.1.min.js"></script>
</head><body>
<div id="msg">Loading…</div>
<button id="rf" title="Refresh" aria-label="Refresh">↻</button>
<iframe id="doc" referrerpolicy="no-referrer"></iframe>
<script>
(function(){
  var SURFACE = __SURFACE__, SHELL_SLUG = __SHELL_SLUG__;
  var secret = (new URLSearchParams(location.search)).get("auth_secret") || "";
  var frame = document.getElementById("doc"), msg = document.getElementById("msg"),
      rf = document.getElementById("rf"), current = "", nonce = 0;
  // docURL builds the inner doc route URL. nonce is a cache-buster bumped by the
  // refresh button so re-assigning src to the SAME slug forces a refetch; the
  // doc route ignores the extra param (it reads only slug/version/auth_secret).
  function docURL(slug){
    return "/embed/" + SURFACE + "?slug=" + encodeURIComponent(slug) +
           "&auth_secret=" + encodeURIComponent(secret) + (nonce ? "&_r=" + nonce : "");
  }
  function show(cnv){
    var slug = SHELL_SLUG.replace("{id}", cnv);
    if(slug === current){ return; }
    current = slug; nonce = 0;
    frame.src = docURL(slug);
    frame.style.display = "block"; msg.style.display = "none"; rf.style.display = "block";
  }
  function none(text){ current = ""; frame.style.display = "none"; rf.style.display = "none"; msg.style.display = "block"; msg.textContent = text; }
  rf.addEventListener("click", function(){ if(current){ nonce++; frame.src = docURL(current); } });
  if(!window.Front || !Front.contextUpdates){ none("Open this panel inside Front."); return; }
  Front.contextUpdates.subscribe(function(ctx){
    if(ctx && ctx.type === "singleConversation" && ctx.conversation && ctx.conversation.id){ show(ctx.conversation.id); }
    else if(ctx && ctx.type === "multiConversations"){ none("Select a single conversation."); }
    else { none("Open a conversation."); }
  });
})();
</script>
</body></html>`
