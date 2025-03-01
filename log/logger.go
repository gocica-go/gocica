package log

import "github.com/mazrean/gocica/internal/pkg/log"

// Logger defines the interface for logging operations used throughout the protocol
// It provides methods for different log levels: debug, info, and error
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Errorf(format string, args ...any)
}

var DefaultLogger Logger = log.NewLogger(log.Info) // defaultLogger is the default logger instance
