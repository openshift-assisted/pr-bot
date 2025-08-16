// Package gitlab provides functionality to interact with GitLab repositories for MCE validation.
package gitlab

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shay23bra/pr-bot/internal/github"
	"github.com/shay23bra/pr-bot/internal/logger"
	"github.com/shay23bra/pr-bot/internal/models"
	gitlab "gitlab.com/gitlab-org/api/client-go"
	"gopkg.in/yaml.v2"
)

// Client wraps the GitLab API client.
type Client struct {
	client       *gitlab.Client
	githubClient *github.Client
	ctx          context.Context
}

// NewClient creates a new GitLab client.
func NewClient(ctx context.Context, token string, githubClient *github.Client) *Client {
	client, _ := gitlab.NewClient(token, gitlab.WithBaseURL("https://gitlab.cee.redhat.com"))
	return &Client{
		client:       client,
		githubClient: githubClient,
		ctx:          ctx,
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
	return c.ValidateMCESnapshotForComponent(product, version, gaDate, prCommitSHA, "assisted-service")
}

func (c *Client) ValidateMCESnapshotForComponent(product, version string, gaDate *time.Time, prCommitSHA, componentName string) (*models.MCESnapshotValidation, error) {
	if gaDate == nil {
		return nil, fmt.Errorf("GA date is required for validation")
	}

	logger.Debug("Starting MCE snapshot validation for %s %s (GA: %s)", product, version, gaDate.Format("2006-01-02"))

	result := &models.MCESnapshotValidation{
		Product:       product,
		Version:       version,
		GADate:        gaDate,
		ComponentName: componentName,
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

	// Extract component SHA from down-sha.yaml
	componentSHA, err := c.ExtractComponentSHA(mceBranch, snapshotFolder, componentName)
	if err != nil {
		result.ErrorMessage = fmt.Sprintf("Failed to extract %s SHA: %v", componentName, err)
		return result, nil
	}
	result.AssistedServiceSHA = componentSHA

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

// ExtractComponentSHA extracts the SHA for a specific component from down-sha.yaml.
func (c *Client) ExtractComponentSHA(mceBranch, snapshotFolder, componentName string) (string, error) {
	logger.Debug("Extracting %s SHA from down-sha.yaml", componentName)

	// Special handling for assisted-installer-ui
	if componentName == "assisted-installer-ui" {
		return c.extractAssistedInstallerUIVersion(mceBranch, snapshotFolder)
	}

	// Try to extract SHA from the specified snapshot folder first
	sha, err := c.extractComponentSHAFromSnapshot(mceBranch, snapshotFolder, componentName)
	if err == nil {
		return sha, nil
	}

	logger.Debug("Failed to extract SHA from snapshot %s: %v", snapshotFolder, err)
	logger.Debug("Trying fallback to previous snapshots...")

	// Fallback: try previous snapshot folders with the same version
	return c.extractComponentSHAWithFallback(mceBranch, snapshotFolder, componentName)
}

// extractComponentSHAFromSnapshot extracts SHA from a specific snapshot folder.
func (c *Client) extractComponentSHAFromSnapshot(mceBranch, snapshotFolder, componentName string) (string, error) {
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

	// Look for the component (e.g., multicluster-engine-assisted-service-9, multicluster-engine-assisted-installer-X, multicluster-engine-assisted-installer-agent-Y, or assisted-installer-ui via stolostron/console)
	var targetComponent interface{}
	var componentFound bool

	// Try to find component by exact pattern match first
	expectedPrefix := fmt.Sprintf("multicluster-engine-%s-", componentName)
	for key, value := range componentMap {
		if strings.HasPrefix(key, expectedPrefix) {
			logger.Debug("Found component by prefix: %s", key)
			targetComponent = value
			componentFound = true
			break
		}
	}

	// If not found by prefix, try substring match for backward compatibility
	if !componentFound {
		for key, value := range componentMap {
			if strings.Contains(key, componentName) {
				logger.Debug("Found component by substring: %s", key)
				targetComponent = value
				componentFound = true
				break
			}
		}
	}

	if !componentFound {
		return "", fmt.Errorf("component matching '%s' not found in component map", componentName)
	}

	// Convert to map[string]interface{} for repository navigation
	var componentMap2 map[string]interface{}

	if mapInterfaceInterface, ok := targetComponent.(map[interface{}]interface{}); ok {
		// Convert map[interface{}]interface{} to map[string]interface{}
		componentMap2 = make(map[string]interface{})
		for k, v := range mapInterfaceInterface {
			if keyStr, ok := k.(string); ok {
				componentMap2[keyStr] = v
			}
		}
	} else if mapStringInterface, ok := targetComponent.(map[string]interface{}); ok {
		// Already the correct type
		componentMap2 = mapStringInterface
	} else {
		return "", fmt.Errorf("%s component has unexpected structure (not a map), type: %T", componentName, targetComponent)
	}

	// Look for openshift/{componentName} repository
	repoKey := fmt.Sprintf("openshift/%s", componentName)
	targetRepo, exists := componentMap2[repoKey]
	if !exists {
		return "", fmt.Errorf("%s repository not found in %s component", repoKey, componentName)
	}

	// Convert to map[string]interface{} for SHA extraction
	var repoMap map[string]interface{}

	if mapInterfaceInterface, ok := targetRepo.(map[interface{}]interface{}); ok {
		// Convert map[interface{}]interface{} to map[string]interface{}
		repoMap = make(map[string]interface{})
		for k, v := range mapInterfaceInterface {
			if keyStr, ok := k.(string); ok {
				repoMap[keyStr] = v
			}
		}
	} else if mapStringInterface, ok := targetRepo.(map[string]interface{}); ok {
		// Already the correct type
		repoMap = mapStringInterface
	} else {
		return "", fmt.Errorf("%s has unexpected structure (not a map), type: %T", repoKey, targetRepo)
	}

	// Extract SHA
	shaInterface, exists := repoMap["sha"]
	if !exists {
		return "", fmt.Errorf("sha field not found in %s", repoKey)
	}

	sha, ok := shaInterface.(string)
	if !ok {
		return "", fmt.Errorf("sha field is not a string")
	}

	if sha == "" {
		return "", fmt.Errorf("SHA is empty for %s", repoKey)
	}

	logger.Debug("Extracted %s SHA: %s", componentName, sha)
	return sha, nil
}

// extractComponentSHAWithFallback tries to find the SHA from previous snapshots with the same version.
func (c *Client) extractComponentSHAWithFallback(mceBranch, originalSnapshot, componentName string) (string, error) {
	// First, get the expected version from the original snapshot's build-status.yaml
	expectedVersion, err := c.getVersionFromSnapshot(mceBranch, originalSnapshot)
	if err != nil {
		return "", fmt.Errorf("failed to get expected version from original snapshot: %v", err)
	}

	logger.Debug("Looking for snapshots with version %s", expectedVersion)

	// Get all available snapshot folders
	snapshots, err := c.getAllSnapshotFolders(mceBranch)
	if err != nil {
		return "", fmt.Errorf("failed to get snapshot folders: %v", err)
	}

	// Sort snapshots in reverse chronological order (newest first, excluding the original)
	var candidateSnapshots []string
	for _, snapshot := range snapshots {
		if snapshot != originalSnapshot && snapshot < originalSnapshot {
			candidateSnapshots = append(candidateSnapshots, snapshot)
		}
	}

	// Sort in reverse order (newest first)
	for i := 0; i < len(candidateSnapshots)/2; i++ {
		j := len(candidateSnapshots) - 1 - i
		candidateSnapshots[i], candidateSnapshots[j] = candidateSnapshots[j], candidateSnapshots[i]
	}

	// Try each candidate snapshot
	for _, candidateSnapshot := range candidateSnapshots {
		logger.Debug("Trying snapshot %s", candidateSnapshot)

		// Check if this snapshot has the same version
		version, err := c.getVersionFromSnapshot(mceBranch, candidateSnapshot)
		if err != nil {
			logger.Debug("Failed to get version from snapshot %s: %v", candidateSnapshot, err)
			continue
		}

		if version != expectedVersion {
			logger.Debug("Snapshot %s has different version %s, expected %s", candidateSnapshot, version, expectedVersion)
			continue
		}

		logger.Debug("Snapshot %s has matching version %s", candidateSnapshot, version)

		// Try to extract SHA from this snapshot
		sha, err := c.extractComponentSHAFromSnapshot(mceBranch, candidateSnapshot, componentName)
		if err != nil {
			logger.Debug("Failed to extract SHA from snapshot %s: %v", candidateSnapshot, err)
			continue
		}

		logger.Debug("Successfully extracted %s SHA from fallback snapshot %s: %s", componentName, candidateSnapshot, sha)
		return sha, nil
	}

	return "", fmt.Errorf("no valid snapshots found with version %s containing down-sha.yaml", expectedVersion)
}

// getVersionFromSnapshot gets the version from build-status.yaml in a snapshot.
func (c *Client) getVersionFromSnapshot(mceBranch, snapshotFolder string) (string, error) {
	projectID := "acm-cicd/mce-bb2"
	filePath := fmt.Sprintf("snapshots/%s/build-status.yaml", snapshotFolder)

	file, resp, err := c.client.RepositoryFiles.GetFile(projectID, filePath, &gitlab.GetFileOptions{
		Ref: &mceBranch,
	})
	if err != nil {
		return "", fmt.Errorf("failed to get build-status.yaml: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to get build-status.yaml, status: %d", resp.StatusCode)
	}

	// Decode and parse
	content, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return "", fmt.Errorf("failed to decode build-status.yaml: %w", err)
	}

	var buildStatus BuildStatus
	if err := yaml.Unmarshal(content, &buildStatus); err != nil {
		return "", fmt.Errorf("failed to parse build-status.yaml: %w", err)
	}

	return buildStatus.Announce.Version, nil
}

// getAllSnapshotFolders gets all snapshot folder names in a branch.
func (c *Client) getAllSnapshotFolders(mceBranch string) ([]string, error) {
	projectID := "acm-cicd/mce-bb2"
	path := "snapshots"

	opts := &gitlab.ListTreeOptions{
		Path:      &path,
		Ref:       &mceBranch,
		Recursive: gitlab.Ptr(false),
	}

	tree, resp, err := c.client.Repositories.ListTree(projectID, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots directory: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list snapshots directory, status: %d", resp.StatusCode)
	}

	var folders []string
	for _, item := range tree {
		if item.Type == "tree" {
			folders = append(folders, item.Name)
		}
	}

	return folders, nil
}

// extractAssistedInstallerUIVersion extracts the assisted-installer-ui version through stolostron/console
func (c *Client) extractAssistedInstallerUIVersion(mceBranch, snapshotFolder string) (string, error) {
	logger.Debug("Extracting assisted-installer-ui version via stolostron/console")

	// First, get the stolostron/console SHA from down-sha.yaml
	consoleSHA, err := c.extractStolostronConsoleSHA(mceBranch, snapshotFolder)
	if err != nil {
		return "", fmt.Errorf("failed to extract stolostron/console SHA: %v", err)
	}

	logger.Debug("Found stolostron/console SHA: %s", consoleSHA)

	// Fetch package.json from GitHub at that specific SHA
	version, err := c.fetchUILibVersionFromGitHub(consoleSHA)
	if err != nil {
		return "", fmt.Errorf("failed to fetch UI lib version from GitHub: %v", err)
	}

	// Convert version to tag format (e.g., "2.15.1-cim" -> "v2.15.1-cim")
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}

	logger.Debug("Extracted assisted-installer-ui version: %s", version)
	return version, nil
}

// extractStolostronConsoleSHA extracts the SHA for stolostron/console from down-sha.yaml
func (c *Client) extractStolostronConsoleSHA(mceBranch, snapshotFolder string) (string, error) {
	// Get the down-sha.yaml content
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

	// Decode and parse YAML
	content, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return "", fmt.Errorf("failed to decode down-sha.yaml: %w", err)
	}

	var downSHA DownSHA
	if err := yaml.Unmarshal(content, &downSHA); err != nil {
		return "", fmt.Errorf("failed to parse down-sha.yaml: %w", err)
	}

	// Navigate to component structure
	component, exists := downSHA["component"]
	if !exists {
		return "", fmt.Errorf("component key not found in down-sha.yaml")
	}

	componentMap, ok := component.(map[interface{}]interface{})
	if !ok {
		return "", fmt.Errorf("component has unexpected structure")
	}

	// Look for stolostron/console
	for key, value := range componentMap {
		if keyStr, ok := key.(string); ok && strings.Contains(keyStr, "console") {
			// Found console component, extract SHA
			if valueMap, ok := value.(map[interface{}]interface{}); ok {
				if consoleRepo, exists := valueMap["stolostron/console"]; exists {
					if consoleMap, ok := consoleRepo.(map[interface{}]interface{}); ok {
						if sha, exists := consoleMap["sha"]; exists {
							if shaStr, ok := sha.(string); ok {
								logger.Debug("Found stolostron/console SHA: %s", shaStr)
								return shaStr, nil
							}
						}
					}
				}
			}
		}
	}

	return "", fmt.Errorf("stolostron/console not found in down-sha.yaml")
}

