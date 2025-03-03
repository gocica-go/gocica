// Package log provides a simple wrapper around the standard log package
// with support for different log levels (ERROR, WARN, INFO, DEBUG)
package log

import (
	"log"
	"os"
)

type Level uint8

const (
	Silent Level = iota
	Error
	Warn
	Info
	Debug
)

// NewLogger creates a new logger instance
func NewLogger(level Level) *Logger {
	return &Logger{
		level:  level,
		logger: log.New(os.Stderr, "GoCICa: ", log.LstdFlags),
	}
}

// Logger wraps the standard logger with additional log level functionality
type Logger struct {
	level Level
	// logger is the underlying standard logger instance
	logger *log.Logger
}

// Errorf logs a message at ERROR level using printf style formatting
func (l *Logger) Errorf(format string, args ...any) {
	if l.level < Error {
		return
	}
	l.logger.Printf("[ERROR] "+format, args...)
}

// Warnf logs a message at WARN level using printf style formatting
func (l *Logger) Warnf(format string, args ...any) {
	if l.level < Warn {
		return
	}
	l.logger.Printf("[WARN] "+format, args...)
}

// Infof logs a message at INFO level using printf style formatting
func (l *Logger) Infof(format string, args ...any) {
	if l.level < Info {
		return
	}
	l.logger.Printf("[INFO] "+format, args...)
}

// Debugf logs a message at DEBUG level using printf style formatting
func (l *Logger) Debugf(format string, args ...any) {
	if l.level < Debug {
		return
	}
	l.logger.Printf("[DEBUG] "+format, args...)
}
