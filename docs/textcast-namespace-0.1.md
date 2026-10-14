# Textcast Namespace 0.1 (Draft)

Oct 12, 2026 · @Henry

## Status and purpose

This is a first working draft of an RSS 2.0 namespace for textcasting: two-way, Markdown-based publishing where every post lives in its author's own feed. It defines how a post marks itself as a reply, how threads are reassembled, how likes are counted, and how feeds move or get edited.

The spec only adds what RSS 2.0 lacks. Anything RSS 2.0, Winer's Source namespace or Webmention already covers is reused, not redefined. Elements are marked **required**, **recommended** or **optional**.

## Design principles

1. **Titles are optional but allowed.** A post may be a one-line note with no `<title>`, or a long titled essay. Apps must handle both equally well. RSS 2.0 already permits this: an item needs at least one of `<title>` or `<description>`. In textcasting, `<description>` is always present, so `<title>` is truly optional.
2. **Markdown is the source of truth.** Authors write Markdown. The feed carries both rendered HTML (for any reader) and the original Markdown (for textcasting apps).
3. **No length limit.** There are no threads of numbered posts; a long thought is one post.
4. **Posts are editable.** Edits update the item in place and notify anyone the post replied to.
5. **You own your words.** Every post, reply and like lives in its author's own feed at its author's own URL.
6. **Public by design.** Everything in a textcast feed is public. Private posts and messages are out of scope.
7. **Degrade gracefully.** A plain RSS reader that ignores this namespace still shows every post correctly.

## Namespace declaration

The namespace prefix is `tc`. The URI below is a placeholder until a permanent home is chosen.

```xml
<rss version="2.0"
  xmlns:tc="https://example.org/ns/textcast/0.1"
  xmlns:source="http://source.scripting.com/"
  xmlns:atom="http://www.w3.org/2005/Atom">
```

Reused namespaces:

| Prefix | Used for |
| --- | --- |
| `source:` | `source:markdown`, the post's original Markdown (Dave Winer's Source namespace) |
| `atom:` | `atom:link rel="self"` and `rel="hub"` for WebSub pings |
| RSS 2.0 core | `<cloud>` for rssCloud pings, `<category>` for tags, `<enclosure>` for media |

## Channel elements

The author's profile uses standard RSS 2.0: `<title>` is the author or publication name, `<description>` the bio, `<image>` the avatar, `<link>` the homepage. The namespace adds:

| Element | Status | Meaning |
| --- | --- | --- |
| `<tc:webmention>` | Required | URL of the author's Webmention endpoint, where replies and likes are sent |
| `<tc:following>` | Recommended | URL of an OPML file listing the feeds this author follows; the public follow graph used for trust weighting |
| `<tc:likesFeed>` | Recommended | URL of the author's separate likes feed |
| `<tc:movedTo>` | Optional | New feed URL when the feed has moved; readers update their subscription |
| `<tc:key>` | Optional | URL of the author's public signing key, for verifiable snapshots |

For update pings, publish either an RSS 2.0 `<cloud>` element (rssCloud) or `<atom:link rel="hub">` (WebSub). At least one is recommended.

## Item elements

Every post, reply and like is an RSS 2.0 `<item>`.

### Core elements

| Element | Status | Rule |
| --- | --- | --- |
| `<guid isPermaLink="true">` | Required | The post's HTML permalink. This URL is the post's identity everywhere: in replies, likes and Webmentions. |
| `<description>` | Required | Rendered HTML of the post. |
| `<title>` | Optional | Include only if the author gave one. Apps must not invent titles from the first line. |
| `<source:markdown>` | Recommended | The original Markdown, unaltered. |
| `<pubDate>` | Required | First publication time. |
| `<tc:updated>` | Optional | Time of the latest edit, RFC 822 format like `pubDate`. Present only on edited items. |
| `<category>` | Optional | Tags; one element per tag. |
| `<enclosure>` | Optional | Attached media, as in podcasting. |

### Reply elements

`<tc:inReplyTo>` marks the item as a reply. It is the child's upward link, and it survives even if the parent rejects or deletes the reply.

```xml
<tc:inReplyTo href="https://alice.example/2026/10/12/notes"
              feed="https://alice.example/feed.xml"/>
```

