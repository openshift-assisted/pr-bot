// Package github provides a wrapper around the GitHub API client for merged-pr-bot operations.
package github

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"strconv"

	"github.com/google/go-github/v57/github"
	"github.com/shay23bra/pr-bot/internal/models"
	"golang.org/x/oauth2"
)

// Constants for GitHub API operations.
const (
	DefaultPageSize     = 100
	ExpectedMatchGroups = 4
	GitHubHost          = "github.com"
)

// Client wraps the GitHub API client.
type Client struct {
	client *github.Client
	ctx    context.Context
}

// NewClient creates a new GitHub client with authentication.
func NewClient(ctx context.Context, token string) *Client {
	var client *github.Client

	if token != "" {
		ts := oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: token},
		)
		tc := oauth2.NewClient(ctx, ts)
		client = github.NewClient(tc)
	} else {
		client = github.NewClient(nil)
	}

	return &Client{
		client: client,
		ctx:    ctx,
	}
}

// GetPRInfo fetches detailed information about a pull request.
func (c *Client) GetPRInfo(owner, repo string, prNumber int) (*models.PRInfo, error) {
	pr, _, err := c.client.PullRequests.Get(c.ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get PR %d: %w", prNumber, err)
	}

	if pr.MergedAt == nil {
		return nil, fmt.Errorf("PR %d is not merged", prNumber)
	}

	prInfo := &models.PRInfo{
		Number:     pr.GetNumber(),
		Title:      pr.GetTitle(),
		Hash:       pr.GetMergeCommitSHA(),
		MergedAt:   pr.MergedAt.GetTime(),
		MergedInto: pr.GetBase().GetRef(),
		URL:        pr.GetHTMLURL(),
	}

	return prInfo, nil
}

