# Build status

**All 12 planned phases complete, plus a thirteenth closing the gaps this document named as deferred.**

_Last updated: 2026-08-27_

This document records what actually works, what does not, and what is
deliberately deferred. It is updated at the end of every phase.

---

## Summary

| | |
| --- | --- |
| Phases complete | 1 Foundation, 2 Authentication, 3 Core file storage, 4 File browser, 5 Permissions and sharing, 6 Public sharing, 7 Productivity, 8 File versions, 9 Administration, 10 Hardening, 11 Raspberry Pi, 12 Installation and backup, 13 Closing the gaps |
| Backend unit tests | 259 test functions (340 with subtests) |
| Backend integration tests | 276 test functions (288 with subtests), against real PostgreSQL 17 |
| Script tests | 21 — backup, restore, and installer safety properties |
| Benchmarks | 7, against real PostgreSQL — `make bench` |
| Frontend tests | 208 |
| **Total** | **862 tests, all passing, `-race` clean** |
| Lint | clean — `go vet`, `gofmt`, `eslint`, `tsc --strict` |
| Full stack | ✅ starts, migrates, serves, and passes health checks |
| Verified on | Linux x86_64, Docker 29.7, Go 1.25.14, Node 24 |
| Verified on ARM64 | ⚠️ partly — the binary and container image build and start under emulation; **nobody has run it on a real board** |

## Quality gates

| Gate | Result |
| --- | --- |
| Backend compiles | ✅ `go build ./...` |
| Frontend compiles | ✅ `vite build` — 187 kB gzipped total |
| Backend tests | ✅ 632 pass, `-race` clean |
| Frontend tests | ✅ 209 pass |
| Script tests | ✅ 21 pass |
| Lint | ✅ clean |
| Migrations against a clean database | ✅ verified, and verified idempotent |
| Full Compose stack starts | ✅ app, postgres, caddy all healthy |
| Feature exercised end to end | ✅ over HTTP and in a real browser |
| Continuous integration | ✅ `.github/workflows/ci.yml` — every gate above, on every push, plus an ARM64 image build |

---

## What works

### Phase 1 — Foundation

Configuration with accumulated validation errors and unloggable secrets;
structured logging with redaction; a Pi-tuned pgx pool; checksum-verified
embedded migrations under an advisory lock; a storage root opened as an
`os.Root` so traversal and symlink escape are prevented by the kernel; the HTTP
middleware chain; `/health`, `/health/live`, `/health/ready`; the React app
embedded in the Go binary; the full Docker Compose deployment.

### Phase 2 — Authentication

- **Argon2id** password hashing, 64 MiB / t=2 / p=2, tuned for a Pi 4 and above
  the OWASP baseline. Parameters travel in the hash, so the cost can be raised
  later; a weaker hash is upgraded transparently at the next sign-in.
- **Password policy** following NIST SP 800-63B: length enforced, composition
  rules deliberately not. Blocks breach-corpus passwords, anything containing
  the username or email, repeated characters, sequences (including digit
  sequences that wrap 9→0), and short repeated patterns.
- **Server-side sessions**, so revocation is immediate. 256-bit tokens, only
  SHA-256 hashes stored. HttpOnly `SameSite=Lax` cookie; per-user session cap.
- **Sign-in** that is uniform in cost and message: an unknown username performs
  a dummy hash and returns the identical error as a wrong password. Whether an
  account is disabled is revealed only after the password verifies.
- **Account lockout** persisted in the database, plus in-memory per-IP and
  per-username rate limiting with a bounded key count. Locking revokes existing
  sessions.
- **CSRF**: signed double-submit cookie, HMAC of the session ID, constant-time
  comparison.
- **Password changes** requiring the current password and revoking every other
  session; administrator reset with an enforced must-change flag.
- **First administrator** via a browser setup flow that closes permanently once
  an account exists, or `pifiles -create-admin` for the installer.
- **Login, setup, and account pages**, with active-session management.

### Phase 3 — Core file storage

- **Unified `nodes` table**: files and folders with a `kind` discriminator, so
  permissions, sharing, trash, and search have one target type.
- **Content-addressed storage**: blobs are named by their SHA-256 digest and
  fanned out two levels. No user-supplied text ever reaches a filesystem path.
- **Streaming everything**: a fixed 256 KB buffer means a 20 GB upload costs the
  same memory as a 20 KB one. Verified with a 3 MB round trip on the live stack
  and an 8 MB streaming unit test.
- **Deduplication with reference counting**: identical content is stored once;
  copying a folder duplicates rows, not bytes.
- **Resumable chunked uploads**: sequential offsets, durable flush before
  progress is recorded, optional checksum verification, `X-Upload-Offset` on a
  mismatch so a client can resume correctly. Verified with a simulated
  interruption on the live stack.
- **Downloads with HTTP range support**, correct `Content-Disposition` for
  non-ASCII names, and headers that prevent uploaded content from executing.
- **Server-side MIME sniffing** that neutralises script-capable types.
- **Folder operations**: create, rename, move, copy (with automatic
  `Report (2).pdf` naming), soft delete, restore, permanent delete — all
  transactional, with cycle detection and quota accounting.
- **Trash** listing only the top of each deleted subtree.
- **Quota enforcement** pre-checked before transfer and re-checked against the
  actual size, with a recalculation routine that rebuilds the counter.
- **Reconciliation** that detects orphaned blobs, missing content, stale
  temporary files, and wrong reference counts — reporting by default, repairing
  on request, and never "repairing" data loss by deleting metadata.
- **`make db-check`** for an at-a-glance consistency report.

### Phase 4 — File browser

- **Sidebar navigation** with My Files, Shared with me, and Trash live; entries
  for unbuilt features shown but disabled with the phase they arrive in, rather
  than hidden or linking nowhere.
- **List and grid views**, with the specified columns — Name, Owner, Modified,
  Size, Sharing — and a remembered preference.
- **Breadcrumbs** that collapse the middle of a deep path on a narrow screen.
- **Sorting** by name, size, modified, or created, folders always first.
- **Pagination** with a clamped page size.
- **Drag-and-drop upload** onto the browser or onto a specific folder row, and
  **drag-and-drop movement** between folders and onto breadcrumbs.
- **Chunked resumable uploads** driven from the browser through XMLHttpRequest
  (fetch cannot report upload progress), with a bounded queue of three
  concurrent transfers, per-file progress, cancel, retry, and a panel that
  survives navigation. A dropped connection resumes from the server's offset.
- **Multi-select** with the desktop interaction model: click, ctrl/cmd-click,
  shift-click range from a stable anchor.
- **Keyboard navigation** throughout — arrows, Home/End, Enter to open, Space
  to select, F2 to rename, Delete to trash, Ctrl+A, Escape — on a roving
  tabindex so Tab enters the grid once.
- **Context menus** on accessible Radix primitives, with correct focus
  restoration and type-ahead.
- **Responsive layout**: the sidebar becomes a drawer, columns drop
  progressively, action menus stay visible on touch.
- **Toasts, confirmation dialogs, and explicit loading, empty, and error
  states** for every view.

### Phase 5 — Permissions and sharing

