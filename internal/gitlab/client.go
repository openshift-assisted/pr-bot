// Package gitlab provides functionality to interact with GitLab repositories for MCE validation.
package gitlab

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sbratsla/pr-bot/internal/logger"
	"github.com/sbratsla/pr-bot/internal/models"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"gopkg.in/yaml.v2"
)

// Client wraps the GitLab API client.
type Client struct {
	client *gitlab.Client
	ctx    context.Context
}

// NewClient creates a new GitLab client.
func NewClient(ctx context.Context, token string) *Client {
	client, _ := gitlab.NewClient(token, gitlab.WithBaseURL("https://gitlab.cee.redhat.com"))
	return &Client{
		client: client,
		ctx:    ctx,
	}
}

// BuildStatus represents the structure of build-status.yaml
type BuildStatus struct {
	Announce struct {
		Version string `yaml:"version"`
	} `yaml:"announce"`
}

// DownSHA represents the structure of down-sha.yaml
// Using a flexible approach to handle different YAML structures
type DownSHA map[string]interface{}

// ValidateMCESnapshot performs the complete MCE snapshot validation process.
func (c *Client) ValidateMCESnapshot(product, version string, gaDate *time.Time, prCommitSHA string) (*models.MCESnapshotValidation, error) {
	if gaDate == nil {
		return nil, fmt.Errorf("GA date is required for validation")
	}

	logger.Debug("Starting MCE snapshot validation for %s %s (GA: %s)", product, version, gaDate.Format("2006-01-02"))

	result := &models.MCESnapshotValidation{
		Product: product,
		Version: version,
		GADate:  gaDate,
	}

	// Calculate MCE branch name
	mceBranch, err := c.calculateMCEBranch(product, version)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("Failed to calculate MCE branch: %v", err)
		return result, nil
	}
	result.MCEBranch = mceBranch

	// Find appropriate snapshot folder
	snapshotFolder, err := c.findSnapshotFolder(mceBranch, *gaDate)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("Failed to find snapshot folder: %v", err)
		return result, nil
	}
	result.SnapshotFolder = snapshotFolder

	// Convert ACM version to MCE version for validation
	versionToValidate := version
	if product == "ACM" {
		// Convert ACM version to MCE version (e.g., ACM 2.13.1 -> MCE 2.8.1)
		if mceVersion, err := c.convertACMToMCEVersion(version); err == nil {
			versionToValidate = mceVersion
		}
	}

	// Validate version in build-status.yaml
	valid, err := c.validateVersionInBuildStatus(mceBranch, snapshotFolder, versionToValidate)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("Failed to validate build status: %v", err)
		return result, nil
	}
	if !valid {
		result.ErrorMessage = fmt.Sprintf("Version %s not found in build-status.yaml", versionToValidate)
		return result, nil
	}

	// Extract assisted-service SHA from down-sha.yaml
	assistedSHA, err := c.ExtractAssistedServiceSHA(mceBranch, snapshotFolder)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("Failed to extract assisted-service SHA: %v", err)
		return result, nil
	}
	result.AssistedServiceSHA = assistedSHA

	// TODO: Compare PR commit with extracted SHA
	// This would require GitHub integration which we'll handle in the analyzer
	result.ValidationSuccess = true

	logger.Debug("MCE snapshot validation completed successfully for %s %s", product, version)
	return result, nil
}

