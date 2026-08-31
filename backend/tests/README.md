# Backend integration tests

These tests exercise the application against a real PostgreSQL server. Unit
tests live beside the code they test; this package holds the tests that need a
live dependency.

They **skip** unless `PIFILES_TEST_DATABASE_HOST` is set, so `go test ./...`
stays fast and dependency-free for ordinary development.

Run them with:

```sh
make test-integration
```

which starts a disposable PostgreSQL container, points these tests at it, and
tears it down afterwards. Each test creates and drops its own uniquely-named
database, so migrations always run against a genuinely clean schema and tests
never interfere with one another.
