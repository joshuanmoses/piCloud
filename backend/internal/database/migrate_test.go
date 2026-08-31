package database

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func fakeMigrations(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

func TestLoadMigrationsOrdersNumerically(t *testing.T) {
	t.Parallel()
	// Lexical ordering would put 0010 before 0002; numeric ordering must win.
	fsys := fakeMigrations(map[string]string{
		"0010_tenth.sql":  "SELECT 10;",
		"0002_second.sql": "SELECT 2;",
		"0001_first.sql":  "SELECT 1;",
	})

	got, err := LoadMigrations(fsys)
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	want := []int{1, 2, 10}
	if len(got) != len(want) {
		t.Fatalf("loaded %d migrations, want %d", len(got), len(want))
	}
	for i, v := range want {
		if got[i].Version != v {
			t.Errorf("position %d has version %d, want %d", i, got[i].Version, v)
		}
	}
	if got[1].Name != "second" {
		t.Errorf("name = %q, want second", got[1].Name)
	}
}

func TestLoadMigrationsComputesStableChecksums(t *testing.T) {
	t.Parallel()
	fsys := fakeMigrations(map[string]string{"0001_init.sql": "CREATE TABLE t (id INT);"})

	first, err := LoadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Checksum != second[0].Checksum {
		t.Error("checksums must be deterministic")
	}
	if len(first[0].Checksum) != 64 {
		t.Errorf("checksum should be a sha256 hex digest, got %q", first[0].Checksum)
	}

	changed := fakeMigrations(map[string]string{"0001_init.sql": "CREATE TABLE t (id BIGINT);"})
	other, err := LoadMigrations(changed)
	if err != nil {
		t.Fatal(err)
	}
	if other[0].Checksum == first[0].Checksum {
		t.Error("a content change must produce a different checksum")
	}
}

func TestLoadMigrationsRejectsBadFilenames(t *testing.T) {
	t.Parallel()
	bad := []map[string]string{
		{"init.sql": "SELECT 1;"},
		{"abc_init.sql": "SELECT 1;"},
		{"0000_zero.sql": "SELECT 1;"},
		{"0001.sql": "SELECT 1;"},
		{"_nameless.sql": "SELECT 1;"},
	}
	for _, files := range bad {
		if _, err := LoadMigrations(fakeMigrations(files)); err == nil {
			t.Errorf("LoadMigrations should reject %v", files)
		}
	}
}

func TestLoadMigrationsRejectsDuplicateVersions(t *testing.T) {
	t.Parallel()
	fsys := fakeMigrations(map[string]string{
		"0001_users.sql":  "SELECT 1;",
		"0001_groups.sql": "SELECT 2;",
	})
	_, err := LoadMigrations(fsys)
	if err == nil {
		t.Fatal("duplicate versions must be rejected; application order would be ambiguous")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should explain the duplication: %v", err)
	}
}

func TestLoadMigrationsRejectsEmptyFiles(t *testing.T) {
	t.Parallel()
	if _, err := LoadMigrations(fakeMigrations(map[string]string{"0001_empty.sql": "   \n\t"})); err == nil {
		t.Fatal("an empty migration is almost certainly a mistake and must be rejected")
	}
}

func TestLoadMigrationsRequiresAtLeastOne(t *testing.T) {
	t.Parallel()
	if _, err := LoadMigrations(fstest.MapFS{}); err == nil {
		t.Fatal("a build with no migrations is broken and must fail loudly")
	}
}

func TestNoTransactionDirective(t *testing.T) {
	t.Parallel()
	fsys := fakeMigrations(map[string]string{
		"0001_normal.sql": "CREATE TABLE t (id INT);",
		"0002_index.sql":  "-- pifiles:no-transaction\nCREATE INDEX CONCURRENTLY i ON t (id);",
	})

	got, err := LoadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].InTransaction {
		t.Error("migrations should run in a transaction by default")
	}
	if got[1].InTransaction {
		t.Error("the no-transaction directive was not honoured")
	}
}

