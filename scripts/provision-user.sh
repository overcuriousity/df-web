#!/bin/bash
# provision-user.sh <uid> [display_name]
# Creates the persistent save directory for a new user and prints a users.yml skeleton entry.
set -e

UID_ARG="${1:?Usage: $0 <uid> [display_name]}"
DISPLAY_NAME="${2:-$UID_ARG}"
SAVES_ROOT="${SAVES_ROOT:-/srv/df/users}"

SAVE_DIR="$SAVES_ROOT/$UID_ARG/save"

echo "Creating $SAVE_DIR …"
mkdir -p "$SAVE_DIR"
chown -R 1000:1000 "$SAVES_ROOT/$UID_ARG"  # DF runs as uid 1000 inside the container.
chmod 700 "$SAVES_ROOT/$UID_ARG"

RAW_TOKEN="$(openssl rand -base64 32 | tr -d '=+/' | head -c 40)"
TOKEN_HASH="$(printf '%s' "$RAW_TOKEN" | sha256sum | cut -d' ' -f1)"

cat <<EOF

Add to users.yml:

- uid: "$UID_ARG"
  display_name: "$DISPLAY_NAME"
  token_hash: "$TOKEN_HASH"
  oidc_sub: ""
  passkeys: []
  default_mode: "sdl"

Secret token URL (share privately — anyone with this link can log in as $UID_ARG):
  https://YOUR_DOMAIN/auth/token?t=$RAW_TOKEN

Save the raw token somewhere safe; it cannot be recovered from the hash.
EOF
