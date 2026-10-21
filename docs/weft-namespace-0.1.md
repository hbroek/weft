# Weft Namespace 0.1 (Draft)

Weft (Web-External Federated Threads) builds on Dave Winer's textcasting.

Oct 12, 2026 · @Henry · revised Oct 14, 2026 (reactions replace likes; `weft:context` may be a bare URL); revised Oct 20, 2026 (back to likes only: no dislikes, no emoji; emoji go in replies)

## Status and purpose

This is a first working draft of an RSS 2.0 namespace for Weft, which builds on Dave Winer's textcasting: two-way, Markdown-based publishing where every post lives in its author's own feed. It defines how a post marks itself as a reply, how threads are reassembled, how likes are counted, and how feeds move or get edited.

The spec only adds what RSS 2.0 lacks. Anything RSS 2.0, Winer's Source namespace or Webmention already covers is reused, not redefined. Elements are marked **required**, **recommended** or **optional**.

## Design principles

1. **Titles are optional but allowed.** A post may be a one-line note with no `<title>`, or a long titled essay. Apps must handle both equally well. RSS 2.0 already permits this: an item needs at least one of `<title>` or `<description>`. In textcasting, `<description>` is always present, so `<title>` is truly optional.
2. **Markdown is the source of truth.** Authors write Markdown. The feed carries both rendered HTML (for any reader) and the original Markdown (for Weft apps).
3. **No length limit.** There are no threads of numbered posts; a long thought is one post.
4. **Posts are editable.** Edits update the item in place and notify anyone the post replied to.
5. **You own your words.** Every post, reply and like lives in its author's own feed at its author's own URL.
6. **Public by design.** Everything in a Weft feed is public. Private posts and messages are out of scope.
7. **Degrade gracefully.** A plain RSS reader that ignores this namespace still shows every post correctly.

## Namespace declaration

The namespace prefix is `weft`. Parsers match on the URI, not the prefix. Feeds written before the rename declare `https://example.org/ns/textcast/0.1` with the prefix `tc`; readers accept that URI as an alias of the current one for one release. Readers also accept, for one release, the earlier `weft:reactionOf` (a `polarity` of `+` is a like, `-` is ignored), `weft:reactions plus="…"` (read as the like count) and `weft:reactionsFeed` (read as the likes feed).

```xml
<rss version="2.0"
  xmlns:weft="https://weftseed.com/ns/weft/0.1"
  xmlns:source="http://source.scripting.com/"
  xmlns:atom="http://www.w3.org/2005/Atom">
```

Reused namespaces:

| Prefix | Used for |
| --- | --- |
| `source:` | `source:markdown`, the post's original Markdown (Dave Winer's Source namespace) |
| `atom:` | `atom:link rel="self"` and `rel="hub"` for WebSub pings |
| RSS 2.0 core | `<cloud>` for rssCloud pings, `<category>` for keywords, `<enclosure>` for media, and the channel `<image>` (its `<url>`) as the author's avatar |

## Channel elements

The author's profile uses standard RSS 2.0: `<title>` is the author or publication name, `<description>` the bio, `<image>` the avatar, `<link>` the homepage. The namespace adds:

| Element | Status | Meaning |
| --- | --- | --- |
| `<weft:webmention>` | Required | URL of the author's Webmention endpoint, where replies and likes are sent |
| `<weft:following>` | Recommended | URL of an OPML file listing the feeds this author follows; the public follow graph used for trust weighting |
| `<weft:likesFeed>` | Recommended | URL of the author's separate likes feed |
| `<weft:movedTo>` | Optional | New feed URL when the feed has moved; readers update their subscription |
| `<weft:key>` | Optional | URL of the author's public signing key, for verifiable snapshots |

For update pings, publish either an RSS 2.0 `<cloud>` element (rssCloud) or `<atom:link rel="hub">` (WebSub). At least one is recommended.

## Item elements

Every post, reply and like is an RSS 2.0 `<item>`.

### Core elements

