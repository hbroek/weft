# Implementation plan: round trip with hosted users and AI agents

## Goal

Several hosted users, human or AI agent, can post, reply, like and edit, with every post living in its author's own RSS 2.0 feed. A scripted scenario proves the full loop: Alice posts, Bob replies, Carol likes, the agent replies as Dana, and Alice's thread view shows all of it.

## Decisions so far

- Go server, one static executable (`CGO_ENABLED=0`).
- Allowed third parties, both pure Go: `github.com/yuin/goldmark` (Markdown) and `modernc.org/sqlite`. Everything else is the standard library. New dependencies need approval.
- SQLite, one database per user (`users/<2hex>/<name>.db`) plus a shared `instance.db` for the directory and the feed cache: see `per-user-databases.md`. Each file has its own migration list. No code assumes that all users share an instance; remote users are always reached over HTTP.
- Feeds are RSS 2.0 plus the `weft:` namespace and `source:markdown`. No new DTD.
- Hosting: `<name>.<hosted domain>`, or a custom domain a user proves with a TXT record (`_weft.<domain>`) and that reaches the server (two steps: verified, then active). Routing is by `Host` header. The test setup uses `*.localhost`.
- The server renders the HTML app and serves a REST API. Client JS is plain HTML5/CSS/JS with no libraries.
- Humans log in through the web UI. Agents and CLIs use bearer tokens on the same public REST API.
- Likes: a like is one positive signal, `<weft:likeOf href="…"/>`, with no polarity and no emoji. There are no dislikes; emoji belong in replies. (Iterations 5 and 6 first built reactions with a polarity and an emoji; they were taken out on Oct 20, 2026.)
- Markdown: no tables, task lists or footnotes, so the rendered HTML in a feed stays readable by any reader. Only Linkify and Strikethrough are added to CommonMark. (Decided Oct 24, 2026.)
- Keywords: an optional box under the Markdown, stored per post and published as RSS `<category>`; Markdown stays plain (no hashtags, no front matter). Remote categories are cleaned with the same limits. Filtering by keyword comes later.
- Moderation: replies from followed users auto-approve and others queue; the owner can unapprove at any time.
- `weft:context` is the parent's URL only for now. The URL may point to any page or RSS item, Weft or not.
- Email is optional on accounts (login is by username and password). Nothing sends mail. A password set from the command line is temporary: the user must choose their own at the first sign-in (`weft user add|passwd`, a generated `xxxxx-xxxxx` when none is given, `-permanent` to skip).
- Post identity: `guid` is `urn:uuid:` + UUIDv7 (not a URL, survives a move); the permalink `<link>` is `/posts/<slug>` (words the author chose or the title or first sentence, + `.` + a 4-character suffix that is always added; unique per user, frozen at creation, never edited). One URL form serves a post. A move (`weft move`) answers the old address with permanent redirects, and followers of a feed that moved follow it to the new address.
- Likes: every like received counts, from anyone (verified by Webmention). Each post also publishes a liked-by feed (`<post>/likes.xml`, advertised by `weft:likes href`), so readers can show who liked it and offer to follow the people they do not follow yet.
- Limits: server timeouts, login throttling, per-address rate limits on the API, follows and Webmentions, and caps on the Webmention queue (see the README). Public page reads are left to a proxy or CDN.
- Polling: followed feeds are fetched with `ETag` / `Last-Modified`; a failing feed backs off from 15 minutes up to a day and honours `Retry-After`.
- Backups: `weft backup` (consistent snapshots) with restic for the offsite copy.
- Deferred: rssCloud, WebSub, signing, Vouch, license choice.
- Agents: a separate Go test program using the Claude API (`claude-haiku-5-5` by default), mostly replying and occasionally posting, through the public API only. A deterministic stub agent is used in tests.
- Repo: github.com/hbroek/weft, branch `main`.

## Layout

