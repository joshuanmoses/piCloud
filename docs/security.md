# Security

What PiFiles defends against, how, and what it does not.

This document is written for somebody deciding whether to trust PiFiles with
their files, and for somebody who has found a problem and wants to know whether
it is one. It states the model plainly, including the parts that are
uncomfortable.

---

## The threat model

PiFiles assumes a small, private deployment: a household, a small business, a
club. Everyone with an account is somebody the operator chose to give one to.
That assumption shapes what is defended and what is not.

### Defended against

| Adversary | What they can do | What stops them |
| --- | --- | --- |
| **A stranger on the internet** | Reach the login page and any public link | No unauthenticated route reads content; link tokens are 256-bit and only their hashes are stored; every failure mode returns an identical 404 |
| **A stranger on the local network** | The same, plus reach the application port if it is published | The same, plus a startup warning when forwarded-header trust is misconfigured |
| **A signed-in user** | Everything their permissions allow | The permission model is evaluated server-side on every request; the interface is an affordance, never a control |
| **A public-link recipient** | Whatever the link grants | A file-request portal holds `upload` and `create` and no `read`, so it cannot enumerate; a folder link reaches direct children only |
| **An administrator** | Manage accounts and the instance | Administration grants **no** access to anybody's content — see below |
| **Somebody with a stolen session cookie** | Act as that user until it expires | Sessions are server-side and revocable; a password change ends the others; "sign out everywhere else" is one click |

### Not defended against

Stated plainly, because a security document that only lists wins is not useful.

- **An operator with shell access to the machine.** They can read the database,
  the storage root, and the secret key. This is not a limitation to be fixed;
  it is what "self-hosted" means. If you do not trust the person running the
  server, do not put your files on it.
- **A user who is entitled to read a file copying it elsewhere.** Nothing stops
  somebody downloading what they may download. There is no DRM and there will
  not be.
- **Malicious content that ClamAV does not recognise.** Scanning is optional
  and signature-based. It reduces exposure; it does not eliminate it.
- **A compromised browser or device.** A keylogger or a malicious extension
  defeats everything below.
- **Traffic analysis.** An observer who can see the encrypted connection learns
  request sizes and timings. PiFiles does not pad or delay.
- **Denial of service by somebody with an account.** Rate limits bound the
  cost, but a determined user with valid credentials can still make the server
  work hard. See *Rate limiting*.

---

## The administrator boundary

**An administrator manages accounts and the instance. They cannot read anybody's
files.**

This is the least obvious decision in PiFiles and the one most worth stating.
Most self-hosted file servers give the administrator implicit access to
everything, on the reasoning that they control the machine anyway. PiFiles
does not, for two reasons:

1. **"They could get it from the disk anyway" is not the same as "the
   application will hand it to them."** One requires deliberate action outside
   the product; the other is a click. The difference matters to the person
   whose files they are.
2. **An audit trail cannot distinguish legitimate support from snooping** if the
   application offers the same route for both. Removing the route removes the
   ambiguity.

Concretely: there is no endpoint under `/api/v1/admin` that reads, lists, or
downloads content, and the permission evaluator gives an administrator nothing
extra. Asking for another user's file as an administrator returns 404 — the
same answer a stranger gets. This is tested against a file, a folder listing, an
archive, a version list, and a thumbnail.

What an administrator *can* do is disable an account, reset its password, and
delete it. All three are recorded. A password reset ends every session, so it
cannot be used to quietly assume somebody's identity — and an
administrator-created account is required to choose its own password on first
sign-in for the same reason.

---

## Authentication

- **Argon2id**, 64 MiB / t=2 / p=2 — above the OWASP baseline, tuned so a Pi 4
  can still sign somebody in. Parameters travel in the hash, so the cost can be
  raised later and an older hash is upgraded transparently at the next sign-in.
- **Passwords follow NIST SP 800-63B**: length enforced, composition rules
  deliberately not. Breach-corpus passwords, anything containing the username or
  email, repeated characters, sequences, and short repeated patterns are refused.
- **Sessions are server-side.** The cookie carries a random token; only its
  SHA-256 is stored, so the database never holds a value that opens a session.
  HttpOnly, SameSite=Lax, and Secure when the connection is genuinely encrypted.
- **CSRF** is a signed double-submit: an HMAC of the session identifier, in a
  readable cookie and echoed in a header. A cross-site attacker can cause the
  session cookie to be sent but cannot read the other one.
