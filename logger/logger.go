// Package logger provides a zero-dependency, level-filtered, thread-safe
// logging system for arweave-light. It wraps Go's standard library and adds:
//
//   - Five log levels: DEBUG, INFO, WARN, ERROR, FATAL
//   - Per-module named loggers via NewLogger("modname")
//   - Global level filtering via SetLevel()
//   - Optional dual output: stderr + file via SetLogFile()
//   - Same-line progress updates via \r (no newline)
//
// Format: 2006/01/02 15:04:05 [LEVEL] [module] message
package logger

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// Level represents the severity of a log message.
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
	FATAL
)

var levelNames = map[Level]string{
	DEBUG: "DEBUG",
	INFO:  "INFO",
	WARN:  "WARN",
	ERROR: "ERROR",
	FATAL: "FATAL",
}

// String returns the upper-case name of the level.
func (l Level) String() string {
	if s, ok := levelNames[l]; ok {
		return s
	}
	return "UNKNOWN"
}

var (
	globalLevel Level = INFO
	globalMu    sync.RWMutex
	logFile     *os.File
	// outputMu serialises writes to stderr and the optional log file so that
	// concurrent log lines never interleave.
	outputMu sync.Mutex
)

// SetLevel sets the global minimum log level. Messages below this level are
// silently discarded. It is safe for concurrent use.
func SetLevel(level Level) {
	globalMu.Lock()
	globalLevel = level
	globalMu.Unlock()
}

// GetLevel returns the current global log level.
func GetLevel() Level {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalLevel
}

// SetLogFile opens (or replaces) the log file for dual stderr + file output.
// Pass an empty string to close the current log file and disable file output.
// It is safe for concurrent use.
func SetLogFile(path string) error {
	outputMu.Lock()
	defer outputMu.Unlock()

	// Close previous file if any.
	if logFile != nil {
		logFile.Close()
		logFile = nil
	}

	if path == "" {
		return nil
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("logger: open log file: %w", err)
	}
	logFile = f
	return nil
}

// Logger is a module-specific logger that automatically prefixes every
// message with the module name. Create one with NewLogger.
type Logger struct {
	module string
}

// NewLogger returns a Logger that tags every message with [module].
func NewLogger(module string) *Logger {
	return &Logger{module: module}
}

// output is the central write path. It checks the global level, formats the
// message, and writes to stderr + optional file.
func (l *Logger) output(level Level, format string, v ...interface{}) {
	globalMu.RLock()
	currentLevel := globalLevel
	globalMu.RUnlock()

	if level < currentLevel {
		return
	}

	msg := fmt.Sprintf(format, v...)
	now := time.Now()
	timestamp := now.Format("2006/01/02 15:04:05")
	line := fmt.Sprintf("%s [%s] [%s] %s\n", timestamp, level.String(), l.module, msg)

	outputMu.Lock()
	os.Stderr.WriteString(line)
	if logFile != nil {
		logFile.WriteString(line)
	}
	outputMu.Unlock()

	if level == FATAL {
		os.Exit(1)
	}
}

// Debug logs a message at DEBUG level.
func (l *Logger) Debug(format string, v ...interface{}) {
	l.output(DEBUG, format, v...)
}

// Info logs a message at INFO level.
func (l *Logger) Info(format string, v ...interface{}) {
	l.output(INFO, format, v...)
}

// Warn logs a message at WARN level.
func (l *Logger) Warn(format string, v ...interface{}) {
	l.output(WARN, format, v...)
}

// Error logs a message at ERROR level.
func (l *Logger) Error(format string, v ...interface{}) {
	l.output(ERROR, format, v...)
}

// Fatal logs a message at FATAL level, then calls os.Exit(1).
func (l *Logger) Fatal(format string, v ...interface{}) {
	l.output(FATAL, format, v...)
}

// Progress writes a same-line progress message. It uses \r (carriage return)
// instead of \n so the next Progress call overwrites the current line.
// The message is always emitted regardless of the global log level.
func (l *Logger) Progress(format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	now := time.Now()
	timestamp := now.Format("2006/01/02 15:04:05")
	line := fmt.Sprintf("%s [INFO] [%s] %s\r", timestamp, l.module, msg)

	outputMu.Lock()
	os.Stderr.WriteString(line)
	if logFile != nil {
		logFile.WriteString(line)
	}
	outputMu.Unlock()
}