```
cmd/weft/         server and admin CLI (serve, user, move, backup, restore, fsck, reindex, split, export, import)
cmd/weft-agent/   test agent program
cmd/hn-import/    Hacker News import, for load tests
internal/store/  SQLite schema, migrations and queries (per-user files plus instance.db)
internal/feed/   RSS 2.0, Atom and RSS 1.0: generate and parse, with weft:
internal/fetch/  bounded, SSRF-guarded HTTP client with conditional requests
internal/web/    HTML pages, permalinks, login, API handlers, Webmention, rate limits
internal/agent/  the agent behind weft-agent
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

### 8. Beyond the plan (done)
Built after iteration 7, in roughly this order: one database per user; moving to a new address; custom domains; `weft backup` and restore; reply cards with context for non-Weft parents (`weft:context` carries title, description and image) and a Markdown cheatsheet in the editor; replies shown as nested comment cards under a post; likes and replies that return to the post on the timeline; a bookmarklet that finds a page's feeds; Atom and RSS 1.0 input; keyword filters; post addresses chosen at creation; temporary passwords; per-post liked-by feeds; rate limits and timeouts; conditional feed requests with backoff.

### 9. Owning your images (done)
Images are stored in the author's own database, so a post does not break when someone else's server goes away. Decided Oct 26, 2026:
- A lazily created `media` table in each user's file; images are addressed by their hash (`/media/<hash>.jpg`), so the same image is stored once and cached forever. Only png, jpeg, gif, webp and avif; no SVG.
- Upload from the editor (button, drag and drop, paste) and `POST /api/media`.
- The Markdown stores the path without a host; feeds and pages add the current host, so a move keeps every image working.
- "Keep a copy of the images from other sites" is offered on request, never automatic: the server fetches them with the usual size caps and guards, stores them and swaps the addresses. The original address is recorded.
- Quotas (per image and per user) are settings of the server, not fixed in the code. A server can set them high and charge for it; a free server can set them low. That is how servers tell themselves apart.
- Every upload is sanitized: all metadata is removed (location, camera, time, thumbnails, comments), and nothing can be switched off, since an option invites an accidental leak. The orientation of a photo is kept by turning the pixels. Files that do not decode, or are too many pixels, are refused.
- Two sizes, chosen per upload (and as the user's default): Original (the image as it is, metadata removed without recompressing) and Compact (longest side at most 1600 px, JPEG quality 80). The server sets the limits. Standard library only: jpeg, png and gif; webp and avif need a new dependency and wait.
- New user files use 4 KB pages again (1 KB pages were chosen to keep empty files small, which does not suit images). Files made before keep their 1 KB pages.

Built Oct 26, 2026: all of the above except webp and avif.

Timeline pages (Oct 27, 2026): 50 posts to a page with "Older posts"; a date cursor (`before`) rather than an offset, so new posts arriving do not shift the pages. Replies and threads are not folded.

### 10. Roadmap: automatic account transfers
Moving an account from one server to another without the operators doing it by hand. Planned Oct 27, 2026; not built. The case in mind is a person who outgrows a free server's quota and moves to a paid one (for example a "weftbig" server with large quotas), and back. Today the same result takes three steps by hand: export, import, `weft move` (see the README). The new pieces are the authorization code, the transfer between the two servers, and the safe order of the switch-over.

**The flow, from the user's side**
1. The user makes an account on the new server, with the same name as on the old one. The name is the identity: the old addresses redirect to the same place on the new server, so the name must match.
2. On the old server (Profile, "Move to another server") the user gives the password and the address of the new server, and receives a **move code**. It is shown once.
3. On the new server (Profile, "Move here from another server") the user enters the address of the old server and the code.
4. The new server fetches the account, checks it and replaces its own, empty account with it. The old server's addresses redirect to the new server, and the user sees a message on both.

**The code**
- 128 bits from the system's random source, written in groups for typing. The old server keeps only a hash of it, with the account name, the destination the user named, an expiry (one hour) and a count of failed attempts. It is single use, and a new request replaces the previous code.
- The user names the destination when asking for it. That fixes where the redirect will point, and the old server refuses a transfer asked for by any other address. This does not prove who the caller is (anybody can claim an address); the code does.
- The code travels in a request header, never in an address, and never into logs. Both servers require HTTPS, except in development mode.
- Requesting a code needs the password, and a recent sign-in is not enough. Codes are rate limited per account and per address.

**The transfer: separate calls on the old server**, which has one protected path (`/.well-known/weft/move/`) for them:
1. `prepare` (the code, the new server's address, the account name): the old server checks the code, **freezes** the account (no posts, replies, likes or settings change while it is frozen, so nothing is lost and the checksum stays true), makes one consistent copy with the existing export code, and answers with a manifest: export format, schema version, size, SHA-256 and the Weft version. The copy is kept until the transfer ends.
2. The new server checks the manifest before downloading anything: format and schema version not newer than it knows (the existing import check), size within its quotas and `MaxImportBytes`, the name matches its own account and the account is still unused (no posts, no followers). If any check fails it calls `abort` and the old server unfreezes.
3. `data`: the copy as a stream, with `Range` so a broken download resumes. The new server computes the SHA-256 while it writes to a temporary file and compares it with the manifest. A second call for the checksum is not needed: it is in the manifest, which cannot change because the account is frozen.
4. The new server runs the same checks as `weft import` (integrity, no triggers or views, one user, the right name) on the temporary file. Failing, it calls `abort`.
5. `received` (checksum matched, file valid): from here the old server treats the account as moved pending. It no longer unfreezes by itself; only the user can cancel it (`weft move -cancel`, or the profile page).
6. The new server swaps the files: the staged file replaces its account in one rename. The new account keeps its own password and API token, so the user stays signed in; the old password is not carried over. Sessions of the old server are not carried over.
7. `done`: the old server sets the account to moved, with the redirect to the new server and the name reserved for good. The existing machinery then takes over: its file goes to `trash/` and is deleted after 30 days, and `weft move -cancel` brings it back until then.

If a step before 5 fails, the old server unfreezes (an expired `prepare` also unfreezes) and nothing changed on either side. If the new server fails after 5 and before 6, the staged file waits, the user retries on the new server, and the old account stays frozen: it never has two live copies. If `done` cannot be delivered, the new server retries it in the background and the page tells the user, who can also press "Finish the move" on the old server.

**What is not carried over**
- The custom domain. The user points it at the new server and claims it there, as for any new domain (the TXT record is new, since its token belongs to the account on that server).
- Remote sessions, the API token, queued Webmentions and the feed cache (rebuilt by polling).
- The old password. The user keeps the one they chose on the new server.

**What changes for others**
- Addresses of posts, feeds, pictures and the likes feed: only the host part, and all addresses stored in the file are relative or take the current host, so they resolve on the new server. The old host answers with 301/308 redirects, which readers and Webmention senders already follow (see the tests of a reader following a moved feed).
- Guids are `urn:uuid:` so a post keeps its identity. Followers' readers see the same items on the new feed address.
- Likes and replies already stored under the old host keep their old source addresses; those still verify through the redirect.

**Build order**
1. Store: freeze and unfreeze, prepare-copy keeping, move code table in `instance.db` (hash, destination, expiry, attempts, state), staged import with a given file and expected checksum, replacing an unused account.
2. Old server: the profile page and the `/.well-known/weft/move/` calls, the freeze check in every write path, and `weft move` / `weft move -cancel` showing the new states.
3. New server: the profile page and the client of those calls (using the same guarded HTTP client as feed fetching, so private addresses are refused outside development mode).
4. Tests: two servers in one test, the whole flow; every failure point (bad code, expired, reused, wrong destination, newer schema, checksum mismatch, a broken download and its resume, `abort`, a crash between 5 and 7) leaves one live copy; a hostile old server (a huge file, a manifest that lies, a zip with extra files).
5. README section and a changelog line.

**Open questions**
- Same name only, or may the user rename while moving? Renaming changes every address, so the first version refuses it.
- Whether the new account must be empty. The plan says yes: it protects a user from moving over an account they are using.
- Should the code also be sent by email to a verified address (see iteration 11), as a second channel? Optional, and only after email exists.

### 11. Roadmap: email, as a plug-in
Planned Oct 27, 2026; not built. Weft does not send email itself (no mail server, no vendor built in). It defines the one thing it needs and lets the operator choose how it is done.
- A `Mailer` interface with a single call, send a message (to, subject, text, and an optional HTML part). The server is configured with one mailer, or none. With none, everything that would send an email falls back to what works today: the operator hands out a temporary password, and the move code is shown on the screen.
- Providers, each small and separate: `smtp` (host, port, user, password from the environment, standard library only); `command` (the message goes to the standard input of a program the operator names, such as `sendmail -t`, which gives every other provider without a new dependency); `http` (a configured endpoint and header for services with a plain JSON API, such as Postmark, Resend or Mailgun). `cloudflare` is a ready-made setting of the `http` provider for servers whose domain is on Cloudflare: it posts `{to, from, subject, text, html}` as JSON to `https://api.cloudflare.com/client/v4/accounts/<account id>/email/sending/send` with a bearer API token, and reads `result.delivered`, `result.queued` and `result.permanent_bounces` from the answer (a permanent bounce marks the address as bad, so the server stops mailing it). The operator onboards the domain once in the Cloudflare dashboard (Email Service, Email Sending), which adds SPF, DKIM, DMARC and bounce records under `cf-bounce`; the domain has to use Cloudflare DNS. Mail is sent from an address on the operator's domain (for example `no-reply@weftseed.com`), never from a user's own domain. The account id and the token (a token limited to sending) come from the environment, not from a file in the repository. Checked against Cloudflare's [send-email guide](https://developers.cloudflare.com/email-service/get-started/send-emails/) on Oct 27, 2026, which does not say what plans it needs, its sending limits, or whether recipients are restricted; look those up before building, and treat the request format as one to re-check, since the service is new. Nothing is added to the module unless a provider cannot be done without it.
- Email stays optional on an account. An address is used only after it is **verified**: a message with a one-time link to the address, hashed token, one hour. Changing the address verifies the new one and tells the old one.
- First uses: password reset (a one-time link; the answer is the same whether the address exists or not; rate limited per address and per client; the link signs out other sessions on use) and the move code (iteration 10) as a second channel.
- Later uses, each a setting the user turns on, off by default: a note for a reply or mention on your posts, a note for a new follower, and a daily digest instead of single notes. Each message has a one-click way to stop that kind of mail. A queue (the Webmention queue's pattern) with a cap and retries, so a slow provider never slows a request.
- Not planned: receiving mail, posting by email, and any tracking pixels or links.

### 12. Later
WebSub or rssCloud. Packaging and a hosted-environment signup (with email, iteration 11). License. A cache for thread-page fetches. Rate limiting of public page reads, if not left to a proxy.

**Action item (before production):** work through [production-optimizations.md](production-optimizations.md): measure, then decide which of its items to build. First on the list is a fetch cache for thread pages.

## Spec updates to make along the way

- Reactions with a polarity and an emoji were tried, then removed: only `weft:likeOf` and a `weft:likes` count remain, and there are no dislikes. Done.
- `weft:context` may be a bare URL to any page, with optional title, description and image. Done.
- `weft:likes href` (the liked-by feed) is in the namespace document. Done.

## Risks

- Cross-instance Webmention verification must fetch pages over HTTP and guard against SSRF. Block private ranges in production, allow `*.localhost` in test mode.
- Feed polling cost and staleness, until a ping mechanism exists. Conditional requests and backoff keep the cost down.
- Like spam, since anyone can send likes and every verified like now counts. The Webmention rate limit and queue caps bound the volume; a stricter count (for example only followed senders) can come back if it is abused.
