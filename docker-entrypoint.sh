#!/bin/sh
# Starts as root only to make DATA_DIR writable by the app user (a bind mount
# such as ./data is created root-owned by Docker), then drops privileges.
set -e

if [ "$(id -u)" = "0" ]; then
	mkdir -p "$DATA_DIR"
	chown -R app:app "$DATA_DIR"
	chmod 700 "$DATA_DIR"
	exec su-exec app "$@"
fi

exec "$@"