- `href` (required): the parent's permalink, equal to the parent's `guid`.
- `feed` (recommended): the parent's feed URL, so readers can fetch it directly.

`<tc:root>` names the post that started the thread, like email's `References` header. Any reply can be placed in its conversation even if middle posts disappear.

```xml
<tc:root href="https://alice.example/2026/10/12/notes"
         feed="https://alice.example/feed.xml"/>
```

- Required on every reply. Omitted on the root post itself.
- When replying directly to the root, `tc:root` and `tc:inReplyTo` point to the same URL.

`<tc:context>` carries a short snippet of the parent, so the reply still makes sense if the parent goes offline.

```xml
<tc:context href="https://alice.example/2026/10/12/notes"
            retrieved="Mon, 12 Oct 2026 22:30:00 -0400">
  RSS plus links already gives you a social web.
</tc:context>
```

- Recommended. Plain text, kept brief: no more than about 300 characters.
- It is a quote for context, not a copy. Apps must not display it as the parent post when the parent is reachable.
- Optional `signature` attribute: the parent's signature over its own text, verifiable with the parent's `tc:key`.
- **Required when the parent is not a textcast post.** A reply to a plain RSS 2.0 item treats the parent as if it were already deleted: the link and the snippet are all a reader can rely on.
- Optional `origin` attribute: `author` (written by the replier, the default) or `generated` (summarized by an app, for example with AI). Readers may label generated context, and authors should review it before publishing.

### Like elements

`<tc:likeOf href="…"/>` marks the item as a like (an upvote) of the post at `href`. A like item has no `<title>` and may have an empty `<description>`. Likes are published in the likes feed, not the main feed.

### Reply tally elements

- `<tc:replies href="…" count="7"/>` (recommended): the URL of this post's replies feed and the number of approved replies.
- `<tc:likes count="12"/>` (optional): the number of verified likes received. It is a cache; the likes themselves live in the likers' feeds and can be recounted.

## Replies feed

Each post may publish a replies feed: an RSS 2.0 feed of the replies its author has approved. It is the parent's downward link to its children, and it lets readers rebuild a thread one level at a time.

Channel elements of a replies feed:

- `<tc:repliesTo href="…"/>` (required): the permalink of the post these replies belong to.
- `<tc:depth>` (optional, default 1): how many levels of descendants this feed caches.
- `<lastBuildDate>` (required): when the cache was last refreshed.

Each `<item>` is a reference to a reply, not a full copy:

- `<guid>` and `<link>`: the reply's permalink.
- `<description>`: a short excerpt for display.
- `<author>` or `<tc:feed href="…"/>`: who wrote it, by feed URL.
- `<tc:inReplyTo>`: the reply's parent. Required when `tc:depth` is greater than 1, so deeper items can be placed in the tree.
- `<tc:likes count="…"/>`: the reply's like tally, if known.

### Cached subtrees

With `tc:depth` of 2 or 3, one fetch returns most of a conversation. Readers rebuild the tree from each item's `tc:inReplyTo`. A post's cache goes stale when a deeper reply arrives; the update rule under Processing rules keeps it fresh.

### Curated and raw views

The replies feed is the curated view: only approved replies. The raw view is every item anywhere whose `tc:inReplyTo` or `tc:root` points into the thread, as an index sees it. Apps should let readers switch between them.

## Likes feed

Likes go in a separate feed, linked from the main feed's `tc:likesFeed`, so they don't bury posts for subscribers. Each like is an item with a `guid` permalink of its own and a `tc:likeOf`. Each like is also sent to the liked post's Webmention endpoint.

Only upvotes are public. A downvote is a private moderation signal to the post owner, not a feed item; its mechanism is an open question.

## Permalink page requirements

Every item's `guid` must resolve to an HTML page. Webmention verification, search engines and people without feed readers all depend on it. The page must include:

- `<link rel="alternate" type="application/rss+xml" href="…">` pointing to the author's feed.
- `<link rel="webmention" href="…">` pointing to the author's endpoint.
- For a reply or like, a visible `<a href>` to the parent or liked post. Webmention receivers check for this link.

Recommended for IndieWeb compatibility: microformats2 markup (`h-entry`, with `u-in-reply-to` on replies and `u-like-of` on likes).

## Processing rules

### Publishing a post

