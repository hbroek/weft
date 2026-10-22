package store

import (
	"database/sql"
	"strings"
	"sync"
)

// lazyTables are the tables of a user's file that a new file does not have (see lazySchema). Code
// uses them as if they always existed; lazyDB makes that true:
//
//   - a read of a missing one runs against an empty in-memory copy of the schema, and finds nothing;
//   - an INSERT into a missing one creates all of them and runs again;
//   - an UPDATE or DELETE of a missing one has nothing to change and succeeds.
var lazyTables = []string{"sessions", "replies", "subreplies", "likes", "likes_in", "wm_queue"}

type lazyDB struct{ *sql.DB }

// missing reports whether err says one of the lazy tables does not exist.
func missing(err error) bool {
	if err == nil {
		return false
	}
	_, rest, ok := strings.Cut(err.Error(), "no such table: ")
	if !ok {
		return false
	}
	for _, t := range lazyTables {
		if strings.HasPrefix(rest, t) && (len(rest) == len(t) || !isIdent(rest[len(t)])) {
			return true
		}
	}
	return false
}

func isIdent(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// ensure creates the lazy tables. It is safe to call when they exist.
func (d lazyDB) ensure() error {
	tx, err := d.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(lazySchema); err != nil {
		return err
	}
	return tx.Commit()
}

// emptyDB is a database with every table and none of the rows, for reads of tables a file lacks.
var emptyDB = sync.OnceValue(func() *sql.DB {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		panic(err)
	}
	db.SetMaxOpenConns(1) // one connection is the database: a second would be another, empty, one
	if _, err := db.Exec(spaceSchema + lazySchema); err != nil {
		panic(err)
	}
	return db
})

type noRows struct{}

func (noRows) LastInsertId() (int64, error) { return 0, nil }
func (noRows) RowsAffected() (int64, error) { return 0, nil }

func (d lazyDB) Exec(q string, args ...any) (sql.Result, error) {
	res, err := d.DB.Exec(q, args...)
	if !missing(err) {
		return res, err
	}
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(q)), "INSERT") {
		return noRows{}, nil
	}
	if err := d.ensure(); err != nil {
		return nil, err
	}
	return d.DB.Exec(q, args...)
}

func (d lazyDB) Query(q string, args ...any) (*sql.Rows, error) {
	rows, err := d.DB.Query(q, args...)
	if missing(err) {
		return emptyDB().Query(q, args...)
	}
	return rows, err
}

// lazyRow is a sql.Row that can fall back, which is only known when it is scanned.
type lazyRow struct {
	d    lazyDB
	q    string
	args []any
}

func (d lazyDB) QueryRow(q string, args ...any) lazyRow { return lazyRow{d, q, args} }

func (r lazyRow) Scan(dest ...any) error {
	err := r.d.DB.QueryRow(r.q, r.args...).Scan(dest...)
	if missing(err) {
		return emptyDB().QueryRow(r.q, r.args...).Scan(dest...)
	}
	return err
}
