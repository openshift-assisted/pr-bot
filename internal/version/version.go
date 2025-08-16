// Package version provides version checking functionality.
package version

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shay23bra/pr-bot/internal/logger"
)

const (
	// GitHub API URL for releases
	releasesAPI = "https://api.github.com/repos/shay23bra/pr-bot/releases/latest"
	// Timeout for version check
	versionCheckTimeout = 5 * time.Second
)

// Release represents a GitHub release response
type Release struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
}

// GetCurrentVersion returns the current version from the VERSION file
func GetCurrentVersion() (string, error) {
	// Get the directory of the executable or working directory
	execPath, err := os.Executable()
	if err != nil {
		// Fallback to current working directory
		execPath, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
	}

	versionFile := filepath.Join(filepath.Dir(execPath), "VERSION")

	// If VERSION file doesn't exist next to executable, try current directory
	if _, err := os.Stat(versionFile); os.IsNotExist(err) {
		versionFile = "VERSION"
	}

	data, err := os.ReadFile(versionFile)
	if err != nil {
		return "", fmt.Errorf("failed to read VERSION file: %w", err)
	}

	return strings.TrimSpace(string(data)), nil
}

// GetLatestVersion fetches the latest version from GitHub releases
func GetLatestVersion(ctx context.Context) (string, error) {
	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: versionCheckTimeout,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", releasesAPI, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	// Remove 'v' prefix if present
	version := strings.TrimPrefix(release.TagName, "v")
	return version, nil
}

// CheckForUpdates checks if a newer version is available and prints a message if so
func CheckForUpdates(ctx context.Context) {
	currentVersion, err := GetCurrentVersion()
	if err != nil {
		logger.Debug("Could not determine current version: %v", err)
		return
	}

	latestVersion, err := GetLatestVersion(ctx)
	if err != nil {
		logger.Debug("Could not check for updates: %v", err)
		return
	}

	if currentVersion != latestVersion {
		fmt.Printf("\n⚠️  A newer version is available: %s (current: %s)\n", latestVersion, currentVersion)
		fmt.Printf("📦 Update with: go install github.com/shay23bra/pr-bot@latest\n")
		fmt.Printf("🔗 Or download from: https://github.com/shay23bra/pr-bot/releases/latest\n\n")
	}
}

// PrintVersion prints the current version
func PrintVersion() {
	currentVersion, err := GetCurrentVersion()
	if err != nil {
		fmt.Printf("Version: unknown (%v)\n", err)
		return
	}
	fmt.Printf("Version: %s\n", currentVersion)
}