| Element | Status | Rule |
| --- | --- | --- |
| `<guid isPermaLink="false">` | Required | The post's identity: `urn:uuid:` plus a UUID (version 7 recommended) made once when the post is created. It is not a URL and never changes, so the post stays the same post if its author moves to another host. |
| `<link>` | Required | The post's HTML permalink: the author's host plus a slug, such as `https://alice.example/posts/the-reader-half.k3x9`. This URL is the post's address in replies (`weft:inReplyTo`, `weft:root`), likes and Webmentions. It is the only URL that serves the post. The slug is built from the title or the first sentence of a note plus a short random suffix, is unique in the author's namespace, and never changes after publishing, even if the post is edited. A deleted post keeps its slug reserved. |
| `<description>` | Required | Rendered HTML of the post. |
| `<title>` | Optional | Include only if the author gave one. Apps must not invent titles from the first line. |
| `<source:markdown>` | Recommended | The original Markdown, unaltered. |
| `<pubDate>` | Required | First publication time. |
| `<weft:updated>` | Optional | Time of the latest edit, RFC 822 format like `pubDate`. Present only on edited items. |
| `<category>` | Optional | Keywords: one element per keyword or phrase, as typed by the author (not Markdown, not hashtags). Writers should publish at most 10 of up to 50 characters, with no commas, and readers should apply the same limits to what other hosts publish. A reader decides how to show them; Weft shows them as chips under the post, and serves a topic feed at `feed.xml?category=<keyword>` (the posts with that keyword, matched ignoring case, paged like the main feed). The query is a convenience, not part of the format: a reader follows the feed address it is given. |
| `<enclosure>` | Optional | Attached media, as in podcasting. |

### Reply elements

`<weft:inReplyTo>` marks the item as a reply. It is the child's upward link, and it survives even if the parent rejects or deletes the reply.

```xml
<weft:inReplyTo href="https://alice.example/posts/rss-plus-links-already-gives-you.w3kp"
              feed="https://alice.example/feed.xml"/>
```

- `href` (required): the parent's permalink, equal to the parent's `<link>`.
- `feed` (optional): the parent's feed URL. A Weft reader does not need it: it reads the parent from its permalink (see *Post representations*). It matters when the parent is not a textcast post, since a plain RSS item can only be found through its feed. Required in that case (see *Replying to any RSS 2.0 item*).

`<weft:root>` names the post that started the thread, like email's `References` header. Any reply can be placed in its conversation even if middle posts disappear.

```xml
<weft:root href="https://alice.example/posts/rss-plus-links-already-gives-you.w3kp"
         feed="https://alice.example/feed.xml"/>
```

- Required on every reply. Omitted on the root post itself.
- When replying directly to the root, `weft:root` and `weft:inReplyTo` point to the same URL.

`<weft:context>` carries a short snippet of the parent, so the reply still makes sense if the parent goes offline.

```xml
<weft:context href="https://alice.example/posts/rss-plus-links-already-gives-you.w3kp"
            retrieved="Mon, 12 Oct 2026 22:30:00 -0400">
  RSS plus links already gives you a social web.
</weft:context>
```

- Recommended. In its minimal form it is an empty element carrying only `href` (and `retrieved`): the URL of the parent. A text snippet is optional: plain text, kept brief, no more than about 300 characters.
- It is a quote for context, not a copy. Apps must not display it as the parent post when the parent is reachable.
- Optional `signature` attribute: the parent's signature over its own text, verifiable with the parent's `weft:key`.
- A reply to a plain RSS 2.0 item or any other page treats the parent as if it were already deleted: the link (and the snippet, when present) are all a reader can rely on. The parent may be any URL, textcast or not.
- Optional `origin` attribute: `author` (written by the replier, the default) or `generated` (summarized by an app, for example with AI). Readers may label generated context, and authors should review it before publishing.

### Like elements

`<weft:likeOf href="…"/>` marks the item as a like of the post at `href`.

- `href` (required): the permalink of the post liked.

A like is a single positive signal: no polarity and no emoji. It names who liked with `<weft:feed href="…">Display name</weft:feed>`, the same element a replies feed uses, because the likes feed's own title is something like "Bob likes". There are no dislikes. Anything a person wants to say beyond approval is a reply, and a reply may be nothing but an emoji; readers may show such replies compactly. A like item has no `<title>` and an empty `<description>`. A user has at most one active like per post. Withdrawing it deletes the item (the permalink then returns 410). Likes are published in the likes feed, not the main feed.

### Reply and like count elements

- `<weft:replies href="…" count="7"/>` (recommended): the URL of this post's replies feed and the number of approved replies.
- `<weft:likes count="12"/>` (optional): the number of verified likes received. Only likes from senders the post's author follows are counted by default. It is a cache; the likes themselves live in the likers' feeds and can be recounted.

## Replies feed

Each post may publish a replies feed: an RSS 2.0 feed of the replies its author has approved. It is the parent's downward link to its children, and it lets readers rebuild a thread one level at a time.

Channel elements of a replies feed:

- `<weft:repliesTo href="…"/>` (required): the permalink of the post these replies belong to.
- `<weft:depth>` (optional, default 1): how many levels of descendants this feed caches.
- `<lastBuildDate>` (required): when the cache was last refreshed.

