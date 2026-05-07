#!/bin/sh
# Spawn-time tileset hook. The session-manager bind-mounts the user's PNG
# uploads at /opt/df/user-tilesets (read-only) and passes the chosen filename
# via DF_ACTIVE_TILESET. This script:
#   1. Copies any uploaded PNGs into DF's data/art/ so [FONT:foo.png] resolves.
#   2. If DF_ACTIVE_TILESET is set, patches the user's init.txt (or the image
#      template, if first run hasn't happened yet) so the selection is active
#      both in classic-ASCII mode (FONT/FULLFONT) and in the graphical mode
#      (GRAPHICS_FONT/GRAPHICS_FULLFONT).
# Failures are non-fatal — DF falls back to the baked default tileset.

set -u

if [ -d /opt/df/user-tilesets ]; then
    cp -f /opt/df/user-tilesets/*.png /opt/df/data/art/ 2>/dev/null || true
fi

USER_INIT="/root/.config/Bay 12 Games/Dwarf Fortress/init/init.txt"
TEMPLATE="/opt/df/data/init/init_default.txt"
TARGET=""
if [ -f "$USER_INIT" ]; then
    TARGET="$USER_INIT"
elif [ -f "$TEMPLATE" ]; then
    TARGET="$TEMPLATE"
fi

if [ -n "${DF_ACTIVE_TILESET:-}" ] && [ -n "$TARGET" ]; then
    # Escape sed metachars (very limited charset already enforced server-side,
    # but be defensive against periods etc.).
    esc=$(printf '%s\n' "$DF_ACTIVE_TILESET" | sed -e 's/[\/&|]/\\&/g')
    for tok in FONT FULLFONT GRAPHICS_FONT GRAPHICS_FULLFONT; do
        sed -i "s|^\[${tok}:.*\]|[${tok}:${esc}]|" "$TARGET" 2>/dev/null || true
    done
fi

exit 0
