// Standalone entry bundled (esbuild) to public/comments-embed.js and
// injected by arti-server into served HTML package pages. It runs INSIDE
// the (sandboxed) prototype page, so it anchors comments on the real DOM
// and talks to the comments API with a scoped bearer token (the page
// can't send the session cookie).
import { mountCommentsOverlay, type CommentsApi } from "../lib/commentsOverlay";
import type { ThreadDTO } from "../lib/arti";

declare global {
  interface Window {
    __ARTI_COMMENTS__?: { artifactId: string; token: string; me?: { email: string; name?: string; picture?: string } };
  }
}

const cfg = window.__ARTI_COMMENTS__;
if (cfg && cfg.artifactId && cfg.token) {
  const base = "/api/embed";
  const headers = { Authorization: `Bearer ${cfg.token}`, "Content-Type": "application/json" };

  async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
    const resp = await fetch(base + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: "omit", // bearer token, never the cookie
    });
    if (!resp.ok && resp.status !== 204) throw new Error(await resp.text());
    return (resp.status === 204 ? undefined : await resp.json()) as T;
  }

  const api: CommentsApi = {
    list: (id) => call("GET", `/artifacts/${id}/comments`),
    create: (id, anchor, b) => call("POST", `/artifacts/${id}/comments`, { anchor, body: b }),
    reply: (tid, b) => call("POST", `/comments/${tid}/replies`, { body: b }),
    resolve: (tid) => call<void>("POST", `/comments/${tid}/resolve`),
    reopen: (tid) => call<void>("POST", `/comments/${tid}/reopen`),
    del: (tid, cid) => call<void>("DELETE", `/comments/${tid}/comments/${cid}`),
    edit: (tid, cid, b) => call("PUT", `/comments/${tid}/comments/${cid}`, { body: b }),
  };

  const start = () =>
    mountCommentsOverlay({
      container: document.body, // the served page itself is the content
      artifactId: cfg.artifactId,
      me: cfg.me ? { email: cfg.me.email, name: cfg.me.name, picture: cfg.me.picture, is_admin: false } : null,
      allowPin: true, // it's an HTML page → pins allowed
      api,
    });

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", start);
  else start();
}

// keep the type import referenced for the bundler
export type _T = ThreadDTO;