Each `<item>` is a reference to a reply, not a full copy:

- `<guid>` and `<link>`: the reply's permalink. The `guid` may be that permalink with `isPermaLink="true"`; the `urn:uuid:` identity stays in the author's own feed.
- `<description>`: a short excerpt for display.
- `<author>` or `<weft:feed href="…">Display name</weft:feed>`: who wrote it, by feed URL. The element text is an optional display name.
- `<weft:inReplyTo>`: the reply's parent. Required when `weft:depth` is greater than 1, so deeper items can be placed in the tree.
- `<weft:replies href="…" count="…"/>` (optional): where the reply's own replies are published and how many the feed's host has cached, so a reader can show a closed branch's size without opening it. Items one level down need not carry it.
- `<weft:likes count="…"/>`: the reply's like count, if known.

### Cached subtrees

A replies feed fills its second level from the replies feeds of its own replies. Reference implementation: it caches one level below each approved reply (`weft:depth` of 2) and keeps it fresh with the bubble-up rule below, so the cache only changes when a real change was reported. Deeper levels are found by reading the replies feed of a reply, one level at a time.

With `weft:depth` of 2 or 3, one fetch returns most of a conversation. Readers rebuild the tree from each item's `weft:inReplyTo`. A post's cache goes stale when a deeper reply arrives; the update rule under Processing rules keeps it fresh.

### Curated and raw views

The replies feed is the curated view: only approved replies. The raw view is every item anywhere whose `weft:inReplyTo` or `weft:root` points into the thread, as an index sees it. Apps should let readers switch between them.

## Likes feed

Likes go in a separate feed, linked from the main feed's `weft:likesFeed`, so they don't bury posts for subscribers. Each like is an item with a `guid` permalink of its own (an HTML page) and a `weft:likeOf`. Each like is also sent to the liked post's Webmention endpoint.

A person's feeds share one identity: the channel `<link>` (homepage) of the main feed and the likes feed are the same. Receivers use it to recognise that a like comes from someone they follow, even though they follow the main feed.

Likes are public. There is no dislike, and nothing private is sent to a post owner; a reader may let its user hide a post locally, which is not published.

## Post representations

A thread is built from posts, not from feeds. A feed lists an author's newest posts for subscribers, and an old post may no longer be in it. Every post therefore stands on its own at its permalink, which serves these representations by content negotiation (the URL is the same; `Vary: Accept`):

- **HTML** (the default, and for any `Accept` that includes `text/html` or `*/*`): the page described below.
- **`Accept: application/rss+xml`**: the post as a one-item RSS 2.0 feed, with every `weft:` element a feed item has: `weft:inReplyTo`, `weft:root`, `weft:replies` (where its approved replies are published), `weft:likes`. This gives a reader the post, its parent and the way to its replies in one request, whatever the age of the post.
- **`Accept: text/markdown`**: the post's markdown source.

A deleted post answers 410 Gone in every representation.

A reader that has a reply and wants its context needs only the permalinks: the reply's `weft:inReplyTo` for the parent, and `weft:replies` for the approved replies below it.

## Paged feeds

A feed shows its newest posts (100 in this implementation) and links to older ones as in RFC 5005: `<atom:link rel="next">` to the next page of older items, with `rel="previous"` and `rel="first"` on later pages. Readers that follow a feed for the first time should read the `next` links (on the same host) to get the history, and afterwards poll only the first page. A cached item newer than the oldest item on the first page that has disappeared from it was deleted; older cached items are kept.

## Permalink page requirements

Every item's `<link>` must resolve to an HTML page. Webmention verification, search engines and people without feed readers all depend on it. The page must include:

- `<link rel="alternate" type="application/rss+xml" href="…">` pointing to the author's feed.
- `<link rel="webmention" href="…">` pointing to the author's endpoint.
- For a reply or like, a visible `<a href>` to the parent or liked post. Webmention receivers check for this link.
- `<link rel="alternate" type="application/rss+xml" href="…">` to the post's own URL (the one-item representation), `<link rel="in-reply-to" href="…">` to the parent for a reply, and `<link rel="replies" type="application/rss+xml" href="…">` to the post's replies feed, so a reader that starts from the page can find the thread. The author's feed link comes first.

Recommended for IndieWeb compatibility: microformats2 markup (`h-entry`, with `u-in-reply-to` on replies and `u-like-of` on likes).

## Processing rules

### Publishing a post

