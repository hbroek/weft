package store

import (
	"database/sql"
	"errors"
	"log"
	"sync"
	"time"
)

// pool keeps the most recently used users' databases open. An open database costs up to three file
// descriptors (the file, its -wal and its -shm) and a small page cache, so how many may be open at once
// is capped, and databases nobody has used for a while are closed, leaving a single file on disk.
//
// A database that is in use (referenced by a Space that has not been released) is never closed.
type pool struct {
	open func(name string) (*sql.DB, int64, error)
	cap  int
	idle time.Duration

	mu      sync.Mutex
	entries map[string]*entry
	stop    chan struct{}
	done    sync.WaitGroup
}

type entry struct {
	name  string
	ready chan struct{} // closed once db, uid and err are set
	db    *sql.DB
	uid   int64
	err   error
	refs  int
	last  time.Time
}

// poolCap is how many databases may be open at once: 256, or fewer when the process may open few files
// (three descriptors per database, and 64 kept for sockets and the shared database).
func poolCap() int {
	n := 256
	if l := openFileLimit(); l > 0 && (l-64)/3 < n {
		n = (l - 64) / 3
	}
	return max(n, 4)
}

func newPool(open func(name string) (*sql.DB, int64, error), cap int, idle time.Duration) *pool {
	p := &pool{open: open, cap: cap, idle: idle, entries: map[string]*entry{}, stop: make(chan struct{})}
	p.done.Add(1)
	go p.sweep()
	return p
}

// get opens (or reuses) the named database and holds it until release.
func (p *pool) get(name string) (*entry, error) {
	p.mu.Lock()
	if e := p.entries[name]; e != nil {
		e.refs++
		p.mu.Unlock()
		<-e.ready // another request may still be opening it
		if e.err != nil {
			return nil, e.err
		}
		return e, nil
	}
	e := &entry{name: name, ready: make(chan struct{}), refs: 1, last: time.Now()}
	p.entries[name] = e
	evict := p.evictLocked()
	p.mu.Unlock()
	closeAll(evict)

	e.db, e.uid, e.err = p.open(name)
	close(e.ready)
	if e.err != nil {
		p.mu.Lock()
		delete(p.entries, name)
		p.mu.Unlock()
		return nil, e.err
	}
	return e, nil
}

func (p *pool) release(e *entry) {
	p.mu.Lock()
	e.refs--
	e.last = time.Now()
	evict := p.evictLocked()
	p.mu.Unlock()
	closeAll(evict)
}

// evictLocked removes least recently used unreferenced entries until the pool is within its cap and
// returns them to be closed once the lock is dropped. If every entry is in use the pool exceeds its cap
// for a while rather than close a database under a request.
func (p *pool) evictLocked() []*entry {
	var out []*entry
	for len(p.entries) > p.cap {
		var oldest *entry
		for _, e := range p.entries {
			if e.refs == 0 && (oldest == nil || e.last.Before(oldest.last)) {
				oldest = e
			}
		}
		if oldest == nil {
			break
		}
		delete(p.entries, oldest.name)
		out = append(out, oldest)
	}
	return out
}

// sweep closes databases that have been idle for a while.
func (p *pool) sweep() {
	defer p.done.Done()
	every := max(p.idle/4, time.Millisecond)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.mu.Lock()
			var out []*entry
			for n, e := range p.entries {
				if e.refs == 0 && time.Since(e.last) > p.idle {
					delete(p.entries, n)
					out = append(out, e)
				}
			}
			p.mu.Unlock()
			closeAll(out)
		}
	}
}

// size is how many databases are open.
func (p *pool) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// close stops the sweeper and closes every database. Call it when nothing uses a Space any more.
func (p *pool) close() error {
	close(p.stop)
	p.done.Wait()
	p.mu.Lock()
	var all []*entry
	for n, e := range p.entries {
		all = append(all, e)
		delete(p.entries, n)
	}
	p.mu.Unlock()
	var errs []error
	for _, e := range all {
		<-e.ready
		if e.err == nil {
			if e.refs > 0 {
				log.Printf("closing %s while %d requests still use it", e.name, e.refs)
			}
			errs = append(errs, closeDB(e.db))
		}
	}
	return errors.Join(errs...)
}

func closeAll(es []*entry) {
	for _, e := range es {
		<-e.ready
		if e.err == nil {
			closeDB(e.db)
		}
	}
}

// closeDB writes the write-ahead log back and closes the file; the last connection to close then
// deletes the -wal and -shm files, so a database at rest is a single file.
func closeDB(db *sql.DB) error {
	// statistics help the planner only where there is something to plan, and cost a small file two pages
	var pages int
	if db.QueryRow(`PRAGMA page_count`).Scan(&pages) == nil && pages >= 256 {
		db.Exec(`PRAGMA optimize`)
	}
	db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return db.Close()
}
