/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package sdk

import (
	"log"
	"sync"
	"testing"
)

// Logger is the logging interface used throughout the SDK.
// This interface is compatible with the fabric logger from fabric-lib-go.
//
// Users can provide any logger implementation that supports formatted logging at different levels.
type Logger interface {
	Debugf(template string, args ...any)
	Infof(template string, args ...any)
	Warnf(template string, args ...any)
	Errorf(template string, args ...any)
}

// NoOpLogger is a logger implementation that discards all log output.
// Use this when you want to disable logging entirely.
type NoOpLogger struct{}

func (NoOpLogger) Debugf(template string, args ...any) {}
func (NoOpLogger) Infof(template string, args ...any)  {}
func (NoOpLogger) Warnf(template string, args ...any)  {}
func (NoOpLogger) Errorf(template string, args ...any) {}

// StdLogger is a logger implementation that uses Go's standard library log package.
// It prefixes all messages with the component name and log level.
type StdLogger struct {
	prefix string
}

// NewStdLogger creates a new logger that uses the standard library log package.
// All log messages will be prefixed with the given component name.
func NewStdLogger(component string) Logger {
	return &StdLogger{prefix: component}
}

func (l *StdLogger) Debugf(template string, args ...any) {
	log.Printf("[%s] [DEBUG] "+template, append([]any{l.prefix}, args...)...)
}

func (l *StdLogger) Infof(template string, args ...any) {
	log.Printf("[%s] [INFO] "+template, append([]any{l.prefix}, args...)...)
}

func (l *StdLogger) Warnf(template string, args ...any) {
	log.Printf("[%s] [WARN] "+template, append([]any{l.prefix}, args...)...)
}

func (l *StdLogger) Errorf(template string, args ...any) {
	log.Printf("[%s] [ERROR] "+template, append([]any{l.prefix}, args...)...)
}

// TestLogger is a logger implementation that uses testing.T's Log function.
// Use this in tests to have log output captured and displayed by the test runner.
//
// It is safe to log from a goroutine that outlives the test: t.Logf may not be called
// once a test has completed — it races with the testing package's own bookkeeping and
// fails the run, often blaming whichever unrelated test happened to be executing — so
// after the test finishes TestLogger writes to stderr instead. That is a backstop, not
// a licence to leak: join your goroutines, and treat the "(after <test>)" lines it
// emits as a leak to go and fix.
type TestLogger struct {
	t      *testing.T
	prefix string

	// mu guards done and serialises the t.Logf calls it gates. Held across the log
	// call itself, so a log that has started cannot straddle the test's completion.
	mu   sync.Mutex
	done bool
}

// NewTestLogger creates a new logger that writes to testing.T's log output.
// All log messages will be prefixed with the given component name.
func NewTestLogger(t *testing.T, component string) Logger {
	l := &TestLogger{t: t, prefix: component}
	// Cleanups run last-registered-first, so loggers built early in a test's setup
	// stop accepting t.Logf only after the later cleanups have had their say.
	t.Cleanup(func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.done = true
	})
	return l
}

func (l *TestLogger) logf(level, template string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.done {
		// Past the point where t.Logf is legal. Keep the line rather than dropping it:
		// it is usually the only evidence of the goroutine that outlived the test.
		log.Printf("[%s] [%s] (after %s) "+template, append([]any{l.prefix, level, l.t.Name()}, args...)...)
		return
	}
	l.t.Logf("[%s] [%s] "+template, append([]any{l.prefix, level}, args...)...)
}

func (l *TestLogger) Debugf(template string, args ...any) {
	l.logf("DEBUG", template, args...)
}

func (l *TestLogger) Infof(template string, args ...any) {
	l.logf("INFO", template, args...)
}

func (l *TestLogger) Warnf(template string, args ...any) {
	l.logf("WARN", template, args...)
}

func (l *TestLogger) Errorf(template string, args ...any) {
	l.logf("ERROR", template, args...)
}