// calculateMCEBranch calculates the MCE branch name from product version.
func (c *Client) calculateMCEBranch(product, version string) (string, error) {
	if product == "MCE" {
		// For MCE versions, extract major.minor and create branch name
		parts := strings.Split(version, ".")
		if len(parts) < 2 {
			return "", fmt.Errorf("invalid MCE version format: %s", version)
		}
		return fmt.Sprintf("mce-%s.%s", parts[0], parts[1]), nil
	} else if product == "ACM" {
		// For ACM versions, calculate MCE equivalent (minor - 5)
		parts := strings.Split(version, ".")
		if len(parts) < 2 {
			return "", fmt.Errorf("invalid ACM version format: %s", version)
		}

		major, err := strconv.Atoi(parts[0])
		if err != nil {
			return "", fmt.Errorf("invalid major version in ACM version %s: %v", version, err)
		}

		minor, err := strconv.Atoi(parts[1])
		if err != nil {
			return "", fmt.Errorf("invalid minor version in ACM version %s: %v", version, err)
		}

		mceMinor := minor - 5
		if mceMinor < 0 {
			return "", fmt.Errorf("calculated MCE minor version is negative for ACM %s", version)
		}

		return fmt.Sprintf("mce-%d.%d", major, mceMinor), nil
	}

	return "", fmt.Errorf("unsupported product: %s", product)
}

// findSnapshotFolder finds the appropriate snapshot folder before the GA date.
func (c *Client) findSnapshotFolder(mceBranch string, gaDate time.Time) (string, error) {
	logger.Debug("Looking for snapshot folders in branch %s before %s", mceBranch, gaDate.Format("2006-01-02"))

	// List files in the snapshots directory
	projectID := "acm-cicd/mce-bb2"
	path := "snapshots"

	opts := &gitlab.ListTreeOptions{
		Path:      &path,
		Ref:       &mceBranch,
		Recursive: gitlab.Ptr(false),
	}

	tree, resp, err := c.client.Repositories.ListTree(projectID, opts)
	if err != nil {
		return "", fmt.Errorf("failed to list snapshots directory: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to list snapshots directory, status: %d", resp.StatusCode)
	}

	// Find folders with date format YYYY-MM-DD-HH-MM-SS
	var candidateFolders []string
	for _, item := range tree {
		if item.Type == "tree" && len(item.Name) >= 19 { // YYYY-MM-DD-HH-MM-SS is 19 chars
			// Parse the date part (first 10 characters)
			dateStr := item.Name[:10]
			folderDate, err := time.Parse("2006-01-02", dateStr)
			if err != nil {
				continue // Skip folders that don't match date format
			}

			// Only consider folders before the GA date
			if folderDate.Before(gaDate) {
				candidateFolders = append(candidateFolders, item.Name)
			}
		}
	}

	if len(candidateFolders) == 0 {
		return "", fmt.Errorf("no snapshot folders found before GA date %s", gaDate.Format("2006-01-02"))
	}

	// Find the latest folder (closest to GA date)
	var latestFolder string
	for _, folder := range candidateFolders {
		if latestFolder == "" || folder > latestFolder {
			latestFolder = folder
		}
	}

	logger.Debug("Selected snapshot folder: %s", latestFolder)
	return latestFolder, nil
}

// validateVersionInBuildStatus checks if the version matches in build-status.yaml.
func (c *Client) validateVersionInBuildStatus(mceBranch, snapshotFolder, expectedVersion string) (bool, error) {
	logger.Debug("Validating version %s in build-status.yaml", expectedVersion)

	projectID := "acm-cicd/mce-bb2"
	filePath := fmt.Sprintf("snapshots/%s/build-status.yaml", snapshotFolder)

	file, resp, err := c.client.RepositoryFiles.GetFile(projectID, filePath, &gitlab.GetFileOptions{
		Ref: &mceBranch,
	})
	if err != nil {
		return false, fmt.Errorf("failed to get build-status.yaml: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("failed to get build-status.yaml, status: %d", resp.StatusCode)
	}

	// Decode the file content
	content, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return false, fmt.Errorf("failed to decode build-status.yaml: %w", err)
	}

	// Parse YAML
	var buildStatus BuildStatus
	if err := yaml.Unmarshal(content, &buildStatus); err != nil {
		return false, fmt.Errorf("failed to parse build-status.yaml: %w", err)
	}

	// Check if version matches
	matches := buildStatus.Announce.Version == expectedVersion
	logger.Debug("Version validation: expected=%s, found=%s, matches=%v", expectedVersion, buildStatus.Announce.Version, matches)

	return matches, nil
}

