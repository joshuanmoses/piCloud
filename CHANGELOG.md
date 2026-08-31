# Changelog

All notable changes to PiFiles are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Nothing yet.

## [1.0.0] — unreleased

The first release. Everything below is new, so this entry describes what
PiFiles does rather than what changed.

### Added

- **Accounts and sign-in.** Argon2id password hashing, server-side sessions in
  HTTP-only cookies, signed double-submit CSRF, account lockout, and per-class
  rate limiting. No default password: the first administrator is created during
  setup.
- **Files and folders.** Streaming upload and download of any size, resumable
  chunked uploads, drag-and-drop, rename, move, copy, trash with a retention
  period, and folder download as a streamed ZIP.
- **Content-addressed storage.** File contents are named by the SHA-256 digest
  of their bytes and stored outside the database, so no user-supplied text ever
  reaches a filesystem path. Identical files share one blob.
- **Sharing.** Per-item grants to people and groups, four roles, inheritance
  down the tree, and explicit denials that always win. Every check happens on
  the server.
- **Public links.** Share links for people with no account, and upload portals
  that collect files into a folder you choose — both optionally password
  protected, expiring, and use-limited.
- **Versions.** Previous copies kept when a file is replaced, with restore, and
  retention bounded by count, age, and total size.
- **Finding things.** PostgreSQL trigram search, favourites, tags, recent
  activity, and previews for images, PDFs, and text.
- **Administration.** Accounts, quotas, groups, live sessions, background jobs,
  and a queryable audit trail. Administrators manage the server; they cannot
  read other people's files.
- **Operations.** One-command installer, a backup that refuses to report
  success unless every step succeeded, a restore that verifies before it
  touches anything, and an upgrade that backs up first.
- **Optional malware scanning** through ClamAV, off by default because it wants
  roughly 1 GB of RAM.

### Known limits

[docs/BUILD_STATUS.md](docs/BUILD_STATUS.md) records what has not been
verified. The most significant: PiFiles has not yet been run on real Raspberry
Pi hardware. It builds and runs for ARM64 under emulation and behaves correctly
under Pi-sized memory caps, but treat throughput figures as estimates.

[docs/security.md](docs/security.md) lists what PiFiles does *not* defend
against.
