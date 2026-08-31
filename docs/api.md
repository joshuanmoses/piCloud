# API reference

PiFiles exposes a versioned JSON REST API under `/api/v1`.

This document describes what is **implemented today**. Endpoints for
authentication, files, sharing, and administration are documented as those
phases land; see [BUILD_STATUS.md](BUILD_STATUS.md) for the schedule.

---

## Conventions

**Base URL** — `/api/v1`, same origin as the web interface.

**Content type** — requests and responses are `application/json; charset=utf-8`.
A request body with any other `Content-Type` is rejected with `415`.

**Unknown fields are rejected.** A request body containing a field the endpoint
does not define returns `400`. Silently ignoring unknown fields hides client
bugs and can mask a parameter-smuggling attempt such as `{"is_admin": true}`.

**Authentication** *(Phase 2)* — a server-side session in an `HttpOnly`,
`SameSite=Lax` cookie. There are no bearer tokens and nothing is stored in
`localStorage`, so a cross-site script cannot read the credential.

**Authorization** — every permission check happens on the server. The interface
hides actions the user cannot perform, but that is a usability affordance, never
a control. Modifying a request or a UUID gains nothing.

**Request correlation** — every response carries `X-Request-Id`. It also appears
in the server log line for that request and in error bodies, so a user-reported
failure can be traced directly.

### Error envelope

Every non-2xx response has this shape:

```json
{
  "error": {
    "code": "not_found",
    "message": "The requested resource was not found.",
    "fields": { "username": "already taken" }
  },
  "request_id": "5ba1a2f40a920346ebde6856"
}
```

- `code` is stable and machine-readable; branch on it.
- `message` is for humans and may be reworded between releases.
- `fields` appears only for validation failures.

Internal failures never leak their cause. A database error becomes a generic
`internal_error`; the real detail goes to the server log under the same request
ID.

### Error codes

| Code | Status | Meaning |
| --- | --- | --- |
| `bad_request` | 400 | Malformed request |
| `unauthorized` | 401 | Not signed in, or the session expired |
| `forbidden` | 403 | Signed in, but not permitted |
| `not_found` | 404 | No such resource, **or** no permission to know it exists |
| `conflict` | 409 | State conflict, such as a duplicate name |
| `payload_too_large` | 413 | Body or file exceeds the configured limit |
| `unsupported_media_type` | 415 | Wrong `Content-Type` |
| `validation_failed` | 422 | Well-formed but invalid; see `fields` |
| `rate_limited` | 429 | Too many requests |
| `quota_exceeded` | 507 | The user's storage allowance would be exceeded |
| `internal_error` | 500 | Unexpected server failure |
| `service_unavailable` | 503 | A dependency is temporarily unusable |

> `not_found` is returned uniformly whether a resource is absent or merely
> invisible to the caller. A distinguishable "forbidden" would confirm that a
> given UUID exists, which is an enumeration oracle.

---

## Operational endpoints

These live **outside** `/api/v1`. They are infrastructure contracts and stay
stable across API versions. None require authentication.

### `GET /health`

Overall service condition, including dependencies.

```json
{
  "status": "healthy",
  "database": "healthy",
  "storage": "healthy",
  "version": "1.0.0",
  "uptime_seconds": 136,
  "checks": [
    { "name": "database", "status": "healthy", "latency_ms": 2 },
    { "name": "storage",  "status": "healthy" }
  ]
}
```

`status` is the worst of the subsystem statuses:

| Value | HTTP | Meaning |
| --- | --- | --- |
| `healthy` | 200 | Fully operational |
| `degraded` | **200** | Works, but needs attention — a disk nearing full, a slow database |
| `unhealthy` | 503 | Cannot serve requests correctly |

`degraded` returns 200 deliberately: it is an administrator's problem, not a
reason for a load balancer to remove the instance from service.

Results are cached for five seconds, because this endpoint is polled by Docker,
by Caddy, and by whatever monitoring the operator points at it.

The response deliberately contains no hostnames, ports, filesystem paths,
credentials, or database version — it is unauthenticated, and must not describe
the infrastructure to strangers.

### `GET /health/live`

Whether the process is running. Touches no dependencies.

```json
{ "status": "healthy", "uptime_seconds": 136 }
```

This is the container health check. It is deliberately independent of the
database: restarting the application cannot fix a database outage, and a
dependency-aware liveness probe would produce a restart loop that makes
diagnosis harder.

### `GET /health/ready`

Whether the service can accept traffic. Probes dependencies and is never cached,
because startup ordering needs a current answer.

```json
{ "status": "healthy", "database": "healthy", "storage": "healthy" }
```

Returns 503 when any dependency is unhealthy.

---

## `GET /api/v1/config`

The settings the browser needs in order to render correctly. Unauthenticated,
and limited to information that is safe for anyone to see.

