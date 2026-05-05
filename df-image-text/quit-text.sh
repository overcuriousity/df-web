#!/bin/bash
# Send DF quit-save sequence to the dtach session via a detached write.
# dtach accepts input via a slave pty attached to the socket.
# We use dtach's -N flag (no-hangup) to write key sequences to the running session.
# Key sequence: Esc, then s (Save Game), then y (confirm).
DTACH_SOCK=/tmp/df.sock
if [ -S "$DTACH_SOCK" ]; then
    printf '\033' | dtach -N "$DTACH_SOCK" 2>/dev/null || true   # Escape
    sleep 0.5
    printf 's'   | dtach -N "$DTACH_SOCK" 2>/dev/null || true   # Save
    sleep 0.3
    printf 'y'   | dtach -N "$DTACH_SOCK" 2>/dev/null || true   # Confirm
    sleep 8   # wait for DF to write save
fi
