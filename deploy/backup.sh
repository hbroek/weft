#!/bin/sh
# Nightly: a consistent snapshot of instance.db and of the users who changed, then a check, then (when
# /etc/weft-backup.env names a restic repository) an encrypted off-machine copy. Run as the weft user.
set -eu
# the snapshots hold everyone's data: readable by the weft user only
umask 077

# An optional heartbeat monitor (HEARTBEAT_URL is its ping address, like https://host/ping/TOKEN). A good run
# pings it; a failed run reports which step failed, to HEARTBEAT_URL/fail?msg=..., so the alert says why; and a
# run that never happens sends nothing, which the monitor notices by itself.
step=start
ping() { # ping [/fail] [message]
	[ -n "${HEARTBEAT_URL:-}" ] || return 0
	if [ -n "${2:-}" ]; then
		curl -fsS -m 20 --retry 3 -o /dev/null -G --data-urlencode "msg=$2" "${HEARTBEAT_URL%/}$1" || echo "heartbeat ping failed" >&2
	else
		curl -fsS -m 20 --retry 3 -o /dev/null "${HEARTBEAT_URL%/}$1" || echo "heartbeat ping failed" >&2
	fi
}
finish() {
	code=$?
	[ "$code" -eq 0 ] || ping /fail "weft backup: $step failed (exit $code)"
}
trap finish EXIT

step="snapshot"
/opt/weft/weft backup -data /var/lib/weft -to /var/backups/weft-snap
step="fsck"
/opt/weft/weft fsck -data /var/lib/weft

# Off-machine copy. The settings come from the environment (systemd reads /etc/weft-backup.env; see
# deploy/weft-backup.env.example). Without RESTIC_REPOSITORY this step is skipped.
if [ -n "${RESTIC_REPOSITORY:-}" ]; then
	# the first run makes the repository
	step="restic init"
	restic cat config >/dev/null 2>&1 || restic init
	step="restic backup"
	restic backup --quiet --host weft /var/backups/weft-snap
	step="restic forget"
	restic forget --quiet --host weft --keep-daily 7 --keep-weekly 4 --keep-monthly 3 --prune
	step="restic check"
	restic check --quiet
fi

# Only reached when every step above worked.
step="heartbeat"
ping ""
