package web

import (
	"sync"
	"time"
)

// Opening a reply in the thread page makes it the new focus, and the page is built again from the root: about
// depth+2 fetches a click, most of them for posts the last click read. itemCache keeps the answers to those
// fetches for a short time, so a click fetches mostly what is new.
//
// What it keeps is the body of a permalink answer, parsed again at every use, so that no page can change what
// another page sees. Only answers that parsed are kept: a server that failed is asked again. Lists of replies
// are never kept, they are what changes. A post edited or deleted on this server is dropped at once; a post
// on another server may show its old text for up to itemTTL.
const (
	itemTTL     = 60 * time.Second
	itemEntries = 512
)

type itemEntry struct {
	body []byte
	at   time.Time
}

type itemCache struct {
	mu  sync.Mutex
	m   map[string]itemEntry
	ttl time.Duration
	max int
	now func() time.Time
}

func newItemCache(ttl time.Duration, max int) *itemCache {
	return &itemCache{m: map[string]itemEntry{}, ttl: ttl, max: max, now: time.Now}
}

// get returns the kept body of u if it is still fresh.
func (c *itemCache) get(u string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[u]
	if !ok {
		return nil, false
	}
	if c.now().Sub(e.at) >= c.ttl {
		delete(c.m, u)
		return nil, false
	}
	return e.body, true
}

// put keeps body as the answer for u. When the cache is full the stale entries go first, and if that is not
// enough all of them: it is a few minutes of work at most, and not worth an ordering of its own.
func (c *itemCache) put(u string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.m) >= c.max {
		for k, e := range c.m {
			if now.Sub(e.at) >= c.ttl {
				delete(c.m, k)
			}
		}
		if len(c.m) >= c.max {
			c.m = map[string]itemEntry{}
		}
	}
	c.m[u] = itemEntry{body: body, at: now}
}

// drop forgets u, because the post it names was changed here.
func (c *itemCache) drop(u string) {
	c.mu.Lock()
	delete(c.m, u)
	c.mu.Unlock()
}
