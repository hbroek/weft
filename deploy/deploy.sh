#!/bin/sh
# Build, copy, restart, check. Run from the repo on the Mac:  deploy/deploy.sh root@YOUR_SERVER_IP
set -eu
host=${1:?usage: deploy/deploy.sh user@host}
make release
scp bin/weft-linux-amd64 "$host:/opt/weft/weft.new"
rsync -a --no-owner --no-group --delete site/ "$host:/opt/weft/site/"
ssh "$host" '
  set -e
  chmod 755 /opt/weft/weft.new
  # keep the last binary for a quick way back
  [ -f /opt/weft/weft ] && cp -p /opt/weft/weft /opt/weft/weft.prev
  mv /opt/weft/weft.new /opt/weft/weft
  systemctl restart weft
  sleep 2
  systemctl is-active weft
  curl -fsS http://127.0.0.1:8081/metrics >/dev/null && echo "weft answers"
  curl -fsS -H "Host: www.weftseed.com" http://127.0.0.1:8080/status.json
  echo
'