1. Render the Markdown to HTML and write the permalink page.
2. Add the item to the feed, with `source:markdown`, and `tc:inReplyTo`, `tc:root` and `tc:context` if it is a reply.
3. If it is a reply, send a Webmention to the parent. Sending one to the root as well is optional.
4. Ping subscribers and indexes through rssCloud or WebSub.

### Receiving a Webmention

1. Fetch the source URL and confirm it links to the target post. If not, reject it.
2. Read the source's feed item. If it has `tc:inReplyTo` matching the target, treat it as a reply; if it has `tc:likeOf`, treat it as a like; otherwise it is a plain mention.
3. Apply moderation: auto-approve senders the author follows (from `tc:following`), queue others, drop blocked domains.
4. On approval, add the reply to the post's replies feed and update `tc:replies` and `tc:likes` counts.
5. **Bubble up.** If this post is itself a reply, re-send its own Webmention to its parent. Under the Webmention spec, a re-sent Webmention means the source changed, so the parent refreshes its cached subtree. Repeat up to the root, or stop at the parent's `tc:depth`.

### Editing a post

Update the item in place, set `tc:updated`, and re-send Webmentions to every post it replied to or liked. The `guid` never changes.

### Deleting a post

The permalink returns HTTP 410 Gone, ideally with a small tombstone page. Remove the item from the feed and re-send Webmentions; receivers seeing 410 remove the reply or like. Replies to the deleted post keep their own place in the tree.

### Moving a feed

Redirect the old feed URL with HTTP 301 and add `tc:movedTo` to the old feed. Keep both in place for at least 90 days. Permalinks should redirect too, so replies keep working.

### Orphaned replies

Readers handle a reply whose parent is unavailable as follows:

| Parent state | How readers show the reply |
| --- | --- |
| Rejected (parent online, reply not in its replies feed) | As an unapproved branch, visible in the raw view, collapsed or hidden in the curated view |
| Offline (no response) | Under its `tc:root`, with its `tc:context` snippet standing in for the parent |
| Deleted (410 Gone) | Under a tombstone reading "original post removed"; showing `tc:context` is the app's choice |

In every case, replies further down the branch stay attached to their own parents.

### Replying to any RSS 2.0 item

A textcast app can reply to an item in any RSS 2.0 feed, not just textcast feeds. The parent needs no `tc:` elements and its author needs no textcast app. The reply is built as if the parent were already deleted:

1. **Point up.** `tc:inReplyTo href` is the item's `guid` when `isPermaLink` is true, otherwise its `<link>`. The `feed` attribute is required here, since nothing else identifies the source feed.
2. **Set the root.** The non-textcast item has no thread ancestry, so it is the root: `tc:root` equals `tc:inReplyTo`.
3. **Carry context.** `tc:context` is required. The app can draft it from the item's title and description, by excerpting or summarizing with AI, marked `origin="generated"`. The author can edit it before publishing.
4. **Notify if possible.** If the item's page advertises a Webmention endpoint, send one. If not, the parent's author is never notified; the reply is still found through indexes and by readers following both feeds.

This makes the whole existing RSS web, including blogs, news sites and podcasts, something you can reply to on day one.

## Examples

### Alice's feed, with an untitled root post

```xml
<rss version="2.0"
  xmlns:tc="https://example.org/ns/textcast/0.1"
  xmlns:source="http://source.scripting.com/">
<channel>
  <title>Alice</title>
  <link>https://alice.example/</link>
  <description>Notes on the open web.</description>
  <tc:webmention>https://alice.example/webmention</tc:webmention>
  <tc:following>https://alice.example/following.opml</tc:following>
  <tc:likesFeed>https://alice.example/likes.xml</tc:likesFeed>
  <cloud domain="rpc.example.org" port="80" path="/rsscloud/pleaseNotify"
         registerProcedure="" protocol="http-post"/>

  <item>
    <!-- No <title>: a short note -->
    <guid isPermaLink="true">https://alice.example/2026/10/12/notes</guid>
    <description>&lt;p&gt;RSS plus links already gives you a social web. Who's building the &lt;em&gt;reader&lt;/em&gt; half?&lt;/p&gt;</description>
    <source:markdown>RSS plus links already gives you a social web. Who's building the *reader* half?</source:markdown>
    <pubDate>Mon, 12 Oct 2026 21:00:00 -0400</pubDate>
    <category>textcasting</category>
    <tc:replies href="https://alice.example/2026/10/12/notes/replies.xml" count="1"/>
    <tc:likes count="1"/>
  </item>
</channel>
</rss>
```

