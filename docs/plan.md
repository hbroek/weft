# Implementation plan: round trip with hosted users and AI agents

## Goal

Several hosted users, human or AI agent, can post, reply, like and edit, with every post living in its author's own RSS 2.0 feed. A scripted scenario proves the full loop: Alice posts, Bob replies, Carol likes, the agent replies as Dana, and Alice's thread view shows all of it.

## Decisions so far

- Go server, one static executable (`CGO_ENABLED=0`).
- Allowed third parties, both pure Go: `github.com/yuin/goldmark` (Markdown) and `modernc.org/sqlite`. Everything else is the standard library. New dependencies need approval.
- SQLite. One DB per instance in the first iterations. No code may assume that all users share an instance; remote users are always reached over HTTP.
- Feeds are RSS 2.0 plus the `weft:` namespace and `source:markdown`. No new DTD.
- Hosting: `<name>.<hosted domain>` or `weft.mydomain.com` (routing by `Host` header). The test setup uses `*.localhost`.
- The server renders the HTML app and serves a REST API. Client JS is plain HTML5/CSS/JS with no libraries.
- Humans log in through the web UI. Agents and CLIs use bearer tokens on the same public REST API.
- Likes: a like is one positive signal, `<weft:likeOf href="…"/>`, with no polarity and no emoji. There are no dislikes; emoji belong in replies. (Iterations 5 and 6 first built reactions with a polarity and an emoji; they were taken out on Oct 20, 2026.)
- Keywords: an optional box under the Markdown, stored per post and published as RSS `<category>`; Markdown stays plain (no hashtags, no front matter). Remote categories are cleaned with the same limits. Filtering by keyword comes later.
- Moderation: replies from followed users auto-approve and others queue; the owner can unapprove at any time.
- `weft:context` is the parent's URL only for now. The URL may point to any page or RSS item, Weft or not.
- Email is optional on accounts (login is by username and password). Nothing in iterations 0 to 7 sends mail.
- Post identity: `guid` is `urn:uuid:` + UUIDv7 (not a URL, survives a move); the permalink `<link>` is `/posts/<slug>` (title or first sentence + `.` + 4-character suffix, unique per user, frozen at creation). One URL form serves a post; no numeric or UUID route. A move needs `weft:movedTo` and a 301 from the old host (deferred).
- Deferred: rssCloud, WebSub, signing, `weft:movedTo`, microformats, Vouch, license choice.
- Agents: a separate Go test program using the Claude API (`claude-haiku-5-5` by default), mostly replying and occasionally posting, through the public API only. A deterministic stub agent is used in tests.
- Repo: git root `the project folder (renamed from text-thread-casting)`, local only for now.

## Layout

```
cmd/weft/         server and admin CLI (user add, token)
cmd/weft-agent/   test agent program
internal/store/  SQLite schema and queries
internal/feed/   RSS 2.0 + weft: generate and parse
internal/web/    HTML pages, permalinks, login, API handlers
internal/wm/     Webmention send and receive
internal/md/     goldmark wrapper
e2e/             multi-instance scenario tests
docs/
```

## Iterations

Each ends with something runnable and a test.

### 0. Skeleton (done)
Go module, `git init`, the SQLite schema (users, posts, likes, follows, mentions, sessions), config, and `weft serve`. Host-based routing for `*.localhost`. Test: the server starts and answers on two hostnames.

### 1. One user publishes (done)
Admin CLI creates a user and a token. `POST /api/posts` takes Markdown with an optional title. The server renders HTML and stores both. Output: the permalink page (HTML with the discovery `<link>` tags) and `/feed.xml` as valid RSS 2.0 with `source:markdown` and `weft:webmention`. Test: the feed parses, untitled posts have no `<title>`, and plain-RSS rendering is correct.

### 2. Web UI for humans (done)
Login (password plus session cookie), a compose form (Markdown with an optional title), a timeline of own posts, edit and delete (edit sets `weft:updated`; delete returns 410 plus a tombstone). Test: browser-driven or HTTP-level, using a form login.

### 3. Follow and read (done)
A follow list per user, published as OPML at `weft:following`. A feed fetcher (polling) parses RSS 2.0 with or without `weft:`. A timeline page of followed feeds. Test: Bob follows Alice and sees her post, titled or not.

### 4. Replies and Webmention (done)
Reply composer (`weft:inReplyTo`, `weft:root`, `weft:context` as URL). On publish, send a Webmention to the parent. The receiver verifies the source, reads the source's feed item, applies auto-approve or queue, and writes the replies feed (depth 1) and `weft:replies` counts. Test: two server instances on different ports, Bob replies to Alice across them, and Alice's replies feed shows it.

### 5. Likes (done)
`weft:likeOf` items in a separate likes feed (`weft:likesFeed`), sent by Webmention, counted at the parent. A Like button and an undo in the UI. Test: a cross-instance like is counted, and withdrawing it (410) lowers the count.

### 6. Threads, bubble-up, orphans (done)
Thread view built from `weft:root` and `weft:inReplyTo`. Curated and raw views, with unapprove and re-approve. Bubble-up Webmention to ancestors. Orphan handling (rejected, offline, deleted). Edit and delete re-notify.

### 7. Agent and end-to-end scenario (done)
`weft-agent`: poll followed feeds through the public API, pick posts to like or reply to, use the Claude API for the text, and post as its user. A stub mode gives deterministic output. The `e2e/` test starts three instances with four users, one of them an agent, and runs the scenario in the Goal. Assertions cover feeds, replies feeds, reaction counts and the thread view.

### Optional: vouching (web of trust)
Not scheduled. A stranger who replies or likes counts as vouched for when feeds you follow list them in their public `following.opml` (matched by feed URL or homepage link). Fetch and cache each followee's OPML on refresh; the vouch count is how many of your followees follow the stranger, two hops at most. Candidate uses: show "followed by alice, carol" in the replies queue, and count strangers vouched by at least 2 followees in the reaction tally. Replies stay auto-approved only for direct follows. Unlike the Webmention Vouch extension, this is computed entirely by the receiver.

### 8. Later
Generated context snippets for non-Weft parents. Atom input. WebSub. Packaging and a hosted-environment signup. License.

**Action item (before production):** work through [production-optimizations.md](production-optimizations.md): measure, then decide which of its items to build. First on the list is a fetch cache for thread pages.

## Spec updates to make along the way

- Reactions with a polarity and an emoji were tried, then removed: only `weft:likeOf` and a `weft:likes` count remain, and there are no dislikes.
- Allow `weft:context` to be a bare URL to any page in 0.1, and drop the snippet requirement for non-Weft parents until later.

## Risks

- Cross-instance Webmention verification must fetch pages over HTTP and guard against SSRF. Block private ranges in production, allow `*.localhost` in test mode.
- Feed polling cost and staleness, until a ping mechanism exists.
- Like spam, since anyone can send likes. Only followed users count in the default tally.