// fetchUILibVersionFromGitHub fetches the @openshift-assisted/ui-lib version from GitHub
func (c *Client) fetchUILibVersionFromGitHub(consoleSHA string) (string, error) {
	if c.githubClient == nil {
		return "", fmt.Errorf("GitHub client not available")
	}

	// Fetch frontend/package.json from stolostron/console at the specific SHA
	content, err := c.githubClient.GetFileContent("stolostron", "console", "frontend/package.json", consoleSHA)
	if err != nil {
		return "", fmt.Errorf("failed to fetch package.json: %v", err)
	}

	// Parse package.json
	var packageJSON map[string]interface{}
	if err := json.Unmarshal([]byte(content), &packageJSON); err != nil {
		return "", fmt.Errorf("failed to parse package.json: %v", err)
	}

	// Extract dependencies
	deps, ok := packageJSON["dependencies"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("dependencies not found in package.json")
	}

	// Look for @openshift-assisted/ui-lib
	uiLibVersion, ok := deps["@openshift-assisted/ui-lib"].(string)
	if !ok {
		return "", fmt.Errorf("@openshift-assisted/ui-lib not found in dependencies")
	}

	logger.Debug("Found @openshift-assisted/ui-lib version: %s", uiLibVersion)
	return uiLibVersion, nil
}

// ExtractAssistedServiceSHA extracts the SHA for assisted-service - backward compatibility wrapper
func (c *Client) ExtractAssistedServiceSHA(mceBranch, snapshotFolder string) (string, error) {
	return c.ExtractComponentSHA(mceBranch, snapshotFolder, "assisted-service")
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