```json
{
  "app_name": "PiFiles",
  "version": "1.0.0",
  "registration_enabled": false,
  "sharing_enabled": true,
  "public_links_enabled": true,
  "upload_requests_enabled": true,
  "max_upload_bytes": 21474836480,
  "chunk_size_bytes": 16777216,
  "password_min_length": 12,
  "trash_retention_days": 30,
  "versioning_enabled": true
}
```

The client uses `chunk_size_bytes` to size upload chunks and `max_upload_bytes`
to reject an oversized file before starting a transfer. Both limits are also
enforced server-side; these values are a convenience, not a control.

No deployment detail — hosts, ports, paths, secrets — appears here, and a
backend test asserts the exact key set so nothing can slip in.

## `GET /api/v1/version`

Build metadata, useful for confirming an upgrade landed.

```json
{
  "version": "1.0.0",
  "commit": "a1b2c3d",
  "build_date": "2026-08-26T22:00:00Z",
  "go": "go1.25.14",
  "platform": "linux/arm64"
}
```

---

## Authentication

Session cookies are set by the server; a browser sends them automatically.
State-changing requests must echo the CSRF token from the `pifiles_csrf` cookie
in an `X-CSRF-Token` header.

### `GET /api/v1/setup`

Whether the first administrator still needs creating. Unauthenticated.

```json
{ "setup_required": true, "password_min_length": 12 }
```

### `POST /api/v1/setup`

Creates the first administrator and signs them in. Available **only** while the
instance has zero accounts; afterwards it returns 404 permanently.

```json
{ "username": "josh", "display_name": "Josh Moses", "email": "josh@example.com",
  "password": "a memorable passphrase" }
```

### `POST /api/v1/auth/login`

```json
{ "username": "josh", "password": "…", "remember": false }
```

Returns the session envelope and sets both cookies. `remember` extends the
session to `SESSION_REMEMBER_LIFETIME` and makes the cookie persistent.

An unknown username and a wrong password return the **same** 401 with the same
message, so the endpoint cannot be used to enumerate accounts. A locked account
returns 429 with `account_locked`; a disabled one returns 403 with
`account_disabled`, but only once the password is correct.

### `GET /api/v1/auth/session`

The signed-in user, or 401.

```json
{
  "user": { "id": "…", "username": "josh", "display_name": "Josh Moses",
            "is_admin": true, "quota_bytes": null, "used_bytes": 3500000, … },
  "csrf_token": "…",
  "expires_at": "2026-08-28T12:00:00.000Z",
  "must_change_password": false
}
```

### `POST /api/v1/auth/logout`

Revokes the session server-side and clears the cookies. 204.

### `POST /api/v1/auth/password`

```json
{ "current_password": "…", "new_password": "…" }
```

Revokes every *other* session. 204 on success; 422 with a `fields` map when the
current password is wrong or the new one fails the policy.

### `GET /api/v1/auth/sessions`

The caller's active sessions, with `current: true` on the one making the
request.

### `DELETE /api/v1/auth/sessions/{id}`

Revokes one of the caller's own sessions. Another user's session returns 404.

---

## Files and folders

Files and folders are one collection. They are rows in one table with a `kind`
discriminator, so permissions, sharing, trash, and search have a single target
type rather than two implementations that diverge.

A node payload:

```json
{
  "id": "…", "parent_id": "…", "kind": "file", "name": "Report.pdf",
  "size_bytes": 2411724, "mime_type": "application/pdf",
  "created_at": "…", "updated_at": "…",
  "owner": { "id": "…", "username": "josh", "display_name": "Josh Moses" },
  "is_root": false
}
```

The content hash is deliberately **not** included: it would let a client probe
whether specific content exists on the server.

### `GET /api/v1/nodes`

Lists a folder's children.

| Parameter | Default | Notes |
| --- | --- | --- |
| `parent_id` | the caller's root | Accepts a UUID or the literal `root` |
| `limit` | 100 | Clamped to 500 |
| `offset` | 0 | |
| `sort` | `name` | `name`, `size`, `modified`, `created`. Anything else falls back to the default. |
| `order` | `asc` | `asc` or `desc` |
| `kind` | both | `file` or `folder` |

Folders always sort before files, whatever the column.

```json
{
  "folder": { … }, "breadcrumbs": [ { "id": "…", "name": "", "is_root": true }, … ],
  "nodes": [ … ], "total": 42, "has_more": true, "limit": 100, "offset": 0
}
```

### `GET /api/v1/nodes/{id}`

One node, with its breadcrumbs. For a folder, also `subtree_size_bytes` and
`subtree_file_count` — computed on demand, so they cannot drift.

### `POST /api/v1/nodes/folders`

```json
{ "parent_id": "root", "name": "Tax Documents 2026" }
```

409 when a live sibling already uses that name, compared case-insensitively.

### `PATCH /api/v1/nodes/{id}`

Renames, moves, or both at once — so a client resolving a collision at the
destination does not need two round trips.

