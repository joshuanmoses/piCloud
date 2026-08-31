#!/usr/bin/env bash
#
# Upgrades a running PiFiles instance.
#
#   ./scripts/upgrade.sh
#
# Safe to run without reading anything first, which is the whole point: an
# upgrade path that requires homework is one people skip.
#
#   * It takes a backup before changing anything, and refuses to proceed if
#     the backup fails.
#   * Migrations run automatically at startup and are forward-only, so the
#     backup is the only way back. That is why it is not optional.
#   * It verifies the instance is healthy afterwards and says plainly if it
#     is not.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"

RED=''; YELLOW=''; GREEN=''; BOLD=''; DIM=''; RESET=''
if [ -t 1 ] && command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
    RED="$(tput setaf 1)"; YELLOW="$(tput setaf 3)"; GREEN="$(tput setaf 2)"
    BOLD="$(tput bold)"; DIM="$(tput dim)"; RESET="$(tput sgr0)"
fi

step()  { printf '\n%s==>%s %s\n' "$BOLD" "$RESET" "$*"; }
ok()    { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn()  { printf '  %s!%s %s\n' "$YELLOW" "$RESET" "$*"; }
note()  { printf '      %s%s%s\n' "$DIM" "$*" "$RESET"; }

fail() {
    printf '\n  %s✗ UPGRADE FAILED%s\n' "$RED" "$RESET" >&2
    printf '    %s\n\n' "$*" >&2
    if [ -n "${BACKUP_PATH:-}" ]; then
        printf '    A backup was taken first:\n' >&2
        printf '      %s\n\n' "$BACKUP_PATH" >&2
        printf '    To go back:\n' >&2
        printf '      ./scripts/restore.sh %s --force\n\n' "$BACKUP_PATH" >&2
    fi
    exit 1
}
trap 'fail "The upgrade stopped unexpectedly at line $LINENO."' ERR

env_get() {
    local key="$1" default="${2-}" value=""
    if [ -f "$ENV_FILE" ]; then
        value="$(grep -E "^${key}=" "$ENV_FILE" | head -1 | cut -d= -f2- | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
    fi
    printf '%s' "${value:-$default}"
}

[ -f "$ENV_FILE" ] || fail "No .env found. Run this from the PiFiles directory."

printf '%sPiFiles upgrade%s\n' "$BOLD" "$RESET"

BEFORE="$(docker compose exec -T app pifiles -version 2>/dev/null | head -1 || echo 'not running')"
printf '  currently: %s\n' "$BEFORE"

# --- Back up first ------------------------------------------------------------

step "Taking a backup"
note "Migrations are forward-only. This backup is the only way back."

if ! docker compose ps --status running --services 2>/dev/null | grep -qx postgres; then
    fail "PostgreSQL is not running, so no backup can be taken.
    Start the stack with 'make up' and try again."
fi

BACKUP_ROOT="$(env_get STORAGE_ROOT /srv/pifiles)/backups"
BEFORE_DIRS="$(ls -1 "$BACKUP_ROOT" 2>/dev/null | wc -l)"

if ! "$REPO_ROOT/scripts/backup.sh" >/tmp/pifiles-upgrade-backup.log 2>&1; then
    tail -20 /tmp/pifiles-upgrade-backup.log | sed 's/^/      /' >&2
    fail "The backup failed, so the upgrade has not started.
    An upgrade without a way back is not one worth doing."
fi

AFTER_DIRS="$(ls -1 "$BACKUP_ROOT" 2>/dev/null | wc -l)"
[ "$AFTER_DIRS" -gt "$BEFORE_DIRS" ] || fail "The backup script reported success but produced nothing."

BACKUP_PATH="$BACKUP_ROOT/$(ls -1t "$BACKUP_ROOT" | head -1)"
ok "Backed up to $BACKUP_PATH"

# --- Fetch and build ----------------------------------------------------------
#
# PiFiles is distributed as source and the application image is built locally,
# so a plain `docker compose pull` fails: there is no published pifiles/app to
# pull. `--ignore-buildable` fetches the dependency images — PostgreSQL, Caddy —
# and leaves the application to be built from whatever source is checked out.
#
# That means this upgrades to the source that is *currently present*. Take the
# new version first:
#
#     git pull && make upgrade

step "Updating this checkout"

if git -C "$REPO_ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    CURRENT_SOURCE="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo 'no commits')"
    note "Building from source at $CURRENT_SOURCE"
    if ! git -C "$REPO_ROOT" diff --quiet 2>/dev/null; then
        warn "This checkout has uncommitted changes; they will be built as they are"
    fi
else
    note "Not a git checkout; building from the source in $REPO_ROOT"
fi

step "Fetching dependency images"

if ! docker compose pull --ignore-buildable 2>&1 | sed 's/^/      /'; then
    fail "Could not download the dependency images. Check the network and try again."
fi
ok "PostgreSQL and Caddy are up to date"

step "Building the application"
note "This takes a few minutes on a Pi."

# The build arguments carry the version metadata into the binary. Compose reads
# them from the environment, and without them the upgraded instance reports
# "built unknown" — which is exactly the question somebody asks when they are
# trying to work out whether an upgrade actually took.
export PIFILES_COMMIT="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
export PIFILES_BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

if ! docker compose build app 2>&1 | tail -5 | sed 's/^/      /'; then
    fail "The application image did not build. Nothing has been restarted."
fi
ok "Application image built"

step "Restarting"
note "Migrations run automatically as the application starts."

if ! docker compose up -d 2>&1 | sed 's/^/      /'; then
    fail "The stack did not restart."
fi

printf '  waiting'
HTTP_PORT="$(env_get HTTP_PORT 8088)"
READY=0
for _ in $(seq 1 120); do
    if curl -fsS "http://localhost:$HTTP_PORT/health/ready" >/dev/null 2>&1; then
        READY=1; printf ' ready.\n'; break
    fi
    printf '.'; sleep 1
done

if [ "$READY" -ne 1 ]; then
    printf '\n'
    docker compose logs --tail 30 app 2>&1 | sed 's/^/      /' >&2
    fail "PiFiles did not become healthy after the upgrade."
fi

# --- Verify -------------------------------------------------------------------

step "Checking the upgraded instance"

AFTER="$(docker compose exec -T app pifiles -version 2>/dev/null | head -1 || echo 'unknown')"
ok "Now running: $AFTER"

HEALTH="$(curl -fsS "http://localhost:$HTTP_PORT/health" 2>/dev/null || echo '{}')"
case "$HEALTH" in
    *'"status":"healthy"'*) ok "Health check passes" ;;
    *) warn "The health check is not reporting healthy"
       note "$HEALTH"
       note "Check with: docker compose logs app" ;;
esac

note "Run 'make db-check' when convenient to confirm metadata and storage agree."

trap - ERR

cat <<DONE

  ${GREEN}${BOLD}Upgrade complete.${RESET}

    was:  $BEFORE
    now:  $AFTER

  The backup taken beforehand is at:
    $BACKUP_PATH

  If something is wrong, go back with:
    ./scripts/restore.sh $BACKUP_PATH --force

  Otherwise, once you are satisfied, that backup is a good one to keep.

DONE