- **Account lockout** after repeated failures, plus two rate limiters — one per
  address, one per username. Lockout revokes existing sessions.
- **Sign-in errors are uniform.** An unknown username and a wrong password give
  the same message at the same cost, so the endpoint is not a directory.
- **No default password.** Nothing ships with credentials; the first
  administrator is created during setup.

### The setup window

Between first start and first sign-in, the setup endpoint is open to whoever
reaches it. This is unavoidable — the instance must be reachable before an
administrator can exist. It is mitigated by a prominent warning on every start
until it closes, permanent closure after first use, and the fact that the window
is normally seconds long.

---

## Content

- **Files on disk are named by the SHA-256 of their contents**, never by
  anything a user typed. Path traversal, Unicode normalisation, and
  reserved-name bugs are designed out rather than filtered for. The storage root
  is additionally opened as an `os.Root`, so traversal and symlink escape are
  refused by the kernel.
- **MIME types are sniffed from content**, never trusted from the client, and
  script-capable types are neutralised at detection: an uploaded `.html` is
  stored and served as `text/plain`, an SVG as `application/octet-stream`. The
  browser therefore shows their source rather than executing them.
- **Downloads carry `nosniff`, a sandboxing CSP, and `Content-Disposition`.**
  Only a short allowlist of inert types is served inline.
- **Filenames are validated** against control characters, path separators,
  Windows device names, and bidirectional overrides, and sanitised again on the
  way into a ZIP so an archive from PiFiles cannot be a Zip Slip vector.
- **Images are decoded defensively.** Dimensions are read from the header and
  refused past 80 megapixels *before* any pixel is allocated, so a 100 KB
  decompression bomb declaring a 30000×30000 canvas cannot exhaust memory.

---

## Authorization

Every content operation goes through one evaluator. It computes the union of
grants from the user and their groups, walks the ancestry for inherited grants,
and applies denials last — a denial always wins.

Two conventions are worth knowing:

- **404, not 403, for anything invisible.** A distinguishable "forbidden" would
  confirm that a guessed identifier names something real. The exception is when
  the caller can already *see* the item but lacks the specific right, where 403
  with the missing permission named is clearer and reveals nothing new.
- **Nobody can grant what they do not hold.** Sharing is not transitive by
  accident: a link's permissions are intersected with the creator's, and a
  recipient cannot re-share beyond their own grant.

---

## Rate limiting

Two layers, protecting different things.

**Account lockout** protects the *account* from password guessing. It is
persisted in the database and survives a restart.

**API rate limits** protect the *server* from a caller who has already
authenticated. In-memory and per-process, keyed on the account where there is
one and the address otherwise — per-account rather than per-address so a
household behind one NAT address is not throttled collectively.

| Budget | Default per minute | Covers |
| --- | --- | --- |
| `RATE_LIMIT_READ` | 600 | Listings, metadata, downloads |
| `RATE_LIMIT_WRITE` | 120 | Anything that changes state, and folder archives |
| `RATE_LIMIT_UPLOAD` | 1200 | Upload sessions and chunks |
| `RATE_LIMIT_SEARCH` | 60 | Trigram search |
| `RATE_LIMIT_ANONYMOUS` | 60 | The public-link portals |

The budgets are independent, so exhausting one does not block work that costs
something different. A refused request returns 429 with `Retry-After`. Zero
switches a budget off; it is never read as "refuse everything".

**What this does not do:** the limits are per process and reset on restart, and
they bound request *rate* rather than resource *consumption*. A user permitted
1200 upload requests a minute can still saturate the disk. On the hardware
PiFiles targets, that is an availability problem rather than a security one, and
the operator's answer is a quota.

---

## Malware scanning

Optional, off by default, and honest about what it is.

ClamAV needs roughly a gigabyte of RAM for its signature database — a quarter of
a Raspberry Pi 4's memory — so an operator should spend it knowingly rather than
discover it has eaten their headroom.

When enabled, content is streamed to `clamd` over INSTREAM **after** it is
staged and **before** it is committed, so infected content never reaches the
content-addressed store and never gets a metadata row. The same check applies to
both the single-request and resumable upload paths.

Three decisions are configuration rather than defaults, because both answers are
defensible:

