# PiFiles — Architecture

PiFiles is a self-hosted, browser-based file server designed to run comfortably on a
Raspberry Pi 4 or 5 while providing the file-management experience people expect from
Nextcloud, Dropbox, or Google Drive.

This document describes the system as designed. Features are delivered in phases; see
[BUILD_STATUS.md](BUILD_STATUS.md) for what is actually implemented today.

---

## 1. Design principles

1. **The database is the source of truth for structure; the filesystem is the source of
   truth for bytes.** Metadata (names, hierarchy, ownership, permissions, checksums) lives
   in PostgreSQL. File content lives on disk under opaque content addresses. Neither
   half ever derives a path from user input.
2. **Authorization is enforced in the backend, always.** The frontend hides things for
   usability only. Every API handler performs its own permission check.
3. **Stream, never buffer.** Uploads and downloads are streamed through fixed-size
   buffers. A 20 GB upload must not consume 20 GB — or even 200 MB — of RAM on a 4 GB Pi.
4. **Small and boring.** Few dependencies, no Kubernetes, no Redis, no Elasticsearch.
   PostgreSQL does double duty as the metadata store, the search index, and the job queue.
5. **Fail loudly and consistently.** A failed filesystem write must never leave a dangling
   database row, and vice versa. Where perfect atomicity is impossible, the system biases
   toward orphaned *bytes* (recoverable by a reconciliation job) rather than orphaned
   *metadata* (which is a broken user-visible file).
6. **Installable by a nontechnical user.** One command, sensible defaults, no manual
   secret generation.

---

## 2. System topology

```text
                      ┌──────────────────────────────┐
   Browser  ───TLS──▶ │  Caddy                       │
   Mobile             │  • automatic HTTPS           │
                      │  • security headers          │
                      │  • streaming, no body limit  │
                      └──────────────┬───────────────┘
                                     │ HTTP (private compose network)
                      ┌──────────────▼───────────────┐
                      │  pifiles-app  (single Go     │
                      │  binary, non-root)           │
                      │                              │
                      │  ┌────────────────────────┐  │
                      │  │ REST API  /api/v1/...  │  │
                      │  │ SPA assets (embedded)  │  │
                      │  │ Background job runner  │  │
                      │  └────────────────────────┘  │
                      └───┬──────────────────────┬───┘
                          │                      │
              ┌───────────▼─────────┐   ┌────────▼──────────────┐
              │  PostgreSQL 16      │   │  Storage root         │
              │  metadata, sessions │   │  (USB SSD / NVMe)     │
              │  jobs, audit, FTS   │   │  files/ versions/ ... │
              └─────────────────────┘   └───────────────────────┘
                                                 │
                                        ┌────────▼──────────┐
                                        │  ClamAV (optional)│
                                        └───────────────────┘
```

Everything runs under Docker Compose. There is exactly one application container; it
serves the API, serves the compiled frontend, and runs background jobs in-process.

---

## 3. Technology decisions

| Concern | Choice | Rationale |
| --- | --- | --- |
| Backend language | **Go 1.26** | Single static binary, low memory floor, excellent streaming I/O, cheap concurrency. Cross-compiles to `linux/arm64` trivially. |
| HTTP routing | **chi v5** | Zero transitive dependencies, `net/http`-native. Route groups with per-group middleware map cleanly onto the very different trust levels we need (public links, authenticated API, admin). |
| Database driver | **pgx v5 (native, no `database/sql`)** | Binary protocol, implicit prepared statements (SQL-injection resistant by construction), a pool with the tuning knobs a Pi needs. |
| Migrations | **In-process, embedded SQL** | No extra binary in the image, no separate migration container to fail. Runs under a PostgreSQL advisory lock so concurrent app starts are safe. |
| Logging | **`log/slog` (stdlib)** | Structured, zero dependencies, JSON and console handlers built in. |
| Frontend | **React 19 + TypeScript + Vite** | Fast builds, small output, mainstream. |
| Styling | **Tailwind CSS v4** | CSS-first config, no PostCSS pipeline, tiny production CSS. |
| Components | **shadcn/ui pattern (Radix + CVA), vendored** | Accessible primitives we own outright — no runtime component-library dependency to bloat the bundle. |
| Reverse proxy | **Caddy 2** | Automatic HTTPS with near-zero configuration, which matters enormously for the nontechnical target user. |
| Job queue | **PostgreSQL (`SELECT ... FOR UPDATE SKIP LOCKED`)** | Durable, transactional, already present. Redis would add a second stateful service and ~50 MB RSS for no benefit at this scale. |
| Search | **PostgreSQL trigram + full-text** | Adequate for millions of rows on a Pi. Isolated behind a `search.Backend` interface so it can be swapped later. |

### 3.1 Why the frontend is embedded in the Go binary

The compiled SPA is embedded with `go:embed` and served by the app container, rather than
being copied into the Caddy image or shared through a volume.

- **One deployable artifact.** Upgrades cannot land a frontend and backend that disagree
  about the API contract.
