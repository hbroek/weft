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
- Sign-up (planned, iteration 14): the hosted service accepts only people who control a domain of their own, proved with the existing TXT record, plus the operator. The landing page is `www`, the operator's account lives on `admin`, and the number of accounts is capped (1000 to start). A user's database is called a **spool**; the server is the **loom** (always say "loom (server)" or "server" beside it where people first meet the word).
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

### 10. Automatic account transfers (built)
Built Oct 27, 2026, as planned below, with these differences: change detection is the fingerprint described in step 5 (a byte checksum of the zip cannot be compared, as it holds the time it was made); the move code is shown on the screen, since there is no email yet (iteration 11); the new server's progress is kept in memory, so a restart of the new server during a move means starting it again (the old server releases the account when its hold runs out, after 30 minutes, and `weft move -cancel` does it at once); a move to a different name is refused. Manual test: `heavy` (a 208 MB account) from one server to another on localhost.

Moving an account from one server to another without the operators doing it by hand. Planned Oct 27, 2026; not built. The case in mind is a person who outgrows a free server's quota and moves to a paid one (for example a "weftbig" server with large quotas), and back. Today the same result takes three steps by hand: export, import, `weft move` (see the README). The new pieces are the authorization code, the transfer between the two servers, and the safe order of the switch-over.

**The flow, from the user's side**
1. The user makes an account on the new server, with the same name as on the old one. Every user's address has the server in it (`https://myname.weftseed.com`, and after the move `https://myname.weftbig.com`), so the move changes the host and nothing else; the name is the part that must stay the same, and what the old server's redirect is built from.
2. On the old server (Profile, "Move to another server") the user gives the password and the new server (`weftbig.com`), and receives a **move code**. Without email on the server it is shown once on the screen; with email it is sent to the account's verified address instead (see "The code"). Either way it comes with the address the account will have: `https://myname.weftbig.com`.
3. On the new server (Profile, "Move here from another server") the user enters the address of the old server and the code.
4. The new server fetches the account, checks it and replaces its own, empty account with it. The old server's addresses redirect to the new server, and the user sees a message on both.

**The code**
- 128 bits from the system's random source, written in groups for typing. The old server keeps only a hash of it, with the account name, the destination the user named, an expiry (one hour) and a count of failed attempts. It is single use, and a new request replaces the previous code.
- The user names the destination server when asking for it. That fixes where the redirect will point: the old server builds the new address from it (`https://<name>.<new server>`), and in `prepare` the new server states the address it will give the account, which has to be the same or the old server refuses. This does not prove who the caller is (anybody can claim an address); the code does.
- The code travels in a request header, never in an address, and never into logs. Both servers require HTTPS, except in development mode.
- **Once the server can send email (iteration 11), the code is sent only by email, to the account's verified address, and never shown on the screen.** Somebody who has stolen a session or the password then cannot take the account away to another server: they would also need the mailbox. A server with a mailer does not issue a code to an account without a verified address; the user adds and verifies one first. (A server without a mailer keeps showing the code once, as described above, since nothing else is possible.)
- The address is the thing to protect, so changing it needs two emails: a link sent to the current verified address, and then a link sent to the new one. Somebody without the mailbox cannot change it. Adding a first address (when the account has none) can only be checked with the new one, so a code is not sent to an address until a day has passed since it was verified, and the page says so. Every request for a code and every address change sends a notice to the current address.
- Requesting a code needs the password, and a recent sign-in is not enough. Codes are rate limited per account and per address.

