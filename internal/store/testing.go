package store

import "database/sql"

// RawDBForTests exposes the connection so tests can build states the API refuses to produce, such
// as two posts that reply to each other. Production code must not use it.
func (s *Instance) RawDBForTests() *sql.DB { return s.db }

// SpaceForTests opens the named user's space for a test. It is released at once, which is safe while the
// test uses few users: nothing evicts a database until the pool is over its cap.
func (in *Instance) SpaceForTests(t interface {
	Helper()
	Fatal(...any)
}, name string) *Space {
	t.Helper()
	sp, release, err := in.Space(name)
	if err != nil {
		t.Fatal(err)
	}
	release()
	return sp
}

// RawDBForTests exposes the user's connection, as Instance's does for the shared one.
func (s *Space) RawDBForTests() *sql.DB { return s.db }
