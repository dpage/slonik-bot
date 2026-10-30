#!/bin/sh
# Headed Chrome on a virtual display is less likely to be treated as a bot
# than headless Chrome, so start Xvfb unless the caller supplied a display.
set -e

if [ -z "${DISPLAY:-}" ]; then
    Xvfb :99 -screen 0 1280x800x24 -nolisten tcp &
    export DISPLAY=:99
fi

exec slonik-bot "$@"
