#!/bin/bash
# provision-user.sh [--rotate | --promote | --demote] <uid> [display_name]
#                   [--admin] <uid> [display_name]   (create with admin flag)
#
# Default mode: create the persistent save directory for a new user, insert a
# row into session-manager/users.db, and print the raw access key.
#
# --admin (with default mode): set is_admin=1 on the new user. Use only for
#   the bootstrap admin; subsequent admin promotions should use --promote.
#
# --rotate: replace only the token_hash for an existing user (preserves
#   passkeys, oidc_sub, display_name) and print a fresh access key.
#
# --promote / --demote: toggle the is_admin field on an existing user. The
#   admin flag is intentionally script-only: the web UI never exposes it, so a
#   compromised admin session cannot grant itself or others persistent access.
#
# Changes take effect immediately — no daemon reload required.
set -e

ROTATE=0
PROMOTE=0
DEMOTE=0
CREATE_ADMIN=0

while [ "${1:-}" = "--rotate" ] || [ "${1:-}" = "--promote" ] || \
      [ "${1:-}" = "--demote" ] || [ "${1:-}" = "--admin" ]; do
    case "$1" in
        --rotate)  ROTATE=1 ;;
        --promote) PROMOTE=1 ;;
        --demote)  DEMOTE=1 ;;
        --admin)   CREATE_ADMIN=1 ;;
    esac
    shift
done

# Mutually exclusive non-create modes.
nonzero=$((ROTATE + PROMOTE + DEMOTE))
if [ "$nonzero" -gt 1 ]; then
    echo "Error: --rotate, --promote, and --demote are mutually exclusive." >&2
    exit 1
fi
if [ "$CREATE_ADMIN" = "1" ] && [ "$nonzero" -gt 0 ]; then
    echo "Error: --admin is for create mode only; remove it when using --rotate/--promote/--demote." >&2
    exit 1
fi

UID_ARG="${1:?Usage: $0 [--rotate|--promote|--demote|--admin] <uid> [display_name]}"
DISPLAY_NAME="${2:-$UID_ARG}"
SAVES_ROOT="${SAVES_ROOT:-/srv/df/users}"

if ! command -v sqlite3 >/dev/null 2>&1; then
    echo "Error: sqlite3 is required but not found in PATH." >&2
    exit 1
fi

# UIDs become Docker container suffixes, filesystem paths, and SQL values.
# Restrict to a safe charset so a UID can't escape into a path or SQL.
if ! printf '%s' "$UID_ARG" | grep -Eq '^[a-z0-9][a-z0-9_-]{0,31}$'; then
    echo "Error: uid must be 1-32 chars, lowercase alphanumeric / underscore / dash, starting alphanumeric." >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
USERS_DB="$SCRIPT_DIR/../session-manager/users.db"

# Ensure the DB file exists as a regular file before Docker starts the container.
# Docker creates bind-mount targets as directories when the path is absent on
# the host, which prevents SQLite from opening the file.
touch "$USERS_DB"

# Escape a string for safe use in a SQLite single-quoted literal.
sql_escape() {
    printf "%s" "$1" | sed "s/'/''/g"
}

if [ "$ROTATE" = "1" ]; then
    count="$(sqlite3 "$USERS_DB" "SELECT COUNT(*) FROM users WHERE uid='$(sql_escape "$UID_ARG")'")"
    if [ "$count" = "0" ]; then
        echo "Error: uid '$UID_ARG' not found in database — use without --rotate to create." >&2
        exit 1
    fi

    RAW_TOKEN="$(openssl rand -base64 32 | tr -d '=+/' | head -c 40)"
    TOKEN_HASH="$(printf '%s' "$RAW_TOKEN" | sha256sum | cut -d' ' -f1)"

    sqlite3 "$USERS_DB" "UPDATE users SET token_hash='$(sql_escape "$TOKEN_HASH")' WHERE uid='$(sql_escape "$UID_ARG")'"

    echo "Rotated token for $UID_ARG"
    echo ""
    echo "New access key (share privately):"
    echo "  $RAW_TOKEN"
    exit 0
fi

if [ "$PROMOTE" = "1" ] || [ "$DEMOTE" = "1" ]; then
    count="$(sqlite3 "$USERS_DB" "SELECT COUNT(*) FROM users WHERE uid='$(sql_escape "$UID_ARG")'")"
    if [ "$count" = "0" ]; then
        echo "Error: uid '$UID_ARG' not found in database." >&2
        exit 1
    fi
    if [ "$PROMOTE" = "1" ]; then
        new_value=1
        verb="Promoted"
    else
        new_value=0
        verb="Demoted"
    fi

    sqlite3 "$USERS_DB" "UPDATE users SET is_admin=$new_value WHERE uid='$(sql_escape "$UID_ARG")'"

    echo "$verb $UID_ARG (is_admin: $new_value)"
    exit 0
fi

# Default mode: create new user.
USER_ROOT="$SAVES_ROOT/$UID_ARG"
DATA_DIR="$USER_ROOT/data"
CONFIG_DIR="$USER_ROOT/config"

echo "Creating $DATA_DIR and $CONFIG_DIR …"
mkdir -p "$DATA_DIR" "$CONFIG_DIR"
chown -R 1000:1000 "$USER_ROOT"
chmod 700 "$USER_ROOT" "$DATA_DIR" "$CONFIG_DIR"

count="$(sqlite3 "$USERS_DB" "SELECT COUNT(*) FROM users WHERE uid='$(sql_escape "$UID_ARG")'")"
if [ "$count" != "0" ]; then
    echo "Error: uid '$UID_ARG' already exists — use --rotate to issue a fresh token." >&2
    exit 1
fi

RAW_TOKEN="$(openssl rand -base64 32 | tr -d '=+/' | head -c 40)"
TOKEN_HASH="$(printf '%s' "$RAW_TOKEN" | sha256sum | cut -d' ' -f1)"

if [ "$CREATE_ADMIN" = "1" ]; then
    IS_ADMIN=1
else
    IS_ADMIN=0
fi

sqlite3 "$USERS_DB" \
    "INSERT INTO users(uid, display_name, token_hash, oidc_sub, is_admin, active_tileset, default_mode)
     VALUES ('$(sql_escape "$UID_ARG")', '$(sql_escape "$DISPLAY_NAME")', '$(sql_escape "$TOKEN_HASH")', '', $IS_ADMIN, '', '')"

echo "Added $UID_ARG${CREATE_ADMIN:+ (admin)}"
echo ""
echo "Access key (share privately — this is the key the user pastes into the login form):"
echo "  $RAW_TOKEN"
echo ""
echo "Save the raw key somewhere safe; it cannot be recovered from the hash."
echo "After the user logs in with this key they can enroll a passkey at /account."