```json
{ "name": "New name.pdf", "parent_id": "…" }
```

400 when a folder would be moved into its own subtree.

### `POST /api/v1/nodes/{id}/copy`

```json
{ "parent_id": "…", "name": "optional override" }
```

Without a name, a non-colliding one is derived: `Report.pdf` becomes
`Report (2).pdf`. Copying a folder duplicates its whole subtree.

Content is shared on disk through reference counting, so copying a 4 GB video
costs one row. Quota is still charged: the user now has two files, and
deduplication should not become a way around an allowance.

### `DELETE /api/v1/nodes/{id}`

Moves to Trash and releases the quota. Content stays on disk so a restore is
possible.

`?permanent=true` deletes irreversibly and releases the content when nothing
else references it.

### `POST /api/v1/nodes/{id}/restore`

Returns a node and everything deleted alongside it from Trash. Fails if the
original name has been taken, or if its parent folder is also in Trash.

### `GET /api/v1/trash`

The caller's deleted items. Only the top of each deleted subtree is listed, so a
deleted folder does not bury itself under its own contents.

---

## Uploads

Two paths, both streaming. Neither ever buffers a file in memory, so a 20 GB
upload costs the same as a 20 KB one.

`multipart/form-data` is deliberately not used for file bodies: Go's multipart
reader spills large parts to temporary files, which would write every upload to
disk twice.

### `POST /api/v1/files`

A single request. The body is the file.

```text
POST /api/v1/files?parent_id=<uuid>&name=Report.pdf
Content-Type: application/octet-stream

<raw bytes>
```

`name` may also come from a `Content-Disposition` header, which is what a
browser's drag-and-drop naturally supplies. `replace_id` overwrites an existing
file in place, keeping its identity so shares and favourites survive.

The MIME type is determined by **server-side sniffing**. A client-declared
Content-Type is never stored: trusting it is how an upload endpoint becomes
stored XSS. Types that can execute script — HTML, SVG, XML, JavaScript — are
recorded as inert types instead.

507 with `quota_exceeded` when the file would exceed the user's allowance; 413
when it exceeds `MAX_UPLOAD_SIZE`.

### `POST /api/v1/uploads`

Begins a resumable session, for large files.

```json
{ "parent_id": "…", "name": "video.mp4", "size": 4294967296, "replace_id": "…" }
```

Returns the session with the `chunk_size` the server prefers. The quota is
checked here, so an oversized upload is refused before any bytes are sent.

### `PUT /api/v1/uploads/{id}/chunk`

Appends a chunk. The position comes from `?offset=` or a `Content-Range` header.

Chunks must arrive **in order**: the offset must equal what the server already
holds. A mismatch returns 409 with an `X-Upload-Offset` header naming the true
position, so a client can resume correctly rather than guessing.

Each chunk is flushed to durable storage before the progress is recorded. The
reverse order would let a power cut leave the database claiming bytes the disk
never received, and the resumed upload would silently produce a corrupt file.

### `GET /api/v1/uploads/{id}`

Progress, including `received_bytes`. This is how a client resumes after an
interruption.

### `POST /api/v1/uploads/{id}/complete`

```json
{ "checksum": "<sha-256 hex>" }
```

The checksum is optional. When present it is verified against what the server
computed, and a mismatch returns 422 rather than storing a corrupted file.

### `DELETE /api/v1/uploads/{id}`

Aborts and removes the partial content.

### `GET /api/v1/uploads`

The caller's in-flight sessions, so a client that lost its state can resume.

---

## Downloads

### `GET /api/v1/nodes/{id}/download`

Streams the content. Supports **HTTP range requests**, which is what makes media
seeking and interrupted-download resumption work.

| Header | |
| --- | --- |
| `Accept-Ranges: bytes` | |
| `ETag` | The content hash — changes exactly when the bytes do |
| `Content-Disposition` | `inline` only for types that cannot execute script; `attachment` otherwise. `?download=1` forces attachment. |
| `X-Content-Type-Options: nosniff` | The browser must not re-sniff uploaded content |
| `Content-Security-Policy` | `default-src 'none'; sandbox; frame-ancestors 'self'` |
| `Cache-Control: private` | One user's content; never held by a shared cache |

Non-ASCII filenames use RFC 6266: an ASCII fallback plus an RFC 5987 encoded
form. The fallback is sanitised so a quote or newline in a filename cannot split
the response.

`frame-ancestors` is `'self'` rather than `'none'`. The directive exists to stop
*other* origins framing a user's content; PiFiles framing it in its own preview
is the intended use, and `'none'` blocks that too — silently, as an empty box.
The `sandbox` directive is what actually contains the document: no scripts, no
forms, no popups, no same-origin access.

Requesting a **folder** here redirects (303) to its archive endpoint, so a
client that guessed wrong still gets the download the user asked for.

---

## Sharing

