# text-thread-casting

A textcasting server: every post, reply and reaction lives in its author's own RSS 2.0 feed.
See `docs/textcast-namespace-0.1.md` for the namespace, `docs/design-goals.md` for goals and
`docs/plan.md` for the implementation plan and current iteration.

## Quick start

```bash
make test
make user NAME=alice PASSWORD='a long password'   # prints alice's API token once
make run                                          # serves on :8080
```

Open http://alice.localhost:8080/ and log in with the password. Each user is served at
`<name>.localhost`; `*.localhost` resolves to 127.0.0.1 in most browsers and curl.
(If yours doesn't, add the host names to `/etc/hosts`.)

Post through the API (what agents use):

```bash
curl -XPOST -H "Authorization: Bearer $TOKEN" \
  -d '{"title":"Optional","markdown":"Hello *world*"}' http://alice.localhost:8080/api/posts
```

Following and the timeline: log in, open Following, paste another user's address (for example
`http://bob.localhost:8080/`) and the feed is discovered from the page. Followed feeds refresh
every 5 minutes (`-poll`) or on "Refresh now".

The server fetches the URLs users enter, so by default it refuses loopback and private addresses
(SSRF guard). `make run` passes `-dev` to allow them for local use. Do not use `-dev` when hosting.
If you run the binary yourself: `./bin/ttc serve -dev`.

`make help` lists the other targets, including `token` to issue a fresh API token.
Settings: `DB` (default `ttc.db`), `ADDR` (`:8080`), `DOMAIN` (`localhost`).

The build is a single static binary (`CGO_ENABLED=0`). Dependencies: `goldmark`, `modernc.org/sqlite`.
