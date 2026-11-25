<img src="img/weft-icon-128.png" alt="Weft logo" width="96" align="right">

# Weft

Weft (Web-External Federated Threads) builds on Dave Winer's textcasting. Home: https://weftseed.com

A Weft host: every post, reply and like lives in its author's own RSS 2.0 feed. The protocol is specified at https://weftprotocol.org: [Weft Namespace 0.1](https://weftprotocol.org/spec/namespace/0.1/) (what goes in a feed) and [Weft Hosting 0.1](https://weftprotocol.org/spec/hosting/0.1/) (where feeds live). See [docs/design-goals.md](docs/design-goals.md) for goals and [docs/plan.md](docs/plan.md) for the implementation plan and current iteration.

> **Authorship.** All code and documentation in this project was written by Claude, Anthropic's AI model. The functionality, the architecture and the verification are the work of Henry van den Broek.

## Contents

- [Quick start](#quick-start)
  - [Post through the API](#post-through-the-api)
  - [Two instances on one machine](#two-instances-on-one-machine)
  - [Local development](#local-development)
- [Using Weft](#using-weft)
  - [Writing posts](#writing-posts)
  - [Following and the timeline](#following-and-the-timeline)
  - [Replies, likes and threads](#replies-likes-and-threads)
  - [Your profile](#your-profile)
  - [Apps that act for you (OAuth)](#apps-that-act-for-you-oauth)
  - [The fediverse (ActivityPub)](#the-fediverse-activitypub)
  - [Weft it (bookmarklet)](#weft-it-bookmarklet)
  - [Pages as Markdown](#pages-as-markdown)
  - [Which feeds Weft can follow](#which-feeds-weft-can-follow)
- [Running a server](#running-a-server)
  - [Docker and hosting](#docker-and-hosting)
  - [Data, backups and moving](#data-backups-and-moving)
  - [Custom domains](#custom-domains)
  - [Limits](#limits)
  - [OAuth for apps](#oauth-for-apps)
  - [Landing page and sign-ups](#landing-page-and-sign-ups)
  - [Administration](#administration)
  - [Behind Cloudflare](#behind-cloudflare)
  - [Speed](#speed)
  - [Monitoring](#monitoring)
- [Development](#development)
  - [Try the UI on a real conversation](#try-the-ui-on-a-real-conversation)
  - [Icons](#icons)
  - [Look](#look)
- [Licence and authorship](#licence-and-authorship)

## Quick start

```bash
make user NAME=alice PASSWORD='a long password'   # build, then make the account "alice" in the data folder weft; prints its API token once
make run                                          # build, then serve every account at <name>.localhost:8080 (with -dev, for local use)
```

The password for a user made or reset on the command line is temporary: the account is marked, and the first time the user signs in on the web they are taken to a page where they must choose their own before doing anything else (the API token is not affected). Without `PASSWORD=` (`-password`) one is made and shown on the console, such as `kx4mq-9bnrt`; hand it to the user. `make passwd NAME=bob` (`weft user passwd`) resets one the same way. Add `PERMANENT=1` (`-permanent`) to give a password the user keeps, which needs one to be given. Anyone can change their password later with *Change password* on the profile page, which also signs out their other sessions.

Open http://alice.localhost:8080/ and log in with the password. Each user is served at
`<name>.localhost`; `*.localhost` resolves to 127.0.0.1 in most browsers and curl.
(If yours doesn't, add the host names to `/etc/hosts`.)

### Post through the API

Post through the API (what agents use):

```bash
curl -XPOST -H "Authorization: Bearer $TOKEN" \
  -d '{"title":"Optional","markdown":"Hello *world*"}' http://alice.localhost:8080/api/posts
```

Listing, reading, editing and deleting posts, and posts as Markdown documents with frontmatter, are in [Posts through the API](#posts-through-the-api).

### Two instances on one machine

To try two separate instances on one machine:

```bash
./bin/weft user add -data a alice && ./bin/weft user add -data b bob   # each prints a token
./bin/weft serve -addr :8101 -data a -dev &   # http://alice.localhost:8101/
./bin/weft serve -addr :8102 -data b -dev &   # http://bob.localhost:8102/
```

### Local development

The server fetches the URLs users enter, so by default it refuses loopback and private addresses
(SSRF guard). `make run` passes `-dev` to allow them for local use. Do not use `-dev` when hosting.
If you run the binary yourself: `./bin/weft serve -dev`.

`make run VERBOSE=1` (or `weft serve -v`) logs every API call and inbound Webmention: host, method, path, status, time, and the start of the request body. Tokens are never logged.

`make follow NAME=dana TO='alice henry'` makes a user follow others (bare names on this instance, or feed URLs); the target servers must be running, since the feed is fetched.

`make help` lists the other targets, including `token` to issue a fresh API token.
Settings: `DATA` (default `weft`, the data folder), `ADDR` (`:8080`), `DOMAIN` (`localhost`).

The build is a single static binary (`CGO_ENABLED=0`). Dependencies: `goldmark`, `modernc.org/sqlite`.

## Using Weft

### Writing posts

#### Markdown

Posts are written in Markdown: headings, emphasis, strikethrough, lists, quotes, code, links, pictures, tables and task lists. The editor shows a cheat sheet beside it, made by the same renderer that publishes the post, so it only shows what works. Raw HTML in a post is not passed through. The boxes of a task list (`- [x] done`, `- [ ] to do`) are shown as symbols with a text for screen readers, not as form controls; they are for reading, and are changed in the source. A post is rendered once, when it is saved, so a post written before a Markdown feature existed shows it after it is edited and saved.

#### Post addresses and identity

A post has one address and one identity, kept apart so a post can move between instances.
- **Address:** `<link>` is `https://<user host>/posts/<slug>`, for example `/posts/the-reader-half.k3x9`. The slug comes from the title (or the first sentence of an untitled note) plus a random 4-character suffix after a `.`, such as `.k3x9`. Posts made before this keep their `-k3x9` addresses. The compose form suggests the words as you type (from the title or first sentence) and you can reword them before publishing, or send `"slug"` in the API. The words are cleaned to lowercase letters, digits and hyphens (a dot, underscore or space becomes a hyphen), at most 80 characters, and the short code is always added after them. It is unique per user, never changes (even when the post is edited, and a chosen address cannot be changed later), and stays reserved after the post is deleted. It is the only URL that serves the post.
- **Identity:** `<guid isPermaLink="false">urn:uuid:…</guid>` is a UUIDv7 made when the post is created. It is not a URL and travels with the post.
- Likes still have instance-local addresses (`/likes/<n>`); they will get the same treatment later.

#### Keywords

An optional comma-separated box under the Markdown editor (`travel, weekend project`, phrases allowed, up to 10 of 50 characters). They are published as the item's `<category>` elements and shown as chips; the API takes `"keywords": ["a", "b c"]`.

#### Reposts

Under Advanced, **This is a repost** reveals a **Canonical link** box for the address where the content first appeared (an `http://` or `https://` address). The post's page then says "Originally published at …" and carries `<link rel="canonical">`, and its feed item has `<weft:canonical href="…"/>`. Unchecking the box removes it. An original has nothing. The API takes `"canonical": "https://…"` on create and edit (an edit without it removes it). Weft shows the same line under followed posts whose feed gives `weft:canonical` (or an Atom `rel="canonical"` link).

#### Long posts

In a list of posts (the timeline, the home page, a keyword page) a post longer than about 20 lines is folded, with a Show more button. A post on its own page is never folded.

#### Pictures

Pictures live in the author's own spool (their database file), so a post does not break when someone else's server goes away, and they move with the account (backup, `weft export`, `weft move`).

- **Add** a JPEG, PNG or GIF in the editor (Safari can also add a HEIC photo, which is converted to JPEG): the Add image button, a paste, or a drop on the text box. It inserts `![image](/media/<hash>.jpg)`; type a description in place of "image". Programs use `POST /api/media` (the picture as the body, or the form field `file`; `?size=compact`, `?alt=…`) and get the path back.
- **Made smaller in the browser first.** Before a picture is sent, the browser scales a JPEG or PNG that is longer than 1600 pixels (at the Compact size) to that, or, at the Original size, makes one that is over the size limit smaller until it fits. A profile picture is scaled to 512 pixels. Transparent PNGs stay PNG, GIFs are never touched, and if the browser cannot do it the file is sent as it is. The server still checks everything.
- **Sanitized, always.** The kind is read from the bytes. All metadata is removed (location, camera, time, thumbnails, comments, anything after the end of the image); a photo's orientation is applied to the pixels; a file that does not decode, or has too many pixels, is refused. SVG is not accepted. There is no way to keep the metadata.
- **Two sizes.** *Original* leaves the picture as it is. *Compact* (the default on a phone, so its camera photos are made smaller) accepts a file up to 25 MB and makes the longest side at most 1600 pixels (JPEG quality 80), and keeps the smaller of that and the cleaned original. The choice is remembered in the browser.
- **Addresses.** A picture is stored under the hash of its bytes, so the same picture is kept once, and its address never changes and can be cached for ever. The Markdown keeps the path without a host, so a post that moves to a new address keeps its pictures; feeds, the Markdown form of pages and the one-item feed make it a full address.
- **Smaller copies.** Pages do not show a large picture at full size. `/media/<name>?w=800` is a copy with its longest side at most 800 px (`?w=240` is a thumbnail, and no other size is served), made when it is first asked for and not stored, so it does not count toward the quota. Browsers and proxies keep it like the picture (`immutable`), and the server keeps the most recent 64 MB of copies in memory; two pictures are scaled at a time and the same copy is never made twice at once. A picture that is already small (under 100 KB, or not longer than 800 px), and any GIF, is served as it is. A page links the smaller copy to the picture itself.
- **Pictures from other sites.** Press "Copy images" in the editor (it appears when the post shows a picture from another site) to fetch the pictures a post shows from elsewhere, store them here, and point the post at the copies (the original address is recorded). It never happens by itself; copying other people's pictures is the author's decision.
- **Your images** (`/images`) lists them with their size, where each is used, and how much of the quota is left; a picture in use is only deleted when asked twice.
- **Limits are the server's.** `weft serve -max-image-mb 5 -max-upload-mb 25 -media-quota-mb 200 -image-side 1600 -image-megapixels 24` (a quota of 0 is no limit). A server can set them high and charge for it, or low and be free.

#### Posts through the API

Every call goes to the user's own host, with `Authorization: Bearer <token>`. `{ref}` is a post's number (`42`) or its slug, the end of its address (`hello.k3x9`); create answers with both (`{"id": 42, "url": "…/posts/hello.k3x9"}`).

| To | Call |
|---|---|
| List your posts | `GET /api/posts`: newest first, without their text. `limit` (1 to 200, default 50) and `before` (a date like `2026-10-27_14-35` in UTC, or a Unix time) page back; `Link: <…>; rel="next"` names the next page and is missing on the last. |
| Show one | `GET /api/posts/{ref}`: JSON, or with `Accept: text/markdown` a document (below). |
| Create | `POST /api/posts` with JSON, or `Content-Type: text/markdown` and a document. The answer is `201` with the post's address in `Location`. `POST /` (the root of the site) does the same, for an editor that reads the posts there: `GET /` with `Accept: text/markdown` says `Allow: GET, HEAD, POST` to a token that may post. |
| Edit | `PUT /api/posts/{ref}`, the same two ways. |
| Delete | `DELETE /api/posts/{ref}`: answers 204, and the address answers 410 from then on. |

Reading a published post needs no token: that is its public address (`GET /posts/<slug>`, with `Accept: text/markdown` for its source as a document, below). The same address takes the document back with `PUT /posts/<slug>` (a bearer token with the `post` scope; `If-Match` with the `ETag` of what you read guards against overwriting a change, and a mismatch answers `412`). `DELETE /posts/<slug>` removes it (`204`; the address then answers `410` and stays reserved; `If-Match` works as for `PUT`). For a reader who may change the post, the `GET` says so with `Allow: GET, HEAD, PUT, DELETE` and `Link: </posts/<slug>>; rel="edit"`; everyone else gets `Allow: GET, HEAD`. The `ETag` is strong.

**A post as a document** is for editors: the file is the post. Frontmatter between two lines of `---`, then the Markdown exactly as stored (pictures keep their `/media/…` paths; the public address makes them absolute, an editor must not save those back):

```markdown
---
id: 42
url: https://alice.example/posts/hello.k3x9
title: Hello
keywords: [weft, "two words"]
canonical: https://example.org/original
published: 2026-11-21T09:30:00Z
---
The text of the post.
```

- **Keys:** `title`, `keywords` (`[a, b]`, one `- item` a line, or `a, b`), `canonical`, and for a new post `slug` (the words of its address; the short code is added), `in_reply_to`, `in_reply_to_feed` and `context` (`title`, `description` and `image`, indented below it), all as in the JSON. `id`, `url`, `slug`, `published` and `updated` are sent when you read and ignored when you write, except that an `id` that is not the post being edited is refused with 409, so a file saved to the wrong post changes nothing, and so is a `slug` that is not the post's own address (an address cannot be changed). On a new post `slug` is the words you want, and the short code is added. As with JSON, an edit changes the title, keywords, canonical link and text only; the address and the reply links stay.
- **A document is the whole post.** Leaving `canonical` or `keywords` out of an edit removes them, as with JSON.
- **Other keys are ignored,** so an editor can keep its own; they are not stored and not sent back.
- **The frontmatter is a small subset of YAML:** values are plain, `"double quoted"` (`\"`, `\\`, `\n`, `\t`, `\uXXXX`) or `'single quoted'`; a line that cannot be read is refused with 400 and its number. Without a first line of `---` the whole text is the post. A post that itself begins with a line of `---` is written with an empty frontmatter in front (`---` and `---`).
- Any other content type is read as JSON, as before. A body over 1 MB gets 413.

### Following and the timeline

Following and the timeline: log in, open Following, paste another user's address (for example
`http://bob.localhost:8080/`) and the feed is discovered from the page. Followed feeds refresh
every 5 minutes (`-poll`) or on "Refresh now". A refresh asks the feed's server whether anything changed
(`ETag` / `Last-Modified`) and a `304` costs almost nothing. A feed that fails is left alone for 15 minutes,
then 30, doubling up to a day (longer if the server sends `Retry-After`); "Refresh now" always tries.

Your own posts appear in your timeline, marked "You" with an Edit link; "Hide my posts" (`/timeline?mine=0`) takes them out. One person's page (`?feed=`) never has them.

Chips are links. On a site, `/?keyword=pickleball` lists that author's posts with the keyword, and `/feed.xml?category=pickleball` is a topic feed of just those posts (paged like the main feed), so someone can follow one topic from one person; following the `?keyword=` page address finds that feed. A chip filters the view it is in: in your timeline it filters everything you follow (`/timeline?keyword=pickleball`), and when you are looking at one person's feed (`/timeline?feed=…`) it filters just that feed. Matching is on the whole keyword, ignoring case.

Search looks through one account's posts, never the rest of the server: `/search?q=words` lists the posts that hold every word in their title, text or keywords (ignoring case), newest first, up to 50. A person finds a search box on the home page and the search page; for `Accept: text/markdown` the result is a list of links, each with a line of what the post says around the first match. `/.well-known/bonbon.json` is public and names that search address (`urlSearch`) and the draft an editor such as Mardo starts a new post from (`mardo.newPages`), with the keys of a post of one's own: `title`, `slug`, `keywords` and `canonical` (the reply keys are left out). Whether an editor may add a post stays what the `Allow` header of `/` says.

#### Timeline pages

The timeline shows 50 posts at a time. "Older posts" at the bottom leads to the next 50 (`/timeline?before=2026-10-27_14-35`, a minute in UTC with 24-hour time that you can edit to jump to a date, with the feed, keyword and likes choices kept), and "Back to the newest" returns. Your own posts and, if shown, likes are cut at the same dates, so every post appears once. The Markdown view of the timeline has the same links. `GET /api/timeline` takes `limit` (1 to 500, default 200) and `before` (the same date, or a Unix time) and names the next page in a `Link: <…>; rel="next"` header, which is missing on the last page.

### Replies, likes and threads

#### Replying

Replying: on the Timeline click "Reply" under a post, or use the API with `in_reply_to`
(and optionally `in_reply_to_feed`). The reply is published in your own feed and a Webmention is
sent to the parent's site. Replies you receive from people you follow are approved automatically;
others wait on the Replies page (or `GET /api/replies?status=pending`) until you approve them.
You can reject an approved reply later.

#### Likes

Liking: under each timeline post, "Like" publishes a like in your likes feed, and "undo" withdraws it.
API: `POST /api/likes {"target","target_feed"}`, `DELETE /api/likes {"target"}`. A like is counted at the
post only when the sender is someone the post's author follows; `GET /api/likes/received?post_id=N` shows
all of them. There are no dislikes and no emoji reactions: an emoji is just text, so put it in a reply.

The like count is a link to a list of who liked the post, with Follow and Unfollow behind each name (`/liked-by?target=<post URL>`): every like received counts, whoever sent it. On your own posts it lists everyone whose like arrived. On a post in a feed you follow it lists you and the people you follow who liked it, and an "Also liked by" section with everyone else on the author's public liked-by list (`<post URL>/likes.xml`), each with a Follow button.

#### Threads

Threads: "Thread" under any post opens the conversation it belongs to, rooted at the original post.
The **curated** view shows only what each author approved, read down from the root through the
replies feeds. The **raw** view shows everything your reader knows, including replies that were not
approved. Replies whose parent your reader does not have are placed by the spec's rules: a deleted
parent (410) leaves a tombstone, an offline parent leaves the reply under the root, and a live but
unknown parent gets a placeholder. When the replies under one of your replies change, a Webmention
is re-sent up the chain so each ancestor's cached second level stays fresh.

#### Reading a thread

`/thread?url=<post>` (and `GET /api/thread?url=`) shows the **branch** around a post by default: the chain of posts from the root down to it, and its own replies, with no siblings. Every post on the chain, and every reply, carries a reply count (or just "replies" when the count is unknown) and a link that makes it the new focus. It costs about one fetch per level of depth, however large the whole thread is. `view=curated` shows the whole approved tree (up to a fetch limit), and `view=raw` shows everything this reader knows, approved or not. In the JSON, `focus` marks the requested post, `on_path` the chain to it, and `replies` the count on a closed branch (-1 unknown).

`replies.xml` items carry `weft:replies` (href and the count cached by the host) for the replies that have their own replies feed, so a closed branch can show its size without being opened.

A post's permalink answers to its `Accept` header: `text/markdown` returns the source as a document with frontmatter, and `application/rss+xml` (without `text/html` or `*/*`) returns the post as a one-item feed, with all its `weft:` links. A thread is built from posts, not feeds: a reader asks each post's URL for the one-item form to get the post, its parent and the link to its replies, so old posts work. A feed holds the newest 100 posts and links to older pages (`/feed.xml?page=2`, RFC 5005 `rel="next"`); a new follower reads all of them.

### Your profile

#### Avatars

The bio is Markdown, up to 2,000 characters, written in a text area on the Profile page (or with `weft user set -bio`). The home page shows the first two lines of it, as they fall on the screen, and puts the rest behind a **More** that opens in place (a small script adds the button only when the bio is longer; without script the whole bio shows). A bio that fits in two lines has no More. The meta description and the feed's description use the bio as plain text, and the Markdown form of the home page carries it as written.

A user can have an avatar image address (`http://` or `https://`): set it on the Profile page (the "Edit profile" link under your bio) or with `weft user set -avatar URL NAME` (`weft user add` takes `-avatar` too, and `-avatar ""` removes it). It shows in the header and on your cards, and it is published as the feed's RSS `<image>`, so people who follow you see it on your posts in their timeline. Without one, or if the image fails to load, a circle with the first letter of the name is shown. Avatars from other feeds are only used if they are plain http(s) addresses, and are loaded without sending a referrer.

You can also upload a picture on the Profile page (or `POST /api/avatar` with the picture as the body). It is cropped to the middle square, made at most 256 pixels wide, stripped of metadata, and kept in your own database like any other picture, so it moves with your account. The feed's channel image gives its full address, so readers on other servers show it. Replacing or removing it deletes the old picture, unless a post shows it.

#### Your own domain

On the profile page, *Your address* lets you add a domain such as `weft.example.com`. You prove it is yours with a TXT record (`_weft.weft.example.com`, value `weft-verify=…`; a CNAME cannot prove it, because every user's CNAME names the same target), and point it at the server with a CNAME. The domain becomes your address in two steps, so that a domain whose DNS is not ready cannot lock you out: the TXT record makes it *verified*, and it becomes *active* when a request to it reaches this server (the server asks the domain for `/.well-known/weft-domain`; press *Check again* after the CNAME has spread). Until then your site stays at the old address and nothing redirects. Once active the domain is your address: the old `name.<domain>` address, the feed included, redirects to it with a permanent redirect (301, or 308 for a POST), and readers that follow you switch over by themselves, because a feed that is permanently redirected has moved. Use a subdomain: a bare domain cannot be a CNAME. You sign in again at the new address. The sign-in, profile and domain pages stay available at the old address, so if the domain ever stops working you can still sign in there and remove it.

See [Custom domains](#custom-domains) under Running a server for the proxy and certificate side.

### Apps that act for you (OAuth)

If the server has turned it on, an app can act for you through the API without ever holding your password or your API token: you say yes once, on your own site, and can end it at any time. The app opens your address's `/oauth/authorize`, you sign in if you are not, and you see its name, where it will send you back and what it asks to do. **Allow** connects it. The apps you have connected are listed on your profile under *Connected apps*, each with what it may do and when it last did something; **Disconnect** stops it at once. Changing your password disconnects all of them, and so does leaving the server.

What an app can be allowed to do (it is asked for by name, and you see each one in plain words):

| Scope | Lets the app |
|---|---|
| `read` | read your posts, your timeline, who you follow, and the replies and likes you have received |
| `post` | write, change and delete your posts, and upload pictures, including your profile picture |
| `follow` | follow and unfollow feeds |
| `like` | like and unlike |
| `moderate` | approve or hide replies to your posts |

Your own API token is not limited by scopes and keeps working. An app that asks for something its token is not allowed to do gets `403` with `WWW-Authenticate: Bearer error="insufficient_scope", scope="post"`.

**For app developers.** Each account is its own authorization server, at its own address (`https://alice.example.org`, or her own domain). An app finds the rest from `/.well-known/oauth-authorization-server` ([RFC 8414](https://www.rfc-editor.org/rfc/rfc8414)) or, from a `401` on the API, `/.well-known/oauth-protected-resource` ([RFC 9728](https://www.rfc-editor.org/rfc/rfc9728)).

1. **Register** the app, once per server, with `POST /oauth/register` and a JSON body ([RFC 7591](https://www.rfc-editor.org/rfc/rfc7591)): `client_name` and `redirect_uris` (1 to 10); optionally `client_uri`, `logo_uri` and `policy_uri` (https addresses). The answer holds the `client_id`. The server fetches the logo once, while you register, and keeps a 96-pixel square copy (JPEG, PNG or GIF; a wide picture is cropped to its middle) to show on the consent page: people's browsers never load it from you. A logo that cannot be fetched or is not a picture is left out, and the app is registered all the same. Apps are public clients: there is no secret, and `token_endpoint_auth_method` can only be `none`. Redirect addresses are `https`, or `http` to `localhost`, `127.0.0.1` or `[::1]` (any port), or a private scheme such as `com.example.app:/cb`; no fragment.
2. **Send the person to** `/oauth/authorize` with `response_type=code`, `client_id`, `redirect_uri` (exactly as registered), `code_challenge` and `code_challenge_method=S256` ([PKCE](https://www.rfc-editor.org/rfc/rfc7636): `plain` is not accepted), `scope` (space-separated; `read` when left out) and a `state`. They come back to your `redirect_uri` with `code`, `state` and `iss`, or with `error` (`access_denied` when they say no).
3. **Swap the code** at `POST /oauth/token` (form-encoded, in the body): `grant_type=authorization_code`, `code`, `client_id`, `redirect_uri` and `code_verifier`. The code works once, for 60 seconds. The answer has `access_token` (an hour), `refresh_token` (30 days), `expires_in` and `scope`.
4. **Call the API** with `Authorization: Bearer <access_token>`, at the same address.
5. **Renew** with `grant_type=refresh_token`, `refresh_token` and `client_id`. Each refresh token works once and comes with a new pair; using an old one again ends the whole connection. Keep the new pair before you use it.
6. **Disconnect** with `POST /oauth/revoke` (`token`, `client_id`), which ends the connection a token belongs to.

The whole flow with `curl`, for an app that listens on a port of the computer (the browser step is the one thing a script cannot do):

```sh
SITE=https://alice.example.org

# 1. register once (keep the client_id)
curl -s $SITE/oauth/register -H 'Content-Type: application/json' \
  -d '{"client_name":"My editor","redirect_uris":["http://127.0.0.1/callback"]}'
CLIENT=wc_...   # from the answer

# 2. a PKCE pair: keep the verifier, send the challenge
VERIFIER=$(openssl rand -base64 48 | tr -d '=+/\n' | cut -c1-64)
CHALLENGE=$(printf %s "$VERIFIER" | openssl dgst -sha256 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '=')

# 3. open this in the browser; after Allow it goes to http://127.0.0.1:53124/callback?code=...&state=...&iss=...
echo "$SITE/oauth/authorize?response_type=code&client_id=$CLIENT&redirect_uri=http%3A%2F%2F127.0.0.1%3A53124%2Fcallback&code_challenge=$CHALLENGE&code_challenge_method=S256&scope=read+post&state=abc"
CODE=wco_...    # from the address the browser ended at

# 4. swap the code (within 60 seconds) for tokens
curl -s $SITE/oauth/token -d grant_type=authorization_code -d code=$CODE -d client_id=$CLIENT \
  -d redirect_uri=http://127.0.0.1:53124/callback -d code_verifier=$VERIFIER
ACCESS=wat_...; REFRESH=wrt_...

# 5. use the API: create a post from a Markdown document, then read it back as one
curl -s $SITE/api/posts -H "Authorization: Bearer $ACCESS" -H 'Content-Type: text/markdown' \
  --data-binary $'---\ntitle: Hello from my editor\n---\nThe text.\n'
curl -s $SITE/api/posts/42 -H "Authorization: Bearer $ACCESS" -H 'Accept: text/markdown'

# 6. an hour later: a new pair (the old refresh token is now spent: keep the new one)
curl -s $SITE/oauth/token -d grant_type=refresh_token -d refresh_token=$REFRESH -d client_id=$CLIENT

# 7. disconnect
curl -s $SITE/oauth/revoke -d token=$ACCESS -d client_id=$CLIENT
```

### Weft it (bookmarklet)

Your home page shows a "Weft it" link to drag to the bookmarks bar. Clicking it on any page opens the compose form with the page's title, description and image (from Open Graph or Twitter tags, falling back to the page itself). It also checks whether the page can take a Webmention (a `rel="webmention"` link in the page, or in the `Link` header of its response). If it can, the form starts as a **reply to the page**; if not, as a **bookmark card** (the image, the description as a quote and a link). A link under the form switches between the two, and nothing is published until you press Publish. If the page advertises RSS or Atom feeds (up to five), they are listed above the form with a **Follow** button, or "Following ✓" for those you already follow; the button follows without leaving the form, so what you have typed stays. A reply to a page carries what the page looked like as optional attributes of `weft:context` (`title`, `description`, `image`), so followers see a card for it. The API takes the same as `"context": {"title": "", "description": "", "image": ""}` next to `in_reply_to`.

**Reposting a page:** **Repost** copies the page's text into the form, with its canonical link already set (see [Reposts](#reposts)), and nothing is published until you press Publish. The text starts with a quoted line, `> Repost from [blog.example](https://blog.example/post)`, that you can keep or change. Weft asks the page for Markdown first (a Weft post or another feed item gives its own, and a repost of a repost points at the first original), then takes the article text of the HTML page: its title, headings, paragraphs, lists, quotes, code, links and pictures, without the menus and footers around it. Pictures stay at the original address; the editor's **Copy images** button keeps them in your database if you want that. The canonical link is the page's own `<link rel="canonical">` if it has one, otherwise its address, without tracking parameters (`utm_*`, `fbclid` and the like). A page whose text comes from scripts, or one that needs a login, gives nothing; the form then holds a bookmark card and says so, and you paste the text.

Post and like pages mark up their author as an `h-card` (`p-author` with the display name, the site address and, if set, the avatar as `u-photo`) next to the `h-entry`, so Webmention receivers that read microformats can show who wrote a reply.

**Liking a page that is not on Weft:** in the compose form the bookmarklet opens, the **Bookmark · Reply · Like · Repost** links at the top choose what to do with the page. **Like** asks for one click, publishes the like in your likes feed together with the page's title, description and image (as optional attributes of `weft:likeOf`), and tells the page with a Webmention if it can take one. Your likes, and those of people you follow, then read "liked “The web of links” (blog.example)" under "Show likes", and your own likes there have an Unlike button. The API takes `"context": {"title": "", "description": "", "image": ""}` next to `target` on `POST /api/likes`.

### Pages as Markdown

Send `Accept: text/markdown` to any page a person navigates and it answers with the content the page shows as one Markdown document, with absolute links to the other pages: `/` (including `?keyword=`), `/timeline`, `/thread?url=`, `/following`, `/replies`, `/liked-by?target=`, `/likes/<id>` and a removed post's 410. It is the content, not the interface: no menus, forms or buttons. A post's own address (`/posts/<slug>`) returns the post as a document, with its frontmatter (see the API above).

The feeds do the same: `/feed.xml` (with `?page=` and `?category=`), `/posts/<slug>/replies.xml`, `/likes.xml` and `/following.opml` return their content as Markdown for `Accept: text/markdown`, with "Older posts" and "Newer posts" links for the pages of a feed. A feed reader that accepts anything (`*/*`, or `application/rss+xml`) keeps getting the XML. Pages without content of their own (the login and edit forms) stay HTML, and the pages for the owner still need the owner's session. Posts from other people's feeds are passed through as their authors wrote them, so a reader of the document should treat them as untrusted.

### Which feeds Weft can follow

RSS 2.0 (what Weft publishes, and most blogs), RSS 1.0 (`rdf:RDF`, as Slashdot publishes) and Atom, in UTF-8, ASCII, ISO-8859-1 / Windows-1252 or ISO-8859-15. Other encodings are not read yet, and the error says so. Following a site's address finds its feed from the page: an RSS feed if it advertises one, otherwise an Atom feed. From an Atom feed Weft reads the title, link, logo (shown as the author's avatar) and `rel="next"` page, and for each entry its address (or id), title, text (content, else summary, as plain text), dates, categories (as keywords) and the post it replies to (`thr:in-reply-to`). Weft itself publishes RSS 2.0 only.

## Running a server

This part is for the person who runs a loom. For one account at your own address, use the [Docker install](docs/docker.md); for many accounts under your own domain, see [docs/hosting.md](docs/hosting.md).

### Docker and hosting

`docker/compose.yaml` runs one account at your own address (`weft serve -host weft.example.com -user me`: the account is made at the first start and the host is its address, with no DNS proof, no sign-up and no domain settings), with Caddy in front for the certificate. See [docs/docker.md](docs/docker.md). To host many accounts under your own domain (Cloudflare, Caddy, optionally nginx), see [docs/hosting.md](docs/hosting.md).

### Data, backups and moving

#### Data folder

The server keeps its data in a folder (`-data DIR`, `weft` by default): `instance.db` holds the feeds everyone follows and the list of users, and each user has a **spool** of their own: the file where your posts, replies, likes, follows and pictures are saved, such as `users/ab/alice.db`, together with the login. The spool is what `weft export` writes and `weft move` carries to another server; the server (the loom) only weaves spools into sites. A database from before this layout (a single `weft.db`) is converted once with `weft split -db weft.db -data weft`; the old file is only read. See [docs/per-user-databases.md](docs/per-user-databases.md).

#### Stopping the server

`weft serve` stops cleanly on Ctrl-C or SIGTERM: it stops taking requests, lets the ones in progress finish (up to 15 seconds), stops the background refresh, and then closes every database. Each database that was closed properly is a single file; the `-wal` and `-shm` files only exist while the server runs (or after a crash, when the next start recovers them). The write-ahead log is capped at 1 MiB after each checkpoint (`journal_size_limit`). To copy a database while the server runs, use `sqlite3 FILE "VACUUM INTO 'copy.db'"` rather than copying the files.

#### Your data, your move

The profile page has *Download my data*: one zip with your posts, replies, likes, follows and login, without sessions or API token. `weft export USERNAME` does the same from the command line, and `weft import FILE` brings an export onto a server (it refuses a name that is taken, and prints a new API token). For backups, `weft backup -to DIR` writes consistent snapshots of the shared database and of each user who changed since the last run, while the server runs; back `DIR` up with [restic](https://restic.net) (encrypted, deduplicated, with retention and many storage backends):

```
weft backup -data weft -to /var/backups/weft-snap
restic -r sftp:backup@host:weft backup /var/backups/weft-snap
restic -r sftp:backup@host:weft forget --keep-daily 7 --keep-weekly 4 --keep-monthly 12 --prune
```

Run both from cron, one after the other. Back up `DIR`, never the live data folder: its files can be half-written. `restic restore latest --target DIR2` brings the snapshot back, and `weft restore -from DIR2 -data NEW` builds a data folder from it. Plain rsync of `DIR` works too.

#### Moving to another server

The person does it from two profile pages. Make an account with the same name on the new server (it has to be empty). On the old server, Profile, "Move to another server": give the new server (such as `weftbig.com`) and your password, and you get a move code, shown once, good for an hour. On the new server, Profile, "Move here from another server": give the old server and the code. The new server asks the old one for a copy, checks its size and checksum, the format and that the schema is not newer than it knows, then replaces the empty account with the copy. Your account at the new server keeps its own password, API token and sign-in. The old server redirects every address, the feed included, to the new one (the host changes: `https://name.old.example` becomes `https://name.new.example`), and its file waits 30 days in `trash/`. Changes are refused on the old account only for the last moment of the move; Webmentions that arrive meanwhile are kept and sent on to the new server. When the move is done the new server sends a Webmention again for every reply and like of the account, from its new addresses, so that the sites it wrote to update their links. The old account's custom domain is not moved: point it at the new server and claim it there. Stop a move that has not finished from Profile (Cancel the move) or with `weft move -cancel NAME`. The same can be done by hand: export the account, import it on the new server, then on the old one run `weft move -data DIR USERNAME https://new.example.org`. From then on every request to the old address, the feed included, gets a permanent redirect to the same place on the new one (a 301, or a 308 for a POST), for good: the name stays reserved. A running server puts the account's file in `DIR/trash/` within a minute and deletes it after 30 days; until then `weft move -cancel USERNAME` brings everything back.

#### Looking after a server

`weft fsck` checks `instance.db` and every user's file and that the directory matches the files; `-full` checks more, `-fix` rebuilds the directory when that is all that is wrong, and it exits 1 if anything is left. It only reads, so it can run beside a server (it takes about a millisecond a user). `weft reindex` rebuilds the directory on its own, which is also how a restored folder gets its follows back. `weft serve` locks the data folder, so a second server on it refuses to start, warns at startup if the limit on open files is low or if the directory and the files disagree, and with `-internal` also serves `/metrics` (pool, `instance.db` waits, users, trash) in the Prometheus text format; keep that address private.

### Custom domains

Run the server behind [Caddy](https://caddyserver.com), which gets a certificate for each host the first time it is asked for, but only if Weft says the host is real:

```
{
    on_demand_tls {
        ask http://127.0.0.1:8081/domain-ok
    }
}
https:// {
    tls { on_demand }
    reverse_proxy 127.0.0.1:8080
}
```

```
weft serve -addr 127.0.0.1:8080 -internal 127.0.0.1:8081 -domain example.org -scheme https -cname-target custom.example.org
```

`-internal` is a second, private listener that answers only that one question (200 for `name.example.org` of a user and for verified domains, 404 for anything else); keep it on a loopback address. `-cname-target` is the name shown to users as the CNAME target; point it directly at the server (not through a proxy that cannot issue certificates for other domains, such as Cloudflare's orange cloud).

#### When the domain stops working

An account that signed up with its domain depends on it. Once a day Weft checks that the TXT record is still there and that the domain reaches the server. After three failed checks in a row (three days) the domain has lapsed: the owner sees a warning on every page of their own site and on the profile page, and the administration overview lists it. If the domain has not verified again after **90 days**, the account is removed: it answers 410, its file waits 30 days in `trash/`, and the name stays taken. A passing check, or *Check now* on the profile page, starts it over. The owner can leave at any time with a move or a download; the domain of such an account cannot be changed or removed on the profile page. Accounts let in from the waiting list, accounts the operator made, and administrators' accounts have no such rule. Their custom handles, if they have one, are checked once a day too, but only that the handle still reaches the server: after three failed days in a row it stops being the account's address, which is served at its default address again (nothing is removed, and the owner can make the handle the address again from the profile page).

When an account is removed (by the operator, or because its domain lapsed) its addresses answer 410, and within a minute the server sends a Webmention again for every reply and like it had published, so that the receivers fetch the source, see the 410 and take them down; the file waits in place until those have been sent, and then goes to the trash. With a mail connector (`web.Config.Mail`, none is built in yet) the owner is also mailed when the domain lapses and 14 days before the end, at the address the owner gave at sign-up (required there, never shown to anyone; `weft user set -email ADDRESS` sets or changes it by hand).

### Limits

The server keeps one caller from using it up. Each limit answers `429 Too Many Requests` with `Retry-After`, in memory, per caller (the address, a whole /64 for IPv6):

- **Wrong passwords:** 5 per address and user, and 100 per user from everywhere, in 15 minutes; then that user's sign-in (and the Change password form) is refused, a right password included, until the window is over. A right password clears that address's count.
- **API:** 600 calls a minute (a burst of 120) per address and user, 60 writes a minute (30 at once), and 10 follows a minute. After 20 wrong or missing tokens in a minute an address is refused for a while, even with the right token.
- **Webmentions:** 30 a minute per address (20 at once). Each user also has an inbox of at most 500 Webmentions waiting to be checked, and 50 from one host; past that the server answers `503` with `Retry-After: 300`, since each one makes it fetch the sender's page.
- **Connections:** 10 seconds to send the headers, 30 to send the request, 60 to answer, 2 minutes idle.

Behind a reverse proxy every request comes from the proxy, so start the server with `-trust-proxy` and the limits use the address in `X-Forwarded-For`, the last entry, which Caddy sets to the client. Do that only behind a proxy that sets it: otherwise anyone can claim to be any address. `-no-rate-limits` turns the limits off, for load tests. Reading public pages (the feed, permalinks) is not limited by the server: put a limit on those at the proxy or a CDN (the standard Caddy has none; the `caddy-ratelimit` module adds one, and Cloudflare has rate limiting rules).

### OAuth for apps

`weft serve -oauth` (off by default) lets the people on this server connect apps that act for them through the API (see [Apps that act for you](#apps-that-act-for-you-oauth)). Off, the addresses `/oauth/…` and `/.well-known/oauth-…` answer 404 and the API accepts only each account's own token, even from an app that was connected earlier; turning it on again brings those connections back. Switching it off does not delete them.

## The fediverse (ActivityPub)

*Phase 4 (first half) of 6: an account can be found and followed from Mastodon and the rest of the fediverse, its posts go out to its followers, and replies, likes and boosts from the fediverse come back. Writing replies and likes to fediverse posts, and following people, come next.*

`weft serve -activitypub` (off by default) lets each account switch ActivityPub on, on its profile page (also off by default). Switched on, the account answers:

- **WebFinger** at `/.well-known/webfinger?resource=acct:name@host` (or the actor's address), as `application/jrd+json`, naming the actor. `host` is the account's address with or without a port.
- **The actor** at `/`, the same address as the home page: a request with `Accept: application/activity+json` (or `application/ld+json` with the ActivityStreams profile) gets a `Person` with the name, the bio as the summary, the picture, `inbox`, `outbox` and the public key. Every other `Accept` keeps getting the page or the Markdown.
- **NodeInfo 2.1** at `/.well-known/nodeinfo` and `/nodeinfo/2.1`.
- **The inbox**, `POST /inbox`, and the collections `/followers` and `/following` (as the actor's `followers` and `following`; they say how many, not who). `/outbox` lists the posts that were sent (the first 20, newest first, as their Create activities).

**The inbox.** Every request must be signed (HTTP signatures as Mastodon sends them: `rsa-sha256` over `(request-target)`, `host`, `date` and the `Digest` of the body, which must match, with a `Date` within an hour). Weft fetches the key from the sender's actor (never from an address on its own network, and signed with the receiving account's key, because servers such as mastodon.social answer nothing else), keeps it for a day, and fetches it again, at most once a minute, if a signature does not fit, because keys change. An activity counts only if its `actor` is the one that signed it. Handled: `Follow` (the follower is stored, and a signed `Accept` goes back to its inbox; asking again is answered again), `Undo` of a `Follow` and `Delete` of the actor (the follower is removed). Replies, likes and boosts are described below; every other activity is acknowledged with 202 and ignored. Followers are listed on the profile page, for the owner only, and are part of an export. Accounts do not approve followers one by one. The `Accept` is delivered from the queue described below.

**Posts go out.** With ActivityPub on, a new post is sent to the account's followers as a `Create` of a `Note`, an edit as an `Update` and a removal as a `Delete` of a `Tombstone`; it makes no difference whether the post was made on the web, with the API or with an editor. Each activity is kept in the account's file (`ap_outbox`) and delivered from the same durable queue as Webmentions, so a restart loses nothing: to each follower's shared inbox if its server has one, once per server, signed with the account's key. A delivery that fails (a server that is down, busy or answering 5xx or 429) is tried again after 1, 2, 4 minutes and so on up to 6 hours, twelve times in all, which is a day or so; any other answer, a 4xx, is final, and a 410 removes the followers on that inbox.

- **The Note.** Its id and `url` are the post's own address, which answers with the Note when asked for `application/activity+json` (and with a `Tombstone` and 410 once the post is removed), so a server that looks the post up finds it. Audience: public, and the followers collection.
- **The text.** It is the text of a post on Mastodon: the whole post, with the title as a bold first line, and no link back to the site. A link would give the post a preview card that repeats it; the post's address is the Note's `url`, which Mastodon offers as "open original page". The post's HTML is cut down to what Mastodon keeps: paragraphs, links, emphasis, lists, quotes and code stay, headings become bold paragraphs, and pictures leave the text and go as attachments (the first four, with their alt text). Only a post of more than 10,000 characters, which other servers may refuse, goes as its first paragraphs (about 240 to 400 characters, cut at a word if one paragraph is longer) and a "Read more" link to the post.
- **Hashtags.** Keywords become hashtags (letters, digits and underscores kept: "Indie Web" is `#IndieWeb`; a keyword of only digits is left out), at most ten, with a line of them under the text and the `tag` entries.
- **What does not go.** A reply and a repost stay on Weft: a reply names a page, and the fediverse can only be replied to on one of its own posts, which comes with the next phase. A post made before ActivityPub was switched on is not sent, and neither is its edit; only what the fediverse has is edited or removed. A removed post's earlier activities are emptied in the account's file, and one that is still waiting goes nowhere.

**What comes back.** From the fediverse, to one of the account's posts:

- **A reply** (a `Create` of a `Note` whose `inReplyTo` is the post, public or unlisted) is kept like a reply that came by Webmention: it waits on the Replies page for the owner to approve it, and shows under the post once approved. It is kept as Markdown, made from the HTML Mastodon sends (links, emphasis, quotes and code stay; mentions and hashtags become plain text), without the mention that addressed the account. A reply to followers only, or a direct message, is never kept, and neither is a reply to something that is not one of the account's posts. At most 500 replies wait at once; past that a stranger's reply is dropped.
- **An edit or removal** of that reply by its sender (`Update`, `Delete`) changes or removes it, and the owner's approval stays as it was.
- **A like** (`Like`) is a received like, in the tally and the "liked by" list of the post, with the sender's name and handle. A boost (`Announce`) is counted with the likes and shown as "(boosted)" in the list. `Undo` of either takes it back.

Everything must come from the actor that signed it, and an object it names must live on that actor's own server: one actor cannot change or remove what another sent.

**The actor's id is pinned.** It is the account's origin and `/`, fixed when ActivityPub is first switched on (with the custom domain if the account has one), because servers that know the account know it by that address. The instance's own address redirects to the custom domain as for every page, and an address that is no longer the pinned one answers nothing here (the profile page says so). Set up a custom domain before switching it on if you want one. Switching off keeps the address and the key, so switching on again is the same account.

**A handle on your own domain** is that domain's WebFinger file pointing at the actor, for example `https://example.com/.well-known/webfinger` answering with `{"subject":"acct:you@example.com","aliases":["https://you.example.net/"],"links":[{"rel":"self","type":"application/activity+json","href":"https://you.example.net/"}]}`, served as `application/jrd+json`. A redirect to the Weft address works as well. Weft does not need to serve the domain for that. Also name the domain on the profile page ("Domain of your handle"): Weft's own WebFinger then answers with `acct:you@example.com` as the subject, which is what makes Mastodon and the others show that handle and not the Weft address; without it they ask the actor's own host who it is and show what it says. The domain's file must point at the actor, as above, because the asking server checks it.

**The key.** Each account has an RSA key of 2048 bits, made when it is first switched on and kept in the account's file. An export leaves the private key out, like the sessions and the API token, and an account imported from an export makes a new key. Moving an account with its fediverse identity comes in a later phase.

Everything about apps is kept in the server's own database (`instance.db`), not in the users' files: the apps that registered (with a small copy of each logo), who connected what, and the hashes of the codes and tokens (the tokens themselves are never stored). A user's export and a move to another server carry none of it, and a move or removal ends the account's connections. Anyone can register an app, so registration is limited: 6 a minute per address (10 at once), at most 2,000 apps on the server (when it is full an app nobody ever connected, at least 10 minutes old, is dropped to make room), and at most 100 connections for one account. Expired codes and tokens, connections that were never finished and apps nobody connected within 7 days are deleted every hour. The token endpoints are limited to 60 calls a minute per address.

### Landing page and sign-ups

`weft serve -site site` serves the folder `site/` (static files, `index.html` first) at `www.example.org`. `www` is a reserved name, so no account can take it; the page is served for GET and HEAD only. The page has a waiting list for accounts: the form at `/waitlist` (account name, an email address used only to answer, where the person writes, an optional note) stores the request in `instance.db`; a hidden field catches programs, each address may send three at once and then one a minute, and the list closes at 5000. `weft waitlist list` prints a table of who asked (a reviewed mark, name, address, links; `-new` shows only those not yet reviewed), `weft waitlist review ID` marks a request as read (`-undo` takes the mark off) and `weft waitlist remove ID` takes it off the list once it is answered. `-max-users N` is the number of places the page's counter says there are (accounts and people waiting come from `/status.json`); it is only shown for now, and the operator's `weft user add` does not check it.

#### Sign-up with a domain

`weft serve -signups` (off by default) opens `www.example.org/signup`: a person who controls a domain gives `weft.` plus their registered domain (`weft.example.com`, `weft.example.co.uk`; the registered domain comes from the public suffix list) and the account name they want. They are shown a TXT record for `_weft.weft.example.com` and, when the server has `-cname-target`, a CNAME record. When the TXT record is in the DNS they choose a password and the account is made, with the domain already verified; the record is looked for again at that last step. If the CNAME already reaches the server the domain becomes the address at once, otherwise the account is at `name.example.org` until the person presses *Check now* on their profile. One account for each registered domain, the name and the domain are held for an hour while the DNS spreads, the places are those of `-max-users`, and attempts are limited per address. With `-signups` off `/signup` answers 404. The landing page shows its sign-up button only when sign-ups are on.

The form and the counter need Weft to serve the page; on a separate static host they do not work. The operator's email address on the page is put together by a script from two parts in `site/index.html` (`data-u` and `data-d` on the `mail` elements), so it is changed in one place.

### Administration

`weft user admin NAME` marks an account as an administrator's: it can sign in to the administration pages and is not counted toward `-max-users`. `weft serve -admin -internal 127.0.0.1:8081` serves the pages on that private address (reach them over an SSH tunnel); `-admin-public` also serves them at `admin.<domain>` on the public address, which must then be behind Cloudflare Access or a VPN, since the pages are only as private as the way to them. They are off unless `-admin` is given. Administrators sign in with their own account's password (a temporary one is refused: choose a new one at your own site first), for eight hours, with a cookie that only the admin host receives. The pages show the numbers and the state of the accounts, never what anyone wrote: **Overview** (accounts against the cap, switched off, deleted, moved, the waiting list, sign-ups waiting, mentions held for moving accounts, disk used and free), **Accounts** (name, state, domain, size of the file, created, last post; make an account with a temporary password within the cap, switch one off or on, delete one), **Waiting list** (read, unread, *Make account*, remove) and **Sign-ups** waiting for their DNS record, and a log of the ones that did not end in an account (a refused form, a DNS record that was not there, an hour that ran out; the name and the domain tried and why, no email addresses or callers' addresses, the last 1000 entries and nothing older than 30 days). A switched-off account answers 410 at every address; a deleted one answers 410 too, its file waits 30 days in `trash/`, the name stays taken, and *Bring back* restores it until the file is purged. An administrator cannot be switched off or deleted. The same from the command line: `weft user suspend|restore NAME` and `weft user remove -yes NAME` (these skip the cap). `weft user add` respects the cap the server last ran with (`-max-users`, which the server keeps in `instance.db`): at the cap it refuses, unless you add `-over-cap` to make the account anyway, or `-admin` to make the operator's own account, which is not counted.

### Behind Cloudflare

Cloudflare can sit in front of the server for the users' addresses (`name.example.org`): a wildcard DNS record `*.example.org` can be proxied (the orange cloud) on every plan, and the free Universal certificate covers one level of names under the domain, which is what user addresses are. An exact record, such as `www`, wins over the wildcard. Pictures are then cached at Cloudflare's edge as well as in browsers: they carry `Cache-Control: public, max-age=31536000, immutable` and no cookies, and the smaller copies (`?w=800`, `?w=240`) are separate entries because the query string is part of the cache key. Check with `curl -sI https://alice.example.org/media/<name>?w=800` twice: the second answer should say `cf-cache-status: HIT`. Things to know:

- A user's own domain (a CNAME to `-cname-target`) is not in your zone, so your Cloudflare cannot serve it. The user can put it behind their own Cloudflare: verify the TXT record as usual, add the CNAME with the proxy on, set SSL/TLS to Full (strict) so Cloudflare talks HTTPS to your server (Flexible would loop with the redirect to HTTPS), and keep the `-cname-target` record in your zone DNS-only (a proxied target in another account gives Cloudflare error 1014). The domain becomes the address once it reaches the server, which works through the proxy; the profile page shows the CNAME as "not found yet" until then, since a proxied name hides it. Not yet tried with a real account.
- A layout that serves both: proxy the wildcard (`*.example.org`) and add an exact, DNS-only record for the target (`weft.example.org`, run with `-cname-target weft.example.org`). The exact record wins over the wildcard, so only that name skips the proxy, and users with their own domain CNAME to it from their own proxied record. The cost is that the target shows your server's real address; the name `weft` is reserved so nobody can take it. Also untested.
- A deleted picture stays in browsers and at the edge until it expires (up to a year) or you purge it, since its address is never reused for other content.
- Leave Cloudflare's image features (Polish, Mirage) off for the media paths: the server already makes the smaller copies.
- The limits count requests per address, so the server has to see the visitor's address and not Cloudflare's: have the proxy in front of Weft pass it on, and see `-trust-proxy` under [Limits](#limits).

### Speed

Pages, style sheets, feeds and JSON are sent gzip-compressed to a client that accepts it (a fifth of the size or less); pictures are not, and neither is a range request. The thread page keeps the posts it fetched from other servers for a minute, so opening a reply in a deep thread fetches mostly what is new (a drill-down of eight clicks in a thread eight levels deep went from 51 fetches to 16); an edit or delete made on this server shows at once, one made elsewhere shows within a minute. Every page has a meta description: the author's bio, or for a post its first words. Pictures in posts carry their width and height and load lazily after the first (see Pictures), and avatars and cards have fixed sizes, so a page does not jump while it loads.

### Monitoring

To watch the numbers over time, point a [Prometheus](https://prometheus.io) server at the private address (the format is plain text, so `curl http://127.0.0.1:8081/metrics` shows it too). In `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: weft
    scrape_interval: 15s
    static_configs:
      - targets: ["127.0.0.1:8081"]   # the -internal address
```

Two numbers worth an alert: `rate(weft_pool_evictions_total[5m])` staying high means the cap on open user databases is too small for the traffic (raise `ulimit -n`), and `rate(weft_instance_read_wait_seconds_total[5m])` rising means reads of `instance.db` are queueing for a connection.

## Development

### Try the UI on a real conversation

`make hackernews` copies the current Hacker News front page into a new data folder, `hackernews/` (it refuses to touch an existing one): each of the top 20 stories and all its comments become posts in their authors' own feeds, every comment is an approved reply to its parent with the thread root set, and every author is a user (names are lowercased, `_` becomes `-`). Story points become anonymous likes, since Hacker News does not publish who voted or what comments scored. Then `make run-hn` serves it on port 8090 (`HNPORT=` and `HNDATA=` override). Log in at `http://me.localhost:8090/login` with password `hackernews`; that user follows every story's submitter, so the stories are already in its timeline.

The importer is `internal/tools/hn-import`, a test-data tool and not part of the server. It writes to the data folder directly through the store (`make hackernews` builds it as `bin/hn-import`), so it needs no running server and cannot be run against a live one. Its flags are `-data`, `-n` (how many stories, default 20), `-port`, `-domain`, `-scheme`, `-viewer` and `-password`; the post addresses it stores contain the port, so serve the data on the port you gave it.

### Icons

`img/` holds the logo at every size (16 to 1024 px, the 500 px avatar, and the square SVG). The ones the site uses are compiled into the binary from `internal/web/static/` and served at `/static/` and `/favicon.ico` on every host: the favicon (`.ico` and SVG), the Apple touch icon, the 192 and 512 px icons for `site.webmanifest`, and the SVG logo in the page footer.

### Look

Every page has a header with the logo on the left and the site name, a theme button and Log in or Log out on the right. The styles are in `internal/web/static/weft.css` (compiled in), built on indigo `#26337A`, thread orange `#F08A4B` and off-white `#EEF0FA`. The theme button cycles auto (follows the system), light and dark, and the choice is remembered in the browser.

## Licence and authorship

Copyright (C) 2026 Henry van den Broek. SPDX-License-Identifier: AGPL-3.0-or-later.

Weft is free software: you can redistribute it and modify it under the terms of the [GNU Affero General Public License](LICENSE) as published by the Free Software Foundation, either version 3 of the License, or (at your option) any later version. It is distributed in the hope that it will be useful, but without any warranty. You may run it, study it, change it and share it. If you run a changed version as a service for other people, the AGPL asks you to offer them the source of your changes (the "Source" link in the footer of the pages is how this loom does it). Running it unmodified, for yourself or for others, asks nothing more.

The libraries Weft is built with keep their own licences, listed in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES).

All code and documentation in this project was written by Claude, Anthropic's AI model. The functionality, the architecture and the verification are the work of Henry van den Broek.
