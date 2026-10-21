# Functionality

1) RSS (2.0 publisher / editor) and reader
2) support markdown, html, xml (RSS 2.0) (minimal or no xml extensions, try to use existing xml flavors, ie no new dtd)
3) support commenting both in and out
4) support likes (in and out); no dislikes, and emoji go in reply text
5) permalinks based on hosted setup


# distribution
1) Open source, everyone can build and host
2) Hosted environment

# infrastructure
1) rss links are either <name>.weftseed.com or weft.mydomain.com.
2) server renders html based app
3) server support REST API for client apps
4) markdown support

# environment

minimal 3rd parties.

## server
1) go implemented server
2) sqlite database (hosted setup 1 per user, or 1 for the instance)

## client
1) modern: HTML5, CSS, JS
2) minimal library dependencies, let me know if you want one.






# Textcasting: conversation summary

**Starting point:** Textcasting (Dave Winer's idea) applies podcasting's model to text: inbound and outbound RSS 2.0, with Markdown as the text format. The app it calls for is a two-way feed client, a Markdown writer plus a reader, with RSS as the only contract between apps.

**vs. Ghost:** Ghost publishes RSS, but its two-way social layer (Ghost 6) runs on ActivityPub, and Markdown isn't its native format. Ghost is a full publishing and membership business; textcasting is a minimal protocol habit with optional titles and no length limit.

**Search:** There's no central database, so search becomes a separate, swappable service: local search over your subscriptions, feed crawlers or indexes, and searches published as feeds you can subscribe to. This follows podcast directories (Apple, Podcast Index, Podping), extended to item-level search. It can be federated: an open, mirrored feed registry, an open ping stream, many indexes speaking OpenSearch and returning RSS, and readers that fan out queries.

**Replies and threads:** A thread is a distributed tree of posts, each at its own URL.
- Up-links: each reply carries in-reply-to plus a root pointer (like email's References header).
- Down-links: Webmention notifies the parent, which publishes a replies feed. That feed can cache several levels, with updates bubbling up toward the root.
- Spam: per-node moderation, social proximity, Vouch, shared blocklists. There's a curated view (parent-approved replies) and a raw view (the per-thread firehose).
- Votes: likes are tiny posts in the liker's own likes feed, sent by Webmention and tallied at the parent. They're verifiable and weighted by your personal trust graph. There are no downvotes; anything more than approval is a reply, and an emoji is just reply text.
- Orphans: the child's up-pointer, root pointer and a short context snippet heal the tree. Rejected replies become unapproved branches, offline parents fall back to the snippet, and deleted parents (410) become tombstones.
- Any RSS 2.0 item can be replied to: treat it as already deleted and carry a context snippet, which AI can generate (origin="generated").

**Pieces needed:** a two-way RSS 2.0 app with a Markdown editor, a Webmention handler, the reply/ref mechanism, and hosting (weft.mydomain.com, providers like myname.weftseed.com, or static sites and git repos). Also: a namespace spec, HTML permalink pages, discovery tags, a live Webmention receiver for static sites, identity and feed portability (custom domains, a moved-feed element), OPML follow lists, a notifications and moderation inbox, edit and delete rules, a separate likes feed, and search hooks. Out of scope: private posts and messages.

**Output:** "Weft Namespace 0.1 (Draft)" (written when the project was still called Text Thread Casting), an RSS namespace (weft:) defining webmention, following, likesFeed, movedTo, key, inReplyTo, root, context, likeOf, replies, likes, updated, the replies feed (repliesTo, depth), processing rules and examples. Titles are optional but allowed. It's saved as a Claude doc and as docs/weft-namespace-0.1.md in the text-thread-casting folder.

**Open questions:** permanent namespace URI; whether replies should also notify the root; signing format; context snippet length; whether to require Vouch; replies-feed depth limits.

https://en.wikipedia.org/wiki/Markdown