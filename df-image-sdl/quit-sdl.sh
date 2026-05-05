#!/bin/bash
# Send DF's quit-save key sequence via xdotool.
# DF quit flow: Esc → (fortress mode) s to select "Save Game" → y to confirm.
# Extra keypresses are harmless if DF is in a menu that doesn't use them.
export DISPLAY=:0
sleep 0.3
xdotool key Escape
sleep 0.5
xdotool key s
sleep 0.3
xdotool key y
