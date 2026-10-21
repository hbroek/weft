<img src="img/weft-icon-128.png" alt="Weft logo" width="96" align="right">

# Weft

Weft (Web-External Federated Threads) builds on Dave Winer's textcasting. Home: https://weftseed.com

A Weft host: every post, reply and like lives in its author's own RSS 2.0 feed.
See `docs/weft-namespace-0.1.md` for the namespace, `docs/design-goals.md` for goals and
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

Keywords: an optional comma-separated box under the Markdown editor (`travel, weekend project`, phrases allowed, up to 10 of 50 characters). They are published as the item's `<category>` elements and shown as chips; the API takes `"keywords": ["a", "b c"]`.

Your own posts appear in your timeline, marked "You" with an Edit link; "Hide my posts" (`/timeline?mine=0`) takes them out. One person's page (`?feed=`) never has them.

The like count is a link to a list of who liked the post, with Follow and Unfollow behind each name (`/liked-by?target=<post URL>`): on your own posts it lists everyone whose like arrived, marking the ones that are not counted because you don't follow them (following someone counts the likes they already gave, and unfollowing drops them); on a post in a feed you follow it lists the people you follow who liked it.

Chips are links. On a site, `/?keyword=pickleball` lists that author's posts with the keyword, and `/feed.xml?category=pickleball` is a topic feed of just those posts (paged like the main feed), so someone can follow one topic from one person; following the `?keyword=` page address finds that feed. A chip filters the view it is in: in your timeline it filters everything you follow (`/timeline?keyword=pickleball`), and when you are looking at one person's feed (`/timeline?feed=…`) it filters just that feed. Matching is on the whole keyword, ignoring case.

Liking: under each timeline post, "Like" publishes a like in your likes feed, and "undo" withdraws it.
API: `POST /api/likes {"target","target_feed"}`, `DELETE /api/likes {"target"}`. A like is counted at the
post only when the sender is someone the post's author follows; `GET /api/likes/received?post_id=N` shows
all of them. There are no dislikes and no emoji reactions: an emoji is just text, so put it in a reply.
(`/api/reactions`, `/reactions.xml` and `/reactions/<n>` still answer for one release; a `-` is refused.)

Threads: "Thread" under any post opens the conversation it belongs to, rooted at the original post.
The **curated** view shows only what each author approved, read down from the root through the
replies feeds. The **raw** view shows everything your reader knows, including replies that were not
approved. Replies whose parent your reader does not have are placed by the spec's rules: a deleted
parent (410) leaves a tombstone, an offline parent leaves the reply under the root, and a live but
unknown parent gets a placeholder. When the replies under one of your replies change, a Webmention
is re-sent up the chain so each ancestor's cached second level stays fresh.

To try two separate instances on one machine:

```bash
./bin/weft user add -db a.db alice && ./bin/weft user add -db b.db bob   # each prints a token
./bin/weft serve -addr :8101 -db a.db -dev &   # http://alice.localhost:8101/
./bin/weft serve -addr :8102 -db b.db -dev &   # http://bob.localhost:8102/
```

The server fetches the URLs users enter, so by default it refuses loopback and private addresses
(SSRF guard). `make run` passes `-dev` to allow them for local use. Do not use `-dev` when hosting.
If you run the binary yourself: `./bin/weft serve -dev`.

`make run VERBOSE=1` (or `weft serve -v`) logs every API call and inbound Webmention: host, method, path, status, time, and the start of the request body. Tokens are never logged.

`make follow NAME=dana TO='alice henry'` makes a user follow others (bare names on this instance, or feed URLs); the target servers must be running, since the feed is fetched.

`make help` lists the other targets, including `token` to issue a fresh API token.
Settings: `DB` (default `weft.db`; an existing `ttc.db` next to it is renamed on first start), `ADDR` (`:8080`), `DOMAIN` (`localhost`).

The build is a single static binary (`CGO_ENABLED=0`). Dependencies: `goldmark`, `modernc.org/sqlite`.

## Post addresses and identity

A post has one address and one identity, kept apart so a post can move between instances.
- **Address:** `<link>` is `https://<user host>/posts/<slug>`, for example `/posts/the-reader-half.k3x9`. The slug comes from the title (or the first sentence of an untitled note) plus a random 4-character suffix after a `.`, such as `.k3x9`. Posts made before this keep their `-k3x9` addresses. It is unique per user, never changes (even when the post is edited), and stays reserved after the post is deleted. It is the only URL that serves the post.
- **Identity:** `<guid isPermaLink="false">urn:uuid:…</guid>` is a UUIDv7 made when the post is created. It is not a URL and travels with the post.
- Likes still have instance-local addresses (`/likes/<n>`); they will get the same treatment later.

## AI agent

`weft-agent` is a separate program that participates as one user, through the public REST API and that user's
bearer token only. It likes and replies to posts on its timeline, answers replies to its own posts once
each, and can write an occasional note. Before answering it reads the branch around the post (`GET /api/thread?url=`) and gives the model the conversation, and it replies at most once per thread per cycle; if the thread cannot be read it waits for the next cycle. A lifetime budget (`-max`, default 20) stops two bots from talking forever.

