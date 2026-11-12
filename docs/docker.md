# Your own Weft server, with Docker

One account, your own address, a certificate that looks after itself. You need a machine that is on all the time and
reachable from the internet (a small VPS is enough), Docker with the `compose` plugin, and a domain you own.

**It has to be reachable from outside.** Likes and replies from other servers work because each side fetches the other's
public page, and your server's address is checked the same way. A computer behind a router with no open ports cannot take
them: forward ports 80 and 443 to it, or put the server on a VPS.

## Before you start

- **Docker** with the `compose` plugin: <https://docs.docker.com/engine/install/>. Check with `docker compose version`.
- **Ports 80 and 443 (TCP)** must reach the machine, in its own firewall, in a cloud provider's firewall (Hetzner, AWS and
  others have one in their console) and in your router if it is at home. Port 80 is needed to get the certificate.
- **If your domain is on Cloudflare,** make the `weft.` record **DNS only** (grey cloud) at least until the certificate is
  made. With the proxy on, certificate issue can fail, and the proxy's SSL mode must be Full (strict).
- If the machine has an IPv6 address, add an AAAA record as well, or leave it out; do not point it somewhere else.

## No open ports? Use a Cloudflare Tunnel

If the machine is at home, or you cannot open ports 80 and 443, run it behind a Cloudflare Tunnel instead: the machine
makes an outgoing connection to Cloudflare, which brings visitors to it. You need a domain on Cloudflare. Use
`compose.tunnel.yaml` in place of `compose.yaml` and the `Caddyfile` (not needed):

1. In the Cloudflare dashboard, **Networking, then Tunnels** (older documentation calls it Zero Trust, Networks, Connectors):
   make a tunnel of type *Cloudflared*. The page then shows an install command for an operating system: the long string
   starting `eyJ` in it is the tunnel's token. Do not run that command (the container in `compose.tunnel.yaml` is the
   connector). Anyone with the token can run your tunnel, so keep it private, and never paste it into a chat or a commit.
2. In the folder that holds `compose.tunnel.yaml` (the `docker/` folder of a clone), make `.env` from `.env.example` and fill in
   `WEFT_HOST`, `WEFT_USER` and `TUNNEL_TOKEN` (the string from step 1).
3. **Start the connector first:** `docker compose -f compose.tunnel.yaml up -d`. Cloudflare's page says "No connection
   detected yet" and will not let you go on until a connector is running. Check it with
   `docker compose -f compose.tunnel.yaml logs cloudflared`: it should say "Registered tunnel connection" (usually four times).
4. Now, on Cloudflare's page, add the **published application**: hostname = your `WEFT_HOST`, and as the service
   **`http://weft:8080`**. Both parts matter: `weft` is the container's name inside the compose project (`localhost`
   would be the connector itself), and it is `http`, not `https`, because the container speaks plain HTTP and Cloudflare
   does the HTTPS for your visitors. Leave the additional settings on their defaults (Weft needs the `Host` header as it is).
   Cloudflare makes the DNS record for you.
5. Read the first password with `docker compose -f compose.tunnel.yaml logs weft | grep -i "temporary password"` and sign in
   at `https://<your host>/login`.

If it does not work, the connector's log says why (`docker compose -f compose.tunnel.yaml logs cloudflared`):
- **Error 1033 in the browser:** no connector is attached to the tunnel the name points at. The token in `.env` is probably
  from a different tunnel than the one that has the hostname.
- **502:** the connector cannot reach the service. The log line says `originService=...`: it must read `http://weft:8080`.
- **Weft's plain "404 page not found":** the route works, but `WEFT_HOST` does not match the hostname exactly.

The rest of this guide applies unchanged; add `-f compose.tunnel.yaml` to the `docker compose` commands.

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
   - `WEFT_HOST`: the address from step 1, such as `weft.example.com`.
   - `WEFT_USER`: the name of your account: lower-case letters and digits, and not a reserved word such as `weft`, `www` or `admin`.
   - `ACME_EMAIL`: where Let's Encrypt writes if something is wrong with the certificate. It is not shown to anyone.