A share is a permission grant presented for people. The endpoints are named
`/shares` because that is the user-facing concept; the rows they create are the
same ones the access evaluator reads, so a revoked share cannot leave access
behind. Full model: [sharing.md](sharing.md).

### `GET /api/v1/sharing/vocabulary`

The permission and role names the server recognises, so the interface never
hardcodes a list that could drift.

```json
{
  "permissions": ["read", "download", "upload", "create", "modify",
                  "rename", "move", "delete", "share", "manage"],
  "roles": [
    { "name": "viewer", "permissions": ["read", "download"],
      "description": "Can open and download, but not change anything." },
    …
  ],
  "enabled": true
}
```

### `POST /api/v1/shares`

```json
{
  "node_id": "…",
  "username": "bob",
  "permissions": ["editor"],
  "inherit": true,
  "expires_at": "2026-12-31T00:00:00Z",
  "message": "Here are the figures you asked for"
}
```

Name the recipient with exactly one of `username`, `user_id`, `group_name`, or
`group_id`. `permissions` accepts a single role name or an explicit list.

Requires `share` on the node, and **you cannot grant more than you hold** —
attempting to returns 403 naming the missing permission. Sharing a node you
cannot see returns 404, identical to one that does not exist.

`inherit` defaults to true. Sharing a root folder is refused: it would hand
over the whole account.

Re-sharing with the same recipient **updates** the existing grant rather than
adding a second one, so a single revoke always removes all access.

### `DELETE /api/v1/shares/{id}`

Requires `manage` on the node, or having created the share yourself. Takes
effect on the very next request.

### `GET /api/v1/nodes/{id}/shares`

Who has access. With `manage` you see everyone; without it you see only your
own entry, so a recipient cannot enumerate the others.

### `GET /api/v1/shared-with-me`

Everything shared with the caller, directly or through a group. Excludes
anything they own, anything revoked, and anything in the trash.

```json
{
  "shares": [{
    "id": "…",
    "node": { "id": "…", "name": "Ledgers", "kind": "folder",
              "owner": { "username": "alice", "display_name": "Alice Admin" } },
    "subject_type": "group",
    "group": { "id": "…", "name": "Accounting" },
    "permissions": ["read", "download", "upload", "create", "modify", "rename", "move", "delete"],
    "role": "editor",
    "inherit": true,
    "created_at": "…",
    "shared_by": { "username": "alice", "display_name": "Alice Admin" }
  }]
}
```

---

## Groups

### `GET /api/v1/groups`

Search by name. Available to any signed-in user, because sharing with a group
requires finding it. Returns names, descriptions, and member counts only —
never membership.

### `GET /api/v1/groups/mine`

The groups the caller belongs to.

### `GET /api/v1/groups/{id}`

The group with its members. Visible to members and administrators; anyone else
receives 404, because who belongs to a team is not public information.

### `POST /api/v1/groups` · `PATCH /api/v1/groups/{id}` · `DELETE /api/v1/groups/{id}`

Administrators only: a group is an instance-wide building block of access
control. Deleting a group removes its grants.

### `POST /api/v1/groups/{id}/members`

```json
{ "username": "bob", "role": "member" }
```

Administrators, or a **manager of that specific group** — so a team lead can run
their own team without being an administrator. `role` is `member` or `manager`.

### `DELETE /api/v1/groups/{id}/members/{userID}`

Same authority, except that anyone may remove themselves.

## Public links

A share link (`/s/`) and a file request (`/u/`) are the same object. What
distinguishes them is the permission set: a file request holds `upload` and
`create` and deliberately **not** `read`, so its visitor can add files and
cannot enumerate what anybody else submitted.

Only the SHA-256 of a token is stored. The plaintext is returned exactly once,
in the response to creation, and cannot be recovered afterwards.

### `POST /api/v1/public-links`

Requires `share` on the target. The link's permissions are intersected with the
creator's, so nobody can publish more than they hold.

```json
{
  "node_id": "...",
  "kind": "download",
  "password": "optional",
  "expires_at": "2026-09-30T00:00:00Z",
  "max_uses": 25,
  "title": "Holiday photos",
  "message": "Help yourself"
}
```

`kind` is one of `view`, `download`, `upload`, `upload_view`.

The response carries `url` — **the only time it ever does.**

### `GET /api/v1/public-links` · `GET /api/v1/nodes/{id}/public-links`

Lists links you created, or links on one item (the latter needs `manage`).
Neither returns a token.

### `DELETE /api/v1/public-links/{id}`

Revokes it. The row is kept so the owner can see that a link existed and ended.

### `GET /api/v1/portal/{token}` and its transfer routes

The unauthenticated portal behind `/s/{token}` and `/u/{token}`.

Every failure mode — unknown, expired, revoked, exhausted, malformed — returns
an identical 404. An expired link is indistinguishable from one that never
existed, and token probing is rate limited.

