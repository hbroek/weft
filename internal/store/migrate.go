package store

import (
	"database/sql"
	"fmt"
)

// A migration moves a database's schema from version n-1 to n, where n is its position in the list plus
// one. Each runs in a transaction together with the bump of PRAGMA user_version, so a file is never
// left half way. Append new migrations; never edit or reorder an existing one.
//
// There are two lists because there are two kinds of file: one per user, and the instance's own.
var (
	spaceMigrations    = []func(tx *sql.Tx) error{spaceV1}
	instanceMigrations = []func(tx *sql.Tx) error{instanceV1, instanceV2}
)

// SpaceSchemaVersion and InstanceSchemaVersion are the versions a file has after it is opened.
var (
	SpaceSchemaVersion    = len(spaceMigrations)
	InstanceSchemaVersion = len(instanceMigrations)
)

// migrate upgrades db through steps, and refuses a file written by a newer Weft.
func migrate(db *sql.DB, steps []func(tx *sql.Tx) error) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(steps) {
		return fmt.Errorf("database has schema version %d, but this Weft only knows up to %d: upgrade Weft", v, len(steps))
	}
	return runSteps(db, steps, v)
}

// runSteps applies the steps after version from.
func runSteps(db *sql.DB, steps []func(tx *sql.Tx) error, from int) error {
	for v := from; v < len(steps); v++ {
		if err := applyStep(db, steps, v+1); err != nil {
			return fmt.Errorf("migrating to schema version %d: %w", v+1, err)
		}
	}
	return nil
}

func applyStep(db *sql.DB, steps []func(tx *sql.Tx) error, to int) error {
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
	if err := steps[to-1](tx); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, to)); err != nil {
		return err
	}
	return tx.Commit()
}