```bash
make token NAME=dana                        # prints the token
# put ANTHROPIC_API_KEY=... in .env (read from the current directory), or export it
./bin/weft-agent -base http://dana.localhost:8080 -token TOKEN -persona Dana-bot -post-every 10
./bin/weft-agent -base ... -token ... -brain stub -once    # deterministic, no API key needed
```

`-style "dry and skeptical"` gives each bot its own voice (default: friendly), and `-persona` is the name it writes as. Use a separate `-state` file for each bot.

The model defaults to `claude-haiku-5-5` (`-model`). Posts the agent reads are passed to the model as untrusted
content. The `e2e/` test runs three instances and four users, one of them the stub agent: `go test ./e2e/`.

## Reading a thread

`/thread?url=<post>` (and `GET /api/thread?url=`) shows the **branch** around a post by default: the chain of posts from the root down to it, and its own replies, with no siblings. Every post on the chain, and every reply, carries a reply count (or just "replies" when the count is unknown) and a link that makes it the new focus. It costs about one fetch per level of depth, however large the whole thread is. `view=curated` shows the whole approved tree (up to a fetch limit), and `view=raw` shows everything this reader knows, approved or not. In the JSON, `focus` marks the requested post, `on_path` the chain to it, and `replies` the count on a closed branch (-1 unknown).

`replies.xml` items carry `weft:replies` (href and the count cached by the host) for the replies that have their own replies feed, so a closed branch can show its size without being opened.

A post's permalink answers to its `Accept` header: `text/markdown` returns the source, and `application/rss+xml` (without `text/html` or `*/*`) returns the post as a one-item feed, with all its `weft:` links. A thread is built from posts, not feeds: a reader asks each post's URL for the one-item form to get the post, its parent and the link to its replies, so old posts work. A feed holds the newest 100 posts and links to older pages (`/feed.xml?page=2`, RFC 5005 `rel="next"`); a new follower reads all of them.

## Try the UI on a real conversation

`make hackernews` copies the current Hacker News front page into a new `hackernews.db` (it refuses to touch an existing file): each of the top 20 stories and all its comments become posts in their authors' own feeds, every comment is an approved reply to its parent with the thread root set, and every author is a user (names are lowercased, `_` becomes `-`). Story points become anonymous likes, since Hacker News does not publish who voted or what comments scored. Then `make run-hn` serves it on port 8090 (`HNPORT=` and `HNDB=` override). Log in at `http://me.localhost:8090/login` with password `hackernews`; that user follows every story's submitter, so the stories are already in its timeline.

## Icons

`img/` holds the logo at every size (16 to 1024 px, the 500 px avatar, and the square SVG). The ones the site uses are compiled into the binary from `internal/web/static/` and served at `/static/` and `/favicon.ico` on every host: the favicon (`.ico` and SVG), the Apple touch icon, the 192 and 512 px icons for `site.webmanifest`, and the SVG logo in the page footer.

## Look

Every page has a header with the logo on the left and the site name, a theme button and Log in or Log out on the right. The styles are in `internal/web/static/weft.css` (compiled in), built on indigo `#26337A`, thread orange `#F08A4B` and off-white `#EEF0FA`. The theme button cycles auto (follows the system), light and dark, and the choice is remembered in the browser.

## Avatars

A user can have an avatar image address (`http://` or `https://`): set it on the Profile page (the "Edit profile" link under your bio) or with `weft user set -avatar URL NAME` (`weft user add` takes `-avatar` too, and `-avatar ""` removes it). It shows in the header and on your cards, and it is published as the feed's RSS `<image>`, so people who follow you see it on your posts in their timeline. Without one, or if the image fails to load, a circle with the first letter of the name is shown. Avatars from other feeds are only used if they are plain http(s) addresses, and are loaded without sending a referrer.

## Weft it (bookmarklet)

Your home page shows a "Weft it" link to drag to the bookmarks bar. Clicking it on any page opens the compose form with the page's title, description and image (from Open Graph or Twitter tags, falling back to the page itself). It also checks whether the page can take a Webmention (a `rel="webmention"` link in the page, or in the `Link` header of its response). If it can, the form starts as a **reply to the page**; if not, as a **bookmark card** (the image, the description as a quote and a link). A link under the form switches between the two, and nothing is published until you press Publish. A reply to a page carries what the page looked like as optional attributes of `weft:context` (`title`, `description`, `image`), so followers see a card for it. The API takes the same as `"context": {"title": "", "description": "", "image": ""}` next to `in_reply_to`.

Post and like pages mark up their author as an `h-card` (`p-author` with the display name, the site address and, if set, the avatar as `u-photo`) next to the `h-entry`, so Webmention receivers that read microformats can show who wrote a reply.

**Liking a page that is not on Weft:** in the compose form the bookmarklet opens, the **Bookmark · Reply · Like** links at the top choose what to do with the page. **Like** asks for one click, publishes the like in your likes feed together with the page's title, description and image (as optional attributes of `weft:likeOf`), and tells the page with a Webmention if it can take one. Your likes, and those of people you follow, then read "liked “The web of links” (blog.example)" under "Show likes", and your own likes there have an Unlike button. The API takes `"context": {"title": "", "description": "", "image": ""}` next to `target` on `POST /api/likes`.
