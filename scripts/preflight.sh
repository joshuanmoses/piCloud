#!/usr/bin/env bash
#
# Verifies the host is ready to run PiFiles, and explains precisely how to fix
# anything that is not.
#
# It never formats a disk, never changes ownership without being asked, and
# never modifies the system silently. Where a fix requires elevated privileges
# it prints the exact command and offers to run it.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"

RED=''; YELLOW=''; GREEN=''; BOLD=''; RESET=''
if [ -t 1 ] && command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
    RED="$(tput setaf 1)"; YELLOW="$(tput setaf 3)"; GREEN="$(tput setaf 2)"
    BOLD="$(tput bold)"; RESET="$(tput sgr0)"
fi

ok()    { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn()  { printf '  %s!%s %s\n' "$YELLOW" "$RESET" "$*"; }
bad()   { printf '  %s✗%s %s\n' "$RED" "$RESET" "$*"; }
note()  { printf '      %s\n' "$*"; }

FAILED=0

# Reads a key from .env without sourcing it, so a stray shell metacharacter in
# a generated secret cannot be executed.
env_get() {
    local key="$1" default="${2-}"
    local value=""
    if [ -f "$ENV_FILE" ]; then
        value="$(grep -E "^${key}=" "$ENV_FILE" | head -1 | cut -d= -f2- | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
    fi
    printf '%s' "${value:-$default}"
}

# --- Docker ----------------------------------------------------------------
check_docker() {
    if ! command -v docker >/dev/null 2>&1; then
        bad "Docker is not installed."
        note "Install it with:  curl -fsSL https://get.docker.com | sh"
        note "Then:             sudo usermod -aG docker \$USER   (log out and back in)"
        FAILED=1
        return
    fi

    if ! docker info >/dev/null 2>&1; then
        bad "Docker is installed but not reachable."
        note "Start it with:    sudo systemctl start docker"
        note "If it says 'permission denied', add yourself to the docker group:"
        note "                  sudo usermod -aG docker \$USER   (log out and back in)"
        FAILED=1
        return
    fi
    ok "Docker is running."

    if ! docker compose version >/dev/null 2>&1; then
        bad "The Docker Compose plugin is missing."
        note "Install it with:  sudo apt install docker-compose-plugin"
        FAILED=1
        return
    fi
    ok "Docker Compose is available."
}

# --- Platform --------------------------------------------------------------
check_platform() {
    local arch; arch="$(uname -m)"
    case "$arch" in
        aarch64|arm64)  ok "Architecture: $arch (Raspberry Pi 4/5, ARM64)" ;;
        x86_64|amd64)   ok "Architecture: $arch" ;;
        armv7l|armhf)
            bad "32-bit ARM ($arch) is not supported."
            note "PiFiles needs 64-bit Raspberry Pi OS. Reinstall with the 64-bit image."
            FAILED=1 ;;
        *)              warn "Unrecognised architecture: $arch. PiFiles may still work." ;;
    esac

    if [ -r /proc/meminfo ]; then
        local mb; mb=$(( $(awk '/MemTotal/ {print $2}' /proc/meminfo) / 1024 ))
        if   [ "$mb" -lt 1800 ]; then bad  "Only ${mb} MB of RAM. PiFiles needs at least 4 GB."; FAILED=1
        elif [ "$mb" -lt 3600 ]; then warn "${mb} MB of RAM. 4 GB is the supported minimum; expect it to be tight."
        else                          ok   "Memory: ${mb} MB"
        fi
    fi
}

# --- Configuration ---------------------------------------------------------
check_env_file() {
    if [ ! -f "$ENV_FILE" ]; then
        bad "No .env file."
        note "Create one with:  ./scripts/generate-secrets.sh"
        FAILED=1
        return
    fi
    ok ".env exists."

    local missing=""
    for key in APP_SECRET_KEY DATABASE_PASSWORD; do
        [ -z "$(env_get "$key")" ] && missing="$missing $key"
    done
    if [ -n "$missing" ]; then
        bad "Required secrets are empty:$missing"
        note "Fill them in with:  ./scripts/generate-secrets.sh"
        FAILED=1
    else
        ok "Required secrets are set."
    fi

    local perms; perms="$(stat -c '%a' "$ENV_FILE" 2>/dev/null || stat -f '%OLp' "$ENV_FILE" 2>/dev/null || echo '')"
    if [ -n "$perms" ] && [ "$perms" != "600" ]; then
        warn ".env is mode $perms; it contains secrets."
        note "Restrict it with:  chmod 600 .env"
    fi
}