- **One grants table.** A share *is* a permission grant; there is no separate
  shares table, so a revoked share cannot leave access behind.
- **Ten fine-grained permissions** as a bitmask, with four named roles built
  from them. Bit values are pinned by a test, because changing one would
  silently re-interpret every stored grant.
- **Inheritance** down the folder tree, including content added after the share
  was created, with `inherit: false` to pin a grant to one node.
- **Group grants**, unioned with direct grants. Joining or leaving a group
  changes access on the very next request.
- **Denials** that override any grant at any depth, so one subfolder can be
  carved out of an otherwise-shared tree.
- **Owner precedence**: ownership is not a grant and cannot be revoked by one,
  not even by a denial aimed at the owner.
- **No privilege escalation**: nobody may grant a permission they do not hold.
- **Sharing is not transitive**: passing access on requires `share`, which only
  the manager role includes.
- **Groups** with per-group managers, so a team lead can run their own team
  without being an administrator. Membership is invisible to non-members.
- **Shared with me**, showing the owner, whether access came directly or
  through a group, the role, and who shared it.
- **Share dialog** whose roles come from the server, so the interface cannot
  offer one the server does not recognise.
- **Permission-aware breadcrumbs**, truncated to the ancestors the caller can
  actually see.
- **Batched evaluation**: a folder listing evaluates every child in one query
  rather than one per row.

### Phase 6 — Public sharing

- **One table for both link kinds.** A share link (`/s/`) and a file request
  (`/u/`) are the same object with a different permission bitmask. Two tables
  would mean writing the security-critical resolution path twice, and the two
  copies would drift.
- **Tokens are 256 bits of `crypto/rand`**, base64url-encoded to 43 characters.
  Only the SHA-256 of a token is stored: the database never holds a value that
  opens a link, and the plaintext is returned exactly once, at creation.
- **Every failure looks the same.** Unknown, expired, revoked, exhausted, and
  malformed all return the identical 404. An expired link is indistinguishable
  from one that never existed.
- **A file request cannot read anything.** Its permission set is
  `Upload | Create` with no `Read`, so a visitor can add files and cannot
  enumerate what anybody else submitted — tested by asserting the portal
  response carries no names, no counts, and no identifiers.
- **Containment.** A folder link exposes that folder's direct children and
  nothing else: a request for an item elsewhere in the tree is refused even
  when the identifier is valid, so a link cannot be walked upward.
- **Password protection** uses the same Argon2id hasher as sign-in, behind a
  rate limiter, and grants a short-lived HMAC access cookie scoped to that one
  link's path. The grant is bound to the link it was issued for.
- **You cannot publish more than you hold.** Creating a link requires `Share`
  on the item, and the link's permissions are intersected with the creator's.

### Phase 7 — Productivity features

- **Search** is PostgreSQL trigram matching (`gin_trgm_ops`) behind a `Backend`
  interface. No Elasticsearch, no second stateful service, no extra gigabyte of
  RAM. Visibility is enforced *inside* the query by a recursive ancestry CTE
  joined against `permissions`, so the result count is as private as the rows:
  a total that included other people's files would leak their existence just as
  surely as returning their names.
- **Favourites** are per-user. Two people may star the same shared folder and
  one removing it does not affect the other. Items the user can no longer see
  drop out of the list; unfavouriting still works after access is revoked, so a
  stale entry is never stuck.
- **Recent** stores one row per (user, node, action), updated rather than
  appended. An activity log would grow without bound and would show the same
  file twenty times after twenty downloads; what the view wants is "when did I
  last touch this", which is one row.
- **Tags** are owner-scoped: two users may each have "Urgent" without
  collision, and a tag applied to a shared file is invisible to the other
  party. The colour is validated as a plain six-digit hex value — it reaches a
  CSS context in the browser, so it is refused rather than sanitised.
- **ZIP downloads** stream. Building the archive in a temporary file first
  would need as much free disk as the archive is large, which on a Pi with a
  modest SSD is exactly the case that fails. Peak cost is one 256 KB buffer
  whether the folder holds 10 MB or 200 GB. Already-compressed types are stored
  rather than deflated, which makes a photo folder download several times
  faster on ARM.
- **Thumbnails** are generated in the background for raster images only. The
  decoder reads the header first and refuses anything past 80 megapixels, so a
  100 KB "decompression bomb" declaring a 30000×30000 canvas is rejected before
  it can ask for 3.6 GB on a board that has 4. PDFs, text, audio and video are
  previewed by the browser from the existing download endpoint, which costs the
  server nothing.
- **A durable job queue in PostgreSQL.** `FOR UPDATE SKIP LOCKED` gives safe
  concurrent claiming, jobs survive a restart, and it adds no Redis — which
  would cost roughly 50 MB resident to solve a problem the database already
  solves. Deduplication keys collapse equivalent work, failures back off
  exponentially, permanent failures stop immediately, and a panicking handler
  fails its job rather than the worker.
- **Ten housekeeping sweeps** now actually run: session expiry, grant expiry,
  link expiry, abandoned uploads, trash retention, activity pruning, stalled
  previews, orphaned thumbnails, finished jobs, and a read-only storage
  reconciliation. Their intervals are deliberately unsynchronised (11, 17, 23,
  29 minutes) so they do not all fire in the same second.

### Phase 8 — File versions

- **A version is a reference, not a copy.** When a file is replaced the outgoing
  content already exists as a blob; the version row takes over the reference the
  node was holding, and no bytes move. Copying would double the storage cost of
  every edit on a machine chosen for its modest disk — and it would be wrong
  besides, because a blob shared by two nodes cannot be moved out from under one
  of them. This is also why `STORAGE_ROOT/versions/` stays empty: content
  addressing made a separate directory unnecessary rather than the design
  overlooking it.
- **Restoring is never destructive.** The content a restore displaces becomes a
  version of its own, marked as such, so restoring the wrong thing is one more
  click from being undone rather than gone. That is the entire reason version
  history is worth having.
- **Replacing with byte-identical content creates nothing.** The digest is the
  same, so there is nothing to go back to; a version of it would be an entry
  that changes nothing, which is noise in the one place a person reads
  carefully.
- **Numbers do not renumber.** Pruning version 2 leaves 1 and 3 as 1 and 3, and
  the next replacement is 4. A number somebody has seen must keep meaning the
  same thing.
- **The permission each operation needs is deliberately not uniform.** Listing
  and downloading need Read; restoring needs Modify, because it rewrites the
  file; deleting a version needs **Delete**, because being allowed to edit a
  document is not the same as being allowed to erase the record of what it used
  to say.
- **Retention is bounded three ways** — per file, by age, and by total bytes per
  user — and a policy with none of them set prunes nothing. Version storage is
  outside the quota by design and capped separately, so replacing a 1 GB file
  repeatedly cannot fill the disk while the quota still reads 1 GB.

### Phase 9 — Administration

- **An administrator manages accounts and the instance, and nothing else.**
  There is no endpoint in the administration API that reads, lists, or downloads
  anybody's content, and the permission model does not grant an administrator
  access to it. Running a server is not the same as being entitled to read what
  people keep on it — and an audit trail could not tell legitimate support from
  snooping if it were. Tested by asking for another user's file, folder listing,
  archive, version history, and thumbnail as an administrator, and asserting a
  404 from every one.