A password-protected link answers 401 until `POST .../password` succeeds, which
sets a short-lived HMAC cookie scoped to that one link's path. The grant names
the link it was issued for, so a cookie from one link opens nothing else.

A folder link reaches that folder's **direct children** and nothing else: a
request naming an item elsewhere in the tree is refused even when the
identifier is valid, so a link cannot be walked upward.

---

## Search

### `GET /api/v1/search`

| Parameter | |
| --- | --- |
| `q` | Required. Fewer than 2 characters returns an empty result set, not an error — an empty search box is a normal state. |
| `kind` | `file` or `folder` |
| `type` | A category, not a MIME type: `image`, `video`, `audio`, `pdf`, `text` |
| `in_folder` | Restrict to a subtree. The folder must be visible to you. |
| `owned_by_me` | `1` to exclude anything reached through a share |
| `modified_after`, `modified_before` | RFC 3339, or `YYYY-MM-DD` |
| `min_size`, `max_size` | Bytes |
| `tag` | Repeatable. Matches only **your own** tags. |
| `limit`, `offset` | Default 50, maximum 200 |

Matching is PostgreSQL trigram similarity against the name. Ordering is exact
prefix match, then similarity, then recency.

Visibility is enforced inside the query rather than by filtering afterwards, so
`total` counts only what you may see. A count that included other people's
files would leak their existence as surely as returning their names.

Results carry the same decoration as a folder listing: effective permissions,
owner, `favorite`, `has_preview`, and your tags.

---

## Favourites, recent activity, and tags

All three are personal metadata. They belong to the caller, not to the file: a
tag you apply to a shared document is invisible to whoever else can see it.

### `GET /api/v1/favorites`

Your starred items, newest first. Anything you can no longer see is omitted.

### `PUT` / `DELETE /api/v1/nodes/{id}/favorite`

Adding requires `read`. Removing does not — you must be able to clear your own
list after access is revoked, or the entry is stuck there forever.

### `GET /api/v1/recent`

One entry per (item, action), newest first. `action` is `viewed`,
`downloaded`, `uploaded`, or `modified`. Repeating an action updates the row
rather than adding one, so five downloads of a file do not bury everything else.

### `GET/POST /api/v1/tags` · `PATCH/DELETE /api/v1/tags/{id}`

Your tags, with a usage count. Names are compared case-insensitively and are
unique per user, so two people may each have "Urgent".

`color` must be `#rrggbb` or empty. It reaches a CSS context in the browser, so
anything else is refused rather than sanitised.

### `GET /api/v1/nodes/{id}/tags` · `PUT`/`DELETE /api/v1/nodes/{id}/tags/{tagID}`

Your tags on one item. Applying somebody else's tag returns 404 — it is not
yours, so as far as you are concerned it does not exist.

---

## Archives

### `GET /api/v1/nodes/{id}/archive`

Streams a folder and its subtree as a ZIP. Requires `read` **and** `download`:
seeing a folder in a listing does not entitle you to bulk-export every byte in
it.

### `GET /api/v1/archive?id=…&id=…`

Streams an explicit selection as one ZIP. Each item is checked individually;
anything you cannot read is silently omitted rather than failing the request, so
a selection containing something newly unshared still delivers the rest.

Both are streamed, which has consequences worth stating:

- **No `Content-Length`.** The total is unknowable until the last entry is
  written, so the browser shows an indeterminate progress bar. That is the
  right trade: it is what allows a folder larger than the server's free disk to
  be downloaded at all.
- **No range requests.** `Accept-Ranges: none`.
- **A failure partway through cannot become an error.** The status is 200 as
  soon as the first byte is on the wire; a mid-stream failure produces a
  truncated archive that every extractor rejects — the correct signal — with
  the detail logged server-side.

Entry names are stripped of separators and dot segments, and names that would
collide on a case-insensitive filesystem are suffixed, so extracting cannot
silently overwrite. Already-compressed types (images, video, audio, archives)
are stored rather than deflated.

Downloading your **root** places its children at the top level of the archive
rather than nesting them, because a root has no name.

---

## Previews

Thumbnails are generated in the background for raster images only —
JPEG, PNG, GIF, WebP, BMP, TIFF. PDFs, text, audio, and video are previewed by
the browser from the download endpoint above, which costs the server nothing.

### `GET /api/v1/nodes/{id}/preview/status`

```json
{ "status": "pending" | "ready" | "unavailable", "available": false,
  "width": 480, "height": 360 }
```

`pending` is a normal 200, not a 404: the client needs to distinguish "not yet"
from "never". Asking for a preview that has not been requested queues one.

The reason a preview is unavailable is deliberately not returned — it describes
why a decoder rejected a file, which belongs in the operator's log.

### `GET /api/v1/nodes/{id}/preview`

The JPEG, at most 480×480, aspect preserved and never upscaled. Requires
`read`, not `download`: a thumbnail is what a listing shows, and it is not a
route to the original bytes.