- **No volume choreography.** A shared static volume between two containers is a
  well-known source of stale-asset bugs on restart.
- **Correct SPA fallback for free.** Unknown non-`/api` paths return `index.html`;
  unknown `/api` paths return a JSON 404. Doing this in Caddy requires fragile matchers.
- **Negligible cost.** The bundle is a few hundred kilobytes; hashed assets get immutable
  cache headers, `index.html` gets `no-cache`.

Caddy stays a pure reverse proxy: TLS, headers, and byte-shovelling.

---

## 4. Backend package layout

`backend/internal/` — each package owns one domain concern and exposes a service type.
Packages depend downward only; `api` is the only package that knows about HTTP.

```text
config      Environment parsing, validation, typed settings, human sizes/durations
logging     slog setup, console/JSON handlers, request-scoped loggers
version     Build metadata injected via -ldflags
database    pgx pool lifecycle, health probe, embedded migration runner
storage     Storage-root layout, safe path construction, capacity/health probing
web         Embedded SPA assets and the SPA/API-aware fallback handler
api         Router, middleware chain, response envelopes, health endpoint

auth        Argon2id hashing, login flow, lockout, password policy      (Phase 2)
sessions    Server-side session records, cookies, revocation            (Phase 2)
users       User CRUD, quota accounting                                 (Phase 2/9)
groups      Groups and membership                                       (Phase 5)
permissions ACL evaluation with folder inheritance                      (Phase 5)
files       File node metadata operations                               (Phase 3)
folders     Folder tree operations, move/copy semantics                 (Phase 3)
uploads     Chunked/resumable upload sessions, atomic finalization      (Phase 3)
downloads   Range requests, streaming ZIP generation                    (Phase 3/7)
sharing     Internal shares to users and groups                         (Phase 5)
publiclinks Tokenized public links and upload requests                  (Phase 6)
search      Search backend interface + PostgreSQL implementation        (Phase 7)
favorites   Per-user favorites                                          (Phase 7)
recent      Recent-activity tracking                                    (Phase 7)
trash       Soft delete, restore, retention                             (Phase 7)
versions    Version history and retention policies                      (Phase 8)
quotas      Quota enforcement and recalculation                         (Phase 3/9)
audit       Append-only audit events                                    (Phase 9)
jobs        PostgreSQL-backed worker pool                               (Phase 7+)
malware     Optional ClamAV INSTREAM client                             (Phase 10)
previews    Thumbnail and preview generation                            (Phase 7)
```

Directories for not-yet-implemented packages exist so the shape of the project is
obvious, but they contain no placeholder code. Empty stubs that compile but do nothing
are worse than absent packages.

---

## 5. Storage architecture

### 5.1 Layout

```text
$STORAGE_ROOT/
├── files/        committed content, content-addressed
├── versions/     superseded content, same addressing scheme
├── trash/        reserved for content whose metadata is soft-deleted
├── previews/     generated thumbnails, derived and disposable
├── temp/         in-flight chunked uploads
├── quarantine/   content ClamAV rejected
└── backups/      database dumps written by scripts/backup.sh
```

`STORAGE_ROOT` is configurable and never hardcoded. `/srv/pifiles` is only a default
suggested by the installer and documentation.

### 5.2 Content addressing

A stored blob's path is derived from its SHA-256 digest, never from its filename:

```text
$STORAGE_ROOT/files/ab/cd/abcdef0123456789...
                    └┬┘└┬┘ └──────┬───────┘
                     │  │         └ full 64-hex digest
                     └──┴ first two byte-pairs, fanned out to keep
                          directory entry counts manageable
```

This eliminates an entire class of problems by construction:

- **Path traversal** — the only characters that reach the filesystem are `[0-9a-f/]`.
- **Unicode normalization** — `café` in NFC and NFD are the same database string and
  never touch a filesystem name.
- **Collisions and reserved names** — `CON`, `..`, emoji, 3000-character names, and
  case-insensitive filesystems are all irrelevant to on-disk layout.
- **Symlink escape** — every resolved path is verified to remain inside the storage root
  before use, and blobs are opened with `O_NOFOLLOW` where the platform supports it.

The user-visible name, MIME type, hierarchy, owner, size, checksum, timestamps, and
permissions are database columns.

### 5.3 Write path (Phase 3 design)

```text
stream to $STORAGE_ROOT/temp/<upload-id>   (hashing while writing)
        ↓ verify size against quota and MAX_UPLOAD_SIZE
        ↓ finalize digest
        ↓ optional ClamAV scan → quarantine on hit
        ↓ BEGIN; insert/update metadata rows
        ↓ link(2) temp → files/ab/cd/<digest>   (atomic; EEXIST means dedup, which is fine)
        ↓ COMMIT
        ↓ unlink temp
```

If the commit fails, the blob may remain unreferenced. That is a recoverable condition:
the storage-reconciliation job sweeps unreferenced blobs. The inverse — committed
metadata pointing at bytes that were never written — is prevented by ordering the link
before the commit.

