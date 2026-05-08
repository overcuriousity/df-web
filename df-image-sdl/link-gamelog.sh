#!/bin/sh
# Redirect DF's gamelog.txt into the user's bind-mounted save dir so the
# session-manager can tail it from the host without docker exec.
# BIND_SAVE_DIR is the container-side path of the user's data directory
# (/root/.local/share/Bay 12 Games/Dwarf Fortress).

BIND_SAVE_DIR="/root/.local/share/Bay 12 Games/Dwarf Fortress"
GAMELOG="${BIND_SAVE_DIR}/gamelog.txt"

: > "$GAMELOG"   # truncate / create; fresh log per session start
ln -sf "$GAMELOG" /opt/df/gamelog.txt
