---
title: Attachments
order: 5
summary: A user-uploaded binary file stored as an S3-only ATTACHMENT artifact — slugless and creator-only when another app posts it, a normal versioned document when a person publishes it under a slug.
---

# Attachments

An **ATTACHMENT** is an artifact type for a single user-uploaded binary file — an
image, PDF, csv, doc, zip — that doesn't belong in a TEXT artifact (which must be
textual) and isn't a multi-file bundle (which is a PACKAGE). It reuses the same
S3 + Postgres-pointer machinery as every other type. Where it came from decides
how constrained it is: a file another app posts (couch's chat attachments) is
S3-only, slugless and creator-only, while a file a person publishes under a slug
they named is a versioned document with that document's access.

The original use case is chat attachments dropped onto a Couch session. As of
now only the arti side (the artifact type, its invariants, and serving) is built;
the couch composer and gateway ingestion paths are not in this repo.

## How it works

The data path is the same one PACKAGE and large-TEXT artifacts take:
**client → arti → S3**. arti owns the S3 credentials and the DB pointer row, so
consistency and access control live in one place.

There are two ways to send the bytes:

- **multipart/form-data** — a `file` part with raw bytes plus form fields
  (title, content type, artifact type). No base64. This is what the CLI uses.
- **JSON** — base64 in the request body (~33% inflation).

Either way the body is capped (200 MiB → HTTP 413). The S3 write buffers the
whole object in memory to compute its hash, so each in-flight upload holds the
full file in arti RAM. That's fine for chat-sized files (KB–low-MB); large or
high-volume uploads would need a presigned direct-to-S3 path, which isn't built.

On create, arti forces the ATTACHMENT rules at the store — the single write
chokepoint — so neither a caller nor the server's slug-inherit path can violate
them:

- **Slugless.** No named slug, so the row is single-version. Append is rejected
  for ATTACHMENT whatever its slug.
- **Creator-only.** Allowed-access is forced empty (creator-only; admins bypass).
  An access update on a slugless ATTACHMENT can't widen it either.
- **S3-only.** The inline-content branch is TEXT-only, so ATTACHMENT always falls
  through to S3 even for tiny files, under an `attachments/` key prefix.

### The one exemption: a person publishing under a slug

The first two rules exist because another app's files must not pick up a
document's ACL by landing on a slug. A person naming a slug is the opposite
situation — they are publishing a document — so `KeepAttachmentSlug` exempts that
write: the slug is kept, the row versions, and access follows the slug like any
other type. The artifacts service sets it only for an **interactive** caller (a
browser session, or someone's own CLI/MCP client); a service credential — an
`arti_` API key or a device-flow upload token — naming a slug gets a `400` rather
than the silently slugless artifact it used to get. This is what makes
drop-to-version work for a PDF or an image in the viewer.

Serving: a previewable attachment (PDF, image) is served `inline` so `/a/<uuid>`
links and the viewer's `<img>`/`<iframe>` render it; non-renderable bytes get
`Content-Disposition: attachment` so the browser saves them. `?download=1` forces
the download. Because attachments are creator-only, inline serving only ever
happens for the uploader viewing their own file.

![How an attachment flows through arti](./attachments.svg)

## Invariants / rules

- ATTACHMENT is **slugless and single-version** unless a person published it
  under a slug; append is rejected either way.
- Bytes always go to **S3**, never inline content.
- A slugless ATTACHMENT's access is **creator-only** and can never be widened.
- **No migration needed.** The artifact type is a free string and every column
  it uses already exists.

For where this lives in the code, see the [Codebase map](../development/codebase-map.md).
