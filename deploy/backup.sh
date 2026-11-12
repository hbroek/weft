#!/bin/sh
# Nightly: a consistent snapshot of instance.db and of the users who changed, then a check, then (when
# /etc/weft-backup.env names a restic repository) an encrypted off-machine copy. Run as the weft user.
set -eu
# the snapshots hold everyone's data: readable by the weft user only
umask 077
/opt/weft/weft backup -data /var/lib/weft -to /var/backups/weft-snap
/opt/weft/weft fsck -data /var/lib/weft

# Off-machine copy. The settings come from the environment (systemd reads /etc/weft-backup.env; see
# deploy/weft-backup.env.example). Without RESTIC_REPOSITORY this step is skipped.
if [ -n "${RESTIC_REPOSITORY:-}" ]; then
	# the first run makes the repository
	restic cat config >/dev/null 2>&1 || restic init
	restic backup --quiet --host weft /var/backups/weft-snap
	restic forget --quiet --host weft --keep-daily 7 --keep-weekly 4 --keep-monthly 3 --prune
	restic check --quiet
fi

# A heartbeat for an uptime monitor: it alerts when the ping stops arriving. Only reached when every step above worked.
if [ -n "${HEARTBEAT_URL:-}" ]; then
	curl -fsS -m 20 --retry 3 -o /dev/null "$HEARTBEAT_URL" || echo "heartbeat ping failed" >&2
fi