// GetReleaseBranches fetches all branches matching the release pattern.
func (c *Client) GetReleaseBranches(owner, repo, branchPrefix string) ([]string, error) {
	var allBranches []string

	opts := &github.BranchListOptions{
		ListOptions: github.ListOptions{PerPage: DefaultPageSize},
	}

	for {
		branches, resp, err := c.client.Repositories.ListBranches(c.ctx, owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list branches: %w", err)
		}

		for _, branch := range branches {
			name := branch.GetName()
			if strings.HasPrefix(name, branchPrefix) {
				allBranches = append(allBranches, name)
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return allBranches, nil
}

// CheckCommitInBranch checks if a commit exists in a specific branch.
func (c *Client) CheckCommitInBranch(owner, repo, commitSHA, branchName string) (bool, *time.Time, error) {
	// Get the commit to check if it exists
	commit, _, err := c.client.Repositories.GetCommit(c.ctx, owner, repo, commitSHA, nil)
	if err != nil {
		return false, nil, fmt.Errorf("failed to get commit %s: %w", commitSHA, err)
	}

	// Check if the commit is reachable from the branch
	comparison, _, err := c.client.Repositories.CompareCommits(c.ctx, owner, repo, branchName, commitSHA, nil)
	if err != nil {
		// If comparison fails, the commit might not be in this branch
		return false, nil, nil
	}

	// If the comparison shows no commits ahead, the commit is in the branch
	found := comparison.GetAheadBy() == 0

	var mergedAt *time.Time
	if found && commit.Commit != nil && commit.Commit.Committer != nil {
		mergedAt = commit.Commit.Committer.Date.GetTime()
	}

	return found, mergedAt, nil
}

// GetVersionTags gets all tags that match a version prefix (e.g., v2.40 -> v2.40.0, v2.40.1, etc.)
func (c *Client) GetVersionTags(owner, repo, versionPrefix string) ([]string, error) {
	var matchingTags []string

	opts := &github.ListOptions{PerPage: DefaultPageSize}

	for {
		tags, resp, err := c.client.Repositories.ListTags(c.ctx, owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list tags: %w", err)
		}

		for _, tag := range tags {
			tagName := tag.GetName()
			// Check if tag matches the version prefix pattern (e.g., v2.40.0, v2.40.1 for prefix v2.40)
			if strings.HasPrefix(tagName, versionPrefix+".") {
				matchingTags = append(matchingTags, tagName)
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return matchingTags, nil
}

// CheckCommitInTag checks if a commit exists in a specific tag
func (c *Client) CheckCommitInTag(owner, repo, commitSHA, tagName string) (bool, *time.Time, error) {
	// Get the tag reference
	tagRef, _, err := c.client.Git.GetRef(c.ctx, owner, repo, "tags/"+tagName)
	if err != nil {
		return false, nil, fmt.Errorf("failed to get tag %s: %w", tagName, err)
	}

	// Get the tag object SHA
	tagSHA := tagRef.GetObject().GetSHA()

	// List commits from this tag and search for our commit SHA
	opts := &github.CommitsListOptions{
		SHA:         tagSHA,
		ListOptions: github.ListOptions{PerPage: DefaultPageSize},
	}

	for {
		commits, resp, err := c.client.Repositories.ListCommits(c.ctx, owner, repo, opts)
		if err != nil {
			return false, nil, fmt.Errorf("failed to list commits for tag %s: %w", tagName, err)
		}

		// Search for our commit SHA in this page
		for _, commit := range commits {
			if commit.GetSHA() == commitSHA {
				// Found the commit in this tag's history
				var tagDate *time.Time

				// Try to get the tag date
				tag, _, err := c.client.Git.GetTag(c.ctx, owner, repo, tagSHA)
				if err == nil && tag.Tagger != nil && tag.Tagger.Date != nil {
					tagDate = tag.Tagger.Date.GetTime()
				} else {
					// Fallback: get tag commit date
					tagCommit, _, err := c.client.Repositories.GetCommit(c.ctx, owner, repo, tagSHA, nil)
					if err == nil && tagCommit.Commit != nil && tagCommit.Commit.Committer != nil {
						tagDate = tagCommit.Commit.Committer.Date.GetTime()
					}
				}

				return true, tagDate, nil
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// Commit not found in this tag's history
	return false, nil, nil
}

// FindCommitInVersionTags finds which version tags contain a specific commit for a given version prefix
func (c *Client) FindCommitInVersionTags(owner, repo, commitSHA, versionPrefix string) ([]string, error) {
	// Get all tags for this version prefix
	tags, err := c.GetVersionTags(owner, repo, versionPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to get version tags for %s: %w", versionPrefix, err)
	}

	var foundTags []string
	for _, tag := range tags {
		found, _, err := c.CheckCommitInTag(owner, repo, commitSHA, tag)
		if err != nil {
			// Log error but continue with other tags
			continue
		}
		if found {
			foundTags = append(foundTags, tag)
		}
	}

	// If we found tags, return only the earliest (first) release version
	// since later patch versions automatically include commits from earlier versions
	if len(foundTags) > 0 {
		earliestTag := findEarliestVersion(foundTags)
		return []string{earliestTag}, nil
	}

	return foundTags, nil
}

// findEarliestVersion finds the earliest version from a list of version tags
func findEarliestVersion(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	if len(tags) == 1 {
		return tags[0]
	}

	// Simple string comparison works for semantic versions like v2.40.0, v2.40.1
	// since Go's string comparison will sort them correctly
	earliest := tags[0]
	for _, tag := range tags[1:] {
		if tag < earliest {
			earliest = tag
		}
	}

	return earliest
}

// GetAllTags gets all tags from the repository
func (c *Client) GetAllTags(owner, repo string) ([]string, error) {
	var allTags []string

	opts := &github.ListOptions{PerPage: DefaultPageSize}

	for {
		tags, resp, err := c.client.Repositories.ListTags(c.ctx, owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list tags: %w", err)
		}

		for _, tag := range tags {
			allTags = append(allTags, tag.GetName())
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return allTags, nil
}

// TagExists checks if a specific tag exists in the repository
func (c *Client) TagExists(owner, repo, tag string) (bool, error) {
	_, _, err := c.client.Git.GetRef(c.ctx, owner, repo, "tags/"+tag)
	if err != nil {
		// If we get a 404, the tag doesn't exist
		if strings.Contains(err.Error(), "404") {
			return false, nil
		}
		// Other errors should be reported
		return false, fmt.Errorf("error checking tag %s: %w", tag, err)
	}
	return true, nil
}

// FindPreviousVersion finds the previous version for a given version tag
// For v2.40.0 -> find v2.39.X (latest patch of previous minor)
// For v2.40.1 -> find v2.40.0 (previous patch)
func (c *Client) FindPreviousVersion(owner, repo, version string) (string, error) {
	// Get all tags
	allTags, err := c.GetAllTags(owner, repo)
	if err != nil {
		return "", fmt.Errorf("failed to get tags: %w", err)
	}

	// Parse the input version
	major, minor, patch, err := parseVersion(version)
	if err != nil {
		return "", fmt.Errorf("invalid version format %s: %w", version, err)
	}

	var candidates []string

	if patch > 0 {
		// For patch versions (e.g., v2.40.3), find the nearest previous patch
		// Try v2.40.2, v2.40.1, v2.40.0 in that order
		for p := patch - 1; p >= 0; p-- {
			targetVersion := fmt.Sprintf("v%d.%d.%d", major, minor, p)
			for _, tag := range allTags {
				if tag == targetVersion {
					return tag, nil
				}
			}
		}

		// If no patch versions found, fall back to looking for previous minor versions
		targetPrefix := fmt.Sprintf("v%d.%d.", major, minor-1)
		for _, tag := range allTags {
			if strings.HasPrefix(tag, targetPrefix) {
				candidates = append(candidates, tag)
			}
		}
	} else {
		// For minor versions (e.g., v2.40.0), find the latest patch of the previous minor (v2.39.X)
		targetPrefix := fmt.Sprintf("v%d.%d.", major, minor-1)
		for _, tag := range allTags {
			if strings.HasPrefix(tag, targetPrefix) {
				candidates = append(candidates, tag)
			}
		}
	}

	// Find the latest patch version from candidates
	if len(candidates) > 0 {
		latest := candidates[0]
		for _, candidate := range candidates[1:] {
			if candidate > latest { // String comparison works for semantic versions
				latest = candidate
			}
		}
		return latest, nil
	}

	return "", fmt.Errorf("no previous version found for %s", version)
}

// GetCommitsBetweenTags gets all commits between two tags
func (c *Client) GetCommitsBetweenTags(owner, repo, baseTag, headTag string) ([]*github.RepositoryCommit, error) {
	// Compare the two tags to get commits
	comparison, _, err := c.client.Repositories.CompareCommits(c.ctx, owner, repo, baseTag, headTag, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to compare %s...%s: %w", baseTag, headTag, err)
	}

	return comparison.Commits, nil
}

// GetCommitsBetweenSHAs gets all commits between two SHA hashes
func (c *Client) GetCommitsBetweenSHAs(owner, repo, baseSHA, headSHA string) ([]*github.RepositoryCommit, error) {
	// Compare the two SHAs to get commits
	comparison, _, err := c.client.Repositories.CompareCommits(c.ctx, owner, repo, baseSHA, headSHA, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to compare %s...%s: %w", baseSHA[:8], headSHA[:8], err)
	}

	return comparison.Commits, nil
}

// parseVersion parses a version string like "v2.40.1" into major, minor, patch
func parseVersion(version string) (major, minor, patch int, err error) {
	// Remove 'v' prefix if present
	version = strings.TrimPrefix(version, "v")

	// Split by dots
	parts := strings.Split(version, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, 0, fmt.Errorf("invalid version format: expected x.y or x.y.z")
	}

	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid major version: %s", parts[0])
	}

	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid minor version: %s", parts[1])
	}

	if len(parts) == 3 {
		patch, err = strconv.Atoi(parts[2])
		if err != nil {
			return 0, 0, 0, fmt.Errorf("invalid patch version: %s", parts[2])
		}
	}

	return major, minor, patch, nil
}

// ExtractVersionFromBranch extracts version from branch name using regex.
func ExtractVersionFromBranch(branchName, prefix string) string {
	return ExtractVersionFromBranchWithPattern(branchName, prefix)
}

// ExtractVersionFromBranchWithPattern extracts version from branch name using regex for different patterns.
func ExtractVersionFromBranchWithPattern(branchName, pattern string) string {
	// Remove the pattern prefix to get the version part
	version := strings.TrimPrefix(branchName, pattern)

	// Use regex to extract version pattern based on the pattern type
	var versionRegex *regexp.Regexp

	switch pattern {
	case "release-ocm-":
		// For release-ocm- branches, extract version like "2.13", "1.0.5", etc.
		versionRegex = regexp.MustCompile(`^(\d+\.\d+(?:\.\d+)?)`)
	case "releases/v":
		// For releases/v branches, extract version like "2.15-cim", "2.44.0", etc.
		versionRegex = regexp.MustCompile(`^(\d+\.\d+(?:\.\d+)?(?:-\w+)?)`)
	case "release-v":
		// For release-v branches, extract version like "1.0.9.6", "2.1.0", etc.
		versionRegex = regexp.MustCompile(`^(\d+\.\d+\.\d+(?:\.\d+)?)`)
	case "release-":
		// For release- branches, extract version like "4.6", "4.7", etc.
		versionRegex = regexp.MustCompile(`^(\d+\.\d+(?:\.\d+)?)`)
	case "v":
		// For v branches, extract version like "2.40", "1.0.9.6", etc.
		versionRegex = regexp.MustCompile(`^(\d+\.\d+(?:\.\d+)?)`)
	default:
		// Default fallback for any other patterns
		versionRegex = regexp.MustCompile(`^(\d+\.\d+(?:\.\d+)?)`)
	}

	matches := versionRegex.FindStringSubmatch(version)
	if len(matches) > 1 {
		return matches[1]
	}

	return version
}

// BranchInfo represents information about a release branch and its pattern.
type BranchInfo struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"` // "release-ocm-", "release-", or "release-v"
	Version string `json:"version"`
}

// GetAllReleaseBranches fetches all branches matching various release patterns.
// It searches for branches that start with:
// - release-ocm-<version> (for ACM and MCE repositories)
// - release-<version> (like release-4.6, release-4.7, etc.)
// - release-v<version> (like release-v1.0.9.6)
// - v<version> (like v2.40)
func (c *Client) GetAllReleaseBranches(owner, repo string) ([]BranchInfo, error) {
	var allBranches []BranchInfo

	opts := &github.BranchListOptions{
		ListOptions: github.ListOptions{PerPage: DefaultPageSize},
	}

	// Define the branch patterns to search for
	branchPatterns := []string{
		"release-ocm-",
		"releases/v", // For assisted-installer-ui style branches
		"release-v",
		"release-",
		"v",
	}

	for {
		branches, resp, err := c.client.Repositories.ListBranches(c.ctx, owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list branches: %w", err)
		}

		for _, branch := range branches {
			name := branch.GetName()

			// Check each pattern and add to results
			for _, pattern := range branchPatterns {
				if strings.HasPrefix(name, pattern) {
					// Special handling for patterns to avoid duplicates
					if pattern == "release-" {
						// Skip if it matches release-v, release-ocm-, or releases/v patterns
						if strings.HasPrefix(name, "release-v") || strings.HasPrefix(name, "release-ocm-") || strings.HasPrefix(name, "releases/v") {
							continue
						}
					}
					if pattern == "release-v" {
						// Skip if it matches releases/v pattern
						if strings.HasPrefix(name, "releases/v") {
							continue
						}
					}
					if pattern == "v" {
						// Skip if it matches release-v or releases/v patterns or if it's not a version pattern
						if strings.HasPrefix(name, "release-v") || strings.HasPrefix(name, "releases/v") {
							continue
						}
						// Only match if it's v followed by a digit (version pattern)
						if len(name) > 1 && !regexp.MustCompile(`^v\d`).MatchString(name) {
							continue
						}
					}

					version := ExtractVersionFromBranchWithPattern(name, pattern)
					branchInfo := BranchInfo{
						Name:    name,
						Pattern: pattern,
						Version: version,
					}
					allBranches = append(allBranches, branchInfo)
					break // Only match one pattern per branch
				}
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return allBranches, nil
}

// GetCommit gets commit information by SHA.
func (c *Client) GetCommit(owner, repo, sha string) (*github.RepositoryCommit, *github.Response, error) {
	return c.client.Repositories.GetCommit(c.ctx, owner, repo, sha, nil)
}

// GetFileContent fetches the content of a file from a specific SHA.
func (c *Client) GetFileContent(owner, repo, path, sha string) (string, error) {
	fileContent, _, _, err := c.client.Repositories.GetContents(c.ctx, owner, repo, path, &github.RepositoryContentGetOptions{
		Ref: sha,
	})
	if err != nil {
		return "", fmt.Errorf("failed to fetch file %s from %s/%s at SHA %s: %w", path, owner, repo, sha, err)
	}

	if fileContent == nil {
		return "", fmt.Errorf("file %s not found in %s/%s at SHA %s", path, owner, repo, sha)
	}

	content, err := fileContent.GetContent()
	if err != nil {
		return "", fmt.Errorf("failed to decode file content: %w", err)
	}

	return content, nil
}