- **A queryable audit trail**, separate from the structured log. A log is
  rotated and read while debugging; an audit trail answers "who did this, and
  when", months later, often about an account that no longer exists. Sign-ins
  and their failures, password changes, deletions, shares, publications,
  version restores, and every administrator action are recorded.
- **The trail survives account deletion.** The actor's *name* is captured
  alongside their identifier, so removing an account does not render its history
  anonymous. Foreign keys are `ON DELETE SET NULL` for exactly this reason.
- **Nothing in the trail is ever a credential.** No path writes a password, a
  session token, a link token, or a hash of one — asserted by creating a link
  and a failed sign-in and then searching the whole trail for the secrets.
- **Failures are recorded as carefully as successes**, and denials separately
  from mistakes: a run of refusals is a different signal from a run of typos.
- **Disabling ends sessions; resetting a password ends sessions.** Leaving a
  cookie working until it expires is not what "disabled" means to whoever
  clicked it, and a reset is what an administrator does when an account may be
  compromised.
- **An administrator cannot strand the instance** — no demoting or disabling
  themselves, no deleting their own account, and the store separately refuses to
  remove the last administrator.
- **A "sign out everywhere else" button for everybody**, not just
  administrators. It is what a person reaches for when they think somebody has
  their password, and it deliberately keeps the current session — signing
  yourself out at the same moment would leave you unable to change it.
- **The job queue is visible.** Failed jobs show their handler's own error and
  can be retried with a fresh attempt budget. A running job cannot be retried:
  two workers executing it at once is the single thing the claim protocol exists
  to prevent.

### Phase 10 — Hardening

- **API rate limiting**, protecting the *server* the way lockout has protected
  the *account* since Phase 2. Five independent budgets, because the operations
  differ by orders of magnitude in cost and one number generous enough for a
  listing would be useless against a trigram search. Keyed on the account where
  there is one and the address otherwise — per-account so a household behind one
  NAT address is not throttled collectively. A refusal returns 429 with
  `Retry-After`, which the upload queue reads and reports rather than retrying
  straight into the wall.
- **Zero means unlimited, never "refuse everything".** A misconfiguration must
  not become an outage.
- **Optional ClamAV scanning**, streamed over INSTREAM after the content is
  staged and *before* it is committed, so infected content never reaches the
  content-addressed store and never gets a metadata row. Both upload paths —
  single-request and resumable — go through the same check.
- **Three genuine trade-offs are configuration rather than defaults**, because
  both answers are defensible: what to do when clamd is unreachable, whether
  content above the scan limit is stored unscanned, and whether infected content
  is kept for investigation. Each is documented with the reasoning, and the
  defaults are the ones that fail safe for a family server.
- **A scan that cannot reach clamd backs off for 30 seconds**, so a stopped
  container does not add the full connection timeout to every upload.
- **The Content-Security-Policy no longer allows inline styles blanket-wide.**
  `style-src 'self'` forbids injected `<style>` elements and remote stylesheets;
  `style-src-attr 'unsafe-inline'` permits the element style attributes React
  genuinely needs. A narrowing rather than a removal, and it is described as one:
  a `<style>` block can match anything on the page and exfiltrate attribute
  values through selector-driven requests, while an attribute affects only the
  element it is on.
- **A startup warning when forwarded-header trust is misconfigured.** The
  default is right behind Docker and wrong if the application port is published
  directly; the process cannot tell which topology it is in, so it states the
  condition and what it means rather than guessing.
- **`docs/security.md`** — the threat model, including what is *not* defended
  against.

### Phase 11 — Raspberry Pi

- **ARM64 builds and runs.** The binary is 13 MB, the container image 10.1 MB,
  and both start correctly under QEMU emulation.
- **Seven benchmarks against a real PostgreSQL** (`make bench`), covering
  listing, permission evaluation, batched evaluation, search, upload, download,
  and archiving.
- **The measurements found and fixed a real duplicate.** A folder listing was
  evaluating permissions on the folder twice — once resolving it, once building
  the response — which is a second recursive ancestry query per request for an
  answer already in hand. Removing it took about 7% off the fixed cost.
- **What the benchmarks establish transfers; the numbers do not.** A listing is
  a fixed number of queries rather than one per row; batched permission
  evaluation is genuinely sub-linear (6 µs per item at 100, 24 µs at 10); a
  32-deep tree costs less than twice a 1-deep one. Those shapes hold whatever
  the clock speed. The absolute figures are from a development machine and are
  optimistic by roughly five to eight times.
- **The memory claim is verified under Pi-sized caps.** Running the stack with
  the production limits (`make up-pi-limits`), a single 2 GB upload moved
  resident memory from 133 MiB to 136 MiB, and four concurrent 2 GB downloads
  peaked at 153 MiB. The allocation profile agrees: an upload allocates about
  304 KB whether the file is 1 KiB or 8 MiB.
- **`docker-compose.pi.yml`** applies those caps on a development machine. It
  does not make a fast machine slow; it catches the class of bug that only shows
  up under a cap.

### Phase 12 — Installation and backup

- **A guided installer** for somebody who has never used Docker. Four questions
  in plain language, and it refuses to guess at anything expensive to get wrong.
  It never formats a disk, never changes ownership of a directory it did not
  create, never overwrites an existing configuration, and is safe to run twice.
  It warns — rather than refusing — when the storage location is on a microSD
  card, and when the address is unencrypted and does not look like a home
  network.
- **A backup script that never reports success unless every step succeeded.**
  It refuses to run with the database stopped, checks free space first, reads
  the dump back with `pg_restore --list` and the archive back with `tar --test`,
  records and immediately re-verifies checksums, and **deletes its own output on
  any failure** — so nothing is ever left behind that could be mistaken for a
  usable backup.
- **`--verify` mode** for checking a backup without restoring it. Worth running
  on a schedule: bit rot on a cheap USB drive is real, and discovering it while
  restoring is discovering it too late.
- **A restore that verifies everything before touching anything.** A restore
  that gets halfway and then finds the archive corrupt has destroyed the
  instance it was meant to save.
- **It refuses to merge two installations.** The result would be a database
  referring to content that is not there, which is worse than either instance
  alone. `--force` plus a typed `replace` — not a keypress — overrides it, and
  the previous content is *moved aside* rather than deleted.
- **An upgrade path safe to run without reading anything first.** It backs up
  first and refuses to proceed if the backup fails, because migrations are
  forward-only and that backup is the only way back.
- **`docs/backup-restore.md`**, including a restore rehearsal against a scratch
  instance — fifteen minutes that turn a hope into a backup.

### Phase 13 — Closing the gaps

Not in the original twelve-phase plan. This is the work this document itself
listed as deferred, done rather than carried forward.

- **Denials are reachable at last.** The permission model has supported them
  since Phase 5 and nothing could create one — they existed only in tests that
  wrote to the database directly, which meant the feature was real for the
  evaluator and imaginary for anybody using the product. They now have an API
  field, a control in the sharing dialog, and their own audit action.
- **Making them reachable immediately exposed three defects** that could never
  have surfaced while the feature was unreachable. All three are in the table
  below; the most serious is that a denial *replaced* a grant rather than
  reducing it, so "you may read this but not delete it" silently became "you may
  do nothing at all".