// ExtractAssistedServiceSHA extracts the SHA for multicluster-engine-assisted-service-9 from down-sha.yaml.
func (c *Client) ExtractAssistedServiceSHA(mceBranch, snapshotFolder string) (string, error) {
	logger.Debug("Extracting assisted-service SHA from down-sha.yaml")

	projectID := "acm-cicd/mce-bb2"
	filePath := fmt.Sprintf("snapshots/%s/down-sha.yaml", snapshotFolder)

	file, resp, err := c.client.RepositoryFiles.GetFile(projectID, filePath, &gitlab.GetFileOptions{
		Ref: &mceBranch,
	})
	if err != nil {
		return "", fmt.Errorf("failed to get down-sha.yaml: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get down-sha.yaml, status: %d", resp.StatusCode)
	}

	// Decode the file content
	content, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return "", fmt.Errorf("failed to decode down-sha.yaml: %w", err)
	}

	// Parse YAML
	var downSHA DownSHA
	if err := yaml.Unmarshal(content, &downSHA); err != nil {
		return "", fmt.Errorf("failed to parse down-sha.yaml: %w", err)
	}

	// Debug: log available keys
	logger.Debug("Available keys in down-sha.yaml:")
	for key := range downSHA {
		logger.Debug("  - %s", key)
	}

	// Navigate to the component structure
	component, exists := downSHA["component"]
	if !exists {
		return "", fmt.Errorf("component key not found in down-sha.yaml")
	}

	// Debug: log component type and value
	logger.Debug("Component type: %T", component)

	// Convert from map[interface{}]interface{} to map[string]interface{}
	var componentMap map[string]interface{}

	if mapInterfaceInterface, ok := component.(map[interface{}]interface{}); ok {
		// Convert map[interface{}]interface{} to map[string]interface{}
		componentMap = make(map[string]interface{})
		for k, v := range mapInterfaceInterface {
			if keyStr, ok := k.(string); ok {
				componentMap[keyStr] = v
			}
		}
	} else if mapStringInterface, ok := component.(map[string]interface{}); ok {
		// Already the correct type
		componentMap = mapStringInterface
	} else {
		return "", fmt.Errorf("component has unexpected structure (not a map), type: %T", component)
	}

	// Debug: log available component keys
	logger.Debug("Available component keys:")
	for key := range componentMap {
		logger.Debug("  - %s", key)
	}

	// Look for multicluster-engine-assisted-service-9
	assistedServiceComponent, exists := componentMap["multicluster-engine-assisted-service-9"]
	if !exists {
		// Try alternative component names that might contain assisted-service
		for key, value := range componentMap {
			if strings.Contains(key, "assisted-service") {
				logger.Debug("Found potential assisted-service component: %s", key)
				assistedServiceComponent = value
				exists = true
				break
			}
		}

		if !exists {
			return "", fmt.Errorf("multicluster-engine-assisted-service-9 component not found in component map")
		}
	}

	// Convert to map[string]interface{} for repository navigation
	var assistedServiceMap map[string]interface{}

	if mapInterfaceInterface, ok := assistedServiceComponent.(map[interface{}]interface{}); ok {
		// Convert map[interface{}]interface{} to map[string]interface{}
		assistedServiceMap = make(map[string]interface{})
		for k, v := range mapInterfaceInterface {
			if keyStr, ok := k.(string); ok {
				assistedServiceMap[keyStr] = v
			}
		}
	} else if mapStringInterface, ok := assistedServiceComponent.(map[string]interface{}); ok {
		// Already the correct type
		assistedServiceMap = mapStringInterface
	} else {
		return "", fmt.Errorf("assisted-service component has unexpected structure (not a map), type: %T", assistedServiceComponent)
	}

	// Look for openshift/assisted-service
	assistedService, exists := assistedServiceMap["openshift/assisted-service"]
	if !exists {
		return "", fmt.Errorf("openshift/assisted-service repository not found in assisted-service component")
	}

	// Convert to map[string]interface{} for SHA extraction
	var assistedServiceRepoMap map[string]interface{}

	if mapInterfaceInterface, ok := assistedService.(map[interface{}]interface{}); ok {
		// Convert map[interface{}]interface{} to map[string]interface{}
		assistedServiceRepoMap = make(map[string]interface{})
		for k, v := range mapInterfaceInterface {
			if keyStr, ok := k.(string); ok {
				assistedServiceRepoMap[keyStr] = v
			}
		}
	} else if mapStringInterface, ok := assistedService.(map[string]interface{}); ok {
		// Already the correct type
		assistedServiceRepoMap = mapStringInterface
	} else {
		return "", fmt.Errorf("openshift/assisted-service has unexpected structure (not a map), type: %T", assistedService)
	}

	// Extract SHA
	shaInterface, exists := assistedServiceRepoMap["sha"]
	if !exists {
		return "", fmt.Errorf("sha field not found in openshift/assisted-service")
	}

	sha, ok := shaInterface.(string)
	if !ok {
		return "", fmt.Errorf("sha field is not a string")
	}

	if sha == "" {
		return "", fmt.Errorf("SHA is empty for openshift/assisted-service")
	}

	logger.Debug("Extracted assisted-service SHA: %s", sha)
	return sha, nil
}

