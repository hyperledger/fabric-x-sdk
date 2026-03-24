// Copyright IBM Corp. All Rights Reserved.
//
// SPDX-License-Identifier: Apache-2.0
//

// Package sqlite provides a production-ready SQLite database opener with
// sensible defaults for both write and read-only access patterns.
package sqlite

import (
	"database/sql"
	"fmt"
	"runtime"

	_ "modernc.org/sqlite"
)

const busyTimeout = 5000 // milliseconds

// Open opens a SQLite database for read-write access. It enforces a single
// connection (SQLite's single-writer constraint) and configures the database
// with production-ready pragmas.
func Open(path string) (*sql.DB, error) {
	return open(path, 1, 1)
}

// OpenReadOnly opens a SQLite database for read-only access. It allows
// multiple concurrent connections since WAL mode permits concurrent readers.
// The caller may append ?mode=ro to path for OS-level read-only enforcement.
func OpenReadOnly(path string) (*sql.DB, error) {
	n := max(4, runtime.NumCPU())
	return open(path, n, 4)
}

func open(path string, maxOpen, maxIdle int) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(0)

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeout),
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-64000",
	}
	for _, p := range pragmas {
		if _, err = db.Exec(p); err != nil {
			db.Close() //nolint:errcheck
			return nil, fmt.Errorf("set pragma %q: %w", p, err)
		}
	}

	return db, nil
}
