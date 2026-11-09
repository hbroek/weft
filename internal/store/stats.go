package store

import (
	"os"
	"time"
)

// Stats is a snapshot of how the instance is being used, for a metrics endpoint.
type Stats struct {
	PoolOpen, PoolInUse, PoolCap                 int
	PoolHits, PoolOpens, PoolEvictions, PoolIdle int64 // totals since the process started
	ReadsInUse, ReadsOpen                        int   // connections of instance.db's read pool
	ReadWait                                     time.Duration
	WriteInUse                                   int
	WriteWait                                    time.Duration
	Users, Moved, TrashFiles                     int
	FileLimit                                    int
}

func (in *Instance) Stats() Stats {
	p := in.pool
	st := Stats{
		PoolOpen: p.size(), PoolInUse: p.inUse(), PoolCap: p.cap,
		PoolHits: p.n.hits.Load(), PoolOpens: p.n.opens.Load(), PoolEvictions: p.n.evictions.Load(), PoolIdle: p.n.idleCloses.Load(),
		FileLimit: openFileLimit(),
	}
	r, w := in.rdb.Stats(), in.db.Stats()
	st.ReadsInUse, st.ReadsOpen, st.ReadWait = r.InUse, r.OpenConnections, r.WaitDuration
	st.WriteInUse, st.WriteWait = w.InUse, w.WaitDuration
	count := func(q string, into *int) { in.rdb.QueryRow(q).Scan(into) }
	count(`SELECT COUNT(*) FROM users`, &st.Users)
	count(`SELECT COUNT(*) FROM users WHERE status IN ('moved','removed')`, &st.Moved)
	if files, err := os.ReadDir(in.TrashDir()); err == nil {
		st.TrashFiles = len(files)
	}
	return st
}