| Setting | The trade |
| --- | --- |
| `CLAMAV_REJECT_ON_ERROR` | Refuse uploads when clamd is unreachable (safe) or accept them unscanned (available). Refusing every upload because a container is restarting fails the user one way; quietly accepting unscanned content fails them another. |
| `CLAMAV_MAX_SCAN_SIZE` | Content above the limit is stored **unscanned**, and recorded as skipped. Refusing it instead would make the cap a de facto upload limit, which is not what it says. |
| `CLAMAV_QUARANTINE` | Keep infected content under `quarantine/` instead of discarding it. Off by default: keeping malware on a family file server is a real risk for value most operators will never collect, and the audit trail already records what was blocked, by whom, and which signature matched. |

A scan that cannot reach clamd backs off for 30 seconds, so a stopped container
does not add the full connection timeout to every upload.

---

## The audit trail

Separate from the structured log, and for a different job. A log is rotated and
read while debugging; the trail answers "who did this, and when" — months later,
under pressure, often about an account that no longer exists.

Two properties are load-bearing:

- **It survives account deletion.** The actor's name is captured alongside their
  identifier, and the foreign keys are `ON DELETE SET NULL`.
- **It never holds a credential.** No path writes a password, a session token, a
  link token, or a hash of one. Creating a public link records that a link was
  made, never what it is. This is asserted by a test that creates a link and
  then searches the whole trail for its token.

Reads are deliberately absent: recording every download on a file server would
drown the trail and make the audit table the largest thing in the database. The
trail answers "who changed or shared this", not "who looked at it".

Retention defaults to a year. Zero keeps events forever and is never read as
"delete everything" — the pruning sweep refuses to run rather than interpreting
an unset value as a policy.

---

## Transport and headers

Caddy terminates TLS; the application speaks plain HTTP on the private network
behind it.

- **HSTS is sent only over a genuinely encrypted connection**, not merely in
  production. A plain-HTTP LAN install is supported, and sending HSTS there
  would pin the browser to a scheme the server cannot serve — locking the user
  out until they manually clear the entry.
- **Forwarded headers are honoured only from trusted proxies.** Otherwise any
  client could set `X-Forwarded-For` and defeat per-IP rate limiting, account
  lockout, and the address recorded in the audit trail. `TRUSTED_PROXIES`
  defaults to the private ranges, which is correct behind Docker and **wrong**
  if the application port is published directly — the server warns about this
  at startup.
- **Content-Security-Policy** forbids inline `<style>` elements and remote
  stylesheets (`style-src 'self'`) while permitting style *attributes*
  (`style-src-attr 'unsafe-inline'`), which React genuinely needs for a progress
  bar's width and a tag's colour. This is a narrowing, not a removal: an
  injected `<style>` block can define rules matching anything on the page and
  exfiltrate attribute values through selector-driven requests, while an
  attribute can only affect the element it is already on.

---

## Secrets

- **Never logged.** Three independent layers: a redacting slog handler, a
  `Secret` type whose `String()` returns `[REDACTED]`, and a pgx tracer that
  drops query arguments entirely.
- **Never serialized.** Password hashes are unexported in every response type,
  and content digests are omitted from node payloads so a client cannot probe
  whether particular content exists.
- **Never committed.** `.env` is gitignored and generated with random values by
  the installer.

---

## Reporting a problem

Open an issue describing what you found and how to reproduce it. If it is
exploitable and you would rather not post it publicly, say so in the issue
without the details and a private channel will be arranged.

Please do include: the version (`/api/v1/version`), whether the instance is
exposed to the internet, and whether you were signed in.

---

## What is still outstanding

Carried from `docs/BUILD_STATUS.md`, which is the authoritative list:

1. **Rate limits are per process.** A restart resets them, and they bound
   request rate rather than resource consumption.
2. **Permission evaluation is not cached.** Every request runs a recursive
   ancestry query. It is one round trip and listings are batched, but a very
   deep tree under load is the first thing to measure.
3. **A public link's token appears in the URL**, so it reaches browser history
   and any `Referer` sent onward. Inherent to link sharing; mitigated by
   expiry, use limits, passwords, and revocation.
4. **Preview responses are cached for an hour** in the viewer's own browser, so
   a viewer whose access is revoked may still hold a cached thumbnail. It is
   their browser, not a shared cache — the response is `private` — but it is
   worth stating.
5. **Audit writes are best-effort.** A failure is logged and swallowed rather
   than failing the request, because an audit write that could fail the
   operation would turn a full disk into an outage.
