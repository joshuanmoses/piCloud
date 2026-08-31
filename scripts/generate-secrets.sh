#!/usr/bin/env bash
#
# Creates .env from .env.example and fills in the required secrets.
#
# Safe to run more than once: an existing .env is never overwritten, and a
# secret that already has a value is left alone.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"
EXAMPLE_FILE="$REPO_ROOT/.env.example"

# Secrets that must be present and non-empty.
REQUIRED_SECRETS=(APP_SECRET_KEY DATABASE_PASSWORD)

info()  { printf '  %s\n' "$*"; }
warn()  { printf '  warning: %s\n' "$*" >&2; }
fail()  { printf '\n  error: %s\n\n' "$*" >&2; exit 1; }

generate_secret() {
    # 48 random bytes, base64-encoded, with characters that would need escaping
    # in a .env or a connection string removed.
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -base64 48 | tr -d '\n=+/' | cut -c1-48
    elif [ -r /dev/urandom ]; then
        LC_ALL=C tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 48
    else
        fail "no source of secure randomness found (need openssl or /dev/urandom)"
    fi
}

# set_if_default replaces key's value only while it still holds the shipped
# default, so an operator's deliberate choice is never overwritten.
set_if_default() {
    local key="$1" default_value="$2" new_value="$3"
    local current
    current="$(grep -E "^${key}=" "$ENV_FILE" | head -1 | cut -d= -f2- || true)"

    if [ -n "$current" ] && [ "$current" != "$default_value" ]; then
        return 0
    fi
    if [ "$current" = "$new_value" ]; then
        return 0
    fi

    local tmp
    tmp="$(mktemp "${ENV_FILE}.XXXXXX")"
    chmod 600 "$tmp"
    if grep -qE "^${key}=" "$ENV_FILE"; then
        awk -v key="$key" -v value="$new_value" \
            'BEGIN{FS=OFS="="} $1==key && !done {print key "=" value; done=1; next} {print}' \
            "$ENV_FILE" > "$tmp"
    else
        cp "$ENV_FILE" "$tmp"
        printf '%s=%s\n' "$key" "$new_value" >> "$tmp"
    fi
    mv "$tmp" "$ENV_FILE"
    info "Set $key=$new_value"
}

main() {
    echo ""
    echo "PiFiles — secret generation"
    echo ""

    [ -f "$EXAMPLE_FILE" ] || fail ".env.example not found at $EXAMPLE_FILE"

    if [ ! -f "$ENV_FILE" ]; then
        cp "$EXAMPLE_FILE" "$ENV_FILE"
        info "Created .env from .env.example"
    else
        info "Using the existing .env"
    fi

    # Restrict access before writing anything sensitive into the file.
    chmod 600 "$ENV_FILE"

    local generated=0
    for key in "${REQUIRED_SECRETS[@]}"; do
        local current
        current="$(grep -E "^${key}=" "$ENV_FILE" | head -1 | cut -d= -f2- || true)"

        if [ -n "$current" ]; then
            info "$key is already set — leaving it unchanged"
            continue
        fi

        local secret
        secret="$(generate_secret)"

        if grep -qE "^${key}=" "$ENV_FILE"; then
            # A portable in-place edit: GNU and BSD sed disagree about -i.
            local tmp
            tmp="$(mktemp "${ENV_FILE}.XXXXXX")"
            chmod 600 "$tmp"
            # The replacement is written with awk rather than sed so that
            # characters in the secret are never interpreted as sed syntax.
            awk -v key="$key" -v value="$secret" \
                'BEGIN{FS=OFS="="} $1==key && !done {print key "=" value; done=1; next} {print}' \
                "$ENV_FILE" > "$tmp"
            mv "$tmp" "$ENV_FILE"
        else
            printf '%s=%s\n' "$key" "$secret" >> "$ENV_FILE"
        fi

        info "Generated $key"
        generated=$((generated + 1))
    done

    # Default the container's uid to the account running the installer, so a
    # storage directory this user owns is writable with no further steps.
    set_if_default PIFILES_UID 10001 "$(id -u)"
    set_if_default PIFILES_GID 10001 "$(id -g)"

    chmod 600 "$ENV_FILE"

    echo ""
    if [ "$generated" -gt 0 ]; then
        info "Wrote $generated secret(s) to .env (permissions set to 600)."
    else
        info "Nothing to do — all required secrets were already set."
    fi

    echo ""
    info "Next steps:"
    info "  1. Review .env — in particular STORAGE_ROOT and APP_URL"
    info "  2. Start the stack:  make up"
    echo ""
    warn ".env contains secrets. It is git-ignored; never commit it."
    echo ""
}

main "$@"
