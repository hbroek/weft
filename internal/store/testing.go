package store

import "database/sql"

// RawDBForTests exposes the connection so tests can build states the API refuses to produce, such
// as two posts that reply to each other. Production code must not use it.
func (s *Store) RawDBForTests() *sql.DB { return s.db }