- **A group management screen.** Groups have been enforced since Phase 5 and had
  no interface, so using them required curl. Deleting a group is the
  consequential action and the page says so: every share granted to it
  disappears, and anyone who reached a folder only through it loses access with
  nothing to tell them why.
- **The audit trail is complete for changes.** Renames, moves, and copies were
  absent; "what happened to the file called X" was unanswerable. A rename now
  records what the file *was*, which is the only thing that makes the record
  findable from the old name.

### Endpoints

| | Status |
| --- | --- |
| `/health`, `/health/live`, `/health/ready` | ✅ |
| `/api/v1/config`, `/api/v1/version` | ✅ |
| `/api/v1/setup`, `/api/v1/auth/*` | ✅ |
| `/api/v1/nodes*`, `/api/v1/trash` | ✅ |
| `/api/v1/files`, `/api/v1/uploads*` | ✅ |
| `/api/v1/shares`, `/api/v1/shared-with-me` | ✅ |
| `/api/v1/groups*`, `/api/v1/sharing/vocabulary` | ✅ |
| `/api/v1/public-links*`, `/s/{token}`, `/u/{token}` portals | ✅ |
| `/api/v1/search` | ✅ |
| `/api/v1/favorites`, `/api/v1/nodes/{id}/favorite` | ✅ |
| `/api/v1/recent` | ✅ |
| `/api/v1/tags*`, `/api/v1/nodes/{id}/tags*` | ✅ |
| `/api/v1/nodes/{id}/archive`, `/api/v1/archive` | ✅ |
| `/api/v1/nodes/{id}/preview`, `.../preview/status` | ✅ |
| `/api/v1/nodes/{id}/versions*` | ✅ |
| `/api/v1/auth/sessions*` | ✅ |
| `/api/v1/admin/overview`, `/admin/users*` | ✅ |
| `/api/v1/admin/sessions`, `/admin/audit*`, `/admin/jobs*` | ✅ |
| Everything else | ⬜ later phases |

### Database schema

| Table | Status |
| --- | --- |
| `schema_migrations`, `system_settings` | ✅ Phase 1 |
| `users`, `groups`, `group_members`, `sessions` | ✅ Phase 2 |
| `nodes`, `blobs`, `uploads` | ✅ Phase 3 |
| `permissions` | ✅ Phase 5 — a share is a grant; no separate `shares` table |
| `public_links` | ✅ Phase 6 — one table for share links and file requests alike |
| `favorites`, `recent_items`, `tags`, `file_tags` | ✅ Phase 7 |
| `background_jobs`, `previews` | ✅ Phase 7 |
| `versions` | ✅ Phase 8 — references a blob rather than copying bytes |
| `audit_events` | ✅ Phase 9 |

> A separate `shares` table is deliberately absent: modelling a share apart from
> the grant it creates would be two sources of truth for one question. The same
> reasoning kept `upload_requests` out of Phase 6 — a file request *is* a public
> link whose permission set omits `Read`.

---

## What does not work yet

No mock or placeholder implementation of any of this exists.

- **No settings screen.** Everything is configured by environment variable and
  read at startup; there is no way to change a setting without a restart. This
  is a deliberate non-goal rather than an unfinished one: a runtime settings
  store would mean two sources of truth for every value, and the drift between
  them is a worse problem than the restart.
- **Reads are not audited.** Deletions, restores, renames, moves, copies,
  shares, denials, publications, and version changes all are. Downloads and
  listings are deliberately absent: recording every read on a file server would
  drown the trail and make the audit table the largest thing in the database.
  The trail answers "who changed or shared this", not "who looked at it".
- **No full-text search inside documents.** Search matches names only. Indexing
  file *contents* would mean extracting text from PDFs and Office formats,
  which is a different order of cost on this hardware.
- **No previews for PDFs, video, or audio generated server-side.** They render
  in the browser from the download endpoint; there is no server-side
  rasterising or transcoding, deliberately.
- **No tag management screen.** Tags are created and applied from a file's
  details; renaming, recolouring, and deleting exist in the API but have no
  dedicated page yet.
- **Nobody has run this on a Raspberry Pi.** The ARM64 binary and image build
  and start under emulation, and the memory behaviour is verified under
  Pi-sized caps — but sustained throughput to USB storage, thumbnail time for a
  large photograph, and behaviour under thermal throttling are all unmeasured.
- **Malware scanning has not been tested against a real ClamAV.** The INSTREAM
  client is tested against a fake clamd that speaks the protocol; the
  handshake, the chunking, and every verdict path are covered, but no test has
  ever fed a real signature database.
- **Rate limits are per process and reset on restart**, and bound request rate
  rather than resource consumption.
- Denials are not exposed in the sharing dialog; they are API-only.

---

## Known defects

None outstanding.

Defects found and fixed during Phases 2 and 3, each caught by a test:

| Defect | Cause | Fix |
| --- | --- | --- |
| Sharing revealed that a hidden node exists | Attempting to share a node the caller could not see returned 403, confirming a guessed identifier named something real | Return 404 unless the caller can already read it |
| Breadcrumbs disclosed private ancestors | The path was walked without a permission check, so a shared folder showed the owner's parent folder names and labelled their root "My Files" | Truncate the path to the ancestors the caller can read |
| A grant with no creator failed to save | A zero UUID was written to a foreign key instead of NULL | Store NULL for a system-created grant |
| The file browser disabled every action | Menu items keyed off a permission list the server might not send | Offer the action when the list is absent; the server still refuses, with a clearer message |
| Role radio buttons were indistinguishable to a screen reader | The whole explanatory sentence was the control's accessible name | Name the control by the role, describe it with `aria-describedby` |
| `123456789012` accepted as a password | Sequence detection did not handle digits wrapping 9→0 | Compare digits modulo 10; also added repeated-pattern detection |
| Uploaded HTML stored as `text/html` | Only the serving layer refused to render it, one bug away from stored XSS | Neutralise script-capable types at detection |
| Blob record survived a permanent delete | The row was left with `ref_count=0` and no bytes, which reconciliation would report as data loss | Delete the record before the bytes, guarded on the count still being zero |
| Checksum mismatch returned 500 | Fell through to the generic internal-error path, so the user never saw why | Dedicated sentinel error mapped to 422 |
| Migration tamper test broke as the schema grew | It replaced the whole migration set, so the downgrade check fired first | Build the tampered set from the real embedded files |
| `make db-query` broke on quoted SQL | Value interpolated into a shell `-c` | Pipe via stdin; added `make db-check` with the SQL in a file |

Defects found and fixed during Phases 6 and 7:

| Defect | Cause | Fix |
| --- | --- | --- |
| A 20 GB upload would fail on its final metadata write | chi's `Group` inherits parent middleware, so the 30-second JSON timeout reached the transfer routes after all — the opposite of what an earlier comment claimed | Two sibling groups under `/api/v1`; the timeout made configurable so a regression test can prove the boundary |
| A database hiccup signed everyone out | `Authenticate` treated *any* session-validation error as a stale cookie and cleared it | Distinguish definitive invalidity from infrastructure failure; log and continue on the latter |
| The portal showed the filename twice | Rendered in both the heading and the item block; the password error appeared as both alert title and body | Found by writing the test, not by reading the code |
| The preview dialog was permanently blank | `frame-ancestors 'none'` on the download response blocks PiFiles' own iframe as surely as anybody else's, and fails silently | `frame-ancestors 'self'` — the directive exists to stop *other* origins framing a user's content; `sandbox` is what actually contains the document and is unchanged |
| Downloading your root produced a folder called `unnamed` | A root has no stored name — "My Files" is an interface label — and `sanitizeSegment("")` fell back to a placeholder | Put a root's children at the top level of the archive |
| A newly uploaded file claimed you did not own it | `is_owner` is a bool with no `omitempty`, and the handlers returning a bare node never populated it | `toNodeResponse` takes the caller, so the answer is right by construction everywhere |
| Search results lacked stars, tags, and thumbnails | The search handler built its own response instead of using the batched decoration | One decoration path for every flat listing |
| Two identical search boxes on the search page | The header box and the page's own box both rendered, leaving the user to guess which the filters belonged to | The header box renders nothing on `/search` |
| A truncated password hash was accepted as valid | `DecodeHash` checked only that base64 decoded to something non-empty; a hash cut short still parsed, which also made the test for it flaky | Enforce the same salt and key length floors `Validate` applies — cost parameters deliberately still not checked there, so an old weak hash is upgraded rather than locking its owner out |
| A tag filter could be satisfied by somebody else's tag | The search SQL matched tag *names* without scoping to the caller's own tags | Scope the join to `t.owner_id` |

Defects found and fixed during Phases 8 and 9:

| Defect | Cause | Fix |
| --- | --- | --- |
| Purging a versioned file leaked its storage forever | The foreign key cascaded the version rows away, but a cascade cannot decrement a reference count — so the blobs stayed "referenced" and were never reclaimed | Delete the version rows explicitly inside the purge transaction, releasing each reference |
| The consistency check reported a false alarm on every versioned file | Both `make db-check` and the reconciler counted only *node* references to a blob | Count node and version references; under repair the old query would have "fixed" counts downward and handed still-referenced bytes to the sweeper |
| A wrong password signed you out of the session you already had | `handleLogin` revoked the existing session *before* authenticating, so a mistyped retry from a stale tab ended the working one | Revoke after a successful sign-in; session fixation is still prevented because a successful sign-in always issues a new identifier |
| Clearing a quota silently did nothing | `quota_bytes: null` reached a `*quotaField` pointer, and `encoding/json` sets a pointer field to nil for a JSON null without ever calling `UnmarshalJSON` — so "null" and "absent" arrived identically | A value type with a `Present` flag, which is always unmarshalled when the key appears |
| The details dialog showed a stale size after a restore | It held the node object it was opened with, so the header contradicted the version list directly beneath it | Hold the identifier and resolve from the listing on every render |
| The audit trail labelled an unauthenticated actor "no account" | A failed sign-in and a deleted account look identical there, and the name may well belong to a real account | "Not signed in", which is true of both |
| A truncated password hash was accepted as valid | Carried over from Phase 7's fix; listed there |

Defects found and fixed during Phases 10 and 11:

| Defect | Cause | Fix |
| --- | --- | --- |
| A folder listing evaluated the same permission twice | `resolveFolder` computed the decision and discarded it; the handler then asked again | Return the decision alongside the folder — about 7% of a listing's fixed cost, found by benchmarking rather than by reading |

Defects found and fixed during Phases 12 and 13.

All three denial defects share a cause worth naming: the feature was
unreachable, so nothing exercised it. A permission model that supports something
no interface can produce is a model with untested paths in it, and every one of
these was waiting there since Phase 5.

| Defect | Cause | Fix |
| --- | --- | --- |
| A denial replaced a grant instead of reducing it | The unique index was `(node, subject)` — one row per subject — while the evaluator accumulates grants and denials separately and subtracts. Denying Bob the delete permission overwrote his read grant, so "you may read this but not delete it" silently became "you may do nothing at all" | Migration 0009 puts `deny` in the index, so one grant and one denial can coexist as the evaluator always assumed |
| Denying one permission removed all access | `Parse` implies `Read`, which is right for a grant — you cannot download what you cannot find — and dangerous for a denial, where it turns "you may not delete this" into "you may not see this at all" | `ParseExact` for denials: take away exactly what was named |
| A denied folder still appeared in Shared with me | `SharedWithMe` filtered out denial *rows* but not grants a denial had overridden, so the folder was listed and 404'd when opened — which reads as a broken product rather than an access decision | Exclude a grant when a denial removing `Read` applies; a partial denial leaves it listed, because the folder does still open |
| `pg_restore --list` could not read the dump it had just written | A custom-format archive is read by seeking, and it was being piped | Write to a file inside the container first |
| The restore's confirmation prompt read EOF | An earlier `docker compose exec -T` consumed the script's stdin | `</dev/null` on every exec that does not read stdin, and read the confirmation from `/dev/tty` when one can actually be opened — `[ -r /dev/tty ]` is true in environments where the open then fails |

Defects found after Phase 13, while preparing the release: the first two came
from photographing the running application for the README, which is a different
activity from reading it. The interface asserts things, and a screenshot makes
you read the assertions.

| Defect | Cause | Fix |
| --- | --- | --- |
| The Sharing column said `Private` next to files that were shared and published | `shared` means "reached through a grant" — the *recipient's* side. For an owner it is false on every row, and the column rendered false as "Private", so a file with a public link to the whole internet was labelled private to the person who published it | A `shared_out` field, batched like the other per-row decorations: true when a node carries a live non-deny grant or a usable public link. Sent only to the owner, because who else holds a share is not a recipient's business |
| A focus ring sat on the first row of every listing on load | The roving-tabindex pattern keeps exactly one row at `tabIndex 0` so Tab can enter the grid; the ring was drawn from that same flag rather than from real focus | `focus-visible:` instead. Arrow navigation calls `focus()` on the row it moves to, so keyboard users see the ring exactly when it means something |
| The administration overview reported the database at `0 ms` | Latency is whole milliseconds and a local PostgreSQL answers faster than that. Next to a health indicator, `0 ms` reads as "not measured" | Render sub-millisecond as `<1 ms` |

Phase 1 defects are listed in the git history of this file.

---

## Security posture

Implemented and tested:

