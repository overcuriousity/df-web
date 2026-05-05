#!/bin/bash
set -e

# Ensure /save exists (bind-mount may be absent in dev runs).
mkdir -p /save

# On SIGTERM the idle reaper wants a clean DF quit+save.
# We write the DF quit sequence to a named pipe that the DF process reads as stdin.
# DF's quit sequence: Escape → Save Game → Yes  (keys: Esc, S, Y)
_quit_df() {
    echo "entrypoint: received SIGTERM, requesting DF quit-save" >&2
    if [ -n "$DF_PID" ] && kill -0 "$DF_PID" 2>/dev/null; then
        # SIGTERM to DF itself causes an immediate exit without saving.
        # Instead, use xdotool (SDL mode) or write to the fifo (TEXT mode).
        # Each variant's wrapper handles this via $QUIT_HANDLER.
        if [ -n "$QUIT_HANDLER" ] && [ -f "$QUIT_HANDLER" ]; then
            bash "$QUIT_HANDLER" "$DF_PID"
        else
            kill -TERM "$DF_PID"
        fi
        wait "$DF_PID" 2>/dev/null || true
    fi
    exit 0
}
trap _quit_df SIGTERM SIGINT

# Variants source this file and then set DF_PID after launching DF.
# This script is not called directly; variants exec their own launch sequence
# and source this for the trap logic.
