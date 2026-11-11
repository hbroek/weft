#!/bin/sh
# Writes /etc/caddy/cloudflare-ips.caddy from Cloudflare's published lists. Run once, then now and then (monthly cron).
set -eu
out=${1:-/etc/caddy/cloudflare-ips.caddy}
tmp=$(mktemp)
{
  printf 'trusted_proxies static'
  for u in https://www.cloudflare.com/ips-v4 https://www.cloudflare.com/ips-v6; do
    curl -fsS "$u" | while read -r r; do [ -n "$r" ] && printf ' %s' "$r"; done
  done
  printf '\n'
} > "$tmp"
[ "$(wc -w < "$tmp")" -gt 10 ] || { echo "the lists look empty, keeping the old file" >&2; exit 1; }
install -m 644 "$tmp" "$out"; rm -f "$tmp"
systemctl reload caddy 2>/dev/null || true
