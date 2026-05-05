#!/bin/bash
# provision-user.sh <uid> [display_name]
# Creates the persistent save directory for a new user, appends an entry to
# session-manager/users.yml, and prints the raw access key.
set -e

UID_ARG="${1:?Usage: $0 <uid> [display_name]}"
DISPLAY_NAME="${2:-$UID_ARG}"
SAVES_ROOT="${SAVES_ROOT:-/srv/df/users}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
USERS_YML="$SCRIPT_DIR/../session-manager/users.yml"

SAVE_DIR="$SAVES_ROOT/$UID_ARG/save"

echo "Creating $SAVE_DIR …"
mkdir -p "$SAVE_DIR"
chown -R 1000:1000 "$SAVES_ROOT/$UID_ARG"  # DF runs as uid 1000 inside the container.
chmod 700 "$SAVES_ROOT/$UID_ARG"

RAW_TOKEN="$(openssl rand -base64 32 | tr -d '=+/' | head -c 40)"
TOKEN_HASH="$(printf '%s' "$RAW_TOKEN" | sha256sum | cut -d' ' -f1)"

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
