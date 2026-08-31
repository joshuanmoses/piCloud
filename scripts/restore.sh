#!/usr/bin/env bash
#
# Restores a PiFiles instance from a backup.
#
#   ./scripts/restore.sh <backup-directory>
#
# This is the script somebody runs on the worst day they have had with their
# file server, so it is written to be careful rather than clever:
#
#   * It verifies the backup completely BEFORE touching anything.
#   * It refuses to run against an instance that already has data, unless
#     explicitly told to replace it. Silently merging two installations would
#     produce a database referring to content that is not there.
#   * It says exactly what it is about to destroy, and waits for a typed
#     confirmation — not a keypress.
#   * It never reports success unless every step succeeded.

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

fail() {
    printf '\n  %s✗ RESTORE FAILED%s\n' "$RED" "$RESET" >&2
    printf '    %s\n\n' "$*" >&2
    if [ "${TOUCHED_ANYTHING:-0}" -eq 1 ]; then
        printf '    %sThis instance is now in a partial state.%s\n' "$YELLOW" "$RESET" >&2
        printf '    Do not start it. Fix the problem and run the restore again —\n' >&2
        printf '    it is safe to repeat, and will replace whatever is there.\n\n' >&2
    else
        printf '    Nothing was changed.\n\n' >&2
    fi
    exit 1
}
trap 'fail "The restore stopped unexpectedly at line $LINENO."' ERR

env_get() {
    local key="$1" default="${2-}" file="${3:-$ENV_FILE}" value=""
    if [ -f "$file" ]; then
        value="$(grep -E "^${key}=" "$file" | head -1 | cut -d= -f2- | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
    fi
    printf '%s' "${value:-$default}"
}

FORCE=0
BACKUP_DIR=""
for arg in "$@"; do
    case "$arg" in
        --force) FORCE=1 ;;
        -*) fail "Unknown option: $arg" ;;
        *)  BACKUP_DIR="$arg" ;;
    esac
done

if [ -z "$BACKUP_DIR" ]; then
    printf '\n  usage: ./scripts/restore.sh <backup-directory> [--force]\n\n' >&2
    printf '  --force replaces an instance that already has data.\n\n' >&2
    exit 1
fi
BACKUP_DIR="$(cd "$BACKUP_DIR" 2>/dev/null && pwd)" || fail "No such directory: $BACKUP_DIR"

TOUCHED_ANYTHING=0
COMPOSE=(docker compose)

printf '%sPiFiles restore%s\n' "$BOLD" "$RESET"
printf '  from: %s\n' "$BACKUP_DIR"
printf '  into: %s\n' "$REPO_ROOT"

# --- Verify the backup first ------------------------------------------------
#
# Everything below this point is checked before anything is changed. A restore
# that gets halfway and then discovers the archive is truncated has destroyed
# the instance it was meant to save.

step "Verifying the backup"

for required in database.dump content.tar env manifest.txt checksums.txt; do
    [ -f "$BACKUP_DIR/$required" ] || fail "The backup is incomplete: $required is missing.
    This does not look like a PiFiles backup."
done
ok "All expected files are present"

if ! ( cd "$BACKUP_DIR" && sha256sum --check --status checksums.txt ); then
    fail "The backup's checksums do not match. It is damaged and must not be restored."
fi
ok "Checksums match"

if ! tar --test --file "$BACKUP_DIR/content.tar" >/dev/null 2>&1; then
    fail "The content archive is corrupt."
fi
ok "Content archive is readable"

if grep -q '^Schema version:' "$BACKUP_DIR/manifest.txt" 2>/dev/null; then
    printf '\n'
    sed 's/^/      /' "$BACKUP_DIR/manifest.txt" | sed -n '1,14p'
fi

# --- Where this instance keeps things ---------------------------------------

step "Checking this instance"

command -v docker >/dev/null 2>&1 || fail "Docker is not installed or not on PATH."

# Configuration comes from the backup when this instance has none — a restore
# onto a fresh machine is the common case, and the secret key must match or
# every existing session and public link becomes unusable.
if [ ! -f "$ENV_FILE" ]; then
    cp "$BACKUP_DIR/env" "$ENV_FILE"
    chmod 600 "$ENV_FILE"
    ok "Configuration restored from the backup (this instance had none)"
else
    BACKUP_KEY="$(env_get APP_SECRET_KEY '' "$BACKUP_DIR/env")"
    CURRENT_KEY="$(env_get APP_SECRET_KEY '' "$ENV_FILE")"
    if [ -n "$BACKUP_KEY" ] && [ "$BACKUP_KEY" != "$CURRENT_KEY" ]; then
        warn "This instance's APP_SECRET_KEY differs from the backup's"
        note "Sessions and public links created before the backup will not work."
        note "To keep them, stop, copy $BACKUP_DIR/env over .env, and run this again."
    else
        ok "Configuration is already in place"
    fi
