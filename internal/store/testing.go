package store

import "database/sql"

// RawDBForTests exposes the connection so tests can build states the API refuses to produce, such
// as two posts that reply to each other. Production code must not use it.
func (s *Instance) RawDBForTests() *sql.DB { return s.db }

// SpaceForTests opens the named user's space and keeps it for the length of the test.
func (in *Instance) SpaceForTests(t interface {
	Helper()
	Fatal(...any)
	Cleanup(func())
}, name string) *Space {
	t.Helper()
	sp, release, err := in.Space(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return sp
}