| Control | Status |
| --- | --- |
| Path traversal | ✅ Structurally impossible — content addressing; 13 malformed-digest cases tested |
| Symlink escape | ✅ `os.Root` confinement, tested with a planted symlink |
| SQL injection | ✅ pgx extended protocol; the one `ORDER BY` concatenation is from a validated enum and tested against injection |
| **IDOR / broken access control** | ✅ Every content operation tested cross-user; 404 not 403; responses asserted byte-identical to a nonexistent object |
| **Privilege escalation via sharing** | ✅ Nobody may grant a permission they do not hold; sharing is not transitive; an editor cannot revoke another's access — all tested |
| Privilege escalation | ✅ Administrators cannot read other users' content, even with the permission model in place; tested |
| Access outliving its grant | ✅ Revoking a share, leaving a group, deleting a group, and expiry each take effect on the next request; tested |
| Recipient enumeration | ✅ A recipient sees only their own entry in an access list; group membership is invisible to non-members; tested |
| Path disclosure through breadcrumbs | ✅ Truncated to what the caller may see; tested |
| CSRF | ✅ Signed double-submit; missing, forged, and cross-session tokens all tested |
| Session fixation | ✅ A new sign-in issues a new token and kills the old session; tested |
| Brute force | ✅ Lockout plus two rate limiters; lockout revokes sessions; tested |
| Account enumeration | ✅ Uniform sign-in errors with equal cost; tested |
| Stored XSS | ✅ Server-side sniffing, dangerous types neutralised, `nosniff`, sandboxing CSP, `Content-Disposition` |
| MIME confusion | ✅ Client-declared types never stored; content wins over extension; tested |
| Malicious filenames | ✅ Control characters, path separators, Windows device names, and bidi overrides rejected; tested |
| Response splitting | ✅ `Content-Disposition` sanitised; tested |
| Secrets in logs | ✅ Three independent layers; 19 attribute names tested in both formats |
| Secrets in responses | ✅ Password hashes unserialisable; content hashes never published; tested by reflection |
| Upload abuse | ✅ Size limits, quota pre-check and re-check, bounded chunk size, abandoned-upload sweep |
| Source-IP spoofing | ✅ Forwarded headers honoured only from trusted proxies; tested |
| HSTS correctness | ✅ Sent only over genuine HTTPS |
| No default password | ✅ Nothing ships with credentials |
| **Public-link token disclosure** | ✅ Only a SHA-256 is stored; the plaintext is returned once, at creation, and never again; tested by reflection over the stored row |
| **Link enumeration** | ✅ Unknown, expired, revoked, exhausted, and malformed tokens return byte-identical 404s; probing is rate limited |
| **File-request read access** | ✅ An upload portal holds `Upload \| Create` and no `Read`; a visitor cannot see or count other submissions; tested |
| **Link containment** | ✅ A folder link reaches its direct children and nothing else; a valid identifier from elsewhere in the tree is refused; tested |
| **Access-grant reuse across links** | ✅ The password cookie is an HMAC bound to one link ID and scoped to its path; tested |
| **Publishing beyond your own rights** | ✅ A link's permissions are intersected with the creator's; tested |
| **Search as a disclosure channel** | ✅ Visibility is in the SQL, so both the rows *and* the count are private; revocation takes effect on the next query; tested |
| **Wildcard injection into `ILIKE`** | ✅ `%`, `_`, and `\` escaped; a bare `%%` query matches nothing; tested |
| **Personal metadata as a side channel** | ✅ Favourites, tags, and recent activity are per-user and invisible to anybody else, including on a shared file; tested |
| **Decompression bombs** | ✅ Dimensions read from the header and refused past 80 megapixels *before* any pixel is decoded; source bytes capped; tested |
| **Zip Slip** | ✅ Entry names stripped of separators, dot segments, and control characters; case-colliding names disambiguated so extraction cannot silently overwrite; tested |
| **Bulk export beyond your rights** | ✅ A folder ZIP requires `Read` *and* `Download`; a selection archive checks each item and silently omits what the caller cannot read; tested |
| **Framing of user content** | ✅ `frame-ancestors 'self'` — PiFiles may preview it, no other origin may frame it; `sandbox` blocks scripts, forms, popups, and same-origin access regardless |
| **SVG and HTML uploads** | ✅ Neutralised into inert types at detection, so neither is ever served as itself; the preview classifier independently refuses them; tested on both sides |
| **Administrator access to content** | ✅ No administration endpoint reads, lists, or downloads anybody's files; the permission model grants an administrator nothing extra; tested against a file, a folder listing, an archive, a version list, and a thumbnail |
| **Version history as a back door** | ✅ A version carries no permissions of its own; every route resolves the file first and checks against that, so a valid version identifier cannot reach a file the caller may not open — tested by naming one version under a different file |
| **Erasing evidence** | ✅ Deleting a version needs Delete, not Modify: being allowed to edit a document does not entitle somebody to erase what it used to say |
| **Retention as a data-loss vector** | ✅ A retention of zero means "keep everything" for trash, versions, and the audit trail alike; each store refuses to run rather than reading it as "delete everything"; tested for all three |
| **Credentials in the audit trail** | ✅ No path writes a password, session token, link token, or hash; asserted by searching the whole trail for a link token and two attempted passwords |
| **Audit trail survivability** | ✅ Actor and target names are captured at the time, and foreign keys are SET NULL, so deleting an account does not make its history anonymous; tested |
| **Session outliving a disable or reset** | ✅ Both revoke every session for the account immediately; tested |
| **Cross-user session revocation** | ✅ Ownership is verified before revoking, and a valid identifier belonging to somebody else returns 404; tested |
| **Administrative lockout** | ✅ An administrator cannot demote, disable, or delete themselves, and the last administrator cannot be removed at all; tested |
| **API flooding by an authenticated user** | ✅ Five independent per-caller budgets with 429 and `Retry-After`; keyed per account so one household member cannot throttle the rest; tested including that the budgets are genuinely independent |
| **A rate limit becoming an outage** | ✅ Zero switches a budget off rather than refusing everything; tested |
| **Malware reaching the store** | ✅ Scanned after staging and before commit, on both upload paths, so infected content never gets a blob or a metadata row; tested with a fake clamd |
| **Misreading a scanner reply as clean** | ✅ Only a reply ending in `OK` is clean; anything unrecognised, truncated, or empty is an error, never a pass; tested against eight reply shapes including one containing the letters "OK" |
| **Injected `<style>` elements** | ✅ `style-src 'self'` with `style-src-attr 'unsafe-inline'`, so an injection can affect an element it already controls but cannot define page-wide rules; tested |
| **Silently trusting forged forwarded headers** | ✅ A startup warning naming the trusted ranges, what they mean, and when the configuration is wrong |
| **Memory growth proportional to file size** | ✅ A 2 GB upload moved resident memory by 3 MiB under a 768 MB cap; an upload allocates ~304 KB regardless of size; measured |
| **A backup that is not one** | ✅ Every step checked, the dump and archive read back, checksums re-verified, and the partial output deleted on any failure; tested against a corrupted, a truncated, and an incomplete backup |
| **Restoring a damaged backup** | ✅ Verified in full before anything is touched; a failure leaves the instance unchanged and says so; tested |
| **Silently merging two installations** | ✅ Refused unless `--force` *and* a typed `replace`; the previous content is moved aside rather than deleted; tested |
| **Upgrading with no way back** | ✅ The upgrade backs up first and refuses to proceed if the backup fails |
| **Denial as a privilege-escalation route** | ✅ Denying needs Manage rather than Share, so somebody a folder was shared with cannot lock everybody else out of it; tested |
| **Denying the owner** | ✅ Refused with a message naming the alternative, rather than accepted and silently ignored; tested |
| **A denial removing more than it names** | ✅ Denials parse exactly, without the implied Read a grant gets; tested at both the parser and the API |

### Security concerns carried forward

1. **Rate limits are per process and bound request rate, not resource use.**
   They reset on restart, and a user permitted 1200 upload requests a minute can
   still saturate the disk. On this hardware that is an availability problem
   rather than a security one, and the operator's answer is a quota.
2. **`style-src-attr 'unsafe-inline'` remains**, because React sets element
   style attributes for a progress bar's width and a tag's colour. Removing it
   entirely would mean pre-generating a class for every possible value. Style
   injection scoped to one element, not script execution.
3. **`TRUSTED_PROXIES` still defaults to the private ranges.** Correct behind
   Docker and wrong if the application port is published directly. The server
   now warns at startup, which is the most it can do from inside the process.
4. **The setup window is unauthenticated by necessity.** Mitigated by a
   prominent warning on every start and permanent closure after first use.
5. **No audit trail.** Security events are logged but not queryable. Phase 9.
6. **Copy is bounded at 10,000 nodes** to stop one request holding a
   transaction open long enough to block a Pi. The limit returns a clear error
   rather than degrading silently.
7. **Permission evaluation is not cached.** Every request runs a recursive
   ancestry query. It is a single round trip and listings are batched, but on a
   very deep tree under load this is the first thing to measure in Phase 11.
8. **A public link's token appears in the URL**, so it reaches the browser
   history and any `Referer` sent by a page the recipient navigates to next.
   That is inherent to link sharing; the mitigations are expiry, use limits,
   optional passwords, and revocation.
9. **The preview endpoint is cached for an hour** keyed on the node identifier
   rather than the content digest, and relies on the ETag for correctness after
   a file is replaced. A viewer whose access is revoked within that hour may
   still hold a cached thumbnail in their own browser. It is their browser, not
   a shared cache — the response is `private` — but it is worth stating.
10. **An archive that fails partway cannot report it.** The status is 200 as
    soon as the first byte is written, so a mid-stream failure produces a
    truncated ZIP that extractors reject. That is the correct signal, and the
    detail is logged server-side, but the client learns nothing beyond "this
    file is broken".
11. **Version storage sits outside the quota.** It is capped separately by
    `VERSIONS_MAX_TOTAL_SIZE`, which defaults to unlimited — so on a default
    configuration a user repeatedly replacing a large file can consume disk
    their quota does not account for. The per-file and age limits bound it in
    practice; an operator on a small disk should set the size cap too.
12. **The audit trail records no reads.** Downloads and listings are absent by
    design — recording them on a file server would drown the trail — which
    means the trail cannot answer "who looked at this". It answers "who changed
    or shared it".
13. **Audit writes are best-effort.** A failure is logged and swallowed rather
    than failing the request, because an audit write that could fail the
    operation would turn a full disk into an outage. The trade is that a gap is
    possible; it is loud in the log.
14. **`/admin/sessions` is bounded at 200.** On an instance with more live
    sessions than that the page shows the most recently active. Not a
    plausible shape for the hardware this targets, but it is a truncation and
    it is silent to the reader.
8. **Group membership is unbounded.** Nothing stops an administrator putting
   every user in one group; evaluation would still be correct but the
   subject-id subquery grows. Worth a limit before v1.

---

## Deferred features

| Feature | Decision |
| --- | --- |
| Office-document previews | Beyond v1 — LibreOffice is too heavy for a Pi |
| Elasticsearch | Rejected — PostgreSQL trigram search is adequate and free of extra RAM |
| Redis | Rejected — PostgreSQL `SKIP LOCKED` gives a durable queue |
| Kubernetes | Out of scope |
| Down migrations | Rejected — forward-only is safer over live data |
| Two-factor authentication | Not scheduled for v1 |
| Password reset by email | Not scheduled — administrators reset passwords |
| OIDC / LDAP / SAML | Not scheduled for v1 |
| Parallel chunk upload | Rejected for v1 — sequential offsets are simpler, and parallel writes to one USB disk cost more in seek thrashing than they gain |
| `multipart/form-data` uploads | Rejected — Go's reader spills to disk, writing every upload twice |
| `scripts/install.sh`, backup scripts | Phase 12 |

---

## Test results

Counts are top-level test functions, with subtests in parentheses where they
differ.

```text
backend/internal/api           34 (46)   transport, middleware, health, errors, auth gates
backend/internal/storage       33 (58)   layout, addressing, traversal, blobs, resume
backend/internal/auth          33 (48)   Argon2id, policy, CSRF, rate limiting
backend/internal/config        29 (50)   parsing, validation, secret redaction
backend/internal/permissions   17        bitmask, roles, parsing, denials, bit stability
backend/internal/malware       12 (20)   INSTREAM protocol, verdict parsing, backoff
backend/internal/database      12        migration loading and history checks
backend/internal/logging       12        redaction, formats, levels
backend/internal/publiclinks   12        link kinds, expiry, use limits, tokens
backend/internal/users         12        validation, quota, projection safety
backend/internal/web           12        SPA serving, fallback, caching
backend/internal/nodes         11        names, sorting, unique-name derivation
backend/internal/previews      10        format support, sizing, status
backend/internal/files          8        MIME detection and inline safety
backend/internal/archive        7        ZIP streaming, naming, nesting
backend/internal/tags           5        normalisation and scoping
                              ─────
                       unit    259 (313)

