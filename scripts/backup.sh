#!/usr/bin/env bash
#
# Backs up a PiFiles instance: the database, the stored content, and the
# configuration needed to bring it back.
#
# The rule this script exists to honour, and the reason it is longer than it
# looks like it needs to be:
#
#   IT NEVER REPORTS SUCCESS UNLESS EVERY REQUIRED STEP SUCCEEDED.
#
# A backup that silently skipped the database, or wrote a truncated archive
# because the disk filled, is worse than no backup at all — because somebody
# will believe it. Every step is checked, the result is verified by reading it
# back, and a failure removes the partial output rather than leaving something
# that looks finished.
#
#   ./scripts/backup.sh [destination-directory]
#
# Defaults to the backups/ directory under STORAGE_ROOT.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"

RED=''; YELLOW=''; GREEN=''; BOLD=''; RESET=''
if [ -t 1 ] && command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
    RED="$(tput setaf 1)"; YELLOW="$(tput setaf 3)"; GREEN="$(tput setaf 2)"
    BOLD="$(tput bold)"; RESET="$(tput sgr0)"
fi

step()  { printf '\n%s==>%s %s\n' "$BOLD" "$RESET" "$*"; }
ok()    { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn()  { printf '  %s!%s %s\n' "$YELLOW" "$RESET" "$*"; }
note()  { printf '      %s\n' "$*"; }

# fail is the only exit path that is not success. It removes the partial
# backup first: a directory that looks like a backup but is not one is the
# specific outcome this script must never produce.
fail() {
    printf '\n  %s✗ BACKUP FAILED%s\n' "$RED" "$RESET" >&2
    printf '    %s\n\n' "$*" >&2
    if [ -n "${WORK_DIR:-}" ] && [ -d "$WORK_DIR" ]; then
        rm -rf "$WORK_DIR"
        printf '    The incomplete backup at %s has been removed.\n' "$WORK_DIR" >&2
        printf '    Nothing here should be mistaken for a usable backup.\n\n' >&2
    fi
    exit 1
}

# Any unexpected exit is a failure, including one from set -e.
trap 'fail "The backup stopped unexpectedly at line $LINENO."' ERR

# Reads a key from .env without sourcing it, so a shell metacharacter in a
# generated secret cannot be executed.
env_get() {
    local key="$1" default="${2-}" value=""
    if [ -f "$ENV_FILE" ]; then
        value="$(grep -E "^${key}=" "$ENV_FILE" | head -1 | cut -d= -f2- | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
    fi
    printf '%s' "${value:-$default}"
}

# --- Verify mode ------------------------------------------------------------
#
# Checks an existing backup without taking a new one. Worth running on a
# schedule against your most recent backup: bit rot on a cheap USB drive is
# real, and discovering it while restoring is discovering it too late.

verify_backup() {
    local dir="$1"

    printf '%sVerifying backup%s\n  %s\n' "$BOLD" "$RESET" "$dir"

    [ -d "$dir" ] || { printf '\n  %s✗ Not a directory: %s%s\n\n' "$RED" "$dir" "$RESET" >&2; exit 1; }

    local problems=0
    for required in database.dump content.tar env manifest.txt checksums.txt; do
        if [ ! -f "$dir/$required" ]; then
            printf '  %s✗%s missing: %s\n' "$RED" "$RESET" "$required"
            problems=1
        fi
    done
    if [ "$problems" -ne 0 ]; then
        printf '\n  %s✗ This is not a complete PiFiles backup.%s\n\n' "$RED" "$RESET" >&2
        exit 1
    fi

    step "Checking checksums"
    if ( cd "$dir" && sha256sum --check --status checksums.txt ); then
        ok "Every file matches its recorded checksum"
    else
        printf '\n' >&2
        ( cd "$dir" && sha256sum --check checksums.txt 2>&1 | grep -v ': OK$' | sed 's/^/      /' ) >&2 || true
        printf '\n  %s✗ VERIFICATION FAILED — this backup is damaged.%s\n' "$RED" "$RESET" >&2
        printf '    Do not rely on it. Take a fresh one and check the drive.\n\n' >&2
        exit 1
    fi

    step "Checking the content archive"
    if tar --test --file "$dir/content.tar" >/dev/null 2>&1; then
        local files
        files="$(tar --list --file "$dir/content.tar" 2>/dev/null | grep -cv '/$' || true)"
        ok "Archive is readable end to end ($files files)"
    else
        printf '\n  %s✗ VERIFICATION FAILED — the content archive is corrupt.%s\n\n' "$RED" "$RESET" >&2
        exit 1
    fi

    step "Checking the database dump"
    # pg_restore is the only thing that can parse a custom-format dump. Prefer
    # a local one; fall back to the running container.
    local listed=0
    if command -v pg_restore >/dev/null 2>&1; then
        pg_restore --list "$dir/database.dump" >/dev/null 2>&1 && listed=1
    elif docker compose ps --status running --services 2>/dev/null | grep -qx postgres; then
        # Written to a file first: a custom-format archive is read by seeking.
        docker compose exec -T postgres sh -c \
            'cat > /tmp/pifiles-verify.dump && pg_restore --list /tmp/pifiles-verify.dump; \
             status=$?; rm -f /tmp/pifiles-verify.dump; exit $status' \
            < "$dir/database.dump" >/dev/null 2>&1 && listed=1
    else
        warn "Cannot check the dump: no local pg_restore and PostgreSQL is not running"
        note "The checksum matched, so the file is intact; its structure is unchecked."
        printf '\n  %s✓ Backup verified as far as possible%s\n\n' "$YELLOW" "$RESET"
        exit 0
    fi

    if [ "$listed" -eq 1 ]; then
        ok "Database dump is readable"
    else
        printf '\n  %s✗ VERIFICATION FAILED — the database dump is corrupt.%s\n\n' "$RED" "$RESET" >&2
        exit 1
    fi

    printf '\n  %s✓ BACKUP VERIFIED%s\n' "$GREEN" "$RESET"
    printf '    Every file is intact and readable.\n\n'
    note "This confirms the backup is not damaged. It does not confirm it"
    note "restores correctly — only a rehearsal does that. See"
    note "docs/backup-restore.md."
    printf '\n'
    exit 0
}

if [ "${1-}" = "--verify" ]; then
    trap - ERR
    [ -n "${2-}" ] || { printf '\n  usage: ./scripts/backup.sh --verify <backup-directory>\n\n' >&2; exit 1; }
    verify_backup "$2"
fi

# --- Where things are -------------------------------------------------------

[ -f "$ENV_FILE" ] || fail "No .env found. Run this from the PiFiles directory."

STORAGE_ROOT="$(env_get STORAGE_ROOT /srv/pifiles)"
DB_NAME="$(env_get DATABASE_NAME pifiles)"
DB_USER="$(env_get DATABASE_USER pifiles)"
DB_PASSWORD="$(env_get DATABASE_PASSWORD)"

DESTINATION="${1:-$STORAGE_ROOT/backups}"
STAMP="$(date -u +%Y%m%d-%H%M%S)"
NAME="pifiles-$STAMP"
WORK_DIR="$DESTINATION/$NAME"

COMPOSE=(docker compose)
POSTGRES_SERVICE=postgres

printf '%sPiFiles backup%s\n' "$BOLD" "$RESET"
printf '  instance:    %s\n' "$REPO_ROOT"
printf '  destination: %s\n' "$WORK_DIR"

# --- Preconditions ----------------------------------------------------------

step "Checking preconditions"

command -v docker >/dev/null 2>&1 || fail "Docker is not installed or not on PATH."

if ! "${COMPOSE[@]}" ps --status running --services 2>/dev/null | grep -qx "$POSTGRES_SERVICE"; then
    fail "PostgreSQL is not running. Start the stack with 'make up' and try again.
    A backup taken from a stopped database would silently omit it."
fi
ok "PostgreSQL is running"

[ -d "$STORAGE_ROOT" ] || fail "STORAGE_ROOT ($STORAGE_ROOT) does not exist."
ok "Storage root is present: $STORAGE_ROOT"

# Free space. A pg_dump plus a content archive needs room; running out
# halfway is the classic way to produce a truncated backup that looks fine.
CONTENT_KB="$(du -sk "$STORAGE_ROOT" 2>/dev/null | cut -f1 || echo 0)"
mkdir -p "$DESTINATION"
AVAILABLE_KB="$(df -Pk "$DESTINATION" | awk 'NR==2 {print $4}')"

# The estimate is deliberately generous. Content is mostly already-compressed
# media, so assume no saving, and add a quarter for the database and headroom.
NEEDED_KB=$(( CONTENT_KB + CONTENT_KB / 4 + 262144 ))
if [ "$AVAILABLE_KB" -lt "$NEEDED_KB" ]; then
    fail "Not enough free space at $DESTINATION.
    Needed:    about $(( NEEDED_KB / 1024 )) MB
    Available: $(( AVAILABLE_KB / 1024 )) MB
    Back up to another disk:  ./scripts/backup.sh /mnt/other-disk/pifiles-backups"
fi
ok "Free space is sufficient ($(( AVAILABLE_KB / 1024 )) MB available)"

mkdir -p "$WORK_DIR"

# --- The database -----------------------------------------------------------

step "Backing up the database"

# --clean --if-exists so the dump can be restored over an existing database,
# and a custom-format dump so restore can be parallel and selective.
if ! "${COMPOSE[@]}" exec -T -e PGPASSWORD="$DB_PASSWORD" "$POSTGRES_SERVICE" \
        pg_dump -U "$DB_USER" -d "$DB_NAME" --format=custom --clean --if-exists \
        > "$WORK_DIR/database.dump" 2> "$WORK_DIR/database.err"; then
    printf '\n' >&2
    sed 's/^/      /' "$WORK_DIR/database.err" >&2
    fail "pg_dump failed. Nothing has been backed up."
fi
rm -f "$WORK_DIR/database.err"

DB_BYTES="$(stat -c %s "$WORK_DIR/database.dump" 2>/dev/null || echo 0)"
# A custom-format dump of even an empty schema is several kilobytes. Anything
# smaller means pg_dump wrote nothing useful.
if [ "$DB_BYTES" -lt 1024 ]; then
    fail "The database dump is only $DB_BYTES bytes, which cannot be a real dump."
fi
ok "Database dumped ($(( DB_BYTES / 1024 )) KB)"

# Read it back. pg_restore --list parses the archive's table of contents, so
# this catches a truncated or corrupt dump now rather than during a restore
# that somebody is attempting because everything else is already on fire.
#
# The dump is written to a file inside the container first: a custom-format
# archive is read by seeking, and pg_restore cannot seek on a pipe.
if ! "${COMPOSE[@]}" exec -T "$POSTGRES_SERVICE" sh -c \
        'cat > /tmp/pifiles-verify.dump && pg_restore --list /tmp/pifiles-verify.dump; \
         status=$?; rm -f /tmp/pifiles-verify.dump; exit $status' \
        < "$WORK_DIR/database.dump" > "$WORK_DIR/database.toc" 2>/dev/null; then
    fail "The database dump could not be read back. It is corrupt or truncated."
fi
TOC_ENTRIES="$(grep -c ';' "$WORK_DIR/database.toc" 2>/dev/null || echo 0)"
if [ "$TOC_ENTRIES" -lt 10 ]; then
    fail "The dump's table of contents has only $TOC_ENTRIES entries. It is not a complete backup."
fi
ok "Database dump verified ($TOC_ENTRIES objects)"

# --- Stored content ---------------------------------------------------------

step "Backing up stored content"

# backups/ is excluded, or a backup written inside STORAGE_ROOT would try to
# include itself — which either fails or produces something enormous.
#
# temp/ is excluded too: it holds partial uploads that are meaningless without
# their session rows, and those sessions will have expired by the time anybody
# restores.
if ! tar --create --file "$WORK_DIR/content.tar" \
        --directory "$STORAGE_ROOT" \
        --exclude=./backups --exclude=./temp \
        --warning=no-file-changed \
        . 2> "$WORK_DIR/content.err"; then
    status=$?
    # tar exits 1 for "some files differ" — a file changed while being read,
    # which on a live server is expected and not a failure. Anything else is.
    if [ "$status" -ne 1 ]; then
        sed 's/^/      /' "$WORK_DIR/content.err" >&2
        fail "Archiving the stored content failed."
    fi
    warn "Some files changed while being read; this is normal on a running server"
fi
rm -f "$WORK_DIR/content.err"

CONTENT_BYTES="$(stat -c %s "$WORK_DIR/content.tar" 2>/dev/null || echo 0)"
[ "$CONTENT_BYTES" -gt 0 ] || fail "The content archive is empty."
ok "Content archived ($(( CONTENT_BYTES / 1048576 )) MB)"

# Read it back. `tar --test` walks the whole archive, so a truncated write is
# caught here rather than discovered during a restore.
if ! tar --test --file "$WORK_DIR/content.tar" >/dev/null 2>&1; then
    fail "The content archive could not be read back. It is corrupt or truncated."
fi
ARCHIVED_FILES="$(tar --list --file "$WORK_DIR/content.tar" 2>/dev/null | grep -cv '/$' || true)"
ok "Content archive verified ($ARCHIVED_FILES files)"

# --- Configuration ----------------------------------------------------------

step "Backing up configuration"

# .env carries the secret key. Without it, sessions and public links from
# before the restore are unusable — so it must be here, and the file must not
# be readable by anybody else.
cp "$ENV_FILE" "$WORK_DIR/env"
chmod 600 "$WORK_DIR/env"
ok "Configuration saved (contains secrets — keep this backup private)"

for optional in docker-compose.yml docker-compose.prod.yml; do
    [ -f "$REPO_ROOT/$optional" ] && cp "$REPO_ROOT/$optional" "$WORK_DIR/"
done

# --- The manifest -----------------------------------------------------------

step "Writing the manifest"

VERSION="$("${COMPOSE[@]}" exec -T app pifiles -version </dev/null 2>/dev/null | head -1 || echo 'unknown')"
SCHEMA="$("${COMPOSE[@]}" exec -T -e PGPASSWORD="$DB_PASSWORD" "$POSTGRES_SERVICE" \
    psql -U "$DB_USER" -d "$DB_NAME" -tAc \
    'SELECT max(version) FROM schema_migrations' </dev/null 2>/dev/null || echo 'unknown')"

cat > "$WORK_DIR/manifest.txt" <<MANIFEST
PiFiles backup
==============

Taken:          $(date -u +'%Y-%m-%d %H:%M:%S UTC')
Host:           $(hostname)
Version:        $VERSION
Schema version: $SCHEMA
Storage root:   $STORAGE_ROOT

Contents
--------
database.dump   PostgreSQL custom-format dump ($(( DB_BYTES / 1024 )) KB)
content.tar     Stored content, $ARCHIVED_FILES files ($(( CONTENT_BYTES / 1048576 )) MB)
env             Configuration, INCLUDING SECRETS — keep this private
checksums.txt   SHA-256 of every file above

Restoring
---------
    ./scripts/restore.sh $WORK_DIR

The restore refuses to run against an instance that already has data, so it
cannot silently merge two installations. Read docs/backup-restore.md first.

Verifying this backup without restoring it
------------------------------------------
    ./scripts/backup.sh --verify $WORK_DIR

A backup nobody has restored is a hope, not a backup. Rehearse it — the guide
explains how to do that against a scratch instance rather than your real one.
MANIFEST
ok "Manifest written"

# --- Checksums --------------------------------------------------------------

step "Recording checksums"

( cd "$WORK_DIR" && sha256sum database.dump content.tar env manifest.txt > checksums.txt )
ok "Checksums recorded"

# Verify them immediately. This is what turns "the files exist" into "the
# files are readable and are what we just wrote".
if ! ( cd "$WORK_DIR" && sha256sum --check --status checksums.txt ); then
    fail "The checksums do not match what was just written. The disk may be failing."
fi
ok "Checksums verified"

rm -f "$WORK_DIR/database.toc"

# --- Done -------------------------------------------------------------------

trap - ERR

TOTAL_MB=$(( $(du -sk "$WORK_DIR" | cut -f1) / 1024 ))

printf '\n  %s✓ BACKUP COMPLETE%s\n' "$GREEN" "$RESET"
printf '    %s (%s MB)\n\n' "$WORK_DIR" "$TOTAL_MB"
note "Every step was checked and every file was read back and verified."
note ""
note "This backup contains your secret key. Anyone who has it can decrypt"
note "sessions and open public links. Store it somewhere private."
note ""
note "Copy it off this machine. A backup on the same disk as the original"
note "protects against mistakes, not against the disk failing."
printf '\n'
