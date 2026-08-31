#!/usr/bin/env bash
#
# Tests the safety properties of the backup and restore scripts.
#
# These are the behaviours that matter on the worst day somebody has with
# their file server, and they are exactly the ones nobody exercises until then.
# Every case here is a way the scripts could quietly do the wrong thing:
#
#   * report success on a backup that is not one
#   * accept a damaged backup for restore
#   * merge two installations into an incoherent one
#   * touch a live instance before deciding it should
#
# Deliberately independent of Docker. These check the decisions, which is where
# the risk is; the end-to-end path is rehearsed by hand and documented in
# docs/backup-restore.md.
#
#   ./scripts/test/backup_test.sh

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

GREEN=''; RED=''; BOLD=''; RESET=''
if [ -t 1 ] && command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
    GREEN="$(tput setaf 2)"; RED="$(tput setaf 1)"; BOLD="$(tput bold)"; RESET="$(tput sgr0)"
fi

PASSED=0
FAILED=0

pass() { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$1"; PASSED=$((PASSED + 1)); }
fail() {
    printf '  %s✗%s %s\n' "$RED" "$RESET" "$1"
    [ -n "${2-}" ] && printf '      %s\n' "$2"
    FAILED=$((FAILED + 1))
}

# expect_exit runs a command and checks its status, capturing output for the
# failure message so a surprise is diagnosable rather than just red.
expect_exit() {
    local want="$1" description="$2"; shift 2
    local output status
    output="$("$@" 2>&1)"; status=$?
    if [ "$status" -eq "$want" ]; then
        pass "$description"
    else
        fail "$description" "expected exit $want, got $status: $(printf '%s' "$output" | tail -2 | tr '\n' ' ')"
    fi
}

# --- Fixtures ----------------------------------------------------------------

# REAL_DUMP is a genuine custom-format archive, when one can be obtained.
#
# Most cases below never look inside the dump — a bad checksum or a truncated
# tar is caught first — so a placeholder is enough for them. The "an intact
# backup verifies" case is different: it exercises the pg_restore check, and a
# placeholder would fail it for the right reason at the wrong time.
REAL_DUMP=""
if docker compose --project-directory "$REPO_ROOT" ps --status running --services 2>/dev/null \
        | grep -qx postgres; then
    # The password comes from .env, read without sourcing it so a shell
    # metacharacter in a generated secret cannot be executed.
    db_password=""
    if [ -f "$REPO_ROOT/.env" ]; then
        db_password="$(grep -E '^DATABASE_PASSWORD=' "$REPO_ROOT/.env" | head -1 | cut -d= -f2- \
            | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
    fi
    db_user="$(grep -E '^DATABASE_USER=' "$REPO_ROOT/.env" 2>/dev/null | head -1 | cut -d= -f2- \
        | tr -d ' ')"
    db_name="$(grep -E '^DATABASE_NAME=' "$REPO_ROOT/.env" 2>/dev/null | head -1 | cut -d= -f2- \
        | tr -d ' ')"

    if docker compose --project-directory "$REPO_ROOT" exec -T \
            -e PGPASSWORD="$db_password" postgres \
            pg_dump -U "${db_user:-pifiles}" -d "${db_name:-pifiles}" \
            --format=custom --schema-only \
            </dev/null > "$WORK/real.dump" 2>/dev/null \
       && [ -s "$WORK/real.dump" ]; then
        REAL_DUMP="$WORK/real.dump"
    fi
fi

# make_backup builds a structurally valid backup directory.
make_backup() {
    local dir="$1"
    mkdir -p "$dir"

    if [ -n "$REAL_DUMP" ]; then
        cp "$REAL_DUMP" "$dir/database.dump"
    else
        head -c 4096 /dev/urandom > "$dir/database.dump"
    fi
    mkdir -p "$WORK/content/files"
    echo "some stored content" > "$WORK/content/files/blob"
    tar --create --file "$dir/content.tar" --directory "$WORK/content" .
    printf 'APP_SECRET_KEY=not-a-real-key\nSTORAGE_ROOT=%s/storage\n' "$WORK" > "$dir/env"
    chmod 600 "$dir/env"
    printf 'PiFiles backup\nSchema version: 8\n' > "$dir/manifest.txt"

    ( cd "$dir" && sha256sum database.dump content.tar env manifest.txt > checksums.txt )
}

printf '\n%sBackup and restore safety%s\n\n' "$BOLD" "$RESET"

# --- Verification ------------------------------------------------------------

printf '%sVerification%s\n' "$BOLD" "$RESET"

make_backup "$WORK/good"
if [ -n "$REAL_DUMP" ]; then
    expect_exit 0 "an intact backup verifies" \
        "$REPO_ROOT/scripts/backup.sh" --verify "$WORK/good"
else
    # Said out loud rather than silently skipped: a test suite that quietly
    # covers less than it appears to is worse than one that says so.
    printf '  - an intact backup verifies %s(skipped: no PostgreSQL to build a real dump)%s\n' \
        "$BOLD" "$RESET"
fi

# The failure that matters most: a backup that has rotted on the disk must not
# pass. Somebody only ever checks this when they are about to need it.
cp -r "$WORK/good" "$WORK/rotted"
printf 'X' | dd of="$WORK/rotted/content.tar" bs=1 seek=100 conv=notrunc 2>/dev/null
expect_exit 1 "a backup with a changed byte fails verification" \
    "$REPO_ROOT/scripts/backup.sh" --verify "$WORK/rotted"

cp -r "$WORK/good" "$WORK/truncated"
truncate -s 200 "$WORK/truncated/content.tar"
expect_exit 1 "a truncated archive fails verification" \
    "$REPO_ROOT/scripts/backup.sh" --verify "$WORK/truncated"

cp -r "$WORK/good" "$WORK/incomplete"
rm "$WORK/incomplete/database.dump"
expect_exit 1 "a backup missing its database fails verification" \
    "$REPO_ROOT/scripts/backup.sh" --verify "$WORK/incomplete"

cp -r "$WORK/good" "$WORK/nochecksums"
rm "$WORK/nochecksums/checksums.txt"
expect_exit 1 "a backup with no checksums fails verification" \
    "$REPO_ROOT/scripts/backup.sh" --verify "$WORK/nochecksums"

mkdir -p "$WORK/empty"
expect_exit 1 "an empty directory is not mistaken for a backup" \
    "$REPO_ROOT/scripts/backup.sh" --verify "$WORK/empty"

expect_exit 1 "verifying a path that does not exist fails" \
    "$REPO_ROOT/scripts/backup.sh" --verify "$WORK/nowhere"

expect_exit 1 "--verify with no directory fails rather than guessing" \
    "$REPO_ROOT/scripts/backup.sh" --verify

# --- Restore -----------------------------------------------------------------

printf '\n%sRestore%s\n' "$BOLD" "$RESET"

expect_exit 1 "restore with no arguments fails rather than guessing" \
    "$REPO_ROOT/scripts/restore.sh"

expect_exit 1 "restoring from a path that does not exist fails" \
    "$REPO_ROOT/scripts/restore.sh" "$WORK/nowhere"

expect_exit 1 "restoring an unknown option fails rather than ignoring it" \
    "$REPO_ROOT/scripts/restore.sh" "$WORK/good" --wat

# The important one: verification happens BEFORE anything is touched. A restore
# that gets halfway and then finds the archive is corrupt has destroyed the
# instance it was meant to save.
output="$("$REPO_ROOT/scripts/restore.sh" "$WORK/rotted" 2>&1)"; status=$?
if [ "$status" -ne 0 ] && printf '%s' "$output" | grep -q "Nothing was changed"; then
    pass "a damaged backup is refused before anything is changed"
else
    fail "a damaged backup is refused before anything is changed" \
        "$(printf '%s' "$output" | tail -2 | tr '\n' ' ')"
fi

output="$("$REPO_ROOT/scripts/restore.sh" "$WORK/incomplete" 2>&1)"; status=$?
if [ "$status" -ne 0 ] && printf '%s' "$output" | grep -qi "incomplete"; then
    pass "an incomplete backup is refused, and says which file is missing"
else
    fail "an incomplete backup is refused" "$(printf '%s' "$output" | tail -2 | tr '\n' ' ')"
fi

# --- Installer ---------------------------------------------------------------

printf '\n%sInstaller%s\n' "$BOLD" "$RESET"

# Piping an installer to a shell is a common habit and a bad one here: it
# cannot ask its questions, and guessing at the storage location is exactly
# the answer that must not be guessed.
output="$("$REPO_ROOT/scripts/install.sh" < /dev/null 2>&1)"; status=$?
if [ "$status" -ne 0 ] && printf '%s' "$output" | grep -q "needs a terminal"; then
    pass "the installer refuses to run without a terminal to ask questions in"
else
    fail "the installer refuses to run without a terminal" \
        "$(printf '%s' "$output" | tail -2 | tr '\n' ' ')"
fi

# --- Shell hygiene -----------------------------------------------------------

printf '\n%sScripts%s\n' "$BOLD" "$RESET"

for script in "$REPO_ROOT"/scripts/*.sh "$REPO_ROOT"/scripts/test/*.sh; do
    name="$(basename "$script")"
    if bash -n "$script" 2>/dev/null; then
        pass "$name parses"
    else
        fail "$name parses" "$(bash -n "$script" 2>&1 | head -1)"
    fi
    if [ ! -x "$script" ]; then
        fail "$name is executable"
    fi
done

if command -v shellcheck >/dev/null 2>&1; then
    for script in "$REPO_ROOT"/scripts/*.sh; do
        name="$(basename "$script")"
        if shellcheck --severity=error "$script" >/dev/null 2>&1; then
            pass "$name has no shellcheck errors"
        else
            fail "$name has no shellcheck errors" \
                "$(shellcheck --severity=error "$script" 2>&1 | head -3 | tr '\n' ' ')"
        fi
    done
fi

# --- Result ------------------------------------------------------------------

printf '\n'
if [ "$FAILED" -eq 0 ]; then
    printf '  %s%d passed%s\n\n' "$GREEN" "$PASSED" "$RESET"
    exit 0
fi
printf '  %s%d failed%s, %d passed\n\n' "$RED" "$FAILED" "$RESET" "$PASSED"
exit 1
