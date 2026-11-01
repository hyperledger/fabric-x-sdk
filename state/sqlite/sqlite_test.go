/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package sqlite

import (
	"database/sql"
	"io"
	"path/filepath"
	"testing"
)

func pragma(t *testing.T, conn *sql.Conn, name string) string {
	t.Helper()
	var v string
	if err := conn.QueryRowContext(t.Context(), "PRAGMA "+name).Scan(&v); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

// closeOnCleanup closes c when the test finishes and fails the test if that errors.
func closeOnCleanup(t *testing.T, c io.Closer) {
	t.Helper()
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
}

// Every connection in the pool must carry the pragmas, not only the first one.
func TestOpenReadOnly_PragmasOnEveryConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closeOnCleanup(t, w)

	r, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	closeOnCleanup(t, r)

	// Hold the connections so the pool has to open new ones.
	for i := range 4 {
		conn, err := r.Conn(t.Context())
		if err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		closeOnCleanup(t, conn)

		want := map[string]string{
			"journal_mode": "wal",
			"busy_timeout": "5000",
			"foreign_keys": "1",
			"synchronous":  "1", // NORMAL
			"cache_size":   "-64000",
			"temp_store":   "2", // MEMORY
		}
		for name, v := range want {
			if got := pragma(t, conn, name); got != v {
				t.Errorf("conn %d: %s = %q, want %q", i, name, got, v)
			}
		}
	}
}

// A pragma set in the connection string wins over the default.
func TestOpen_ConnectionStringPragmasWin(t *testing.T) {
	path := "file:" + filepath.Join(t.TempDir(), "test.db") +
		"?_pragma=busy_timeout(100)&_fk=0&_pragma=temp_store%3DFILE"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closeOnCleanup(t, db)

	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	closeOnCleanup(t, conn)

	want := map[string]string{
		"busy_timeout": "100",
		"foreign_keys": "0",
		"temp_store":   "1", // FILE
		"journal_mode": "wal",
	}
	for name, v := range want {
		if got := pragma(t, conn, name); got != v {
			t.Errorf("%s = %q, want %q", name, got, v)
		}
	}
}

func TestOpen_InvalidPragmaFailsAtOpen(t *testing.T) {
	path := "file:" + filepath.Join(t.TempDir(), "test.db") + "?_pragma=journal_mode(bogus"
	if db, err := Open(path); err == nil {
		_ = db.Close()
		t.Fatal("Open succeeded with a malformed pragma")
	}
}

func TestPragmaName(t *testing.T) {
	for in, want := range map[string]string{
		"busy_timeout(100)":      "busy_timeout",
		" Foreign_Keys = 1":      "foreign_keys",
		"main.cache_size(-2000)": "cache_size",
		"temp_store=memory":      "temp_store",
		"journal_mode":           "journal_mode",
	} {
		if got := pragmaName(in); got != want {
			t.Errorf("pragmaName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPragmasFor(t *testing.T) {
	stmts, err := pragmasFor("file:x.db?_busy_timeout=10&_pragma=cache_size(-1)")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stmts {
		if s == "PRAGMA busy_timeout=5000" || s == "PRAGMA cache_size=-64000" {
			t.Errorf("default %q applied although the connection string sets it", s)
		}
	}
	if len(stmts) != len(defaultPragmas)-2 {
		t.Errorf("got %d statements, want %d: %v", len(stmts), len(defaultPragmas)-2, stmts)
	}

	if _, err := pragmasFor("file:x.db?a=%zz"); err == nil {
		t.Error("malformed query string accepted")
	}
}
