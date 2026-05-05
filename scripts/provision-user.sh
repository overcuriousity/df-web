#!/bin/bash
# provision-user.sh [--rotate] <uid> [display_name]
#
# Default mode: create the persistent save directory for a new user, append an
# entry to session-manager/users.yml, and print the raw access key.
#
# --rotate: replace only the token_hash for an existing user (preserves
# passkeys, oidc_sub, display_name) and print a fresh access key. Use this
# when a user has lost their key.
set -e

ROTATE=0
if [ "${1:-}" = "--rotate" ]; then
    ROTATE=1
    shift
fi

UID_ARG="${1:?Usage: $0 [--rotate] <uid> [display_name]}"
DISPLAY_NAME="${2:-$UID_ARG}"
SAVES_ROOT="${SAVES_ROOT:-/srv/df/users}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
USERS_YML="$SCRIPT_DIR/../session-manager/users.yml"

RAW_TOKEN="$(openssl rand -base64 32 | tr -d '=+/' | head -c 40)"
TOKEN_HASH="$(printf '%s' "$RAW_TOKEN" | sha256sum | cut -d' ' -f1)"

if [ "$ROTATE" = "1" ]; then
    if ! grep -q "uid: \"$UID_ARG\"" "$USERS_YML"; then
        echo "Error: uid '$UID_ARG' not found in $USERS_YML — use without --rotate to create." >&2
        exit 1
    fi

    # Replace the token_hash line in the block belonging to this uid.
    # awk pass: when we see `uid: "<UID_ARG>"`, replace the next `token_hash:`
    # line we encounter before the next `- uid:` (block boundary).
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
    echo ""
    echo "Reload the session manager so the new hash takes effect:"
    echo "  docker compose kill -s SIGHUP session-manager"
    exit 0
fi

# Default mode: create new user.
SAVE_DIR="$SAVES_ROOT/$UID_ARG/save"

echo "Creating $SAVE_DIR …"
mkdir -p "$SAVE_DIR"
chown -R 1000:1000 "$SAVES_ROOT/$UID_ARG"  # DF runs as uid 1000 inside the container.
chmod 700 "$SAVES_ROOT/$UID_ARG"

if grep -q "uid: \"$UID_ARG\"" "$USERS_YML"; then
    echo "Error: uid '$UID_ARG' already exists in $USERS_YML — use --rotate to issue a fresh token." >&2
    exit 1
fi

# Append the new user entry to users.yml.
cat >> "$USERS_YML" <<EOF

- uid: "$UID_ARG"
  display_name: "$DISPLAY_NAME"
  token_hash: "$TOKEN_HASH"
  oidc_sub: ""
  passkeys: []
EOF

echo "Added $UID_ARG to $USERS_YML"
echo ""
echo "Access key (share privately — this is the key the user pastes into the login form):"
echo "  $RAW_TOKEN"
echo ""
echo "Save the raw key somewhere safe; it cannot be recovered from the hash."
echo "After the user logs in with this key they can enroll a passkey at /account."
