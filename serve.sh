#!/bin/sh
# Build slonik-bot and start the server that the Chrome extension talks to.
# Any arguments are passed to 'slonik-bot serve', for example --no-summary.
set -e

cd "$(dirname "$0")"
make build
exec ./bin/slonik-bot serve "$@"
