//go:build !embedded
// +build !embedded

// Package embedded provides filesystem-based data access for pr-bot.
// This is the DEFAULT build - used when no build tags are specified
package embedded

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

// ErrNoDataAvailable is returned when no data source is available
var ErrNoDataAvailable = fmt.Errorf("no Excel data available: not embedded and no fallback path provided")

// GetExcelReader returns the Excel data from filesystem.
func GetExcelReader(fallbackPath string) (io.Reader, error) {
	if fallbackPath == "" {
		return nil, ErrNoDataAvailable
	}

	data, err := os.ReadFile(fallbackPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read fallback file %s: %w", fallbackPath, err)
	}
	return bytes.NewReader(data), nil
}

// SaveEmbeddedDataToTempFile returns error for non-embedded builds.
func SaveEmbeddedDataToTempFile() (string, func(), error) {
	return "", nil, fmt.Errorf("no embedded data available - this is a public build")
}

// HasEmbeddedData returns false (never embedded for public builds)
func HasEmbeddedData() bool {
	return false
}

// GetDataSize returns 0 for non-embedded builds
func GetDataSize() int {
	return 0
}

// GetDataSource returns filesystem source description
func GetDataSource() string {
	return "filesystem"
}
