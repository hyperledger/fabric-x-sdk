/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// Package sqlite provides a production-ready SQLite database opener with
// sensible defaults for both write and read-only access patterns.
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"runtime"
	"strings"

	_ "modernc.org/sqlite"
)

// defaultPragma is a pragma set on every connection, unless the connection
// string already sets it.
type defaultPragma struct {
	name  string
	value string
}

// defaultPragmas are applied in order. busy_timeout comes first so that the
// switch to WAL can wait for a lock held by another process.
//
// temp_store=MEMORY keeps temporary tables, indices and spilled statement
// journals in memory. Container images often have no writable temp directory,
// and SQLite then fails large statements with SQLITE_IOERR_GETTEMPPATH.
var defaultPragmas = []defaultPragma{
	{"busy_timeout", "5000"},
	{"journal_mode", "WAL"},
	{"foreign_keys", "ON"},
	{"synchronous", "NORMAL"},
	{"cache_size", "-64000"},
	{"temp_store", "MEMORY"},
}

// dsnShorthands maps the modernc connection string keys that set a pragma to
// the pragma they set.
var dsnShorthands = map[string]string{
	"_busy_timeout": "busy_timeout",
	"_timeout":      "busy_timeout",
	"_journal_mode": "journal_mode",
	"_journal":      "journal_mode",
	"_foreign_keys": "foreign_keys",
	"_fk":           "foreign_keys",
	"_synchronous":  "synchronous",
	"_sync":         "synchronous",
}

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

// open passes path to the driver unchanged. Most pragmas only apply to the
// connection that runs them, so the defaults are set by the connector on every
// connection the pool opens, not once on the *sql.DB. A pragma that path sets
// with _pragma or a shorthand key is left as path sets it.
func open(path string, maxOpen, maxIdle int) (*sql.DB, error) {
	pragmas, err := pragmasFor(path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// sql.Open does not connect; it only gives access to the registered driver.
	probe, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	drv := probe.Driver()
	probe.Close() //nolint:errcheck,gosec // No connection was opened.

	db := sql.OpenDB(&connector{drv: drv, dsn: path, pragmas: pragmas})
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
	db.SetConnMaxLifetime(0)

	// Open one connection now, so a bad path or pragma fails here and not on
	// first use.
	if err := db.PingContext(context.Background()); err != nil {
		db.Close() //nolint:errcheck,gosec // Preserve the initialization error; the DB is already unusable.
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	return db, nil
}

// pragmasFor returns the default pragma statements that dsn does not set itself.
func pragmasFor(dsn string) ([]string, error) {
	set := map[string]bool{}
	// The driver treats everything after the first '?' as the query string,
	// except a '?' in the first position.
	if pos := strings.IndexByte(dsn, '?'); pos >= 1 {
		q, err := url.ParseQuery(dsn[pos+1:])
		if err != nil {
			return nil, fmt.Errorf("parse connection string parameters: %w", err)
		}
		for key, name := range dsnShorthands {
			if q.Has(key) {
				set[name] = true
			}
		}
		for _, p := range q["_pragma"] {
			set[pragmaName(p)] = true
		}
	}

	var stmts []string
	for _, p := range defaultPragmas {
		if !set[p.name] {
			stmts = append(stmts, fmt.Sprintf("PRAGMA %s=%s", p.name, p.value))
		}
	}
	return stmts, nil
}

// pragmaName returns the lower-case pragma name of a _pragma value such as
// "busy_timeout(100)", "foreign_keys = 1" or "main.cache_size(-2000)".
func pragmaName(v string) string {
	name, _, _ := strings.Cut(v, "(")
	name, _, _ = strings.Cut(name, "=")
	name = strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// connector opens connections with the registered sqlite driver and runs the
// pragma statements on each one.
type connector struct {
	drv     driver.Driver
	dsn     string
	pragmas []string
}

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	execer, ok := conn.(driver.ExecerContext)
	if !ok {
		conn.Close() //nolint:errcheck,gosec // Preserve the original error.
		return nil, fmt.Errorf("sqlite connection %T cannot execute statements", conn)
	}
	for _, p := range c.pragmas {
		if _, err := execer.ExecContext(ctx, p, nil); err != nil {
			conn.Close() //nolint:errcheck,gosec // Preserve the pragma error.
			return nil, fmt.Errorf("set pragma %q: %w", p, err)
		}
	}
	return conn, nil
}

func (c *connector) Driver() driver.Driver { return c.drv }