// convertACMToMCEVersion converts an ACM version to its corresponding MCE version.
// ACM minor version - 5 = MCE minor version (e.g., ACM 2.13.1 -> MCE 2.8.1)
func (c *Client) convertACMToMCEVersion(acmVersion string) (string, error) {
	// Parse version format: X.Y.Z
	parts := strings.Split(acmVersion, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid version format: %s", acmVersion)
	}

	major := parts[0]
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", fmt.Errorf("invalid minor version in %s: %v", acmVersion, err)
	}
	patch := parts[2]

	// Convert ACM minor to MCE minor (subtract 5)
	mceMinor := minor - 5
	if mceMinor < 0 {
		return "", fmt.Errorf("invalid conversion: ACM minor %d would result in negative MCE minor", minor)
	}

	mceVersion := fmt.Sprintf("%s.%d.%s", major, mceMinor, patch)
	logger.Debug("Converted ACM version %s to MCE version %s", acmVersion, mceVersion)

	return mceVersion, nil
}

// FindLatestSnapshot finds the latest snapshot folder in the given MCE branch.
func (c *Client) FindLatestSnapshot(mceBranch string) (string, error) {
	logger.Debug("Finding latest snapshot in branch %s", mceBranch)

	projectID := "acm-cicd/mce-bb2"
	path := "snapshots"

	opts := &gitlab.ListTreeOptions{
		Path:      &path,
		Ref:       &mceBranch,
		Recursive: gitlab.Ptr(false),
	}

	tree, resp, err := c.client.Repositories.ListTree(projectID, opts)
	if err != nil {
		return "", fmt.Errorf("failed to list snapshots directory: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to list snapshots directory, status: %d", resp.StatusCode)
	}

	// Find all directory entries and get the latest one (by name sorting)
	var latestFolder string
	for _, item := range tree {
		if item.Type == "tree" { // Directory
			// Snapshot folders are typically named with timestamps like "2025-03-14-18-55-26"
			if latestFolder == "" || item.Name > latestFolder {
				latestFolder = item.Name
			}
		}
	}

	if latestFolder == "" {
		return "", fmt.Errorf("no snapshot folders found in %s branch", mceBranch)
	}

	logger.Debug("Found latest snapshot folder: %s", latestFolder)
	return latestFolder, nil
}
