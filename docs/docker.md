# Your own Weft server, with Docker

One account, your own address, a certificate that looks after itself. You need a machine that is on all the time and
reachable from the internet (a small VPS is enough), Docker with the `compose` plugin, and a domain you own.

**It has to be reachable from outside.** Likes and replies from other servers work because each side fetches the other's
public page, and your server's address is checked the same way. A computer behind a router with no open ports cannot take
them: forward ports 80 and 443 to it, or put the server on a VPS.

## Three steps

1. **DNS.** Make an A record for `weft.<your domain>` (for example `weft.example.com`) that points at the machine. It is the
   address of your Weft and of your feed. `weft.` first is the convention that lets you move between servers later
   without changing your address.
2. **Files.** In an empty folder:
   ```
   curl -LO https://raw.githubusercontent.com/hbroek/weft/main/docker/compose.yaml
   curl -LO https://raw.githubusercontent.com/hbroek/weft/main/docker/Caddyfile
   curl -L  https://raw.githubusercontent.com/hbroek/weft/main/docker/.env.example -o .env
   nano .env            # your host name, your account name, an email address for the certificate
   ```
3. **Start.**
   ```
   docker compose up -d
   docker compose logs weft      # the first start prints the account's temporary password, once
   ```
   Open `https://weft.example.com/login`, sign in with that password and choose your own. The first visit can take a
   few seconds while the certificate is made.

(From a clone of the repository, `cd docker && docker compose up -d --build` builds the image on your machine instead of
downloading it.)

## What runs

- `weft`: the server, started with `-host weft.example.com -user me`. It makes the account on the first start and serves it
  at exactly that host name. Nobody else can sign up, and the profile page has no domain settings to change. The data is in
  the Docker volume `weft-data` (`instance.db` and your account's file).
- `caddy`: takes the certificate for the host name from Let's Encrypt, renews it, and passes requests to Weft. Weft is not
  published on any port of its own.

## Every day

- **Update:** `docker compose pull && docker compose up -d`. Take a backup first (below): a new version may change the
  database format on its first start.
- **Backup:** a consistent snapshot, while the server runs:
  ```
  docker compose exec weft /weft backup -data /data -to /tmp/backup -full
  docker compose cp weft:/tmp/backup ./weft-backup
  ```
  Keep `weft-backup` somewhere other than this machine (the README suggests restic). To restore into a new, empty volume:
  `docker compose run --rm -v "$PWD/weft-backup:/backup:ro" weft restore -from /backup -data /data`.
- **Your data:** *Download my data* on your profile gives you everything in one file, and `weft move` carries the account to
  another server and redirects the old address.
- **Logs:** `docker compose logs -f weft`. Caddy keeps no access log unless you turn one on.
- **Stop and remove:** `docker compose down` stops it; the volumes keep your data. `docker compose down -v` deletes it too.

## Notes

- The temporary password is written to the container's log. Choosing your own at the first sign-in replaces it; do that
  before you share the address.
- Port 443 over UDP is not used (Caddy is set to HTTP/1.1 and HTTP/2), so only TCP 80 and 443 need to be open.
- `-trust-proxy` is on because only Caddy can reach the server. Do not publish port 8080 yourself.
