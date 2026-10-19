# Production optimizations

Things we left out on purpose so the design stays simple while it is built. Revisit these when we get
close to production, and measure first: each item says what to look at before changing anything.

**Action item:** before the first production deployment, go through this list, measure the cases below
on a realistic dataset (`make hackernews` gives a deep, cold thread), and decide which items to build.

## 1. Fetch cache for thread pages

**Problem.** `/thread` is rebuilt on every click and the fetch cache lives for one page build only.
Clicking "replies →" makes that reply the new focus, so the server reads the whole chain from the
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
