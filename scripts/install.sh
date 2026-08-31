#!/usr/bin/env bash
#
# Installs PiFiles.
#
#   ./scripts/install.sh
#
# Written for somebody who has never used Docker and does not want to learn it
# today. It asks a small number of questions in plain language, explains what
# each one is for, and refuses to guess at anything that would be expensive to
# get wrong.
#
# What it will not do, by design:
#
#   * It never formats, partitions, or repartitions a disk. If storage needs
#     preparing it prints the commands and stops.
#   * It never changes ownership of a directory it did not create.
#   * It never overwrites an existing .env.
#   * It is safe to run again. Every step checks before acting.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"

RED=''; YELLOW=''; GREEN=''; BLUE=''; BOLD=''; DIM=''; RESET=''
if [ -t 1 ] && command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
    RED="$(tput setaf 1)"; YELLOW="$(tput setaf 3)"; GREEN="$(tput setaf 2)"
    BLUE="$(tput setaf 4)"; BOLD="$(tput bold)"; DIM="$(tput dim)"; RESET="$(tput sgr0)"
fi

step()  { printf '\n%s──%s %s\n' "$BLUE" "$RESET" "${BOLD}$*${RESET}"; }
ok()    { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn()  { printf '  %s!%s %s\n' "$YELLOW" "$RESET" "$*"; }
note()  { printf '      %s%s%s\n' "$DIM" "$*" "$RESET"; }
say()   { printf '  %s\n' "$*"; }

fail() {
    printf '\n  %s✗ Installation stopped%s\n' "$RED" "$RESET" >&2
    printf '    %s\n\n' "$*" >&2
    printf '    Nothing has been started. Fix the above and run this again.\n\n' >&2
    exit 1
}

# ask prompts with a default and echoes the answer.
ask() {
    local prompt="$1" default="${2-}" answer=""
    if [ -n "$default" ]; then
        printf '  %s [%s]: ' "$prompt" "$default" >&2
    else
        printf '  %s: ' "$prompt" >&2
    fi
    read -r answer
    printf '%s' "${answer:-$default}"
}

# confirm requires an explicit yes for anything consequential.
confirm() {
    local prompt="$1" answer=""
    printf '  %s [y/N]: ' "$prompt" >&2
    read -r answer
    [[ "$answer" =~ ^[Yy]$ ]]
}

NON_INTERACTIVE=0
[ -t 0 ] || NON_INTERACTIVE=1

cat <<BANNER

  ${BOLD}PiFiles${RESET}
  Your files, on your own hardware.

  This will set up PiFiles on this machine. It takes a few minutes and asks
  about four questions. Nothing is started until the end, and you can stop at
  any point with Ctrl-C.

BANNER

if [ "$NON_INTERACTIVE" -eq 1 ]; then
    fail "This installer needs a terminal it can ask questions in.
    Run it directly rather than piping it to a shell."
fi

# --- 1. Is this machine ready? ----------------------------------------------

step "Checking this machine"

if ! "$REPO_ROOT/scripts/preflight.sh"; then
    fail "This machine is not ready yet. The checks above say what to fix."
fi

# --- 2. Where do the files go? ----------------------------------------------

step "Where should your files be stored?"

cat <<'EXPLAIN'
  This is where everything people upload will live. It should be on a drive
  with room to grow — an external SSD, a USB disk, a mounted volume.

  On a Raspberry Pi, do NOT use the microSD card. Cards wear out under
  continuous writes, and losing one loses everything on it.

EXPLAIN

DEFAULT_STORAGE=/srv/pifiles
# Offer something more likely to be right if an external disk is obviously
# mounted. Suggesting only, never choosing.
for candidate in /mnt/* /media/*/*; do
    [ -d "$candidate" ] || continue
    if mountpoint -q "$candidate" 2>/dev/null; then
        DEFAULT_STORAGE="$candidate/pifiles"
        note "Found a mounted drive at $candidate — suggesting it below."
        break
    fi
done

STORAGE_ROOT="$(ask "Storage location" "$DEFAULT_STORAGE")"
case "$STORAGE_ROOT" in
    /*) ;;
    *)  fail "The storage location must be an absolute path, starting with /." ;;
esac

if [ ! -d "$STORAGE_ROOT" ]; then
    say ""
    say "$STORAGE_ROOT does not exist yet."
    if confirm "Create it?"; then
        if ! mkdir -p "$STORAGE_ROOT" 2>/dev/null; then
            say ""
            say "That needs elevated privileges. Run this, then start again:"
            printf '\n      sudo mkdir -p %s\n' "$STORAGE_ROOT"
            printf '      sudo chown %s:%s %s\n\n' "$(id -un)" "$(id -gn)" "$STORAGE_ROOT"
            fail "Could not create $STORAGE_ROOT."
        fi
        ok "Created $STORAGE_ROOT"
    else
        fail "PiFiles needs somewhere to put files."
    fi
fi

if [ ! -w "$STORAGE_ROOT" ]; then
    say ""
    say "$STORAGE_ROOT exists but is not writable by $(id -un)."
    printf '\n      sudo chown -R %s:%s %s\n\n' "$(id -un)" "$(id -gn)" "$STORAGE_ROOT"
    fail "PiFiles cannot write to $STORAGE_ROOT."
fi
ok "Storage is writable: $STORAGE_ROOT"

# A warning, not a refusal. Somebody testing on a card is a legitimate thing to
# be doing, and the installer's job is to make sure they know.
STORAGE_DEVICE="$(df -P "$STORAGE_ROOT" | awk 'NR==2 {print $1}')"
if [[ "$STORAGE_DEVICE" == *mmcblk* ]]; then
    warn "This location is on the microSD card ($STORAGE_DEVICE)."
    note "Cards wear out under the continuous writes a file server makes."
    note "For anything you care about, use an external SSD instead."
    say ""
    confirm "Continue anyway?" || fail "Stopped so you can attach a different drive."
fi

AVAILABLE_GB=$(( $(df -Pk "$STORAGE_ROOT" | awk 'NR==2 {print $4}') / 1048576 ))
ok "Free space: ${AVAILABLE_GB} GB"
if [ "$AVAILABLE_GB" -lt 5 ]; then
    warn "That is not much room. PiFiles itself needs little, but your files will not fit."
fi

# --- 3. How will people reach it? -------------------------------------------

step "How will people reach PiFiles?"

cat <<'EXPLAIN'
  This becomes the address in the links PiFiles generates, so it has to match
  what people actually type. Getting it wrong is not dangerous — public links
  simply point at the wrong place until you correct it.

  On a home network, http://<this machine>:8088 is usually right.

EXPLAIN

LOCAL_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
DEFAULT_URL="http://${LOCAL_IP:-localhost}:8088"
APP_URL="$(ask "Address" "$DEFAULT_URL")"
case "$APP_URL" in
    http://*|https://*) ;;
    *) fail "The address must start with http:// or https://" ;;
esac
ok "PiFiles will be reachable at $APP_URL"

if [[ "$APP_URL" == http://* ]] && [[ "$APP_URL" != http://localhost* ]] \
   && [[ "$APP_URL" != http://127.* ]] && [[ "$APP_URL" != http://192.168.* ]] \
   && [[ "$APP_URL" != http://10.* ]]; then
    warn "That address is not encrypted and does not look like a home network."
    note "Anyone between the browser and this machine can read passwords and files."
    note "For anything reachable from the internet, use https:// — see docs/installation.md."
    say ""
    confirm "Continue anyway?" || fail "Stopped so you can set up HTTPS first."
fi

# --- 4. Configuration -------------------------------------------------------

step "Writing the configuration"

if [ -f "$ENV_FILE" ]; then
    ok "A .env already exists; leaving it alone"
    note "Delete it and run this again if you want to start over."
    note "Your answers above have NOT been applied."
else
    "$REPO_ROOT/scripts/generate-secrets.sh" >/dev/null || fail "Could not generate secrets."

    # Written with sed rather than by appending, so running the installer twice
    # cannot leave two conflicting values for the same key.
    set_env() {
        local key="$1" value="$2"
        if grep -qE "^${key}=" "$ENV_FILE"; then
            local escaped
            escaped="$(printf '%s' "$value" | sed -e 's/[\/&|]/\\&/g')"
            sed -i -E "s|^${key}=.*|${key}=${escaped}|" "$ENV_FILE"
        else
            printf '%s=%s\n' "$key" "$value" >> "$ENV_FILE"
        fi
    }

    set_env STORAGE_ROOT "$STORAGE_ROOT"
    set_env APP_URL "$APP_URL"
    chmod 600 "$ENV_FILE"

    ok "Configuration written to .env"
    note "It contains generated secrets. Keep it private and back it up."
fi

# --- 5. Start -----------------------------------------------------------------

step "Starting PiFiles"

say "This downloads the container images the first time, which can take a few"
say "minutes on a slow connection."
say ""
confirm "Start now?" || {
    printf '\n  Not started. When you are ready:\n\n      cd %s && make up\n\n' "$REPO_ROOT"
    exit 0
}

if ! docker compose up -d 2>&1 | sed 's/^/      /'; then
    fail "The stack did not start. Run 'docker compose logs' to see why."
fi

printf '\n  Waiting for PiFiles to be ready'
READY=0
HEALTH_URL="http://localhost:$(grep -E '^HTTP_PORT=' "$ENV_FILE" | cut -d= -f2 | tr -d ' ' || echo 8088)"
for _ in $(seq 1 90); do
    if curl -fsS "$HEALTH_URL/health/ready" >/dev/null 2>&1; then
        READY=1
        printf ' ready.\n'
        break
    fi
    printf '.'
    sleep 1
done

if [ "$READY" -ne 1 ]; then
    printf '\n\n'
    warn "PiFiles did not become ready within 90 seconds."
    note "It may still be starting. Check with:  docker compose logs -f app"
    note "The most common cause is the database still initialising on first run."
    exit 1
fi

# --- Done ---------------------------------------------------------------------

cat <<DONE

  ${GREEN}${BOLD}PiFiles is running.${RESET}

  ${BOLD}Open ${APP_URL} and create your administrator account.${RESET}

  ${YELLOW}Do this now.${RESET} Until an administrator exists, anyone who can reach
  this machine can claim it.

  Afterwards:

    make backup          take a backup — and do it regularly
    make logs            watch what it is doing
    make down            stop it
    make up              start it again

  Where things are:

    Your files           $STORAGE_ROOT
    Configuration        $ENV_FILE   ${DIM}(contains secrets)${RESET}

  Read next:

    docs/backup-restore.md   how to back up, and how to rehearse a restore
    docs/security.md         what PiFiles protects against, and what it does not

DONE