404 while one is pending or impossible, so the interface falls back to a generic
icon rather than a broken image.

Source images are refused past 50 MB or 80 megapixels — the dimensions are read
from the header before any pixel is decoded, so a small file declaring an
enormous canvas costs nothing.

---

## Version history

A version is a reference to content the file previously held, not a copy of it.
The bytes already exist as a blob; the version takes over the reference the node
was holding. Nothing moves, and two nodes sharing content are unaffected.

A version carries **no permissions of its own**. Every endpoint here is
addressed through the file, and the check is against that file — so a version
identifier can never reach content the caller could not otherwise open, and
losing access to a file loses access to its history in the same instant.

The permission each operation needs is deliberately not uniform:

| Operation | Requires | Why |
| --- | --- | --- |
| List, download | `read` (+ `download`) | Seeing what a file used to contain is seeing the file |
| Restore | `modify` | It rewrites the file |
| Delete a version | `delete` | It destroys data nothing else recovers. Being allowed to edit a document is not being allowed to erase the record of what it said |

### `GET /api/v1/nodes/{id}/versions`

```json
{
  "versions": [
    { "id": "...", "version": 2, "size_bytes": 4211, "name": "Report.pdf",
      "created_at": "...", "created_by": { "username": "josh", ... },
      "reason": "replaced", "reason_label": "Replaced" }
  ],
  "total": 2,
  "current": { "size_bytes": 4400, "name": "Report.pdf", "updated_at": "..." }
}
```

`current` is not a version; it is included because "which one am I looking at
now" is the first question anybody asks of a version list.

`reason` is `replaced` or `restored`. The second marks the snapshot taken of the
*current* content just before an older version was restored over it — which is
what makes restoring non-destructive.

`name` is what the file was called when the version was made, which may differ
from what it is called now.

Folders return 400: they have no content to version.

### `GET /api/v1/nodes/{id}/versions/{versionID}/download`

The same headers as any other download — `nosniff`, the sandboxing CSP, the
inline allowlist — because a version is uploaded content like any other and none
of those protections may be skipped for it.

The filename carries the version number (`Report (v2).pdf`), so downloading
three of them does not leave you with `Report.pdf`, `Report (1).pdf` and no idea
which is which.

### `POST /api/v1/nodes/{id}/versions/{versionID}/restore`

Points the file back at that version's content, and makes the content it
displaces a version of its own. Restoring the wrong thing is one more restore
away from being undone rather than gone.

Refuses if the content is missing from storage, rather than replacing a working
file with a broken one.

### `DELETE /api/v1/nodes/{id}/versions/{versionID}`

Removes one entry permanently, releasing its hold on the content. The bytes are
dereferenced, never unlinked here; the reconciliation job reclaims what nothing
references.

### Retention

Bounded three ways, any combination, all configurable:

| Setting | Meaning |
| --- | --- |
| `VERSIONS_MAX_PER_FILE` | Keep the newest N per file |
| `VERSIONS_MAX_AGE` | Discard versions older than this |
| `VERSIONS_MAX_TOTAL_SIZE` | Cap what one user's history may occupy |

Zero means unlimited for each. A policy with none of them set prunes nothing —
zero is never read as "delete everything".

Version numbers do not renumber when one is pruned: a number somebody has seen
must keep meaning the same thing.

---

## Sessions

### `GET /api/v1/auth/sessions`

The caller's own sessions. Each carries a `device` label — the user agent
reduced to something readable, server-side, so this and the administrator's view
cannot describe the same session differently.

### `DELETE /api/v1/auth/sessions/{id}`

Ends one. Ownership is verified first; an identifier belonging to somebody else
returns 404, because confirming it exists would tell the caller they guessed a
real one.

### `POST /api/v1/auth/sessions/revoke-others`

Ends every session except this one. Available to everybody, not just
administrators: it is what a person reaches for when they think somebody has
their password. It deliberately keeps the current session — signing yourself out
at the same moment would leave you unable to change it.

---

## Administration

Every route below requires an administrator, and the boundary is worth stating
precisely: **an administrator manages accounts and the instance, not other
people's files.** No endpoint here reads, lists, or downloads content, and the
permission model grants an administrator no access to it. Running a server is
not the same as being entitled to read what people store on it, and an audit
trail could not distinguish legitimate support from snooping if it were.

### `GET /api/v1/admin/overview`

Account counts, live sessions, storage and database health, queue counts, and a
24-hour audit summary. Nothing about anybody's content.

### `GET /api/v1/admin/users` · `POST /api/v1/admin/users`

Listing accepts `q`, `enabled`, `limit`, `offset`.

Creation takes a temporary password and defaults `must_change_password` to
**true**, so an administrator who sets somebody's password cannot go on signing
in as them. The password is checked against the same policy as any other.

### `PATCH /api/v1/admin/users/{id}`

