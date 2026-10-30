#!/bin/sh
# Headed Chrome on a virtual display is less likely to be treated as a bot
# than headless Chrome, so start Xvfb unless the caller supplied a display.
set -e

if [ -z "${DISPLAY:-}" ]; then
    # A stopped container that is started again keeps its old lock file, and
    # Xvfb refuses to start while it exists.
    rm -f /tmp/.X99-lock /tmp/.X11-unix/X99
    Xvfb :99 -screen 0 1280x800x24 -nolisten tcp &
    export DISPLAY=:99

    i=0
    while [ ! -S /tmp/.X11-unix/X99 ]; do
        i=$((i + 1))
        if [ "$i" -gt 100 ]; then
            echo "entrypoint: Xvfb did not create /tmp/.X11-unix/X99 within 10 seconds" >&2
            exit 1
        fi
        sleep 0.1
    done
fi

exec slonik-bot "$@"
