//go:build !filesystem
// +build !filesystem

// Package embedded provides build-time embedded data for pr-bot.
// This is the DEFAULT build - embedded data is included by default
package embedded

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"os"
)

// Excel data embedded at build time (only when -tags=embedded is used)
//
//go:embed schedule.xlsx
var embeddedExcelData []byte

// GetExcelReader returns the Excel data as an io.Reader.
func GetExcelReader(fallbackPath string) (io.Reader, error) {
	// Use embedded data
	return bytes.NewReader(embeddedExcelData), nil
}

// SaveEmbeddedDataToTempFile creates a temporary file with embedded data.
func SaveEmbeddedDataToTempFile() (string, func(), error) {
	// Create temporary file
	tempFile, err := os.CreateTemp("", "pr-bot-schedule-*.xlsx")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp file: %w", err)
	}

	// Write embedded data to temp file
	if _, err := tempFile.Write(embeddedExcelData); err != nil {
		tempFile.Close()
		os.Remove(tempFile.Name())
		return "", nil, fmt.Errorf("failed to write to temp file: %w", err)
	}

	if err := tempFile.Close(); err != nil {
		os.Remove(tempFile.Name())
		return "", nil, fmt.Errorf("failed to close temp file: %w", err)
	}

	// Return cleanup function
	cleanup := func() {
		os.Remove(tempFile.Name())
	}

	return tempFile.Name(), cleanup, nil
}

// HasEmbeddedData returns true (always true for embedded builds)
func HasEmbeddedData() bool {
	return true
}

// GetDataSize returns the size of embedded data in bytes
func GetDataSize() int {
	return len(embeddedExcelData)
}

// GetDataSource returns embedded source description
func GetDataSource() string {
	return fmt.Sprintf("embedded (%d bytes)", len(embeddedExcelData))
}