`display_name`, `email`, `is_enabled`, `is_admin`, `must_change_password`, and
`quota_bytes`.

`quota_bytes` carries three states, which is one more than a pointer expresses:

| Sent | Means |
| --- | --- |
| omitted | leave the quota alone |
| `null` | unlimited |
| a number | that many bytes |

Disabling an account **ends its sessions immediately**. Leaving a cookie working
until it expires is not what "disabled" means to whoever clicked it.

An administrator cannot demote or disable themselves, and the store separately
refuses to remove the last administrator — otherwise nobody could administer the
server again.

### `POST /api/v1/admin/users/{id}/password`

Sets a new password and **revokes every session** for that account. A reset is
what an administrator does when an account may be compromised, and leaving the
intruder signed in would defeat the exercise.

### `POST /api/v1/admin/users/{id}/unlock`

Clears a lockout early.

### `DELETE /api/v1/admin/users/{id}`

Removes the account and everything it owned. The audit record survives, naming
who did it and who it was.

### `GET /api/v1/admin/sessions`

Live sessions across every account, bounded at 200, most recently active first.
Revoked and expired ones are excluded: this answers "who is signed in", and
history belongs in the trail.

### `POST /api/v1/admin/users/{id}/sessions/revoke`

Signs an account out everywhere.

---

## Audit trail

Separate from the structured log by design. A log is rotated and read while
debugging; an audit trail answers "who did this, and when" — a question somebody
asks months later, under pressure, often about an account that no longer exists.

Two properties are load-bearing:

- **It survives account deletion.** The actor's *name* is captured alongside
  their identifier, and the foreign keys are `ON DELETE SET NULL`, so removing
  an account does not render its history anonymous.
- **It never holds a credential.** No path writes a password, a session token, a
  public-link token, or a hash of any of them. Creating a link records that a
  link was made, never what it is.

Reads are deliberately absent. Recording every download on a file server would
drown the trail and make the audit table the largest thing in the database, so
the trail answers "who changed or shared this", not "who looked at it".

### `GET /api/v1/admin/audit`

| Parameter | |
| --- | --- |
| `action` | One action identifier, e.g. `auth.login`, `node.purge` |
| `outcome` | `success`, `failure`, or `denied` |
| `actor_id`, `target_id` | Everything one person did, or everything done to one object |
| `since`, `until` | RFC 3339 or `YYYY-MM-DD` |
| `q` | Matches actor and target labels |
| `limit`, `offset` | Default 50, maximum 500 |

`denied` is kept distinct from `failure`: a run of refusals is a different
signal from a run of mistakes.

Each event carries `label` — the action written for a person — so the interface
does not keep its own copy of the vocabulary.

### `GET /api/v1/admin/audit/vocabulary`

Every action identifier with its label, and the outcomes. The filter in the
interface is built from this, so it cannot offer something that is never
recorded or miss something that is.

### Retention

`AUDIT_RETENTION` defaults to a year. Zero keeps events forever and is
deliberately not read as "delete everything" — the pruning sweep refuses to run
rather than interpreting an unset value as a policy. In some jurisdictions
discarding this is the problem and in others keeping it is, so it is the
operator's decision rather than a default the software imposes.

`AUDIT_ENABLED=false` switches recording off entirely.

---

## Background jobs

### `GET /api/v1/admin/jobs`

Queue counts, the running jobs, and the failed ones with their handler's error.
Safe to display: handlers are written not to put user content or secrets in an
error message.

`oldest_pending_seconds` is the number worth watching — one that keeps climbing
means the workers are not keeping up, which no other figure reveals.

### `POST /api/v1/admin/jobs/{id}/retry`

Requeues a failed or cancelled job with its attempt counter reset, because an
administrator retrying has usually just fixed the cause and wants the full
budget.

A **running** job cannot be retried and returns 404: two workers executing the
same job is the single thing the claim protocol exists to prevent.

---

## Rate limiting

Every endpoint below `/api/v1` is bounded per caller per minute. Sign-in has had
its own limiter since Phase 2, protecting the *account*; this protects the
*server* from a caller who has already authenticated.

Keyed on the signed-in account where there is one, and the client address
otherwise — per-account rather than per-address, so a household behind one NAT
address is not throttled collectively.

| Budget | Default | Covers |
| --- | --- | --- |
| `RATE_LIMIT_READ` | 600 | Listings, metadata, downloads |
| `RATE_LIMIT_WRITE` | 120 | Anything that changes state, and folder archives |
| `RATE_LIMIT_UPLOAD` | 1200 | Upload sessions and chunk transfers |
| `RATE_LIMIT_SEARCH` | 60 | Trigram search |
| `RATE_LIMIT_ANONYMOUS` | 60 | The public-link portals |

The budgets are independent: exhausting one does not block work that costs
something different.

A refused request returns **429** with `Retry-After` in seconds:

