#!/bin/bash
# provision-user.sh [--rotate | --promote | --demote] <uid> [display_name]
#                   [--admin] <uid> [display_name]   (create with admin flag)
#
# Default mode: create the persistent save directory for a new user, append an
# entry to session-manager/users.yml, and print the raw access key.
#
# --admin (with default mode): set is_admin: true on the new user. Use only
#   for the bootstrap admin; subsequent admin promotions should use --promote
#   on a user the running daemon already knows about.
#
# --rotate: replace only the token_hash for an existing user (preserves
#   passkeys, oidc_sub, display_name) and print a fresh access key.
#
# --promote / --demote: toggle the is_admin field on an existing user. The
#   admin flag is intentionally script-only: the web UI never exposes it, so a
#   compromised admin session cannot grant itself or others persistent access.
#
# After --rotate / --promote / --demote you must reload the running daemon:
#   docker compose kill -s SIGHUP session-manager
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

# UIDs become Docker container suffixes ("df-<uid>"), filesystem paths under
# SAVES_ROOT, and regex literals in the awk-rotate path below. Restrict to a
# safe charset up front so a UID with regex metacharacters can't match the
# wrong block in users.yml or escape into a path.
if ! printf '%s' "$UID_ARG" | grep -Eq '^[a-z0-9][a-z0-9_-]{0,31}$'; then
    echo "Error: uid must be 1-32 chars, lowercase alphanumeric / underscore / dash, starting alphanumeric." >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
USERS_YML="$SCRIPT_DIR/../session-manager/users.yml"

reload_reminder() {
    echo ""
    echo "Reload the session manager so the change takes effect:"
    echo "  docker compose kill -s SIGHUP session-manager"
}

if [ "$ROTATE" = "1" ]; then
    if ! grep -q "uid: \"$UID_ARG\"" "$USERS_YML"; then
        echo "Error: uid '$UID_ARG' not found in $USERS_YML — use without --rotate to create." >&2
        exit 1
    fi

    RAW_TOKEN="$(openssl rand -base64 32 | tr -d '=+/' | head -c 40)"
    TOKEN_HASH="$(printf '%s' "$RAW_TOKEN" | sha256sum | cut -d' ' -f1)"

    # Replace the token_hash line in the block belonging to this uid.
    tmp="$(mktemp)"
    awk -v uid="$UID_ARG" -v hash="$TOKEN_HASH" '
        BEGIN { in_block = 0 }
        /^- uid: / {
            in_block = ($0 ~ "uid: \"" uid "\"") ? 1 : 0
            print; next
        }
        in_block && /^[[:space:]]+token_hash:/ {
            sub(/token_hash:.*/, "token_hash: \"" hash "\"")
            print; next
        }
        { print }
    ' "$USERS_YML" > "$tmp"
    mv "$tmp" "$USERS_YML"

    echo "Rotated token for $UID_ARG in $USERS_YML"
    echo ""
    echo "New access key (share privately):"
    echo "  $RAW_TOKEN"
    reload_reminder
    exit 0
fi

if [ "$PROMOTE" = "1" ] || [ "$DEMOTE" = "1" ]; then
    if ! grep -q "uid: \"$UID_ARG\"" "$USERS_YML"; then
        echo "Error: uid '$UID_ARG' not found in $USERS_YML." >&2
        exit 1
    fi
    if [ "$PROMOTE" = "1" ]; then
        new_value="true"
        verb="Promoted"
    else
        new_value="false"
        verb="Demoted"
    fi

    # Within the user's block: drop any existing is_admin line, then insert a
    # fresh one with the new value right after display_name. This is idempotent
    # whether or not is_admin was already present, and avoids the
    # "two lines" bug of trying to update-or-insert in a single pass.
    tmp="$(mktemp)"
    awk -v uid="$UID_ARG" -v val="$new_value" '
        BEGIN { in_block = 0 }
        /^- uid: / {
            in_block = ($0 ~ "uid: \"" uid "\"") ? 1 : 0
            print; next
        }
        in_block && /^[[:space:]]+is_admin:/ { next }
        in_block && /^[[:space:]]+display_name:/ {
            print
            print "  is_admin: " val
            next
        }
        { print }
    ' "$USERS_YML" > "$tmp"
    mv "$tmp" "$USERS_YML"

    echo "$verb $UID_ARG (is_admin: $new_value)"
    reload_reminder
    exit 0
fi

# Default mode: create new user.
USER_ROOT="$SAVES_ROOT/$UID_ARG"
DATA_DIR="$USER_ROOT/data"      # → /root/.local/share/Bay 12 Games/Dwarf Fortress (saves)
CONFIG_DIR="$USER_ROOT/config"  # → /root/.config/Bay 12 Games/Dwarf Fortress (settings)

echo "Creating $DATA_DIR and $CONFIG_DIR …"
mkdir -p "$DATA_DIR" "$CONFIG_DIR"
chown -R 1000:1000 "$USER_ROOT"
chmod 700 "$USER_ROOT" "$DATA_DIR" "$CONFIG_DIR"

if grep -q "uid: \"$UID_ARG\"" "$USERS_YML"; then
    echo "Error: uid '$UID_ARG' already exists in $USERS_YML — use --rotate to issue a fresh token." >&2
    exit 1
fi

RAW_TOKEN="$(openssl rand -base64 32 | tr -d '=+/' | head -c 40)"
TOKEN_HASH="$(printf '%s' "$RAW_TOKEN" | sha256sum | cut -d' ' -f1)"

# Append the new user entry to users.yml.
{
    printf '\n- uid: "%s"\n' "$UID_ARG"
    printf '  display_name: "%s"\n' "$DISPLAY_NAME"
    if [ "$CREATE_ADMIN" = "1" ]; then
        printf '  is_admin: true\n'
    fi
    printf '  token_hash: "%s"\n' "$TOKEN_HASH"
    printf '  oidc_sub: ""\n'
    printf '  passkeys: []\n'
} >> "$USERS_YML"

echo "Added $UID_ARG to $USERS_YML${CREATE_ADMIN:+ (admin)}"
echo ""
echo "Access key (share privately — this is the key the user pastes into the login form):"
echo "  $RAW_TOKEN"
echo ""
echo "Save the raw key somewhere safe; it cannot be recovered from the hash."
echo "After the user logs in with this key they can enroll a passkey at /account."
if [ "$CREATE_ADMIN" = "1" ]; then
    reload_reminder
fi