1. Render the Markdown to HTML and write the permalink page.
2. Add the item to the feed, with `source:markdown`, and `weft:inReplyTo`, `weft:root` and `weft:context` if it is a reply.
3. If it is a reply, send a Webmention to the parent. Sending one to the root as well is optional.
4. Ping subscribers and indexes through rssCloud or WebSub.

### Receiving a Webmention

1. Fetch the source URL and confirm it links to the target post. If not, reject it.
2. Read the source's feed item. If it has `weft:inReplyTo` matching the target, treat it as a reply; if it has `weft:likeOf` matching the target, treat it as a like; otherwise it is a plain mention and is ignored.
3. Apply moderation: auto-approve senders the author follows (from `weft:following`), queue others, drop blocked domains. Likes are recorded once verified, but only those from followed senders count in the public count.
4. On approval, add the reply to the post's replies feed and update the `weft:replies` and `weft:likes` counts.
5. **Bubble up.** If this post is itself a reply, re-send its own Webmention to its parent. Under the Webmention spec, a re-sent Webmention means the source changed, so the parent refreshes its cached subtree. Repeat up to the root, or stop at the parent's `weft:depth`.

### Editing a post

Update the item in place, set `weft:updated`, and re-send Webmentions to every post it replied to or liked. The `guid` and the `link` never change.

### Deleting a post

The permalink returns HTTP 410 Gone, ideally with a small tombstone page. Remove the item from the feed and re-send Webmentions; receivers seeing 410 remove the reply or like. Replies to the deleted post keep their own place in the tree.

### Moving a feed

Redirect the old feed URL with HTTP 301 and add `weft:movedTo` to the old feed. Keep both in place for at least 90 days. Permalinks should redirect too, so replies keep working.

### Orphaned replies

Readers handle a reply whose parent is unavailable as follows:

| Parent state | How readers show the reply |
| --- | --- |
| Rejected (parent online, reply not in its replies feed) | As an unapproved branch, visible in the raw view, collapsed or hidden in the curated view |
| Offline (no response) | Under its `weft:root`, with its `weft:context` snippet standing in for the parent |
| Deleted (410 Gone) | Under a tombstone reading "original post removed"; showing `weft:context` is the app's choice |

In every case, replies further down the branch stay attached to their own parents.

### Replying to any RSS 2.0 item

A Weft app can reply to an item in any RSS 2.0 feed, not just Weft feeds. The parent needs no `weft:` elements and its author needs no Weft app. The reply is built as if the parent were already deleted:

1. **Point up.** `weft:inReplyTo href` is the item's `<link>`, or its `guid` when `isPermaLink` is true and there is no link. The `feed` attribute is required here, since nothing else identifies the source feed.
2. **Set the root.** The non-Weft item has no thread ancestry, so it is the root: `weft:root` equals `weft:inReplyTo`.
3. **Carry context.** `weft:context` is required. The app can draft it from the item's title and description, by excerpting or summarizing with AI, marked `origin="generated"`. The author can edit it before publishing.
4. **Notify if possible.** If the item's page advertises a Webmention endpoint, send one. If not, the parent's author is never notified; the reply is still found through indexes and by readers following both feeds.

This makes the whole existing RSS web, including blogs, news sites and podcasts, something you can reply to on day one.

## Examples

### Alice's feed, with an untitled root post

```xml
<rss version="2.0"
  xmlns:weft="https://weftseed.com/ns/weft/0.1"
  xmlns:source="http://source.scripting.com/">
<channel>
  <title>Alice</title>
  <link>https://alice.example/</link>
  <description>Notes on the open web.</description>
  <weft:webmention>https://alice.example/webmention</weft:webmention>
  <weft:following>https://alice.example/following.opml</weft:following>
  <weft:likesFeed>https://alice.example/likes.xml</weft:likesFeed>
  <cloud domain="rpc.example.org" port="80" path="/rsscloud/pleaseNotify"
         registerProcedure="" protocol="http-post"/>

  <item>
    <!-- No <title>: a short note -->
    <guid isPermaLink="false">urn:uuid:0194f3a0-5c1e-7a52-9d0b-3c7a5e2f1b44</guid>
    <link>https://alice.example/posts/rss-plus-links-already-gives-you.w3kp</link>
    <description>&lt;p&gt;RSS plus links already gives you a social web. Who's building the &lt;em&gt;reader&lt;/em&gt; half?&lt;/p&gt;</description>
    <source:markdown>RSS plus links already gives you a social web. Who's building the *reader* half?</source:markdown>
    <pubDate>Mon, 12 Oct 2026 21:00:00 -0400</pubDate>
    <category>textcasting</category>
    <category>open web</category>
    <weft:replies href="https://alice.example/posts/rss-plus-links-already-gives-you.w3kp/replies.xml" count="1"/>
    <weft:likes count="1"/>
  </item>
</channel>
</rss>
```

