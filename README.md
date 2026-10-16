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

Replying: on the Timeline click "Reply" under a post, or use the API with `in_reply_to`
(and optionally `in_reply_to_feed`). The reply is published in your own feed and a Webmention is
sent to the parent's site. Replies you receive from people you follow are approved automatically;
others wait on the Replies page (or `GET /api/replies?status=pending`) until you approve them.
You can reject an approved reply later.

Reacting: under each timeline post, 👍 or 👎 sets the polarity (`+` / `-`) and the optional emoji
box adds nuance. API: `POST /api/reactions {"target","polarity","emoji"}`, `DELETE /api/reactions {"target"}`.
Reactions are published in your reactions feed and counted at the post only when the sender is
someone the post's author follows; `GET /api/reactions/received?post_id=N` shows all of them.

Threads: "Thread" under any post opens the conversation it belongs to, rooted at the original post.
The **curated** view shows only what each author approved, read down from the root through the
replies feeds. The **raw** view shows everything your reader knows, including replies that were not
approved. Replies whose parent your reader does not have are placed by the spec's rules: a deleted
parent (410) leaves a tombstone, an offline parent leaves the reply under the root, and a live but
unknown parent gets a placeholder. When the replies under one of your replies change, a Webmention
is re-sent up the chain so each ancestor's cached second level stays fresh.

To try two separate instances on one machine:

```bash
./bin/ttc user add -db a.db alice && ./bin/ttc user add -db b.db bob   # each prints a token
./bin/ttc serve -addr :8101 -db a.db -dev &   # http://alice.localhost:8101/
./bin/ttc serve -addr :8102 -db b.db -dev &   # http://bob.localhost:8102/
```

The server fetches the URLs users enter, so by default it refuses loopback and private addresses
(SSRF guard). `make run` passes `-dev` to allow them for local use. Do not use `-dev` when hosting.
If you run the binary yourself: `./bin/ttc serve -dev`.

`make run VERBOSE=1` (or `ttc serve -v`) logs every API call and inbound Webmention: host, method, path, status, time, and the start of the request body. Tokens are never logged.

`make follow NAME=dana TO='alice henry'` makes a user follow others (bare names on this instance, or feed URLs); the target servers must be running, since the feed is fetched.

`make help` lists the other targets, including `token` to issue a fresh API token.
Settings: `DB` (default `ttc.db`), `ADDR` (`:8080`), `DOMAIN` (`localhost`).

The build is a single static binary (`CGO_ENABLED=0`). Dependencies: `goldmark`, `modernc.org/sqlite`.

## Post addresses and identity

A post has one address and one identity, kept apart so a post can move between instances.
- **Address:** `<link>` is `https://<user host>/posts/<slug>`, for example `/posts/the-reader-half-k3x9`. The slug comes from the title (or the first sentence of an untitled note) plus a random 4-character suffix. It is unique per user, never changes (even when the post is edited), and stays reserved after the post is deleted. It is the only URL that serves the post.
- **Identity:** `<guid isPermaLink="false">urn:uuid:…</guid>` is a UUIDv7 made when the post is created. It is not a URL and travels with the post.
- Reactions still have instance-local addresses (`/reactions/<n>`); they will get the same treatment later.

## AI agent

`ttc-agent` is a separate program that participates as one user, through the public REST API and that user's
bearer token only. It reacts to and replies to posts on its timeline, answers replies to its own posts once
each, and can write an occasional note. Before answering it reads the branch around the post (`GET /api/thread?url=`) and gives the model the conversation, and it replies at most once per thread per cycle; if the thread cannot be read it waits for the next cycle. A lifetime budget (`-max`, default 20) stops two bots from talking forever.

```bash
make token NAME=dana                        # prints the token
# put ANTHROPIC_API_KEY=... in .env (read from the current directory), or export it
./bin/ttc-agent -base http://dana.localhost:8080 -token TOKEN -persona Dana-bot -post-every 10
./bin/ttc-agent -base ... -token ... -brain stub -once    # deterministic, no API key needed
```

`-style "dry and skeptical"` gives each bot its own voice (default: friendly), and `-persona` is the name it writes as. Use a separate `-state` file for each bot.

The model defaults to `claude-haiku-5-5` (`-model`). Posts the agent reads are passed to the model as untrusted
content. The `e2e/` test runs three instances and four users, one of them the stub agent: `go test ./e2e/`.

## Reading a thread

`/thread?url=<post>` (and `GET /api/thread?url=`) shows the **branch** around a post by default: the chain of posts from the root down to it, the siblings at every level, and its own replies. Other branches stay closed, each with a reply count (or just "replies" when the count is unknown) and a link that makes it the new focus. It costs about one fetch per level of depth, however large the whole thread is. `view=curated` shows the whole approved tree (up to a fetch limit), and `view=raw` shows everything this reader knows, approved or not. In the JSON, `focus` marks the requested post, `on_path` the chain to it, and `replies` the count on a closed branch (-1 unknown).

`replies.xml` items carry `tc:replies` (href and the count cached by the host) for the replies that have their own replies feed, so a closed branch can show its size without being opened.

A post's permalink answers to its `Accept` header: `text/markdown` returns the source, and `application/rss+xml` (without `text/html` or `*/*`) returns the post as a one-item feed, with all its `tc:` links. A feed holds only an author's newest 100 posts, so readers use the one-item form to find older posts, such as the ancestors of a reply deep in a long conversation.
