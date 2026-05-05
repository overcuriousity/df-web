#!/bin/bash
set -e

mkdir -p /save /tmp

DTACH_SOCK=/tmp/df.sock

_quit() {
    /quit-text.sh
    exit 0
}
trap _quit SIGTERM SIGINT

# ttyd wraps a shell that attaches to (or starts) the dtach session.
# ttyd exits when the last client disconnects only if --once is set; we don't
# use --once so multiple reconnects work. DF keeps running between browser sessions.
exec ttyd \
    --port 7681 \
    --interface 0.0.0.0 \
    --terminal-type xterm-256color \
    --writable \
    -- dtach -A "$DTACH_SOCK" -z bash -c 'cd /opt/df && exec ./dwarfort'
