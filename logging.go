/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package sdk

import "log"

// Logger is the logging interface used throughout the SDK.
// This interface is compatible with github.com/hyperledger/fabric-x-committer/utils/logging.Logger
// (and by extension, *zap.SugaredLogger), allowing direct injection without wrappers.
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
