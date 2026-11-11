# Production optimizations

Things we left out on purpose so the design stays simple while it is built. Revisit these when we get
close to production, and measure first: each item says what to look at before changing anything.

**Action item:** before the first production deployment, go through this list, measure the cases below
on a realistic dataset (`make hackernews` gives a deep, cold thread), and decide which items to build.

## 1. Fetch cache for thread pages

**Problem.** `/thread` is rebuilt on every click and the fetch cache lives for one page build only.
Clicking the replies icon makes that reply the new focus, so the server reads the whole chain from the
root again: about depth+1 fetches. For a thread of strangers (nothing in the local store) that is the
full depth every click. The browser cannot do the fetching instead, because other servers do not allow
cross-origin requests; every remote fetch goes through the user's own server.

**Idea.** Keep recently fetched posts (the one-item permalink feeds read by `fetchItem`) in memory for
a short time, so drilling into a reply only fetches its replies: about 1 fetch per click.

**What we learned from a first attempt (removed).**
- Cache posts only. Lists of replies (`replies.xml`) are what changes: caching them hid new replies
  from the agent (`TestRoundTripWithAgent` failed), so always read them fresh.
- Do not cache failures; a flaky server should be asked again.
- A 90 second TTL and 512 entries, cleared when full, was enough; edited posts then show old text for
  up to a TTL.
- The cache was a mutex-guarded map on `Server`, consulted in `fetchItem` before the network.

**Better options to weigh then.** Persist fetched posts in SQLite with an expiry so restarts and
several instances share them; honour `ETag` and `Last-Modified` and send conditional requests; add
`Cache-Control` to our own permalink and `replies.xml` responses so a CDN or reverse proxy does the work;
or expand a branch in place (a small JS call to our own server) so the page is not rebuilt.

**Measure first.** Fetches per click and p95 page time on a cold thread of depth 8+, and how often the
same ancestors are read within a minute.

**Measured (10 November 2026, `make hackernews` data, 877 comments, depth up to 8).** On a copy of the data served on
another port, signed in as `me`, with every comment opened once as a cold thread click:

| Depth of the comment | Comments | Fetches per click (median / most) | Page time here (median / p95) |
|---|---|---|---|
| 1 | 220 | 3 / 3 | 6 / 10 ms |
| 2 | 269 | 3 / 4 | 8 / 11 ms |
| 3 | 180 | 4 / 5 | 9 / 13 ms |
| 4 | 97 | 5 / 6 | 10 / 13 ms |
| 5 | 58 | 6 / 7 | 12 / 15 ms |
| 6 to 8 | 32 | 7 to 9 / 9 | 11 to 15 ms |

A click costs about depth + 2 fetches. The times are tiny only because every "remote" server is the same
machine: the fetches are one after another, so on the web a click costs roughly fetches times the round trip
(estimate: 3 fetches at 150 ms is half a second, 9 fetches over a second). Drilling down one chain from the top to
the deepest reply, 8 clicks: 3, 4, 5, 6, 7, 8, 9 and 9 fetches, 51 in all, **35 of them (69%) for posts the
previous clicks had already read.** A cache of posts for a minute or two would cut that to about 1 to 2 fetches per
click. The budget (`maxThreadFetches` 24) was never reached at depth 8.

**Built (10 November 2026).** `internal/web/itemcache.go`: the bodies of permalink answers are kept in memory for 60
seconds (512 entries, cleared when full) and parsed again at every use, so no page can change what another sees. Only
answers that parsed are kept; reply lists are never kept. A post edited or deleted on this server is dropped from the
cache at once (`update` and `deletePost`), so readers on this server see the change immediately; a post on another
server may show old text for up to a minute. Kept posts do not count against the 24-fetch budget. Same 8-click
drill-down as above, with the cache: **16 fetches instead of 51** (3 for the first click, then 2 a click, 1 for the
last), none of them repeats. Tests: `itemcache_test.go` (expiry, size, and the behaviour between two users: kept,
edit shown at once, change elsewhere shown after the time, failures not kept, delete asks again; both drops were
checked by removing them).

## 2. Other candidates

Not worked on; each needs a measurement before a decision.

- **Parallel fetches.** The ancestor walk is sequential because each post names only its parent. The
  replies reads for a branch could run concurrently with it.
