# Hosting a loom for many accounts

This is for running a loom (a Weft server) for several people under a domain of your own: `name.example.org` for each
account, custom domains for those who bring one, Cloudflare in front for caching, and Caddy for certificates. For one
account at your own address, use the [Docker install](docker.md) instead, which is much shorter.

The example domain is `example.org` and the server's address is `YOUR_SERVER_IP`. The files are in `deploy/`; nothing here is
switched on by the repository, each step is run by hand.

```
visitor ──► Cloudflare (proxied: *.example.org, www)  ─┐
visitor ───────────────── (DNS only: custom domains) ──┤
                                                       ▼
                                                 Caddy :443 (certificates, on demand for custom domains)
                                                       │ reverse_proxy
                                                       ▼
                                  weft  127.0.0.1:8080 (public)   127.0.0.1:8081 (internal: ask, /metrics, the admin pages)
```

If the machine already serves another site on 80 and 443, put nginx's `stream` module in front, which reads the host name
in each TLS connection (it decrypts nothing) and hands the other site's names to that site and every other name to Caddy
(`deploy/nginx-stream.conf`, section 4). On a machine of its own, Caddy can take 80 and 443 directly and sections 4 and the
PROXY-protocol parts of `deploy/Caddyfile` are not needed.

## 1. Cloudflare