backend/tests                 279 (292)  integration, real PostgreSQL
                                         — auth: sign-in, sessions, CSRF, lockout, setup
                                         — files: CRUD, trash, quota, copy, consistency
                                         — security: IDOR, enumeration, injection, headers
                                         — sharing: roles, inheritance, groups, denials,
                                                    escalation, revocation, expiry
                                         — versions, public links, uploads, jobs, admin,
                                           productivity, timeouts, hardening
                              ─────
                    backend    538 (632)

frontend/src/pages             96        browser, login, admin, portal, search, status
frontend/src/features/files    24        selection model, permission gating, capabilities
frontend/src/lib               23        formatting, user-agent parsing
frontend/src/api               21        HTTP client, error mapping, CSRF
frontend/src/features/sharing  15        share dialog, roles, denials
frontend/src/components/ui     11        primitives, accessibility, keyboard
frontend/src/features/versions  9        history list, restore, delete
frontend/src/stores             7        theme and preferences, storage failures
frontend/src/components         3        layout and routing guards
                              ─────
                   frontend    209

scripts/test/backup_test.sh    21        backup, restore, and installer safety properties
                              ─────
                      TOTAL    862
```

### Manual verification performed

Phase 2, on the live Docker stack:

1. Fresh instance reports `setup_required: true`; a weak password is refused.
2. First administrator created; signed in automatically; setup then returns 404
   permanently and a second attempt creates nothing.
3. Session cookie is HttpOnly; the CSRF cookie is not.
4. Wrong password and unknown username return byte-identical errors.
5. `POST` without a CSRF token → 403; forged token → 403; correct token → 204.
6. The session is dead server-side after logout.
7. Sign-in, theme toggle, deep links, and the account page verified in a real
   browser with no console errors; the sessions list correctly identified both a
   Chrome session and a `curl` session.

Phase 3, on the live Docker stack:

8. Folder created; 3 MB file uploaded by streaming and downloaded **byte-identical**.
9. HTTP range request returned 206 with the correct 100-byte slice.
10. Resumable upload interrupted after 200 KB, resumed from the server-reported
    offset, and reassembled **byte-identical**.
11. Checksum mismatch rejected with 422 and an explanatory message.
12. Rename, copy (auto-named `receipts-2026.bin`), trash, and restore all
    verified.
13. On-disk layout confirmed content-addressed; **zero** user filenames present
    anywhere under the storage root.
14. `make db-check` reported 0 files missing content, 0 refcount errors, 0 quota
    mismatches, 0 orphaned nodes, 0 bad root folders.
15. Blob reference counts confirmed the copy shares one 3 MB blob (`ref_count=2`).

---

## Manual verification performed

Phases 1–3 verification is in the git history of this file. Added for 4 and 5,
on the live Docker stack and in a real browser:

**Phase 4**

1. Signed in; the file browser listed real content with all five columns, the
   storage meter, and the sidebar.
2. Double-clicking a folder navigated into it and updated the URL, so the
   folder is bookmarkable and the back button works.
3. Switched to grid view; the layout and the item both rendered correctly.
4. No application console errors (the only entries were from a browser
   extension).

**Phase 5**

5. Alice shared a *folder* with Bob as a viewer; Bob could then read and
   download a file **nested inside it** — inheritance working end to end.
6. Bob, as a viewer, was refused rename (403), delete (403), and sharing onward
   (403).
7. Bob's Shared-with-me listed both a directly shared file and a folder shared
   with the Accounting group, each showing the owner, the route, the role, and
   the sharing message.
8. Revoking the share removed Bob's access on the very next request (404).
9. Adding Bob to the Accounting group granted the group's editor access
   immediately; he could then rename, but still not share onward.
10. Removing Bob from the group revoked that access immediately (404).
11. Opening the shared folder as Bob showed the breadcrumb as `Ledgers` alone —
    not `My Files > Ledgers` — confirming the path does not disclose Alice's
    private ancestors.

**Phase 6**

12. A share link opened in a signed-out browser and showed only what was
    published — no sidebar, no account menu, nothing about the instance.
13. A file-request portal accepted an upload and showed the visitor nothing
    about what anybody else had submitted.
14. A revoked link, an expired link, and a made-up token were indistinguishable
    from each other.

**Phase 7**

15. Uploaded four files; the background runner generated thumbnails
    unprompted — 1600×1200 → 480×360 and 900×900 → 480×480, aspect preserved —
    and both grid and list views showed them.
16. All ten housekeeping sweeps ran and succeeded on the first scheduling
    round, confirming every scheduled kind has a handler registered.
17. Starred a file from the list; it appeared under Favourites as "You",
    starred, with its thumbnail.
18. Recent listed all four files as "Uploaded 3 minutes ago", one row each,
    after several downloads — confirming the row is updated rather than
    appended.
19. Searched from the header; the query landed in the URL, and the page's own
    box and filters stayed in sync with it.
20. Created and applied a tag from a file's details; it appeared in the row
    behind the dialog immediately, and a `?tag=Urgent` search matched exactly
    that file.
21. Previewed an image, a PDF, and a text file. The PDF rendered in the
    browser's own viewer inside the dialog — the check that caught the
    `frame-ancestors` defect.
22. Downloaded the whole account as a ZIP: valid archive, correct CRCs, files
    at the top level, images stored uncompressed and text deflated.
23. No application console errors on any page (the only entries were from a
    browser extension).

**Phase 8**

24. Replaced a file twice; both earlier states appeared in the details dialog
    with their sizes, authors, and reasons, and both downloaded as
    `Manuscript (v1).txt` and `Manuscript (v2).txt`.
25. Restored version 1: the file went back to its first content, and what it
    had contained became version 3 marked "Superseded by a restore" — the
    check that restoring is not destructive.
26. Restored again from the interface; the toast said so and explained it could
    be undone, and the details header updated to match — the check that caught
    the stale-size defect.
27. `make db-check` reported zero reference-count errors on a versioned file,
    after the check itself was corrected to count version references.

**Phase 9**

28. The administration overview showed accounts, live sessions, free disk,
    database latency, queue counts, and a 24-hour activity table with the
    failed sign-ins in red — and named nothing anybody had stored.
29. A failed sign-in appeared in the trail against the attempted username, with
    the IP and a readable device label, and did **not** end the session the
    caller already had.
30. Creating a public link and permanently deleting a file were both recorded,
    the deletion keeping the file's name; searching the whole trail for the
    link's token found nothing.
31. The accounts page offered unlock, reset, promote, disable, and delete, with
    every action on the administrator's own row disabled and explained.

**Phase 10**

32. The Content-Security-Policy on a live response carried `style-src 'self'`,
    `style-src-attr 'unsafe-inline'`, and `style-src-elem 'self'` — and the
    application still rendered correctly, tag colours and all, with no CSP
    violations in the console.
33. Hammering `/api/v1/search` was refused at request 61 against the 60/minute
    budget, with `Retry-After: 59`.
34. The startup log named the trusted proxy ranges, what trusting them means,
    and when the configuration would be wrong.

**Phase 11**

35. `make build-pi` produced a 13 MB ARM64 binary that reports its version
    correctly under QEMU; `make docker-build-pi` produced a 10.1 MB arm64 image
    that does the same.
36. Under Pi-4 memory caps, a single 2 GB upload moved the application's
    resident memory from 133 MiB to 136 MiB, and four concurrent 2 GB downloads
    peaked at 153 MiB before settling back to 140 MiB.
37. `make bench` ran all seven benchmarks against a real PostgreSQL, and the
    listing benchmark is what surfaced the duplicate permission evaluation.

**Phase 12**

38. Took a real backup of the running instance: every step checked, the dump
    and archive read back, checksums verified.
39. Corrupted one byte of the archive; `--verify` and the restore both refused
    it, and the restore said "Nothing was changed".
40. Ran the restore against the live instance: it refused to merge and printed
    the exact command to override.
41. **Rehearsed the whole thing.** Created a file after the backup, restored
    with `--force`, and confirmed the file was gone, the earlier state was
    back, sign-in worked (so the secret key survived), content was readable,
    and `make db-check` reported zeroes.

**Phase 13**

42. Created a group and added somebody to it through the new screen.
43. Denied an existing editor through the sharing dialog: the button turned red
    and read "Take access away", the access list showed both a red "No Editor"
    and the grey "Editor" it overrides, and the toast explained that a denial
    beats a group grant.
44. Confirmed the denied user got 404 on the folder — and that after the
    denial was removed, their access came back.

---

## What is left

The twelve-phase plan is finished. What remains is not a phase:

1. **Run it on a Raspberry Pi.** Everything else on this list is smaller than
   this one. Sustained throughput to USB storage, thumbnail time for a real
   photograph, and behaviour under thermal throttling are all unmeasured.
2. **Test the malware scanning against a real ClamAV.** The client is tested
   against a fake clamd that speaks the protocol correctly; no test has ever
   fed it a real signature database.
3. **Screenshots for the README**, which currently says they are to be added.
4. **Decide about a settings screen.** Currently argued against above; worth
   revisiting if operators find the restart genuinely painful rather than
   theoretically so.
