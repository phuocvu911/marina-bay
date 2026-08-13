#!/bin/sh
# A freshly created Fly volume mounts root-owned, so /data has to be handed to
# the unprivileged user before we drop to it — otherwise the first boot of a
# new deployment dies on "unable to open database file" and nothing else.
# Only the directory is chowned: files inside it were written by this user.
set -e

DATA_DIR="${DATA_DIR:-/data}"
mkdir -p "$DATA_DIR"
chown marina:marina "$DATA_DIR"

exec su-exec marina /usr/local/bin/marina-bay "$@"