```json
{
  "error": {
    "code": "rate_limited",
    "message": "You are making requests too quickly. Wait a moment and try again."
  }
}
```

Setting a budget to `0` switches it off. It is never read as "refuse
everything" — a misconfiguration must not become an outage. `RATE_LIMIT_ENABLED=false`
switches all of them off.

Folder archives are counted against the **write** budget despite being a `GET`,
because a subtree walk that reads every byte in it has nothing in common with
the cost of a listing.

---

## Malware scanning

Optional and off by default, because ClamAV needs roughly a gigabyte of RAM.

When enabled, content is streamed to `clamd` after it is staged and **before**
it is committed, so infected content never reaches the content-addressed store
and never gets a metadata row. Both upload paths — `POST /files` and the
resumable `POST /uploads/{id}/complete` — go through the same check.

| Outcome | Response |
| --- | --- |
| Clean | The upload proceeds normally |
| Infected | **422** naming the signature, so a person can tell a false positive from a real detection |
| Larger than `CLAMAV_MAX_SCAN_SIZE` | Stored **unscanned** and recorded as skipped |
| Scanner unreachable, `CLAMAV_REJECT_ON_ERROR=true` | **503** — nothing is wrong with the file, so retrying later is the right move |
| Scanner unreachable, `CLAMAV_REJECT_ON_ERROR=false` | Stored unscanned, with a warning in the log |

An infected upload:

```json
{
  "error": {
    "code": "validation_failed",
    "message": "the file matched a malware signature: Eicar-Test-Signature"
  }
}
```

Nothing about the refused content is retained beyond the audit record, unless
`CLAMAV_QUARANTINE=true` — in which case the bytes are kept under
`quarantine/`, addressed by digest. Nothing in the application ever serves from
there: the download path only knows how to address `files/`.

---

## Denials

A denial takes access away rather than granting it. It is the only way to
express "everyone in Accounting may edit this, except Bob": a denial beats every
grant, including one inherited through a group.

Create one by passing `deny` to `POST /api/v1/shares`:

```json
{
  "node_id": "...",
  "user_id": "...",
  "permissions": ["modify", "delete"],
  "deny": true
}
```

Four rules, each for a reason:

| Rule | Why |
| --- | --- |
| Denying needs `manage`, not `share` | Taking somebody's access away is administering the item rather than sharing it out. Requiring only `share` would let anyone a folder was shared with lock everybody else out of it. |
| A denial is **not** bounded by what the denier holds | Taking access away cannot increase anybody's, so denying more than you hold is harmless. Granting more than you hold is privilege escalation, which is why the rule differs. |
| A denial takes away **exactly** what it names | Unlike a grant, it does not imply `read`. Implying it would turn "you may not delete this" into "you may not see this at all". |
| The owner cannot be denied | Ownership is settled before denials are considered, so the entry would sit in the access list appearing to do something. Refused with a message naming the alternative: transfer ownership. |

A subject may hold **one grant and one denial** on the same node. Re-sharing
updates the grant; re-denying updates the denial. The evaluator subtracts the
denial from the grant, so denying `delete` leaves reading intact.

`GET /api/v1/nodes/{id}/shares` returns denials alongside grants, marked with
`"deny": true`. `GET /api/v1/shared-with-me` excludes them — and also excludes
any grant a denial has overridden, so a folder never appears in that list and
then 404s when opened.

Removing a denial is `DELETE /api/v1/shares/{id}`, the same as removing a grant,
and restores whatever the underlying grants give.

---

## Groups

Groups have been enforced by the permission model since Phase 5. The management
endpoints:

| Endpoint | Requires |
| --- | --- |
| `GET /api/v1/groups` · `GET /api/v1/groups/{id}` | Any signed-in user |
| `GET /api/v1/groups/mine` | Any signed-in user |
| `POST /api/v1/groups` · `PATCH` · `DELETE /api/v1/groups/{id}` | An administrator |
| `POST /api/v1/groups/{id}/members` · `DELETE .../{userID}` | An administrator, or a manager of that group |

Creating and deleting need an administrator because a group is an
instance-wide building block of access control; one anybody could create would
quickly become a directory of unmanaged ones. Membership is delegated, because
the person who runs a team knows who is on it.

**Deleting a group removes every share granted to it.** Anyone who reached a
folder only through that group loses access immediately, and nothing tells them
why.

---

---

## Not yet implemented

Returns 404 today.

Nothing planned remains unimplemented. Two things are deliberate non-goals:

```text
  GET/PATCH  /api/v1/admin/settings   changing settings without a restart
```

Configuration is read from the environment at startup. A runtime settings store
would mean two sources of truth for every value, and the drift between them is
a worse problem than the restart.

```text
  GET  /api/v1/admin/audit  ...with read events
```

Downloads and listings are not recorded. Doing so on a file server would drown
the trail and make the audit table the largest thing in the database. The trail
answers "who changed or shared this", not "who looked at it".