3. **Start.**
   ```
   docker compose up -d
   docker compose logs weft      # the first start prints the account's temporary password, once
   ```
   Open `https://weft.example.com/login`, sign in with that password and choose your own. The first visit can take a
   few seconds while the certificate is made. If the page does not load, see *When something is wrong* below.

(From a clone of the repository, `cd docker && docker compose up -d --build` builds the image on your machine instead of
downloading it.)

## What runs

- `weft`: the server, started with `-host weft.example.com -user me`. It makes the account on the first start and serves it
  at exactly that host name. Nobody else can sign up, and the profile page has no domain settings to change. The data is in
  the Docker volume `weft-data` (`instance.db` and your account's file).
- `caddy`: takes the certificate for the host name from Let's Encrypt, renews it, and passes requests to Weft. Weft is not
  published on any port of its own.

## After you sign in

Your home page has a **Weft it** link: drag it to your bookmarks bar, then click it on any page to bookmark it, reply to it,
like it or repost it. The README explains the rest (following people, replies, likes, your feed at `/feed.xml`).

## Every day

- **Update:** `docker compose pull && docker compose up -d`. The compose file follows `:latest`; to stay on one version, change
  the image line to a tag such as `ghcr.io/hbroek/weft:0.1.0`. The images run on amd64 (ordinary PCs and servers) and arm64 (Raspberry Pi, Apple silicon). Take a backup first (below): a new version may change the
  database format on its first start.
- **Backup:** a consistent snapshot, while the server runs:
  ```
  docker compose exec weft /weft backup -data /data -to /tmp/backup -full
  docker compose cp weft:/tmp/backup ./weft-backup
  ```
  Keep `weft-backup` somewhere other than this machine (the README suggests restic). To restore into a new, empty volume:
  `docker compose run --rm -v "$PWD/weft-backup:/backup:ro" weft restore -from /backup -data /data`.
- **Forgot the password, or missed the first one:** `docker compose exec weft /weft user passwd -data /data me` (use your
  account name) prints a new temporary password; you choose your own at the next sign-in.
- **Your data:** *Download my data* on your profile gives you everything in one file, and `weft move` carries the account to
  another server and redirects the old address.
- **Logs:** `docker compose logs -f weft`. Caddy keeps no access log unless you turn one on.
- **Stop and remove:** `docker compose down` stops it; the volumes keep your data. `docker compose down -v` deletes it too.

## When something is wrong

- **Behind a tunnel, the address answers "404 page not found".** The tunnel is pointing at something else. Its service URL
  must be `http://weft:8080` (with `compose.tunnel.yaml`), not `http://localhost:8080`.
- **The page does not load, or the browser warns about the certificate.** Look at what Caddy says:
  `docker compose logs caddy`. Lines about "obtaining certificate" that repeat mean it cannot finish. The usual causes are
  that the DNS record does not point at this machine yet (check with `dig +short weft.example.com`), that port 80 or 443 is
  blocked, or that Cloudflare's proxy is on. Let's Encrypt limits how often it will retry, so fix the cause and wait a few
  minutes rather than restarting again and again.
- **Sign-in works but likes and replies from others do not arrive.** Other servers must be able to open
  `https://weft.example.com/` from the internet. Test from another network (a phone on mobile data).
- **The server will not start.** `docker compose logs weft` says why (for example a reserved account name in `WEFT_USER`).

## Notes

- The temporary password is written to the container's log. Choosing your own at the first sign-in replaces it; do that
  before you share the address.
- Port 443 over UDP is not used (Caddy is set to HTTP/1.1 and HTTP/2), so only TCP 80 and 443 need to be open.
- `-trust-proxy` is on because only Caddy can reach the server. Do not publish port 8080 yourself.
- Weft is free software under the AGPL (see `LICENSE` in the image and the repository). Running it as it is, for yourself or
  for others, asks nothing more of you. If you change it and run the changed version for other people, offer them your source.
