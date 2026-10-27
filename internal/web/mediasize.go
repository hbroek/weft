package web

import (
	"container/list"
	"strconv"
	"sync"

	"github.com/hbroek/weft/internal/img"
	"github.com/hbroek/weft/internal/store"
)

// Smaller copies of large pictures, made when they are asked for. A page shows a picture about 40 em wide,
// and a phone photo or a 1600 px original is many times what that needs, so lists ask for /media/<name>?w=800
// and an overview of pictures asks for ?w=240. The copy is not stored: browsers and proxies keep it (the
// answer is immutable like the picture), and the server keeps recent ones in memory.

// pictureSides are the longest sides a copy may have. Others are refused, so nobody can make the server
// scale a picture to every size.
var pictureSides = map[int]int64{
	800: 100 << 10, // a picture of at most this many bytes is shown as it is
	240: 20 << 10,
}

const (
	listSide     = 800 // posts and lists
	thumbSide    = 240 // the overview of pictures
	scaleMemory  = 64 << 20
	scaleWorkers = 2 // pictures scaled at once; others wait
)

// scalable reports whether a copy of the picture with its longest side at most side is worth making: it is
// larger than that, big in bytes, and not a GIF (which may be animated, and is small anyway).
func scalable(m *store.Media, side int) bool {
	small, ok := pictureSides[side]
	return ok && m.Mime != "image/gif" && max(m.Width, m.Height) > side && m.Size > small
}

// scaleKey names a copy. The name of a picture is the hash of its bytes, so it is the same for every user.
type scaleKey struct {
	name string
	side int
}

type scaled struct {
	data []byte
	mime string
	w, h int
}

// scaleCache keeps the most recently used copies up to a total size and makes sure a copy is made once
// however many ask for it at the same moment.
type scaleCache struct {
	mu      sync.Mutex
	items   map[scaleKey]*list.Element
	order   *list.List // front: most recently used
	bytes   int
	limit   int
	pending map[scaleKey]chan struct{}
	work    chan struct{}
}

type scaleEntry struct {
	key scaleKey
	val *scaled
}

func newScaleCache(limit int) *scaleCache {
	return &scaleCache{items: map[scaleKey]*list.Element{}, order: list.New(), limit: limit, pending: map[scaleKey]chan struct{}{}, work: make(chan struct{}, scaleWorkers)}
}

func (c *scaleCache) peek(k scaleKey) (*scaled, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[k]; ok {
		c.order.MoveToFront(e)
		return e.Value.(*scaleEntry).val, true
	}
	return nil, false
}

func (c *scaleCache) put(k scaleKey, v *scaled) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[k]; ok || len(v.data) > c.limit {
		return
	}
	c.items[k] = c.order.PushFront(&scaleEntry{k, v})
	c.bytes += len(v.data)
	for c.bytes > c.limit {
		last := c.order.Back()
		e := last.Value.(*scaleEntry)
		c.order.Remove(last)
		delete(c.items, e.key)
		c.bytes -= len(e.val.data)
	}
}

// get returns the copy, making it with build if nobody has. If make fails the result is nil. Callers asking
// for the same copy while it is being made wait for it rather than make it again.
func (c *scaleCache) get(k scaleKey, build func() *scaled) *scaled {
	for {
		if v, ok := c.peek(k); ok {
			return v
		}
		c.mu.Lock()
		if wait, busy := c.pending[k]; busy {
			c.mu.Unlock()
			<-wait
			if v, ok := c.peek(k); ok {
				return v
			}
			return nil // the one making it failed; do not try again for every waiting request
		}
		done := make(chan struct{})
		c.pending[k] = done
		c.mu.Unlock()

		c.work <- struct{}{}
		v := build()
		<-c.work
		if v != nil {
			c.put(k, v)
		}
		c.mu.Lock()
		delete(c.pending, k)
		c.mu.Unlock()
		close(done)
		return v
	}
}

// scaleOf makes the copy of a stored picture, or nil if it cannot be scaled or does not get smaller.
func (s *Server) scaleOf(m *store.Media, side int) *scaled {
	lim := s.imageLimits()
	lim.MaxSide, lim.MaxBytes, lim.MaxInput = side, len(m.Data)+1, len(m.Data)+1
	res, err := img.Process(m.Data, img.Compact, lim)
	if err != nil || len(res.Data) >= len(m.Data) {
		return nil
	}
	return &scaled{data: res.Data, mime: res.Mime, w: res.Width, h: res.Height}
}

// sideOf reads the w parameter of a picture's address: 0 for none, -1 for one that is not allowed.
func sideOf(v string) int {
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if _, ok := pictureSides[n]; err != nil || !ok {
		return -1
	}
	return n
}
