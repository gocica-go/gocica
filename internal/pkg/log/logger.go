// Package log provides a simple wrapper around the standard log package
// with support for different log levels (ERROR, INFO, DEBUG)
package log

import (
	"log"
	"os"
)

// NewLogger creates a new logger instance
func NewLogger() *Logger {
	return &Logger{
		logger: log.New(os.Stderr, "", log.LstdFlags),
	}
}

// Logger wraps the standard logger with additional log level functionality
type Logger struct {
	// logger is the underlying standard logger instance
	logger *log.Logger
}

// Errorf logs a message at ERROR level using printf style formatting
func (l *Logger) Errorf(format string, args ...any) {
	l.logger.Printf("[ERROR] "+format, args...)
}

// Infof logs a message at INFO level using printf style formatting
func (l *Logger) Infof(format string, args ...any) {
	l.logger.Printf("[INFO] "+format, args...)
}

// Debugf logs a message at DEBUG level using printf style formatting
func (l *Logger) Debugf(format string, args ...any) {
	l.logger.Printf("[DEBUG] "+format, args...)
}
