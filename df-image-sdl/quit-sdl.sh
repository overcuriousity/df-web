#!/bin/bash
# Send DF's quit-save sequence via xdotool (works against the Xvfb display).
# DF quit flow: Esc → (if in fortress) select "Save Game" (key s) → confirm (key y)
# We send Esc first then s and y with small delays; DF ignores extra keypresses
# if it isn't in a state that uses them.
export DISPLAY=:0
sleep 0.3
xdotool key Escape
sleep 0.5
xdotool key s
sleep 0.3
xdotool key y
# Give DF up to 10 seconds to write the save before the container is killed.
sleep 10