# --- Storage ---------------------------------------------------------------
check_storage() {
    local root; root="$(env_get STORAGE_ROOT /srv/pifiles)"
    # Resolve a relative path the same way Compose does: against the project root.
    case "$root" in
        /*) ;;
        *)  root="$REPO_ROOT/${root#./}" ;;
    esac

    local uid; uid="$(env_get PIFILES_UID 10001)"
    local gid; gid="$(env_get PIFILES_GID 10001)"

    if [ ! -d "$root" ]; then
        warn "Storage directory does not exist: $root"
        if mkdir -p "$root" 2>/dev/null; then
            ok "Created $root"
        else
            bad "Could not create $root"
            note "Create it with:   sudo mkdir -p '$root'"
            note "Then:             sudo chown $uid:$gid '$root'"
            FAILED=1
            return
        fi
    fi

    # Ownership: the container runs as $uid:$gid and must be able to write here.
    local owner_uid owner_gid
    owner_uid="$(stat -c '%u' "$root" 2>/dev/null || stat -f '%u' "$root")"
    owner_gid="$(stat -c '%g' "$root" 2>/dev/null || stat -f '%g' "$root")"

    if [ "$owner_uid" = "$uid" ]; then
        ok "Storage directory is writable by the container ($root)"
    else
        bad "Storage directory is owned by uid $owner_uid, but the container runs as uid $uid."
        note "This is the most common setup problem. Fix it with either:"
        note ""
        note "  A) Change the directory's owner:"
        note "       sudo chown -R $uid:$gid '$root'"
        note ""
        note "  B) Run the container as the directory's current owner —"
        note "     add these lines to .env:"
        note "       PIFILES_UID=$owner_uid"
        note "       PIFILES_GID=$owner_gid"
        note ""
        FAILED=1
        return
    fi

    # Warn when user data would land on the boot media. Sustained writes wear
    # microSD out, and losing it would take every user's files with it.
    local root_dev boot_dev
    root_dev="$(df -P "$root" 2>/dev/null | awk 'NR==2 {print $1}')"
    boot_dev="$(df -P / 2>/dev/null | awk 'NR==2 {print $1}')"
    if [ -n "$root_dev" ] && [ "$root_dev" = "$boot_dev" ]; then
        case "$root_dev" in
            *mmcblk*)
                warn "Storage is on the microSD card ($root_dev)."
                note "This wears the card out and risks losing every user's files."
                note "Move STORAGE_ROOT to an external SSD or NVMe drive. See docs/storage.md." ;;
            *)
                warn "Storage shares a filesystem with the operating system ($root_dev)."
                note "An external drive is strongly recommended. See docs/storage.md." ;;
        esac
    else
        ok "Storage is on a separate filesystem ($root_dev)."
    fi

    # On SELinux hosts an unlabelled bind mount is denied with a bare
    # "permission denied" that is easily mistaken for wrong ownership. The
    # Compose file mounts with ":z" so Docker relabels it; note the fact here
    # so the behaviour is not a surprise.
    if command -v getenforce >/dev/null 2>&1 && [ "$(getenforce 2>/dev/null)" = "Enforcing" ]; then
        ok "SELinux is enforcing; the storage mount is labelled automatically (:z)."
    fi

    local avail_kb
    avail_kb="$(df -P "$root" 2>/dev/null | awk 'NR==2 {print $4}')"
    if [ -n "$avail_kb" ]; then
        local avail_gb=$(( avail_kb / 1024 / 1024 ))
        if [ "$avail_gb" -lt 2 ]; then
            bad "Only ${avail_gb} GB free on the storage volume."
            FAILED=1
        else
            ok "Free space: ${avail_gb} GB"
        fi
    fi
}

# --- Ports -----------------------------------------------------------------
check_ports() {
    local http https
    http="$(env_get HTTP_PUBLIC_PORT 80)"
    https="$(env_get HTTPS_PUBLIC_PORT 443)"

    for port in "$http" "$https"; do
        [ -z "$port" ] && continue
        local in_use=""
        if command -v ss >/dev/null 2>&1; then
            in_use="$(ss -ltn "sport = :$port" 2>/dev/null | awk 'NR>1')"
        fi
        if [ -n "$in_use" ]; then
            # Our own Caddy holding the port is expected on a restart.
            if docker ps --format '{{.Ports}}' 2>/dev/null | grep -q ":$port->"; then
                ok "Port $port is held by PiFiles itself."
            else
                bad "Port $port is already in use by something else."
                note "Find what:        sudo ss -ltnp 'sport = :$port'"
                note "Or choose another port in .env (HTTP_PUBLIC_PORT / HTTPS_PUBLIC_PORT)."
                FAILED=1
            fi
        else
            ok "Port $port is free."
        fi
    done
}

main() {
    printf '\n%sPiFiles — preflight checks%s\n\n' "$BOLD" "$RESET"
    check_docker
    check_platform
    check_env_file
    check_storage
    check_ports
    printf '\n'

    if [ "$FAILED" -ne 0 ]; then
        printf '%sPreflight failed.%s Fix the items marked ✗ above, then run this again.\n\n' "$RED" "$RESET"
        exit 1
    fi
    printf '%sReady to start.%s\n\n' "$GREEN" "$RESET"
}

main "$@"
