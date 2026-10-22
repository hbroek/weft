# Plan: one SQLite database per user

Status: proposal, Oct 21, 2026. Nothing here is built yet. The WAL cap and the clean shutdown that this plan relies on are already in (`0a04210`).

## Why

A Weft site belongs to one person, and apart from the public feeds nothing in it is shared with other users. A database file per user makes that true in the storage as well:

- **Portability.** Export is one consistent file, import is putting it back. Backup, "delete my account" and moving to another instance become file operations.
- **Isolation by construction.** The likes bug of Oct 21 (a likes cache shared by all users on the instance listed people the owner did not follow) cannot be written when a user's code cannot see other users' rows.
- **Parallel writes.** SQLite allows one writer per file; today every user's writes queue behind every other's.
- **Cheap schema changes.** Each file carries its own version and is upgraded when it is first opened.

It is not mainly a speed change: one SQLite file holds far more than a small instance needs. The costs are listed under Risks.

## Decisions already made

1. **Split by ownership.** A user's file holds everything that is theirs: profile and credentials, posts, likes, received replies and likes with the owner's decisions on them, follows, the Webmention queue, and any cache that only that user needs. Caches that are a function of the public network and keyed by URL (feeds, their items, other people's likes) go in one shared database.
2. **Portable identity needs a custom domain.** A `weft.mydomain.com` CNAME lets the user move between instances without changing a single address. Users on a hosted subdomain are warned that moving changes their address. After a move the old address redirects for 3 months, then the account is deleted.
3. **WAL stays on.** It is required on the shared database, and the per-user files use it too (`synchronous=NORMAL`, log capped at 1 MiB). Closing a database properly leaves a single file; idle users are closed by the pool and so are single files at rest.
4. **Backup is `VACUUM INTO` snapshots plus rsync.** Never rsync live database files.
5. **Versioning is `PRAGMA user_version` per file**, with numbered migrations applied when a file is opened.

## What goes where

| Table | Goes to | Why |
|---|---|---|
| `users` (profile, password hash, token hash, avatar) | user file (one row) | it is the account |
| `sessions` | user file | one user's logins; not exported |
| `posts` | user file | |
| `replies`, `subreplies` | user file | replies received on this user's posts, with approval status; the second level is a cache of those |
| `likes` (published) | user file | |
| `likes_in` (received) | user file | |
| `follows` | user file | public follow graph and the owner's choice |
| `wm_queue` | user file | this user's outgoing and incoming Webmentions |
| `feeds`, `items` | shared database | one fetch serves every follower |
| `feed_likes` | shared database | likes published by followed people |

The shared database (`instance.db`) also becomes the **directory**, with no user content in it:

- `users(name, status, created, moved_to, moved_until)`
- `domains(domain, name, verified)`
- `local_follows(feed_url, name)`, a reverse index of which local users follow which feed
- `work(name, next_due)`, which users have Webmention jobs waiting
- `meta` (format version, shard depth)

Everything in the directory can be rebuilt by scanning the user files (`weft reindex`).

## Disk layout

```
-data DIR/
  instance.db                the directory and the shared caches
  users/ab/alice.db          ab = first two hex characters of sha256("alice")
  users/f3/bob.db
  backup/                    snapshots written by `weft backup`
```

The path is a pure function of the name, so routing a request needs no lookup. A fixed two-character prefix gives 256 folders, enough for hundreds of thousands of users; the depth is recorded in `meta` and a `weft reshard` command can change it. A user file is created exclusively, which also makes the name unique.

## Architecture

**Two handles replace today's one `store.Store`.**

- `Instance` opens `instance.db` and owns the pool. Shared-cache and directory methods live here.
- `Space` is one user's database. It carries the existing per-user methods. During the transition the methods keep their `userID` argument (always the single row); it is removed at the end.

**Pool.** `Instance.Space(name)` returns a `Space` and a release function. It is an LRU of open databases, with a reference count so one in use is never closed under a request, a default cap of 256, and idle databases closed after a few minutes. Opening takes a per-name lock and runs migrations. Each open database has `SetMaxOpenConns(1)`, a small page cache and up to three file descriptors, so the cap must stay well under the process limit. Closing the pool at shutdown is part of the existing graceful shutdown.

**Routing.** `hostUser` resolves `name.<base domain>` as now, or a custom domain through `domains` (kept in memory), then asks the pool for the space. `/static/` and `/favicon.ico` need no space.

**Auth.** The host already names the user, so the bearer token and the session cookie are checked against that user's file. `UserByToken`, `UserBySession` and `CheckPassword` stop being global. `weft user list` reads the directory, and `user add` creates the file and the directory row.

**Timeline.** Two steps: read the follows from the user's file, then ask the shared database for the newest items of those feeds (`feed_url IN (...)`, chunked or via a temporary table). `ATTACH` is possible only if the driver has a per-connection hook, which has to be checked; the two-step form is the default.

**Polling.** One loop over `local_follows` fetches each feed once per interval, using `ETag` and `Last-Modified`, and writes the shared cache. No user file is opened. Dormant users cost nothing, and a feed followed by a thousand users is fetched once. This replaces `FollowedFeedURLs`, and `refreshOwnFeed` becomes a lookup in `local_follows`.

**Webmention worker.** Enqueueing marks the user in `work`; the worker takes the users that are due, opens each space, runs its queue and updates `work`. At startup it reads `work`. Retries keep their backoff.

**Cross-user paths that must change** (found by reading the code):

| Today | Becomes |
|---|---|
| `ItemByGUID` searches all users' caches | only items from feeds the user follows |
| `FollowedFeedURLs` | distinct feeds in `local_follows` |
| `refreshOwnFeed` asks whether anyone follows the feed | lookup in `local_follows` |
| `DueQueue`, `FinishQueue`, `RetryQueue` run over everyone | per user, scheduled from `work` |
| `UserByToken`, `UserBySession`, `UserNames`, `AddUser` | host-scoped, or the directory |
| `posts.id`, `likes.id`, `replies.id` are global counters | per-user counters; they only appear in URLs under the user's own host, so they stay unique where it matters |

## Schema versioning

- Numbered migrations in code, each in a transaction, applied in order from the file's `user_version`.
- **Version 1 is the current schema.** Today's database is built with best-effort `ALTER TABLE` calls, so its state is not known; version 1 re-creates what is missing with the same idempotent statements, then stamps the file.
- A file newer than the binary is refused with a clear message. An older one is upgraded when opened; `weft migrate --dry-run` upgrades copies first, and `weft migrate --all` upgrades everything ahead of time.
- Old schema snapshots are kept as test fixtures, so every migration is tested from every earlier version.

## Export, import and moving

- `weft export NAME` (and a button for the owner) writes a zip: `manifest.json` (format, Weft version, schema version, name, domain, time) and a snapshot made with `wal_checkpoint(TRUNCATE)` and `VACUUM INTO`. Sessions are left out.
- `weft import FILE [--as NAME]` unpacks to a temporary folder, runs `integrity_check`, checks the manifest, migrates if the file is older, refuses a taken name, moves the file into place and rebuilds the user's `local_follows` rows.
- **Addresses.** A post's identity is its `urn:uuid:` guid, but replies, roots and likes name each other by permalink, which includes the host. With a custom domain nothing changes. Without one, the old address redirects for 3 months and references to the old permalinks keep resolving through that redirect.
- **The redirect period.** The directory records `moved_to` and `moved_until`. Every request to the old host answers 301 to the same path on the new one. The feed also carries `weft:movedTo`. After `moved_until` the file is deleted (kept in a trash folder for 30 days) and the old address answers 410.
- **Readers.** Today the cached timeline is keyed by an item's link, so a moved feed would show every post twice. Readers must key by the item's uid and, on `weft:movedTo`, update the follow after checking that the new feed carries the same uids. This is a spec change (`weft:movedTo` is already listed as deferred).

## Custom domains

- A user adds a domain in their settings. The server asks for a CNAME to the instance host, or a TXT record at `_weft.<domain>`, and only then marks it `verified`.
- TLS certificates are issued per custom domain on demand. The reverse proxy (Caddy's on-demand TLS) asks an internal endpoint whether a domain is verified before it asks for a certificate.
- Cookies, the same-origin check and absolute URLs already follow the request host.
- An apex domain cannot be a CNAME, so the advice is a subdomain such as `weft.mydomain.com`.

## Backup

`weft backup -to DIR` snapshots `instance.db` always, and each user file that changed since its last backup (compare the modification time of the file and its `-wal` with a `last_backup` stamp in the directory). Snapshots are `VACUUM INTO`, so they are consistent while the server runs. rsync then copies `DIR`. `weft restore` puts snapshots back and runs `reindex`. Cold users are single files and cost nothing to copy again.

## Moving today's database

`weft split -db weft.db -data DIR` reads the existing single database and writes the new layout: for each user a file at the current version with their rows (child tables through their posts), and `instance.db` with the shared tables, the directory and `local_follows`. It never changes the old file, and it compares row counts per table afterwards. The server runs in one of two modes during the transition: `-db FILE` as now, or `-data DIR`. The old mode is removed when the split has been used on the real data.

## Phases

Every phase leaves the tests passing and the server runnable.

0. **Migrations on the current database.** *Done (`78033b9`).* Numbered migrations and `user_version`, with the ad-hoc `ALTER`s folded into version 1. Old fixtures added.
1. **Separate the handles inside one file.** *Done, uncommitted at the time of writing; see the notes below.* Introduce `Instance` and `Space`; route every web call through `inst.Space(name)`; move the shared-cache and directory methods to `Instance`; remove the cross-user queries listed above; add `local_follows` and `work` as tables in the one database. This is most of the refactoring and it changes no storage.
   Notes on what phase 1 did: `store.Store` became `Instance` (shared caches and directory) and `Space` (one user; `inst.Space(name)` returns it with a release function). A `Space` reaches the shared tables only through `Instance` methods, so the timeline, a feed's view, `Follows`, `IsFollowingSite`, `FeedLikes`, `FollowedLikers` and `ItemByGUID` are two-step reads: the space names its feeds, then the instance reads them (`ItemByGUID` now finds only items of feeds the user follows). `local_follows` and `work` are migration 2, kept up to date by `Follow`/`Unfollow` and the queue methods; the poller and `refreshOwnFeed` read the first, the Webmention worker walks the second. Passwords, sessions and tokens are checked inside the host's space. Checked against copies of the real `weft.db` and `hackernews.db`: the timeline, following and replies APIs, the home page and the feeds are byte-identical to the previous build.
2. **Split the storage.** Per-user files, the pool, `instance.db`, the `-data` flag, `weft split`, the CLI changes, pool close in the graceful shutdown.
3. **Export, import and backup.** The commands, the manifest, and the owner's download button.
4. **Custom domains and moves.** Domain verification, on-demand TLS hook, redirects with `moved_until`, `weft:movedTo`, uid-keyed readers, spec update.
5. **Hardening.** `reindex`, `fsck`, `reshard`, pool metrics, descriptor limits checked at startup.

## Tests

- **Pool:** an eviction never closes a database in use; many users at once; the descriptor cap holds under a burst; a killed process leaves `-wal` files that the next open recovers.
- **Migrations:** each fixture upgraded to the latest and then used; a newer file refused; two opens of a new file at once migrate it once.
- **Equivalence:** after `weft split`, every page and feed of a sample instance is byte-for-byte what it was before.
- **Isolation:** with two users following the same feed, one user's likes list, thread lookups and timeline never contain what only the other cached.
- **Portability:** export on one instance, import on another, same pages and feeds; a move leaves a working 301 and `weft:movedTo`, and the old host answers 410 after expiry.
- **Backup:** snapshot while writes run, restore, compare.
- **Shutdown:** after SIGTERM every database file in the data folder is a single file.

## Risks

| Risk | What we do |
|---|---|
| Many small files | hash-prefix folders; the reverse index; at-rest files are single files |
| Cold open latency | opening a small SQLite file takes about a millisecond; the version check is one pragma |
| Descriptor and memory limits | pool cap, small page cache, a startup check against `ulimit -n` |
| Backup is no longer one atomic copy | not needed: users do not depend on each other's rows |
| Replication tools (Litestream) need a config per file | use the snapshot backup instead |
| A migration bug found one file at a time | dry-run on copies; `migrate --all`; a backup before each file's first upgrade |
| The directory drifts from the files | it is rebuildable (`reindex`); checked at startup |
| Moves rely on custom domains that users may not set up | clear warnings, and the 3-month redirect for those who move without one |

## Decisions on the open questions

1. **API tokens in an export.** Tokens are reset on import; the new one is shown once. The export carries no token hash.
2. **Name taken on import.** Refuse, with a clear message. The user finds another server or another name; there is no automatic rename.
3. **Pool cap.** The cap is on databases open at once, not on users. It is computed at startup as `min(256, (ulimit -n - 64) / 3)` (three descriptors per database: `.db`, `-wal`, `-shm`), after trying to raise the soft limit. macOS defaults to 256 descriptors, so a fixed 256 would not work on a laptop.
4. **Trash retention** after the redirect period: 30 days.
5. **Single-file mode.** No. After `weft split` everything runs through `-data DIR`; tests and development use a temporary folder. The `-db FILE` mode is removed once the split has been used on the real data.
6. **Per-user size limits.** None for now. Revisit when an instance has users it does not know.
7. **Removing `userID`.** At the end of phase 2 the argument is dropped from the `Space` method signatures (a mechanical change the compiler checks). The `user_id` column stays in the schema, always 1 and unused, so no migration is needed; it can go in a later numbered migration.
