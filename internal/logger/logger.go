// Package logger provides logging functionality with debug and info levels for the merged-pr-bot application.
package logger

import (
	"log"
	"os"
)

var (
	debugMode   bool
	debugLogger *log.Logger
	infoLogger  *log.Logger
)

func init() {
	debugLogger = log.New(os.Stdout, "[DEBUG] ", log.LstdFlags)
	infoLogger = log.New(os.Stdout, "", 0) // No prefix for info messages
}

// SetDebugMode enables or disables debug logging.
func SetDebugMode(enabled bool) {
	debugMode = enabled
}

// Debug logs debug messages only if debug mode is enabled.
func Debug(format string, args ...interface{}) {
	if debugMode {
		debugLogger.Printf(format, args...)
	}
}

// Info logs info messages always.
func Info(format string, args ...interface{}) {
	infoLogger.Printf(format, args...)
}

// Printf is an alias for Info for compatibility.
func Printf(format string, args ...interface{}) {
	Info(format, args...)
}