### Bob's reply, with a title

```xml
<item>
  <title>The reader half is the hard part</title>
  <guid isPermaLink="true">https://bob.example/posts/reader-half</guid>
  <description>&lt;p&gt;Search and threads are what's missing. Here's a sketch…&lt;/p&gt;</description>
  <source:markdown>Search and threads are what's missing. Here's a sketch…</source:markdown>
  <pubDate>Mon, 12 Oct 2026 22:15:00 -0400</pubDate>
  <tc:updated>Mon, 12 Oct 2026 22:40:00 -0400</tc:updated>
  <tc:inReplyTo href="https://alice.example/2026/10/12/notes"
                feed="https://alice.example/feed.xml"/>
  <tc:root href="https://alice.example/2026/10/12/notes"
           feed="https://alice.example/feed.xml"/>
  <tc:context href="https://alice.example/2026/10/12/notes"
              retrieved="Mon, 12 Oct 2026 22:10:00 -0400">RSS plus links already gives you a social web.</tc:context>
</item>
```

### Carol's like, in her likes feed

```xml
<item>
  <guid isPermaLink="true">https://carol.example/likes/2026-10-12-1</guid>
  <description/>
  <pubDate>Mon, 12 Oct 2026 22:20:00 -0400</pubDate>
  <tc:likeOf href="https://alice.example/2026/10/12/notes"/>
</item>
```

### Alice's replies feed for the post, caching two levels

```xml
<rss version="2.0" xmlns:tc="https://example.org/ns/textcast/0.1">
<channel>
  <title>Replies to: RSS plus links already gives you a social web</title>
  <link>https://alice.example/2026/10/12/notes</link>
  <description>Approved replies</description>
  <lastBuildDate>Mon, 12 Oct 2026 22:45:00 -0400</lastBuildDate>
  <tc:repliesTo href="https://alice.example/2026/10/12/notes"/>
  <tc:depth>2</tc:depth>

  <item>
    <title>The reader half is the hard part</title>
    <link>https://bob.example/posts/reader-half</link>
    <guid isPermaLink="true">https://bob.example/posts/reader-half</guid>
    <description>Search and threads are what's missing.</description>
    <tc:feed href="https://bob.example/feed.xml"/>
    <tc:inReplyTo href="https://alice.example/2026/10/12/notes"/>
    <tc:likes count="3"/>
  </item>

  <item>
    <!-- Depth 2: a reply to Bob, untitled -->
    <link>https://dana.example/n/8812</link>
    <guid isPermaLink="true">https://dana.example/n/8812</guid>
    <description>Agreed. Webmention covers notification.</description>
    <tc:feed href="https://dana.example/feed.xml"/>
    <tc:inReplyTo href="https://bob.example/posts/reader-half"/>
  </item>
</channel>
</rss>
```

## Compatibility

- **Plain RSS readers** ignore the `tc:` elements and show every post normally, titled or not.
- **IndieWeb sites** interoperate through Webmention and microformats on permalink pages.
- **Atom Threading** (RFC 4685) users can map `thr:in-reply-to` to `tc:inReplyTo`. `tc:` adds the root pointer and context snippet that Atom Threading lacks.
- **Search** uses OpenSearch with RSS results; it is outside this namespace.

## Out of scope

- Private posts and direct messages.
- Public downvotes.
- Search and index protocols.
- Moderation policy. The spec carries signals; each app and index decides what to do with them.

## Open questions

- [ ] Permanent namespace URI and who maintains it.
- [ ] Should replies also send a Webmention to the root, so root authors see all thread activity?
- [ ] Signature format for `tc:context` and `tc:key`, or drop signing from 0.1 entirely.
- [ ] Maximum length for `tc:context`: is about 300 characters right?
- [ ] How private downvotes or flags reach a post owner.
- [ ] Whether to require the Webmention Vouch extension for senders the receiver doesn't follow.
- [ ] Default and maximum `tc:depth` for replies feeds, to keep cache sizes bounded.
