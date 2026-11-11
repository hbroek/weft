#!/bin/sh
# Nightly: a consistent snapshot of instance.db and of the users who changed, then a check. Run as the weft user.
# Add an off-machine copy of /var/backups/weft-snap (restic, rclone) once you have a place to put it.
set -eu
# the snapshots hold everyone's data: readable by the weft user only
umask 077
/opt/weft/weft backup -data /var/lib/weft -to /var/backups/weft-snap
/opt/weft/weft fsck -data /var/lib/weft