### Bob's reply, with a title

```xml
<item>
  <title>The reader half is the hard part</title>
  <guid isPermaLink="false">urn:uuid:0194f3b8-90aa-7c13-8e21-6d4f0a9b7c25</guid>
  <link>https://bob.example/posts/reader-half</link>
  <description>&lt;p&gt;Search and threads are what's missing. Here's a sketch…&lt;/p&gt;</description>
  <source:markdown>Search and threads are what's missing. Here's a sketch…</source:markdown>
  <pubDate>Mon, 12 Oct 2026 22:15:00 -0400</pubDate>
  <weft:updated>Mon, 12 Oct 2026 22:40:00 -0400</weft:updated>
  <weft:inReplyTo href="https://alice.example/posts/rss-plus-links-already-gives-you.w3kp"
                feed="https://alice.example/feed.xml"/>
  <weft:root href="https://alice.example/posts/rss-plus-links-already-gives-you.w3kp"
           feed="https://alice.example/feed.xml"/>
  <weft:context href="https://alice.example/posts/rss-plus-links-already-gives-you.w3kp"
              retrieved="Mon, 12 Oct 2026 22:10:00 -0400">RSS plus links already gives you a social web.</weft:context>
</item>
```

### Carol's like, in her likes feed

```xml
<item>
  <guid isPermaLink="true">https://carol.example/likes/2026-10-12-1</guid>
  <link>https://carol.example/likes/2026-10-12-1</link>
  <description/>
  <pubDate>Mon, 12 Oct 2026 22:20:00 -0400</pubDate>
  <weft:likeOf href="https://alice.example/posts/rss-plus-links-already-gives-you.w3kp"/>
</item>
```

### Alice's replies feed for the post, caching two levels

```xml
<rss version="2.0" xmlns:weft="https://weftseed.com/ns/weft/0.1">
<channel>
  <title>Replies to: RSS plus links already gives you a social web</title>
  <link>https://alice.example/posts/rss-plus-links-already-gives-you.w3kp</link>
  <description>Approved replies</description>
  <lastBuildDate>Mon, 12 Oct 2026 22:45:00 -0400</lastBuildDate>
  <weft:repliesTo href="https://alice.example/posts/rss-plus-links-already-gives-you.w3kp"/>
  <weft:depth>2</weft:depth>

  <item>
    <title>The reader half is the hard part</title>
    <guid isPermaLink="false">urn:uuid:0194f3b8-90aa-7c13-8e21-6d4f0a9b7c25</guid>
    <link>https://bob.example/posts/reader-half</link>
    <description>Search and threads are what's missing.</description>
    <weft:feed href="https://bob.example/feed.xml"/>
    <weft:inReplyTo href="https://alice.example/posts/rss-plus-links-already-gives-you.w3kp"/>
    <weft:likes count="3"/>
  </item>

  <item>
    <!-- Depth 2: a reply to Bob, untitled -->
    <link>https://dana.example/n/8812</link>
    <guid isPermaLink="true">https://dana.example/n/8812</guid>
    <description>Agreed. Webmention covers notification.</description>
    <weft:feed href="https://dana.example/feed.xml"/>
    <weft:inReplyTo href="https://bob.example/posts/reader-half"/>
  </item>
</channel>
</rss>
```

## Compatibility

- **Plain RSS readers** ignore the `weft:` elements and show every post normally, titled or not.
- **IndieWeb sites** interoperate through Webmention and microformats on permalink pages.
- **Atom Threading** (RFC 4685) users can map `thr:in-reply-to` to `weft:inReplyTo`. `weft:` adds the root pointer and context snippet that Atom Threading lacks.
- **Search** uses OpenSearch with RSS results; it is outside this namespace.

## Out of scope

- Private posts and direct messages.
- Search and index protocols.
- Moderation policy. The spec carries signals; each app and index decides what to do with them.

## Open questions

- [ ] Should replies also send a Webmention to the root, so root authors see all thread activity?
- [ ] Signature format for `weft:context` and `weft:key`, or drop signing from 0.1 entirely.
- [ ] Maximum length for `weft:context`: is about 300 characters right?
- [ ] Weighting likes by the trust graph beyond "followed or not".
- [ ] Whether to require the Webmention Vouch extension for senders the receiver doesn't follow.
- [ ] Default and maximum `weft:depth` for replies feeds, to keep cache sizes bounded.
