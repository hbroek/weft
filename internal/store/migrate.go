package store

import (
	"database/sql"
	"fmt"
)

// A migration moves the schema from version n-1 to n, where n is its position in migrations plus one.
// Each runs in a transaction together with the bump of PRAGMA user_version, so a file is never left
// half way. Append new migrations; never edit or reorder an existing one.
//
// Version 1 is the schema as it stood when versions were introduced; schemaV1 builds it from nothing
// or from any older database (see its comment). It cannot run in a transaction because the older
// code it replays relies on statements failing harmlessly.
var migrations = []func(tx *sql.Tx) error{
	nil, // 1: schemaV1
}

// SchemaVersion is the version a database has after Open.
var SchemaVersion = len(migrations)

// migrate upgrades db to SchemaVersion, and refuses a database written by a newer Weft.
func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > SchemaVersion {
		return fmt.Errorf("database has schema version %d, but this Weft only knows up to %d: upgrade Weft", v, SchemaVersion)
	}
	if v < 1 {
		if err := schemaV1(db); err != nil {
			return fmt.Errorf("migrating to schema version 1: %w", err)
		}
		if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
			return err
		}
		v = 1
	}
	for ; v < SchemaVersion; v++ {
		if err := applyMigration(db, v+1); err != nil {
			return fmt.Errorf("migrating to schema version %d: %w", v+1, err)
		}
	}
	return nil
}

func applyMigration(db *sql.DB, to int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var v int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= to {
		return nil // another process got here first
	}
	if err := migrations[to-1](tx); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, to)); err != nil {
		return err
	}
	return tx.Commit()
}
