# Contributing to PiFiles

Thanks for looking. PiFiles is a small, deliberately unambitious file server;
the goal is that a non-expert can run it on a Raspberry Pi and trust it with
their files.

Found a security problem? See [SECURITY.md](SECURITY.md) — please don't open a
public issue for it.

## Getting set up

[docs/development.md](docs/development.md) is the full guide. The short version:

```sh
cp .env.example .env
./scripts/generate-secrets.sh
make up            # the whole stack in Docker
make dev           # or: backend and Vite dev server on the host
```

You need Go 1.25+, Node 20.19+, and Docker with Compose v2.

## Before you open a pull request

```sh
make check              # lint, typecheck, and all unit tests
make test-integration   # the tests that need a real PostgreSQL
```

CI runs the same things plus an ARM64 image build. All of it must pass.

## What the project cares about

These are the conventions the existing code follows. Matching them makes a
change much easier to accept.

**Every permission check happens on the server.** The interface uses the
`permissions` array to decide what to *offer*. It is a usability affordance and
never a control; the server re-checks on every request. A change that relies on
the frontend hiding a button is not a change that will be merged.

**Invisible things return 404, not 403.** Telling somebody "you may not read
this" confirms it exists. See [docs/authorization.md](docs/authorization.md).

**No user-supplied text ever reaches a filesystem path.** Content is addressed
by the SHA-256 digest of its bytes. Path traversal and reserved-name bugs are
designed out rather than filtered for. See [docs/storage.md](docs/storage.md).

**Stream; never buffer a whole file.** A 20 GB upload must use no more memory
than a 20 KB one. Fixed-size buffers, and no `io.ReadAll` on anything a user
controls.

**Security properties get explicit tests.** Not incidental coverage — a test
that names the thing an attacker would try. The tests in `backend/tests/` are
the most important code in the repository; the ones in `sharing_test.go` and
`security_test.go` are the model to copy.

**Never log a password, session token, recovery token, or API secret.** The
`logging` package redacts, and there are tests that check it does.

**Don't leave work as a TODO.** If a code path can be reached, it has to work.
An unreachable feature is worse than a missing one: nothing exercises it, so it
rots. Three real access-control bugs in this project came from exactly that.

**Comments say why, not what.** The surrounding code is the guide for density
and tone.

## Commits and pull requests

- One logical change per pull request. A refactor and a behaviour change in the
  same diff are hard to review and harder to revert.
- Explain the *why* in the description. What broke, or what could not be done
  before.
- If you changed behaviour somebody depends on, say so plainly.
- New dependencies need a reason. PiFiles runs on a 4 GB board and deliberately
  has no Redis, no Elasticsearch, and no JVM anywhere near it.

## Things that are settled

These have been decided and are not up for a drive-by change. Arguments are
still welcome — as an issue, with reasoning.

| | |
| --- | --- |
| Kubernetes | Out of scope |
| Redis | PostgreSQL `SKIP LOCKED` is the job queue |
| Elasticsearch | PostgreSQL trigram search is enough, and free of extra RAM |
| Down migrations | Forward-only is safer over live data |
| Files in the database | Content lives on the filesystem, metadata in PostgreSQL |

[docs/BUILD_STATUS.md](docs/BUILD_STATUS.md) records what works today, what
does not, and what has not been verified. It is kept honest; please keep it
that way.

## Licence

By contributing you agree that your contribution is licensed under the MIT
Licence, the same as the rest of the project.