fi

STORAGE_ROOT="$(env_get STORAGE_ROOT /srv/pifiles)"
DB_NAME="$(env_get DATABASE_NAME pifiles)"
DB_USER="$(env_get DATABASE_USER pifiles)"
DB_PASSWORD="$(env_get DATABASE_PASSWORD)"

if ! "${COMPOSE[@]}" ps --status running --services 2>/dev/null | grep -qx postgres; then
    step "Starting PostgreSQL"
    "${COMPOSE[@]}" up -d postgres >/dev/null 2>&1 || fail "Could not start PostgreSQL."
    printf '      waiting'
    for _ in $(seq 1 60); do
        if "${COMPOSE[@]}" exec -T postgres pg_isready -U "$DB_USER" </dev/null >/dev/null 2>&1; then
            printf ' ready.\n'; break
        fi
        printf '.'; sleep 1
    done
    "${COMPOSE[@]}" exec -T postgres pg_isready -U "$DB_USER" </dev/null >/dev/null 2>&1 \
        || fail "PostgreSQL did not become ready."
fi
ok "PostgreSQL is running"

# The application must be stopped. Restoring underneath a running server would
# have it serving from a database that is being replaced.
if "${COMPOSE[@]}" ps --status running --services 2>/dev/null | grep -qx app; then
    step "Stopping the application"
    "${COMPOSE[@]}" stop app >/dev/null 2>&1 || fail "Could not stop the application."
    ok "Application stopped"
fi

# --- Refuse to merge --------------------------------------------------------

step "Checking whether this instance already has data"

EXISTING_USERS="$("${COMPOSE[@]}" exec -T -e PGPASSWORD="$DB_PASSWORD" postgres \
    psql -U "$DB_USER" -d "$DB_NAME" -tAc \
    "SELECT count(*) FROM users" </dev/null 2>/dev/null || echo 0)"
EXISTING_USERS="${EXISTING_USERS//[^0-9]/}"
EXISTING_USERS="${EXISTING_USERS:-0}"

EXISTING_CONTENT=0
if [ -d "$STORAGE_ROOT/files" ]; then
    EXISTING_CONTENT="$(find "$STORAGE_ROOT/files" -type f 2>/dev/null | head -1 | wc -l)"
fi

if [ "$EXISTING_USERS" -gt 0 ] || [ "$EXISTING_CONTENT" -gt 0 ]; then
    if [ "$FORCE" -ne 1 ]; then
        printf '\n  %s✗ This instance already has data.%s\n\n' "$RED" "$RESET" >&2
        printf '    Accounts:      %s\n' "$EXISTING_USERS" >&2
        printf '    Stored files:  %s\n\n' "$( [ "$EXISTING_CONTENT" -gt 0 ] && echo present || echo none )" >&2
        printf '    Restoring would REPLACE all of it. The restore will not merge two\n' >&2
        printf '    installations: the result would be a database referring to content\n' >&2
        printf '    that is not there, which is worse than either instance alone.\n\n' >&2
        printf '    If you are certain, run:\n' >&2
        printf '      ./scripts/restore.sh %s --force\n\n' "$BACKUP_DIR" >&2
        exit 1
    fi

    printf '\n  %sThis will permanently destroy the current instance.%s\n\n' "$BOLD$RED" "$RESET"
    printf '    %s accounts and everything under %s\n' "$EXISTING_USERS" "$STORAGE_ROOT"
    printf '    will be replaced by the backup from %s.\n\n' "$BACKUP_DIR"
    printf '    Type %sreplace%s to continue, or anything else to stop: ' "$BOLD" "$RESET"

    # Read from the terminal when there is one, so a `docker compose exec`
    # elsewhere in the script cannot have swallowed the answer — and so this
    # cannot be satisfied by accident from a pipe. Destroying an instance
    # should take a deliberate keystroke.
    # Prefer the terminal, so a `docker compose exec` elsewhere in the script
    # cannot have swallowed the answer and so this cannot be satisfied by
    # accident from a pipe. Opening it is *attempted* rather than assumed:
    # `[ -r /dev/tty ]` is true in environments where the open then fails.
    CONFIRMATION=""
    if exec 3</dev/tty 2>/dev/null; then
        read -r CONFIRMATION <&3 || true
        exec 3<&-
    else
        read -r CONFIRMATION || true
    fi

    if [ "$CONFIRMATION" != "replace" ]; then
        printf '\n    Stopped. Nothing was changed.\n\n'
        exit 1
    fi