- **Poller load.** `RefreshAll` fetches every followed feed on one timer. Use conditional requests,
  per-host spacing and backoff for feeds that fail or never change.
- **Rate limits** on public endpoints and the Webmention receiver; limits on outgoing fetches per
  thread view (`maxThreadFetches` is 24 today) and per host.
- **Paged feed size.** `feedPageSize` is 100 items; check payload size and parse time with real content.
- **SQLite.** WAL is on. Check indexes against the slow queries (timeline, thread, replies) at 100k+
  rows, and plan backups (`.backup`, not a file copy).
- **Rendering.** Markdown is rendered per request; cache the HTML per post version if it shows up in
  profiles.
- **Static binary under load.** Check connection limits, timeouts and graceful shutdown.

## Measurements of the other candidates (10 November 2026)

Local machine, Go 1.2x, no network; the numbers are about the work the server does, not about latency.

**Paged feed size.** 100 posts of real Hacker News comments (average 264 characters, longest 2476): the page is
**144 KB, 24 KB gzipped**; the server builds it in about 3 ms; reading it (`feed.Parse`) takes **3 ms** and allocates
1.3 MB. At 100 items the page is fine. Under load, 100 simultaneous clients on that feed get 466 requests a second
(about 2 ms of CPU each, most of it rendering and compressing); the profile page is the same. Behind Cloudflare or a
reverse proxy that answers conditional requests this costs little. Caching the compressed page per version of the
feed would raise it, if it ever shows up.

**SQLite at scale** (300 feeds with 1000 cached items each, 300,000 rows and 581 MB in `instance.db`; one author with
100,000 posts and 100,000 replies in a spool file; median of 15 runs):

| Query | Before | With an index |
|---|---|---|
| timeline, 50 items, 20 / 100 / 500 followed feeds | 45 / 213 / 630 ms | no change |
| thread items by `root` (`ItemsInThread`), 20 / 100 / 500 feeds | 28 / 130 / 1290 ms | **0.3 / 1.0 / 2.4 ms** with `items(root)` (measured with a second index on `guid`, which turned out not to be used: the primary key serves it) |
| item by permalink | 0.2 to 1.1 ms | (already uses the primary key) |
| own posts in a thread (`PostsInThread`) | 100 ms | **0.15 ms** with `posts(user_id, root)` |
| own posts, first page of 100 | 0.6 ms | |
| own posts feed page 500 / 1000 (offset 50,000 / 99,900) | 150 / 295 ms | offset paging is linear |
| replies of one post | 0.03 ms | (the unique index on `post_id, source`) |

- The timeline reads every cached item of every followed feed and sorts them, about **2 microseconds a row**: 20 feeds
  of a typical 100 items each is a few ms, 500 feeds of 1000 items is more than half a second. A cache that keeps a
  feed's newest page (about 100 items) is the normal case. Two indexes do not help (`items(feed_url, published)` was
  tried): the fix would be to read the newest 50 of each feed from that index and merge, or to cap the items kept per feed.
- Thread items by `root` and own posts by `root` scan everything now. The two indexes above (`items_root`, `posts_root`, added by
  migrations `instanceV15` and `spaceV4`, which also run `ANALYZE` on the table, because without statistics SQLite
  keeps using `posts_user`) make them instantaneous; they cost some disk and a little time at every `SaveFeed`.
- Backups use `VACUUM INTO` (a consistent snapshot of every file, the live files never copied) and were restored
  and checked on the VPS.

**Static binary under load** (`ab`, signed in where needed):

| Case | Concurrent | Requests/s | p95 | Failed |
|---|---|---|---|---|
| one post page | 100 | 3,865 | 41 ms | 0 |
| one post page | **1000** | 3,850 | 416 ms | 0 |
| profile page | 100 | 494 | 250 ms | 0 |
| feed.xml, 144 KB | 100 | 466 | 263 ms | 0 |
| thread page, signed in, depth 8 | 20 | 753 | 36 ms | 0 |

Memory stays small (93 MB after the runs, 137 MB with 1000 connections). A client that sends part of its headers and
stops is dropped after exactly 10 seconds (`ReadHeaderTimeout`). SIGTERM during a load of 100 connections: the
process logged "finishing requests in progress" and "database closed" and exited **0.25 s** later; no request was cut
off (the load generator stopped on "connection refused", which is what a closed listener gives). The limit on open files
is already checked at start.
