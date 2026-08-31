# Development

## Prerequisites

| | Version | |
| --- | --- | --- |
| Go | 1.25+ | backend |
| Node | 20.19+ or 22+ | frontend |
| Docker | with the Compose plugin | database, integration tests |
| Make | | task runner |

## Getting started

```sh
git clone https://github.com/joshuanmoses/piCloud.git
cd piCloud

cd frontend && npm install && cd ..
./scripts/generate-secrets.sh

make dev
```

`make dev` starts three things: a disposable PostgreSQL container, the Go
backend on `:8080` with `APP_ENV=development`, and the Vite dev server on
`:5173`. Open <http://localhost:5173>.

Vite proxies `/api`, `/health`, `/s`, and `/u` to the backend, so the browser
sees a single origin. That matters: cookies, CSRF, and same-origin policy then
behave in development exactly as they do behind Caddy in production.

`make help` lists every command.

## Layout

```text
backend/
  cmd/server/          entrypoint, flags, startup and shutdown
  internal/
    api/               HTTP transport: router, middleware, responses
    config/            environment parsing and validation
    database/          pgx pool, transactions, migration runner
    health/            the status vocabulary shared by all subsystems
    logging/           slog setup and secret redaction
    storage/           the data root, content addressing, path safety
    web/               the embedded SPA
    <domain>/           one package per concern, added by phase
  migrations/          embedded .sql schema migrations
  tests/               tests that need a live PostgreSQL

frontend/src/
  api/                 HTTP client and per-domain request modules
  components/ui/       vendored shadcn-style primitives
  features/            feature-specific components
  layouts/             application frame
  pages/               routed pages
  stores/              zustand stores
  types/               API types mirroring the backend
```

Packages depend downward only. `api` is the only backend package that knows
about HTTP; domain packages return values and errors, and the transport layer
decides status codes.

Directories exist for packages that later phases will fill. They are empty on
purpose — a stub that compiles but does nothing is worse than an absent package,
because it looks implemented.

## Quality gates

Run before every commit:

```sh
make check       # lint + typecheck + all unit tests
```

Individually:

```sh
make lint              # go vet, gofmt check, eslint, tsc
make test              # unit tests, both halves
make test-integration  # tests needing real PostgreSQL
make test-coverage     # coverage reports
make fmt               # auto-format everything
```

### Continuous integration

`.github/workflows/ci.yml` runs the same gates on every push and pull request,
in four independent jobs so a failure names what broke:

| Job | What it proves |
| --- | --- |
| Backend | `gofmt`, `go vet`, `go.mod` is tidy, and the whole Go suite under `-race` — including the integration tests, against a real PostgreSQL 17 |
| Frontend | `tsc`, ESLint, Vitest, and the production Vite build |
| Shell scripts | `scripts/test/backup_test.sh`, with `shellcheck` installed so its lint checks actually run rather than skipping |
| Container image | The image builds for **linux/amd64 and linux/arm64** |

Two of those deserve explanation.

The backend job checks afterwards that the integration tests did not *skip*.
They skip silently without `PIFILES_TEST_DATABASE_HOST`, so a broken database
service would otherwise produce a green run that proved almost nothing.

The image job builds for ARM64 under QEMU because that is the architecture
PiFiles exists for. Nothing is pushed — PiFiles is distributed as source and
built on the machine it runs on — so the job proves the build works, not that
an image was published. A dependency quietly dropping ARM64 support would
otherwise be discovered by the first person to install it on a board.

## Testing

**Backend unit tests** sit beside the code they test and use no external
services. `internal/api` tests use stub implementations of `DatabaseService` and
`StorageService` — which is why the router depends on those narrow interfaces
rather than on `*database.DB`.

**Backend integration tests** live in `backend/tests/` and skip automatically
unless `PIFILES_TEST_DATABASE_HOST` is set, so plain `go test ./...` stays fast.
Each test creates and drops its own uniquely named database, so migrations
always run against a genuinely clean schema.