- Zone `example.org`. SSL/TLS mode **Full (strict)**.
- `*` and `www`: A `YOUR_SERVER_IP`, **proxied** (the README, "Behind Cloudflare", says why this is fine for users' addresses).
- `weft` (the CNAME target users point their own domains at): A `YOUR_SERVER_IP`, **DNS only** (grey cloud). It must show the real address.
- The bare `example.org` can be proxied too (Weft redirects it to `www`).
- SSL/TLS, Origin Server: make an Origin CA certificate for `example.org` and `*.example.org`. Save as
  `/etc/caddy/certs/example.pem` and `example.key` (key mode 600, owned by `caddy`) and name them in `deploy/Caddyfile`.
- Leave Polish and Mirage off (the README says so).

## 2. Build and install Weft

On the server first (the folders must exist before the copy):

```
useradd --system --home /var/lib/weft --shell /usr/sbin/nologin weft
mkdir -p /opt/weft/site /var/backups/weft-snap && chown weft: /var/backups/weft-snap && chmod 700 /var/backups/weft-snap
```

On your computer, from the repository (edit the host names in `deploy/weft.service` first):

```
make release
scp bin/weft-linux-amd64 root@YOUR_SERVER_IP:/opt/weft/weft.new
rsync -a --no-owner --no-group --delete site/ root@YOUR_SERVER_IP:/opt/weft/site/
scp deploy/weft.service deploy/weft-backup.service deploy/weft-backup.timer root@YOUR_SERVER_IP:/etc/systemd/system/
scp deploy/backup.sh deploy/update-cloudflare-ips.sh root@YOUR_SERVER_IP:/opt/weft/
```

On the server:

```
mv /opt/weft/weft.new /opt/weft/weft && chmod 755 /opt/weft/weft
chmod +x /opt/weft/*.sh
systemctl daemon-reload && systemctl enable --now weft
sudo -u weft /opt/weft/weft user add -data /var/lib/weft -admin YOURNAME    # your own account; prints a temporary password
curl -s http://127.0.0.1:8081/metrics | head
curl -s -H 'Host: www.example.org' http://127.0.0.1:8080/ | head -3
```

After this first install, `deploy/deploy.sh root@YOUR_SERVER_IP` does the build, copy and restart.

`-max-users` in the unit is the number the landing page shows; change it when you want.

## 3. Caddy

```
# Many distributions ship an old Caddy (Ubuntu's is 2.6, without PROXY protocol). Use Caddy's own repository
# (https://caddyserver.com/docs/install#debian-ubuntu-raspbian); version 2.7 or newer is needed.
apt install caddy
# on your computer: scp deploy/Caddyfile root@YOUR_SERVER_IP:/etc/caddy/Caddyfile
nano /etc/caddy/Caddyfile                   # your email address, your certificate file names
chown caddy: /etc/caddy/certs/example.*     # the Origin CA certificate and key from step 1
/opt/weft/update-cloudflare-ips.sh
caddy validate --config /etc/caddy/Caddyfile
systemctl restart caddy
```

Behind nginx, Caddy must not also take 80 and 443: the Caddyfile moves it to 8082 and 8444 and binds it to 127.0.0.1.
`ss -ltnp | grep caddy` should then show only `127.0.0.1:8082`, `:8444` and the admin port `:2019`.

## 4. nginx in front (only when the machine serves another site)

This is the step that can hurt the other site, so do it when it is quiet and keep a copy of `/etc/nginx` first:
`cp -a /etc/nginx /root/nginx-backup-$(date +%F)`.

0. Some distributions build the stream module separately (Ubuntu: `apt install libnginx-mod-stream`; check
   `ls /etc/nginx/modules-enabled`; without it `nginx -t` says `unknown directive "stream"`).
1. List every name nginx serves over HTTPS (`grep -rh 'server_name\|default_server' /etc/nginx/sites-enabled /etc/nginx/conf.d`):
   each must be in the `map` in `deploy/nginx-stream.conf`, or it would be sent to Caddy. Put the file at
   `/etc/nginx/stream-weft.conf` and add `include /etc/nginx/stream-weft.conf;` to `nginx.conf` outside `http { }`.
2. In the other site's server block, change the 443 listener as described at the bottom of `deploy/nginx-stream.conf`. For
   Weft's port-80 names add the `server` block at the bottom of that file as `/etc/nginx/conf.d/weft-http.conf`, with
   `access_log off;` (see section 7).
3. `nginx -t`. If it fails, nothing has changed yet.
4. `systemctl reload nginx` (a reload keeps existing connections), and **at once** check the other site.
5. Back out: restore the copy and reload.

## 5. Checks, in this order

| Check | Command or action | Expected |
|---|---|---|
| The other site still works | open it | the same as before |
| Landing | open https://www.example.org | the landing page, valid certificate |
| Account host | make an account, open `https://NAME.example.org/` and `/feed.xml` | pages load |
| HTTP redirect | `curl -sI http://name.example.org/` | 301 to https |
| The certificate question | `curl -s 'http://127.0.0.1:8081/domain-ok?domain=bogus.example'` | 404; and for `NAME.example.org` 200 |
| Real addresses | 6 wrong passwords from a phone on mobile data, then sign in from another network | the second gets in |
| Admin | `ssh -L 8081:127.0.0.1:8081 root@YOUR_SERVER_IP`, open http://localhost:8081/ (the pages are at the root, not /admin) | the admin pages |
| Backup | `systemctl start weft-backup && journalctl -u weft-backup` | the snapshot and `fsck` pass |
| Restore | `weft restore -from /var/backups/weft-snap -data /tmp/x`, then `weft fsck -data /tmp/x` | no problems |
| Reboot | `reboot` | weft, caddy (and nginx) are back by themselves |

## 6. Testing custom domains with a real domain

Needs a spare domain (or a subdomain of one you own). Run it twice: first with the CNAME **not proxied** (grey cloud), then
with the proxy **on** and SSL/TLS set to Full (strict) in that domain's Cloudflare, which is how users who want
Cloudflare's caching will set it up.

1. At `www.example.org/signup` enter `weft.<their domain>` and a name.
2. Add the TXT record `_weft.weft.<domain>` as shown, then press the check.
3. Add `weft.<domain>` CNAME `weft.example.org` (DNS only).
4. Press *Check again*: Caddy must get a certificate the first time (watch `journalctl -u caddy`), the server must find
   `/.well-known/weft-domain`, and the domain becomes active.
5. Then break it: delete the TXT record and wait for the daily check (or run the tests, which pass the clock in), and delete
   the CNAME to see the reachability check give up after three failed days.

Things learned doing this:

- With the proxy on and Cloudflare's SSL/TLS on *Flexible* (the default), the domain answers `301` to itself and Weft's fetch
  says `too many redirects`. Set the domain's SSL/TLS to Full (strict). The profile page, the sign-up page and the FAQ say so.
- A like from a laptop dev server does not arrive: the loom cannot fetch `localhost`. Test likes between two public addresses.
- Plain `http://` to any name, ours or not, answers `301` to the same address over `https://` and is never served.

## 7. Before real users

- Terms and privacy: the pages in `site/` are drafts (`[REVIEW BEFORE PUBLISHING]` marks): operator, country, law, backup
  frequency, logs. Have them read by someone qualified.
- Access logs: if the privacy page says none are kept, check it. Caddy's is off by default, nginx's stream block has none, and the
  port-80 `server` in `weft-http.conf` must say `access_log off;` (otherwise the `http{}` level log records every plain-http
  visit). `grep -c example.org /var/log/nginx/access.log*` should be 0 after the change, and `weft` must not run with `-v`.
- An off-machine copy of the backups (section 8), and your host's snapshot of the whole server if it offers one. A host
  snapshot is taken while the server runs, so for Weft's data restore from the `weft backup` copies.
- Uptime monitoring from outside the server (a check run on the server goes down with it): a keyword check on
  `https://www.example.org/status.json` (HTTP 200 and `"cap"`), one on a user's address (the wildcard route) and one on a
  custom domain (its certificate and Cloudflare).
- The measurements in [production-optimizations.md](production-optimizations.md) (thread fetch cache and the others).

## 8. Off-machine backup (restic to S3-compatible storage)

`deploy/backup.sh` makes the snapshot and runs `fsck`; when `RESTIC_REPOSITORY` is set it then copies
`/var/backups/weft-snap` to the repository with restic (encrypted before it leaves the server, deduplicated), keeps 7 daily,
4 weekly and 3 monthly copies, and runs `restic check`. An optional `HEARTBEAT_URL` (a dead-man's-switch monitor) is pinged
only after every step worked. The retention is also the longest time a deleted account stays in the copies: say so in the
privacy page and keep the two in step.

1. Make a bucket (Cloudflare R2, Backblaze B2 or any S3-compatible store) with public access **off**, and an access key
   limited to it with read and write.
2. On your computer: `scp deploy/backup.sh root@YOUR_SERVER_IP:/opt/weft/ && scp deploy/weft-backup.service root@YOUR_SERVER_IP:/etc/systemd/system/ && scp deploy/weft-backup.env.example root@YOUR_SERVER_IP:/root/`.
3. On the server:
   ```
   apt install -y restic
   install -m 600 -o root -g root /root/weft-backup.env.example /etc/weft-backup.env
   nano /etc/weft-backup.env        # the repository, the two keys, a long random RESTIC_PASSWORD
   systemctl daemon-reload
   ```
   Keep the `RESTIC_PASSWORD` somewhere other than this server (a password manager). Without it the copy cannot be read.
4. `systemctl start weft-backup && journalctl -u weft-backup -n 30` (the first run makes the repository and uploads everything).
5. **Prove the restore, from the repository into an empty folder:**
   ```
   set -a; . /etc/weft-backup.env; set +a          # as root
   restic snapshots
   restic restore latest --target /tmp/offsite-test
   chown -R weft: /tmp/offsite-test                 # restic made the parent folders as root, mode 700
   sudo -u weft /opt/weft/weft restore -from /tmp/offsite-test/var/backups/weft-snap -data /tmp/offsite-x
   sudo -u weft /opt/weft/weft fsck -data /tmp/offsite-x
   rm -rf /tmp/offsite-test /tmp/offsite-x
   ```
   `fsck` should report no problems. Do this once before you rely on the copy.