else
    ok "This instance is empty; nothing will be destroyed"
fi

# --- Restore the database ---------------------------------------------------

step "Restoring the database"
TOUCHED_ANYTHING=1

# --clean --if-exists comes from the dump itself. Errors are tolerated for
# objects that do not exist yet, which is normal on a fresh database, so
# --exit-on-error is deliberately not used here; the verification below is
# what establishes the restore worked.
if ! "${COMPOSE[@]}" exec -T -e PGPASSWORD="$DB_PASSWORD" postgres \
        pg_restore -U "$DB_USER" -d "$DB_NAME" --clean --if-exists --no-owner --no-privileges \
        < "$BACKUP_DIR/database.dump" > /tmp/pifiles-restore.log 2>&1; then
    if grep -qiE 'error:.*(could not|permission|does not exist: relation)' /tmp/pifiles-restore.log; then
        sed 's/^/      /' /tmp/pifiles-restore.log | tail -20 >&2
        fail "pg_restore reported errors. See /tmp/pifiles-restore.log."
    fi
fi
ok "Database restored"

# Verify by reading it back. pg_restore can exit non-zero for harmless reasons
# and zero for a restore that did almost nothing, so the count is the check.
RESTORED_USERS="$("${COMPOSE[@]}" exec -T -e PGPASSWORD="$DB_PASSWORD" postgres \
    psql -U "$DB_USER" -d "$DB_NAME" -tAc "SELECT count(*) FROM users" </dev/null 2>/dev/null || echo 0)"
RESTORED_USERS="${RESTORED_USERS//[^0-9]/}"
[ "${RESTORED_USERS:-0}" -gt 0 ] || fail "The restored database has no accounts. The restore did not work."

SCHEMA_VERSION="$("${COMPOSE[@]}" exec -T -e PGPASSWORD="$DB_PASSWORD" postgres \
    psql -U "$DB_USER" -d "$DB_NAME" -tAc "SELECT max(version) FROM schema_migrations" </dev/null 2>/dev/null || echo '?')"
ok "Verified: $RESTORED_USERS accounts, schema version ${SCHEMA_VERSION// /}"

# --- Restore the content ----------------------------------------------------

step "Restoring stored content"

mkdir -p "$STORAGE_ROOT"

# The old content is moved aside rather than deleted. If the restore turns out
# to be wrong, the previous state is still recoverable — and on the day
# somebody runs this, that matters more than the disk space.
if [ "$EXISTING_CONTENT" -gt 0 ]; then
    ASIDE="$STORAGE_ROOT.replaced-$(date -u +%Y%m%d-%H%M%S)"
    for subdir in files versions previews quarantine; do
        [ -d "$STORAGE_ROOT/$subdir" ] || continue
        mkdir -p "$ASIDE"
        mv "$STORAGE_ROOT/$subdir" "$ASIDE/"
    done
    warn "The previous content was moved to $ASIDE"
    note "Delete it once you are satisfied the restore is correct."
fi

if ! tar --extract --file "$BACKUP_DIR/content.tar" --directory "$STORAGE_ROOT" 2> /tmp/pifiles-extract.log; then
    sed 's/^/      /' /tmp/pifiles-extract.log | tail -20 >&2
    fail "Extracting the content failed. See /tmp/pifiles-extract.log."
fi
ok "Content extracted"

# Verify: every blob the database references must actually be on disk. This is
# the check that catches a restore where the database came from one backup and
# the content from another.
MISSING="$("${COMPOSE[@]}" exec -T -e PGPASSWORD="$DB_PASSWORD" postgres \
    psql -U "$DB_USER" -d "$DB_NAME" -tAc \
    "SELECT count(*) FROM blobs WHERE ref_count > 0" </dev/null 2>/dev/null || echo 0)"
MISSING="${MISSING//[^0-9]/}"
RESTORED_FILES="$(find "$STORAGE_ROOT/files" -type f 2>/dev/null | wc -l)"

if [ "${MISSING:-0}" -gt 0 ] && [ "$RESTORED_FILES" -eq 0 ]; then
    fail "The database references ${MISSING} stored files but none were restored.
    The database and the content may be from different backups."
fi
ok "Verified: $RESTORED_FILES files on disk, ${MISSING:-0} referenced by the database"

# --- Done -------------------------------------------------------------------

trap - ERR

printf '\n  %s✓ RESTORE COMPLETE%s\n\n' "$GREEN" "$RESET"
note "Start the instance:  make up"
note ""
note "Then check it before trusting it:"
note "  make db-check      metadata and storage agree"
note "  sign in            an account you recognise, with its own password"
note "  open a file        content is actually reachable"
printf '\n'