```sh
make test-integration
# or against your own server:
cd backend && PIFILES_TEST_DATABASE_HOST=localhost \
  PIFILES_TEST_DATABASE_PORT=5432 \
  PIFILES_TEST_DATABASE_USER=pifiles \
  PIFILES_TEST_DATABASE_PASSWORD=... \
  go test ./tests/...
```

**Frontend tests** use Vitest and Testing Library. Query by role and accessible
name rather than by test id — a test that cannot find a button by its name is
usually reporting a real accessibility problem.

### What to test

Security properties get explicit regression tests, not incidental coverage.
Existing examples worth imitating:

- `storage_test.go` — every malformed digest shape, and a planted symlink that
  must not escape the storage root.
- `api_test.go` — a forged `X-Forwarded-For` must not change the client IP; an
  unknown JSON field must be rejected; an internal error must not reach the
  client.
- `logging_test.go` — every sensitive attribute name, in both output formats.

From Phase 5, every permission rule needs a test proving an unauthorized user
**cannot** reach another user's content by changing a UUID.

## Adding a migration

1. Create `backend/migrations/NNNN_short_description.sql`, numbered above every
   existing file.
2. Write forward-only SQL. There are no down migrations: rolling a schema
   backwards on live data is more dangerous than rolling forward.
3. Apply it: `make migrate-local`.

Rules the runner enforces at startup:

- **Never edit an applied migration.** Its checksum is recorded, and a mismatch
  refuses to start. Two deployments claiming the same version must have the same
  schema. Add a new migration instead.
- **Never renumber below the high-water mark.** A pending migration numbered
  below one already applied is refused, because its order would be ambiguous.
- Migrations run inside a transaction unless the file begins with
  `-- pifiles:no-transaction`, which is needed for statements such as
  `CREATE INDEX CONCURRENTLY`.

## Adding an endpoint

1. Put the logic in the domain package (`internal/users`, `internal/files`, …).
   It should know nothing about HTTP.
2. Add the handler in `internal/api`. It parses input, calls the service,
   and maps errors to status codes.
3. Register the route in `buildRoutes`, inside the group whose middleware suits
   it. Long-running routes — uploads, downloads, event streams — must be
   registered **outside** the `Timeout` group, or legitimate transfers will be
   cut off.
4. Add the types to `frontend/src/types/api.ts` and a request function in
   `frontend/src/api/`.
5. Test the handler, the permission rule, and the client function.
6. Document it in [api.md](api.md).

## Conventions

**Go**

- Errors wrap with `%w` and gain context at each level.
- Never return a database error to a client. Wrap it: `ErrInternal(err)` logs
  the cause and returns a generic message.
- Use `db.InTx` for any multi-statement mutation.
- Never log a secret. `config.Secret` and the logging redactor are backstops,
  not permission to be careless.
- Comments explain *why*. What the code does should be evident from the code.

**TypeScript**

- `strict` is on, along with `noUncheckedIndexedAccess` and
  `exactOptionalPropertyTypes`. Fix the type rather than casting.
- `any` is a lint error. So is storing anything in `localStorage` outside the
  theme store — authentication state belongs in an HttpOnly cookie.
- Components handle four states explicitly: loading, empty, error, and content.

**Both**

- Do not add a dependency without a reason that survives being said out loud.
  The backend has two direct dependencies; keeping it that way is a feature.

## Debugging

```sh
make logs-app                     # follow application logs
LOG_LEVEL=debug make dev          # verbose, including SQL
make db-shell                     # psql inside the running database
make db-query SQL="SELECT * FROM schema_migrations"
make health                       # the health endpoint
```

Every response carries `X-Request-Id`, and the same value appears in the log
line for that request. When a user reports a failure, ask for that ID.

## Build targets

```sh
make build            # frontend, then the server binary for this machine
make build-pi         # cross-compile the server for ARM64
make docker-build     # image for this machine
make docker-build-pi  # ARM64 image via buildx
```

`make build-frontend` compiles the SPA and copies it into
`backend/internal/web/dist`, where `go:embed` picks it up. A placeholder
`index.html` is committed there so `go build` succeeds in a clean checkout; the
server logs a warning at startup when only the placeholder is present.