**The transfer: separate calls on the old server**, which has one protected path (`/.well-known/weft/move/`) for them. The account is locked for as short a time as possible: the long part, the download, happens while the user can still use the old site, and the lock covers only the last round trips.
1. `prepare` (the code, the new server's address, the account name): the old server checks the code and makes one consistent copy with the existing export code (nothing is locked). It answers with a manifest: export format, schema version, size, SHA-256 and the Weft version. From now on the old server **holds** incoming Webmentions, likes and replies: the endpoint still accepts them, but keeps them in a `held` table in `instance.db` instead of the user's own queue (which lives in the user's file and would change it), and the worker skips the account, since verifying a mention writes into the user's file. A sender gets the usual answer and nothing is refused or lost, and the user's file now changes only by the user's own actions.
2. The new server checks the manifest before downloading anything: format and schema version not newer than it knows (the existing import check), size within its quotas and `MaxImportBytes`, the name matches its own account and the account is empty (no posts, replies, likes, follows or pictures; decided Oct 27, 2026: the move replaces the account, so the user removes anything they made there beforehand, and the page says what is in the way). If any check fails it calls `abort` and the old server releases the held mentions.
3. `data`: the copy as a stream, with `Range` so a broken download resumes. The new server computes the SHA-256 while it writes to a temporary file and compares it with the manifest. This is the long step, and the old site keeps working during it; the page tells the user not to change anything while the move runs.
4. The new server runs the same checks as `weft import` (integrity, no triggers or views, one user, the right name) on the temporary file. Failing, it calls `abort`.
5. `lock` (the short window starts): the old server stops changes to the account (posts, replies, likes, settings) and takes a fresh copy. A fingerprint of the file (a hash of every row of every table except sessions and the API token, with each picture stood for by its name and size) is compared with the one in the manifest: if they are equal, nothing changed since step 1 and the staged file is the account. If it differs (the user posted during the download), the old server answers with the new manifest and the new server fetches again (steps 3 and 4) with the lock held; this is the slow case, rare because the user was told to wait, and `abort` lifts the lock if it takes too long (a few minutes).
6. `received` (checksum matched, file valid): from here the old server treats the account as moved pending. It no longer unlocks by itself; only the user can cancel it (`weft move -cancel`, or the profile page).
7. The new server swaps the files: the staged file replaces its account in one rename. The new account keeps its own password and API token, so the user stays signed in; the old password is not carried over. Sessions of the old server are not carried over.
8. `done`: the old server sets the account to moved, with the redirect to the new server and the name reserved for good. It sends the held mentions on: each one is posted again as a Webmention (same source, the target with its host changed to the new address) to the new server's endpoint, which Webmention allows any number of times, and the endpoint of the old address now redirects with a 308. The existing machinery then takes over: its file goes to `trash/` and is deleted after 30 days, and `weft move -cancel` brings it back until then.

If a step before 6 fails, the old server lifts the lock and releases the held mentions (an expired `prepare` does the same) and nothing changed on either side. If the new server fails after 6 and before 7, the staged file waits, the user retries on the new server, and the old account stays locked: it never has two live copies. If `done` cannot be delivered, the new server retries it in the background and the page tells the user, who can also press "Finish the move" on the old server.

**What is not carried over**
- The custom domain. The user points it at the new server and claims it there, as for any new domain (the TXT record is new, since its token belongs to the account on that server).
- Sessions, the API token and the feed cache (rebuilt by polling). Webmentions that arrived while the move ran are not dropped: they are sent on to the new server (step 8).
- The old password. The user keeps the one they chose on the new server.

**What changes for others**
- Addresses of posts, feeds, pictures and the likes feed: only the host part changes (`myname.weftseed.com` becomes `myname.weftbig.com`), and all addresses stored in the file are relative or take the current host, so they resolve on the new server. The old host answers with 301/308 redirects to the same path on the new host, which readers and Webmention senders already follow (see the tests of a reader following a moved feed). The redirect target is the user's exact new address, as `weft move` records it today.
- Guids are `urn:uuid:` so a post keeps its identity. Followers' readers see the same items on the new feed address.
- Likes and replies already stored under the old host keep their old source addresses; those still verify through the redirect.

**Build order**
1. Store: lock and unlock, held mentions and their forwarding at `done`, prepare-copy keeping, move code table in `instance.db` (hash, destination, expiry, attempts, state), staged import with a given file and expected checksum, replacing an unused account.
2. Old server: the profile page and the `/.well-known/weft/move/` calls, the lock check in every write path, and `weft move` / `weft move -cancel` showing the new states.
3. New server: the profile page and the client of those calls (using the same guarded HTTP client as feed fetching, so private addresses are refused outside development mode).
4. Tests: two servers in one test, the whole flow; every failure point (bad code, expired, reused, wrong destination, newer schema, checksum mismatch, a broken download and its resume, `abort`, a crash between 5 and 7) leaves one live copy; a hostile old server (a huge file, a manifest that lies, a zip with extra files).
5. README section and a changelog line.

**Open questions**
- Same name only, or may the user rename while moving? A rename changes the name part of every address as well as the host, and the old name would have to redirect to a different name, so the first version refuses it.
- A user with a custom domain: the domain keeps its address, but its server changes. The plan leaves it to the existing domain steps on the new server; whether the old server should keep serving the domain until then is not decided.

### 11. Roadmap: email, as a plug-in
Planned Oct 27, 2026; not built. Weft does not send email itself (no mail server, no vendor built in). It defines the one thing it needs and lets the operator choose how it is done.
- A `Mailer` interface with a single call, send a message (to, subject, text, and an optional HTML part). The server is configured with one mailer, or none. With none, everything that would send an email falls back to what works today: the operator hands out a temporary password, and the move code is shown on the screen.
- Providers, each small and separate: `smtp` (host, port, user, password from the environment, standard library only); `command` (the message goes to the standard input of a program the operator names, such as `sendmail -t`, which gives every other provider without a new dependency); `http` (a configured endpoint and header for services with a plain JSON API, such as Postmark, Resend or Mailgun). `cloudflare` is a ready-made setting of the `http` provider for servers whose domain is on Cloudflare: it posts `{to, from, subject, text, html}` as JSON to `https://api.cloudflare.com/client/v4/accounts/<account id>/email/sending/send` with a bearer API token, and reads `result.delivered`, `result.queued` and `result.permanent_bounces` from the answer (a permanent bounce marks the address as bad, so the server stops mailing it). The operator onboards the domain once in the Cloudflare dashboard (Email Service, Email Sending), which adds SPF, DKIM, DMARC and bounce records under `cf-bounce`; the domain has to use Cloudflare DNS. Mail is sent from an address on the operator's domain (for example `no-reply@weftseed.com`), never from a user's own domain. The account id and the token (a token limited to sending) come from the environment, not from a file in the repository. Checked against Cloudflare's [send-email guide](https://developers.cloudflare.com/email-service/get-started/send-emails/) on Oct 27, 2026, which does not say what plans it needs, its sending limits, or whether recipients are restricted; look those up before building, and treat the request format as one to re-check, since the service is new. Nothing is added to the module unless a provider cannot be done without it.
- Email stays optional on an account. An address is used only after it is **verified**: a message with a one-time link to the address, hashed token, one hour. Changing it needs a link from the current address and then a link to the new one, and the old address is told.
- First uses: the move code (iteration 10), which is sent by email alone whenever a mailer is set, and password reset (a one-time link; the answer is the same whether the address exists or not; rate limited per address and per client; the link signs out other sessions on use).
- Later uses, each a setting the user turns on, off by default: a note for a reply or mention on your posts, a note for a new follower, and a daily digest instead of single notes. Each message has a one-click way to stop that kind of mail. A queue (the Webmention queue's pattern) with a cap and retries, so a slow provider never slows a request.
- Not planned: receiving mail, posting by email, and any tracking pixels or links.

### 12. Roadmap: the weftseed.com website
Planned Oct 27, 2026; not built. The public face of the hosted service, at `www.weftseed.com` (with `weftseed.com` redirecting to it). The server already links to `https://weftseed.com` from every page's footer, from the README and from the fetch user agent, so the address is in use today.
- **Pages:** a landing page (what Weft is, in a paragraph and a screenshot or two, with "Sign up" and "Run your own server"); the terms of use; the privacy policy; contact; a FAQ; and a short "how it works" page for people coming from textcasting, RSS and the fediverse. The FAQ starts from the questions already answered in the README: quotas, picture limits, taking your data with you, moving to another server, and your own domain.
- **Terms and privacy:** these have to say what the server really does. The facts to put in them are known from the code: what a user's file holds (posts, replies, likes, follows, pictures, login), what the shared database holds (the directory, the feed cache, and queued Webmentions), the rate limits that keep addresses for a short time, the server's logs, email if given (iteration 11), how to download everything (`Download my data`), how to leave, and what happens to a deleted or moved account (30 days in the trash). The wording of both is for the operator and a lawyer, not for the code; the plan only keeps the list of facts accurate and the pages in step with the server.
- **What lives where:** static files, kept in this repository under `site/`. Either the Weft server serves them at `www.<domain>` (`weft serve -site site`; `www` is a reserved name, so no account can clash with it), or any static host does (Cloudflare Pages fits the setup in the README); both work from the same folder. It holds no accounts. The apex name needs its own DNS record, since the wildcard (`*.weftseed.com`) does not cover it.
- **Sign up:** the page links to the sign-up page of iteration 14, which lives on the same `www` host. Until sign-up is open, the page says so and how to ask on the contact page.
- **First version, built:** `site/index.html`, one static page with the concepts (Weft, the spool, the loom, federation, posting, replying, liking, reposting, portability), how to sign up and how to run a server. The rest of the pages above are still to do.
- **Other servers:** a page for operators of a server that is not weftseed.com is on the protocol site (iteration 13), not here.

### 13. Roadmap: the weftprotocol.org website
Planned Oct 27, 2026; not built. The home of the protocol itself, apart from any one service, so that other servers and readers have a place to read it.
- **The specification:** the namespace document (`docs/weft-namespace-0.1.md`) as a page with a stable address for each version, a short overview in front of it, and the elements and attributes as a reference (`webmention`, `following`, `likesFeed`, `inReplyTo`, `context`, `movedTo`, `key` and the rest). The document is the source and the page is built from it, so they never differ.
- **Guides for people who build:** how a reader or another server should treat a Weft feed (threads, likes, moved feeds and the redirects that go with them, the Webmention rules), with a worked example feed and a checklist for compatibility.
- **Requirements and guides for server operators:** what a server must do to take part (serve the feed and the Webmention endpoint, verify before counting, protect against fetching private addresses, redirect when an account moves), what it should do (limits and quotas, backups, picture handling), and how to run one: the README's setup, Caddy and Cloudflare notes, `weft backup`, `weft fsck`, and the transfer between servers once it exists (iteration 10).
- **Namespace address:** `https://weftprotocol.org/ns/weft/0.1` (changed from `https://weftseed.com/ns/weft/0.1` on Oct 27, 2026, in the code, the tests and the namespace document, because nothing had been distributed and the only databases are development ones). From now on the address is permanent for its version: it is an identifier and must never change, and it serves the specification page. Later versions follow the pattern (`/ns/weft/0.2`).
- **What lives where:** static files again (`protocol/` in this repository), built from `docs/`. Open: whether the hosting is shared with the weftseed.com site, and who may change the specification (license and a way to propose changes belong with the License item below).

### 14. Roadmap: sign-up with your own domain, the admin account and a cap
Planned Nov 6, 2026; not built. The aim is to keep spam out of the hosted service without email checks or captchas: an account costs a domain, and the operator decides how many there are.

**Who may sign up.** Only a person who controls a domain of their own, such as `weft.example.com` (a name under a domain they own), and the operator. The proof is the one the profile page already uses for custom domains, so no new mechanism is needed:
1. On `www.weftseed.com/signup` the person types the domain they will use and the account name they want (checked against the reserved names).
2. The page shows the TXT record to create (`_weft.<domain>`, value `weft-verify=…`) and the CNAME to the server's `-cname-target`. A pending sign-up holds the name for one hour and does not count toward the cap.
3. The page checks the DNS. When the TXT record is there, the account is made, with the domain already verified, and the person chooses a password on the same page (no temporary password). When the CNAME reaches the server the domain becomes the address, as in iteration 7's two steps.
4. Rules that keep it cheap to run and dear to abuse: one account per registered domain (`example.com` and `weft.example.com` count as one; the registered domain comes from the public suffix list in `golang.org/x/net/publicsuffix`, which is already a dependency); sign-up attempts rate limited per client address; the server never sends mail for this, so nothing can be used to mail others.
5. **Without a domain.** A person with no domain of their own can still have an account at `name.weftseed.com`, hosted by the loom (server), through a **waiting list** (decided Nov 6, 2026). The list is a form on `www`: the account name wanted, a way to reach the person (an email address, used only to answer), and where they already write: social media profiles, a blog, posts, so the operator can see a real person who writes. The operator lets people through by hand, within the cap, and the sign-up page for a let-through person works as the domain one does. The list keeps only what the form asked for and is cleared after the answer. Until the form is built, only the operator creates these accounts (`weft user add`). Moving later works as it does between servers (`weft move`, to another server or to their own domain), and the old address redirects. The landing page says so.
6. **If the domain goes away.** An account that signed up with a domain depends on it. When the domain stops verifying (the TXT record is gone, or the domain no longer reaches the server), the profile page warns the owner, and so does the account's own site; there is a grace period (proposed: 30 days) in which it can be put right. After that the account is **terminated**: it goes to the 30-day trash like any removed account, so it can still be restored or downloaded for a time. The account does not fall back to a `name.weftseed.com` address, since that is what the waiting list is for. Anyone can avoid termination by moving away (`weft move`) or downloading their data before it happens. An account that was let in from the waiting list has no domain to lose, so this rule does not apply to it.

**The cap.** `weft serve -max-users 1000` (0 for no limit). It counts accounts, not pending sign-ups, and the operator's own accounts are exempt. At the cap the sign-up page says there is a waiting list instead of a form, and an account moving in with `weft move` is refused with the same words. The number is a setting, so it can change without a release. Lowering it never removes anyone.

**Storage at 1000 accounts, with each using half of its limit.** The only per-account limit today is the picture quota, 200 MB (`-media-quota-mb`). Text has no limit, but is small.
- Pictures: 1000 × 100 MB = about **100 GB**. If every account filled its quota: 200 GB.
- Text, replies, likes and follows: posts are a few kilobytes each. A person writing ten a day for a year makes about 4 MB; with the same for replies and likes, call it 10 MB a year per active account, so about **10 GB** a year for 1000 accounts. This is an estimate, not a measurement; measure it on the first real accounts.
- The shared database (feed cache and queues) grows with what the accounts follow: expect a few GB, and pruning old cached items keeps it there. The development server's is 0.5 MB today.
- Backups with restic keep deduplicated snapshots, so plan on about as much again as the live data, and the 30-day trash for moved accounts holds a few spools at a time.
- **Disk to plan for:** about 120 GB live, 250 GB with backups and room to grow in the first year, 500 GB if the quota is raised or every account fills it. Smaller picture quotas are the lever: 100 MB halves the biggest part.

**The admin account.** The operator is an ordinary account that is also marked admin, with its own site like anyone's. The administration pages are on a separate host so they never mix with a user's address, and are not reachable by name from the public: **`admin.weftseed.com`** (`admin` is already a reserved name, so no user can take it). Suggestions, in order of preference:
1. `admin.<domain>`, served only on the private `-internal` listener or behind Cloudflare Access (the operator's email, one-time code), with the admin account's own password as the second step. Nothing about it is on the public internet by default.
2. The same pages at `/admin` on the operator's own site (`henry.weftseed.com/admin`), signed in as the admin account: no extra host, but it shares the cookie of an ordinary site, so it asks for the password again before each action.
3. Command line only (`weft user …`, `weft admin …`), for an operator who does not want a web interface at all. Every admin page is also available this way.
What the pages do: the list of accounts (name, domain, size on disk, last post, never the content); suspend or restore an account (its sites answer 410 while suspended); delete one (to the 30-day trash); the cap and how many are left; sign-ups waiting; failed sign-ups; the size of the data folder and the free space; the Webmention queue; the held mentions during moves. An admin cannot read someone's posts that are not public, since the spool is not opened for reading.

**Order of work.** The cap and `-max-users` (small, and useful before anything else); the admin flag and the CLI; the sign-up page with the DNS check; the admin pages; then the landing page's sign-up link (iteration 12).

### 15. Later
WebSub or rssCloud. Packaging. Email at sign-up (iteration 11) to recover a lost password. License. A cache for thread-page fetches. Rate limiting of public page reads, if not left to a proxy.

**Action item (before production):** work through [production-optimizations.md](production-optimizations.md): measure, then decide which of its items to build. First on the list is a fetch cache for thread pages.

## Spec updates to make along the way

- Reactions with a polarity and an emoji were tried, then removed: only `weft:likeOf` and a `weft:likes` count remain, and there are no dislikes. Done.
- `weft:context` may be a bare URL to any page, with optional title, description and image. Done.
- `weft:likes href` (the liked-by feed) is in the namespace document. Done.

## Risks

- Cross-instance Webmention verification must fetch pages over HTTP and guard against SSRF. Block private ranges in production, allow `*.localhost` in test mode.
- Feed polling cost and staleness, until a ping mechanism exists. Conditional requests and backoff keep the cost down.
- Like spam, since anyone can send likes and every verified like now counts. The Webmention rate limit and queue caps bound the volume; a stricter count (for example only followed senders) can come back if it is abused.
