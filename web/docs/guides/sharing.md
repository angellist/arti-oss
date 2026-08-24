---
title: Sharing a document
order: 6
summary: The two halves of the Share dialog — copying the page URL for people who can sign in, and minting a time-limited external link for someone with no AngelList account — plus what pinned and tracking links show, who may create them, and how revocation and the access log behave.
---

# Sharing a document

Open **Share** from the **⋯** menu in the viewer. It offers two different
things, and the difference matters.

## Copy this page's link

The plain `/s/<slug>` URL. Anyone you send it to still has to sign in to arti
and still has to be on the document's access list — copying the URL grants
nothing at all. This half is available to everyone who can see the document.

Use it for colleagues. It is the normal way to point someone at a document.

## External link

A time-limited URL that opens the document for **anyone holding it**, with no
AngelList account and no sign-in. The URL is the whole credential: whoever has
it can read the document until the link expires or you revoke it.

Only the document's **owner** — the creator of its earliest version — or an
administrator can create one. That is the same authority it takes to change the
document's access list, because publishing a document to the internet is a
larger act than widening who inside AngelList can read it. Someone who can
merely edit the document cannot create an external link for it.

The link is shown **once**, when you create it. arti stores only a hash of it,
so it cannot be looked up or re-displayed afterwards. If you lose it, revoke it
and make a new one.

### What the link shows

| Choice | Behaviour |
|---|---|
| **This version** (default) | Frozen. It serves the version that was current when you created the link, whatever happens afterwards. |
| **Latest version, always** | Follows the document. The recipient sees new versions as they are published. |

The tracking option carries a real hazard: **anyone who can write to the
document changes what your external reader sees**, not only you. Publishing a
version needs write access, not ownership, so the set of people who can alter
what the outside world reads is larger than the set who can create or revoke
the link — and they get no warning that an external link exists. Prefer the
pinned option unless you specifically want the recipient to track updates.

### Expiry

Every link expires: 1 hour, 8 hours, 1 day, 7 days or 30 days. There is no
"never". An external link that never expires is a permanent unauthenticated
grant, which is exactly what this feature is built to avoid.

## Revoking, and the access log

The dialog lists every live link on the document, across all its versions, with
its prefix, what it shows, when it expires, and how many times it has been
opened. **Revoke** kills one immediately — the next request to that URL fails,
including requests for images and stylesheets inside a shared package.

Two behaviours worth knowing:

- **Revoked and expired links stay listed for a week.** If you are asking "was
  this used before I killed it", you need the history, not an empty table.
- **A revoked link stops being logged.** Opens are recorded only when a read
  succeeds, so once you revoke, further attempts by whoever holds the URL do
  not appear. The log answers "was it used before I revoked it", never "is
  someone still trying".

Each recorded open carries two addresses. One is derived from the request
headers and can be forged by whoever sends the request; the other is the
network address the connection actually came from. When the two disagree, the
forwarded header was set by the client rather than by our edge.

## What is not shareable

- **Apps.** An APP artifact runs code that reaches arti's tool proxy and the
  language model. Handing that to an anonymous visitor is a different thing
  entirely, so apps cannot be shared externally.
- **Archived documents.** Archiving a document kills its live external links.
  For a tracking link this includes archiving just the newest version: the link
  dies rather than quietly falling back to an older one.

## What the recipient gets

The rendered document and a download of that same document. No catalog, no
search, no version history, no comments, and no way to reach any other document
in arti. Search engines are told not to index it, and the page is served so
that it cannot be framed by another site.