---

## 6. Data model (target shape)

Externally meaningful identifiers are UUIDs so that sequential integers never leak
information or enable enumeration.

```text
users ──┬── group_members ──── groups
        ├── sessions
        ├── favorites
        ├── recent_items
        └── nodes (owner)

nodes (unified files + folders, adjacency list + materialized path)
        ├── versions
        ├── permissions   (subject = user | group)
        ├── shares
        ├── public_links
        ├── upload_requests
        └── file_tags ──── tags

audit_events        append-only
background_jobs     durable queue
system_settings     runtime-editable configuration
schema_migrations   migration bookkeeping
```

Files and folders are stored in **one** `nodes` table with a `kind` discriminator.
Permissions, sharing, trash, and search then have a single target type rather than
parallel implementations that inevitably diverge.

---

## 7. Request lifecycle

```text
Caddy ─▶ RecoverPanic ─▶ RequestID ─▶ RealIP ─▶ RequestLogger ─▶ SecurityHeaders
      ─▶ [rate limit] ─▶ [CSRF] ─▶ [authenticate] ─▶ [authorize] ─▶ handler
```

Bracketed stages arrive in Phases 2, 5, and 10. Every response carries an `X-Request-Id`
that also appears in the structured log line, so a user-reported failure is directly
traceable.

Errors are returned as a stable envelope:

```json
{ "error": { "code": "not_found", "message": "Resource not found" }, "request_id": "..." }
```

Internal errors are logged with full detail and returned to the client as a generic
message. Database errors are never surfaced verbatim.

---

## 8. Security model

| Threat | Mitigation |
| --- | --- |
| Path traversal | Content-addressed storage; no user input reaches a path |
| Symlink escape | Resolved paths verified inside the root; no symlink following |
| IDOR | UUID identifiers plus a mandatory server-side permission check per request |
| Broken access control | Authorization is a separate layer from authentication, evaluated per node with inheritance |
| SQL injection | pgx extended protocol, parameterized statements exclusively |
| Command injection | No shell execution on user-controlled data; ClamAV over its network socket, not a CLI |
| XSS | React escaping, strict CSP, no `dangerouslySetInnerHTML`, previews served with `Content-Disposition` and `X-Content-Type-Options: nosniff` |
| CSRF | Cookies are `SameSite=Lax`; state-changing requests additionally require a double-submit token |
| Session fixation | Session identifier regenerated on privilege change |
| Brute force | Per-account lockout plus per-IP rate limiting, with generic failure messages |
| Token enumeration | 256-bit random tokens; only a hash is stored; lookups are constant-time |
| MIME confusion | Server-side sniffing; client-supplied types are advisory only |
| Zip bombs | Archives are generated, never expanded, by the server |
| Uploaded-file execution | Blobs have no extension, are stored non-executable, and are never handed to a shell |

Secrets — passwords, session tokens, link tokens, API keys — are never logged at any
level. The logging layer redacts known-sensitive field names as a backstop.

---

## 9. Raspberry Pi optimization

- **PostgreSQL** is tuned for 4 GB RAM: `max_connections=50`, `shared_buffers=128MB`,
  `effective_cache_size=768MB`, `random_page_cost=1.1` (SSD/NVMe, not spinning media).
- **Connection pool** defaults to 10 connections, well under the server limit, leaving
  headroom for `psql` and maintenance.
- **Container limits** in `docker-compose.prod.yml` prevent any single service from
  exhausting a 4 GB board.
- **I/O buffers** are fixed at 256 KB for copy loops; the whole file never lands in RAM.
- **Background workers** default to 2 and run at low concurrency; thumbnail generation is
  explicitly rate-limited so it cannot starve interactive requests.
- **Pagination** everywhere. A folder with 50,000 children is served in pages, never
  materialized at once.
- **No polling.** The frontend uses SSE for upload and job progress instead of interval
  requests.
- **microSD is never the default data location.** The installer requires an explicit
  storage path and warns when it appears to be on the boot device.

---

## 10. Deployment

- `docker-compose.yml` — base definition; usable directly for local development.
- `docker-compose.prod.yml` — production overlay adding restart policies, resource
  limits, and log rotation.
- ClamAV lives behind the `malware` Compose profile and is off by default.
- The application container runs as a non-root user (UID 10001) and mounts the storage
  root at `/data`.
- Health checks gate startup ordering: the app waits for PostgreSQL to be healthy, and
  Caddy waits for the app.
- `deploy/systemd/pifiles.service` starts the Compose project at boot for installations
  that prefer systemd supervision.

---

## 11. Deliberate non-goals for v1

- Kubernetes support.
- Office-document conversion (the preview subsystem is designed for it; LibreOffice is
  too heavy for a Pi in the first release).
- Server-side end-to-end encryption.
- Federation between instances.
- Desktop sync clients (the API is designed to permit one later).
- Disk partitioning or formatting. Storage is expected to be mounted by the OS.