// --- History verification --------------------------------------------------

func TestVerifyHistoryAcceptsCleanState(t *testing.T) {
	t.Parallel()
	available, _ := LoadMigrations(fakeMigrations(map[string]string{
		"0001_a.sql": "SELECT 1;",
		"0002_b.sql": "SELECT 2;",
	}))
	applied := map[int]AppliedMigration{
		1: {Version: 1, Name: "a", Checksum: available[0].Checksum, AppliedAt: time.Now()},
	}
	if err := verifyHistory(available, applied); err != nil {
		t.Fatalf("clean history was rejected: %v", err)
	}
}

// Silently tolerating an edited migration means two deployments claiming the
// same version can have different schemas.
func TestVerifyHistoryRejectsEditedMigration(t *testing.T) {
	t.Parallel()
	available, _ := LoadMigrations(fakeMigrations(map[string]string{"0001_a.sql": "SELECT 1;"}))
	applied := map[int]AppliedMigration{
		1: {Version: 1, Name: "a", Checksum: strings.Repeat("0", 64)},
	}

	err := verifyHistory(available, applied)
	if err == nil {
		t.Fatal("an edited applied migration must be refused")
	}
	if !strings.Contains(err.Error(), "modified") {
		t.Errorf("error should say the migration was modified: %v", err)
	}
	if !strings.Contains(err.Error(), "add a new migration") {
		t.Errorf("error should tell the operator what to do instead: %v", err)
	}
}

func TestVerifyHistoryRejectsDowngrade(t *testing.T) {
	t.Parallel()
	available, _ := LoadMigrations(fakeMigrations(map[string]string{"0001_a.sql": "SELECT 1;"}))
	applied := map[int]AppliedMigration{
		1: {Version: 1, Name: "a", Checksum: available[0].Checksum},
		2: {Version: 2, Name: "future", Checksum: strings.Repeat("f", 64)},
	}

	err := verifyHistory(available, applied)
	if err == nil {
		t.Fatal("running an older binary against a newer schema must be refused")
	}
	if !strings.Contains(err.Error(), "older than the database schema") {
		t.Errorf("error should explain the version skew: %v", err)
	}
}

// Two branches each adding "0002" and one being renumbered to "0003" after the
// other was already applied would otherwise apply out of order.
func TestVerifyHistoryRejectsOutOfOrderPending(t *testing.T) {
	t.Parallel()
	available, _ := LoadMigrations(fakeMigrations(map[string]string{
		"0001_a.sql": "SELECT 1;",
		"0002_b.sql": "SELECT 2;",
		"0003_c.sql": "SELECT 3;",
	}))
	applied := map[int]AppliedMigration{
		1: {Version: 1, Name: "a", Checksum: available[0].Checksum},
		3: {Version: 3, Name: "c", Checksum: available[2].Checksum},
	}

	err := verifyHistory(available, applied)
	if err == nil {
		t.Fatal("a pending migration below the high-water mark must be refused")
	}
	if !strings.Contains(err.Error(), "renumber") {
		t.Errorf("error should tell the developer to renumber: %v", err)
	}
}

// --- The real, embedded migrations ----------------------------------------

func TestEmbeddedMigrationsAreValid(t *testing.T) {
	t.Parallel()
	// Imported here rather than at the top so this file has no dependency on
	// the migrations package for the synthetic cases above.
	got, err := LoadMigrations(realMigrations())
	if err != nil {
		t.Fatalf("the migrations shipped in this binary are invalid: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no migrations are embedded")
	}
	if got[0].Version != 1 {
		t.Errorf("the first migration should be version 1, got %d", got[0].Version)
	}
	for i, m := range got {
		if m.SQL == "" {
			t.Errorf("migration %s is empty", m.Filename)
		}
		if i > 0 && got[i-1].Version >= m.Version {
			t.Errorf("migrations are not strictly increasing at %s", m.Filename)
		}
	}
}
