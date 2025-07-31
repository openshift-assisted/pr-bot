// Package main provides a tool to analyze merged pull requests and determine their presence across release branches.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sbratsla/pr-bot/internal/config"
	"github.com/sbratsla/pr-bot/internal/ga"
	"github.com/sbratsla/pr-bot/internal/github"
	"github.com/sbratsla/pr-bot/internal/gitlab"
	"github.com/sbratsla/pr-bot/internal/jira"
	"github.com/sbratsla/pr-bot/internal/logger"
	"github.com/sbratsla/pr-bot/internal/models"
	"github.com/sbratsla/pr-bot/pkg/analyzer"
)

func main() {
	// Parse command-line flags
	debugFlag := flag.Bool("d", false, "Enable debug logging")
	versionFlag := flag.String("v", "", "") // Hidden from help - shown in usage examples
	prFlag := flag.String("pr", "", "Analyze a specific PR by URL")
	jiraTicketFlag := flag.String("jt", "", "Analyze all PRs related to a JIRA ticket")

	slackSearchCmd := flag.NewFlagSet("slack-search", flag.ExitOnError)
	slackSearchOwner := slackSearchCmd.String("owner", "stolostron", "Repository owner")
	slackSearchRepo := slackSearchCmd.String("repo", "backplane-operator", "Repository name")
	slackSearchPR := slackSearchCmd.Int("pr", 0, "PR number to search for")

	versionSearchCmd := flag.NewFlagSet("version-search", flag.ExitOnError)
	versionSearchChannel := versionSearchCmd.String("channel", "acm-z-release-info", "Slack channel name")

	slackTestCmd := flag.NewFlagSet("slack-test", flag.ExitOnError)

	// Set custom usage function
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: ./pr-bot [options]\n")
		fmt.Fprintf(os.Stderr, "\nOptions:\n")
		fmt.Fprintf(os.Stderr, "  -pr <PR_URL>      Analyze a PR across all release branches\n")
		fmt.Fprintf(os.Stderr, "  -jt <JIRA_URL>    Analyze all PRs related to a JIRA ticket\n")
		fmt.Fprintf(os.Stderr, "  -v <version>      Compare GitHub tag with previous version\n")
		fmt.Fprintf(os.Stderr, "  -v mce <version>  Compare MCE version with previous version\n")
		fmt.Fprintf(os.Stderr, "  -d                Enable debug logging\n")
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  ./pr-bot -pr https://github.com/openshift/assisted-service/pull/7788\n")
		fmt.Fprintf(os.Stderr, "  ./pr-bot -jt https://issues.redhat.com/browse/MGMT-20662\n")
		fmt.Fprintf(os.Stderr, "  ./pr-bot -jt MGMT-20662\n")
		fmt.Fprintf(os.Stderr, "  ./pr-bot -v v2.40.1\n")
		fmt.Fprintf(os.Stderr, "  ./pr-bot -v mce 2.8.1\n")
	}

	flag.Parse()

	// Enable debug logging if requested
	if *debugFlag {
		logger.SetDebugMode(true)
	}

	args := flag.Args()

	// Handle version comparison mode
	if *versionFlag != "" {
		// Check if this is MCE version comparison (format: "mce X.Y.Z")
		if strings.HasPrefix(strings.ToLower(*versionFlag), "mce ") {
			mceVersion := strings.TrimPrefix(strings.ToLower(*versionFlag), "mce ")
			mceVersion = strings.TrimSpace(mceVersion)
			handleMCEVersionComparison(mceVersion)
		} else if *versionFlag == "mce" && len(args) > 0 {
			// Handle case where "mce" and version are separate arguments: -v mce 2.8.1
			mceVersion := args[0]
			handleMCEVersionComparison(mceVersion)
		} else {
			handleVersionComparison(*versionFlag)
		}
		return
	}

	// Handle PR analysis mode
	if *prFlag != "" {
		handlePRAnalysis(*prFlag)
		return
	}

	// Handle JIRA ticket analysis mode
	if *jiraTicketFlag != "" {
		handleJiraTicketAnalysis(*jiraTicketFlag)
		return
	}

	// Handle subcommands
	if len(args) > 0 {
		switch args[0] {
		case "slack-search":
			if len(args) < 2 {
				fmt.Fprintf(os.Stderr, "Usage: %s slack-search [options]\n", os.Args[0])
				slackSearchCmd.PrintDefaults()
				os.Exit(1)
			}
			slackSearchCmd.Parse(args[1:])
			if *slackSearchPR == 0 {
				fmt.Fprintf(os.Stderr, "Error: -pr flag is required\n")
				slackSearchCmd.PrintDefaults()
				os.Exit(1)
			}
			handleSlackSearch(*slackSearchOwner, *slackSearchRepo, *slackSearchPR)
			return

		case "version-search":
			if len(args) < 2 {
				fmt.Fprintf(os.Stderr, "Usage: %s version-search [options]\n", os.Args[0])
				versionSearchCmd.PrintDefaults()
				os.Exit(1)
			}
			versionSearchCmd.Parse(args[1:])
			handleVersionSearch(*versionSearchChannel)
			return

		case "slack-test":
			if len(args) < 2 {
				fmt.Fprintf(os.Stderr, "Usage: %s slack-test\n", os.Args[0])
				slackTestCmd.PrintDefaults()
				os.Exit(1)
			}
			slackTestCmd.Parse(args[1:])
			handleSlackTest()
			return
		}
	}

	// If no flags or commands provided, show usage
	flag.Usage()
	os.Exit(1)
}

// handleVersionComparison compares a version with its previous release
func handleVersionComparison(version string) {
	fmt.Printf("=== Version Comparison ===\n")
	fmt.Printf("Target version: %s\n", version)

	// Load configuration to get default repository
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Create GitHub client
	ctx := context.Background()
	githubClient := github.NewClient(ctx, cfg.GitHubToken)

	// First, check if the target version tag exists
	fmt.Printf("Checking if %s tag exists...\n", version)
	exists, err := githubClient.TagExists(cfg.Owner, cfg.Repository, version)
	if err != nil {
		log.Fatalf("Failed to check if tag exists: %v", err)
	}

	if !exists {
		fmt.Printf("❌ Error: No release found with tag '%s'\n", version)
		fmt.Printf("Repository: %s/%s\n", cfg.Owner, cfg.Repository)
		return
	}

	fmt.Printf("✅ Tag %s exists\n", version)

	// Find previous version
	fmt.Printf("Finding nearest previous version...\n")
	previousVersion, err := githubClient.FindPreviousVersion(cfg.Owner, cfg.Repository, version)
	if err != nil {
		log.Fatalf("Failed to find previous version: %v", err)
	}

	fmt.Printf("Previous version found: %s\n", previousVersion)

	// Provide context about the comparison
	major, minor, patch, _ := parseVersionForDisplay(version)
	prevMajor, prevMinor, prevPatch, _ := parseVersionForDisplay(previousVersion)

	if major == prevMajor && minor == prevMinor && patch > 0 && prevPatch < patch-1 {
		fmt.Printf("Note: Comparing with nearest available patch (v%d.%d.%d not found)\n", major, minor, patch-1)
	}

	fmt.Printf("Comparing %s...%s\n\n", previousVersion, version)

	// Get commits between versions
	commits, err := githubClient.GetCommitsBetweenTags(cfg.Owner, cfg.Repository, previousVersion, version)
	if err != nil {
		log.Fatalf("Failed to get commits between versions: %v", err)
	}

	fmt.Printf("=== Changes in %s ===\n", version)
	fmt.Printf("Total commits: %d\n\n", len(commits))

	if len(commits) == 0 {
		fmt.Printf("No commits found between %s and %s\n", previousVersion, version)
		return
	}

	// Display commits in reverse order (oldest first)
	for i := len(commits) - 1; i >= 0; i-- {
		commit := commits[i]
		hash := commit.GetSHA()
		shortHash := hash
		if len(hash) > 8 {
			shortHash = hash[:8]
		}

		message := commit.GetCommit().GetMessage()
		title := strings.Split(message, "\n")[0] // Get first line as title

		var date string
		if commit.Commit != nil && commit.Commit.Committer != nil && commit.Commit.Committer.Date != nil {
			date = commit.Commit.Committer.Date.GetTime().Format("2006-01-02 15:04:05")
		} else {
			date = "Unknown date"
		}

		fmt.Printf("  %s  %s  %s\n", shortHash, date, title)
	}

	fmt.Printf("\nRepository: %s/%s\n", cfg.Owner, cfg.Repository)
}

// parseVersionForDisplay parses a version string for display purposes (similar to parseVersion but returns 0 on error)
func parseVersionForDisplay(version string) (major, minor, patch int, err error) {
	// Remove 'v' prefix if present
	version = strings.TrimPrefix(version, "v")

	// Split by dots
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return 0, 0, 0, fmt.Errorf("invalid version format")
	}

	major, _ = strconv.Atoi(parts[0])
	minor, _ = strconv.Atoi(parts[1])

	if len(parts) >= 3 {
		patch, _ = strconv.Atoi(parts[2])
	}

	return major, minor, patch, nil
}

// handleMCEVersionComparison compares an MCE version with its previous release using GitLab snapshots
func handleMCEVersionComparison(version string) {
	fmt.Printf("=== MCE Version Comparison ===\n")
	fmt.Printf("Target MCE version: %s\n", version)

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Create GitLab client
	ctx := context.Background()
	gitlabClient := gitlab.NewClient(ctx, cfg.GitLabToken)
	if gitlabClient == nil {
		log.Fatalf("Failed to create GitLab client. Please set PR_BOT_GITLAB_TOKEN environment variable.")
	}

	// Create GitHub client for commit comparison
	githubClient := github.NewClient(ctx, cfg.GitHubToken)

	// Load GA parser
	excelFile := "data/ACM - Z Stream Release Schedule.xlsx"
	gaParser := ga.NewParser(excelFile)

	// Find previous MCE version
	previousVersion, err := findPreviousMCEVersion(version, gaParser)
	if err != nil {
		log.Fatalf("Failed to find previous MCE version: %v", err)
	}

	fmt.Printf("Previous MCE version: %s\n", previousVersion)

	// Get SHA for target version
	targetSHA, err := getMCESHA(gitlabClient, version)
	if err != nil {
		log.Fatalf("Failed to get SHA for MCE %s: %v", version, err)
	}

	// Get SHA for previous version
	previousSHA, err := getMCESHA(gitlabClient, previousVersion)
	if err != nil {
		log.Fatalf("Failed to get SHA for MCE %s: %v", previousVersion, err)
	}

	fmt.Printf("MCE %s assisted-service SHA: %s\n", version, targetSHA[:8])
	fmt.Printf("MCE %s assisted-service SHA: %s\n", previousVersion, previousSHA[:8])

	// Check if SHAs are the same - no need to compare if identical
	if targetSHA == previousSHA {
		fmt.Printf("\nNo commits found between %s and %s (same SHA)\n", previousSHA[:8], targetSHA[:8])
		fmt.Printf("\n=== Changes in MCE %s ===\n", version)
		fmt.Printf("Total commits: 0\n\n")
		fmt.Printf("Both versions use the same assisted-service SHA, indicating no changes between them.\n")
		return
	}

	fmt.Printf("\nComparing %s...%s\n\n", previousSHA[:8], targetSHA[:8])

	// Get commits between SHAs from GitHub
	commits, err := githubClient.GetCommitsBetweenSHAs(cfg.Owner, cfg.Repository, previousSHA, targetSHA)
	if err != nil {
		log.Fatalf("Failed to get commits between SHAs: %v", err)
	}

	fmt.Printf("=== Changes in MCE %s ===\n", version)
	fmt.Printf("Total commits: %d\n\n", len(commits))

	if len(commits) == 0 {
		fmt.Printf("No commits found between %s and %s\n", previousSHA[:8], targetSHA[:8])
		return
	}

	// Display commits in reverse order (oldest first)
	for i := len(commits) - 1; i >= 0; i-- {
		commit := commits[i]
		hash := commit.GetSHA()
		shortHash := hash
		if len(hash) > 8 {
			shortHash = hash[:8]
		}

		message := commit.GetCommit().GetMessage()
		title := strings.Split(message, "\n")[0] // Get first line as title

		var date string
		if commit.Commit != nil && commit.Commit.Committer != nil && commit.Commit.Committer.Date != nil {
			date = commit.Commit.Committer.Date.GetTime().Format("2006-01-02 15:04:05")
		} else {
			date = "Unknown date"
		}

		fmt.Printf("  %s  %s  %s\n", shortHash, date, title)
	}

	fmt.Printf("\nRepository: %s/%s\n", cfg.Owner, cfg.Repository)
}

// findPreviousMCEVersion finds the previous MCE version using GitLab snapshot data
func findPreviousMCEVersion(version string, gaParser *ga.Parser) (string, error) {
	logger.Debug("Finding previous MCE version for %s using GitLab snapshot data", version)

	// Parse the version
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid version format: %s", version)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return "", fmt.Errorf("invalid major version: %s", parts[0])
	}

	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", fmt.Errorf("invalid minor version: %s", parts[1])
	}

	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", fmt.Errorf("invalid patch version: %s", parts[2])
	}

	// Load configuration to get GitLab client
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("failed to load configuration: %w", err)
	}

	ctx := context.Background()
	gitlabClient := gitlab.NewClient(ctx, cfg.GitLabToken)
	if gitlabClient == nil {
		return "", fmt.Errorf("failed to create GitLab client")
	}

	if patch == 0 {
		// For X.Y.0 versions, look in the previous minor branch (X.Y-1)
		if minor == 0 {
			return "", fmt.Errorf("cannot find previous version for %s (first minor version)", version)
		}

		previousMinorBranch := fmt.Sprintf("mce-%d.%d", major, minor-1)
		logger.Debug("Looking for latest snapshot in previous minor branch: %s", previousMinorBranch)

		// Get the latest snapshot from the previous minor branch
		_, err := gitlabClient.FindLatestSnapshot(previousMinorBranch)
		if err != nil {
			return "", fmt.Errorf("failed to find latest snapshot in %s: %w", previousMinorBranch, err)
		}

		// The previous version would be the highest patch version in that branch
		// For now, we'll estimate it based on common patterns, but ideally we'd
		// parse the snapshot to determine the exact version

		// Try to find what versions exist in that branch by looking at Excel data
		mceReleases, err := gaParser.GetAllMCEReleases()
		if err != nil {
			logger.Debug("Warning: failed to get MCE releases from Excel: %v", err)
			// Fallback: assume latest patch in previous minor is high number
			return fmt.Sprintf("%d.%d.10", major, minor-1), nil
		}

		// Find the latest released version in the previous minor series
		var latestInPrevious string
		expectedMinor := fmt.Sprintf("%d.%d", major, minor-1)

		for _, release := range mceReleases {
			if release.MCEVersion == "" || release.GADate == nil {
				continue
			}

			releaseParts := strings.Split(release.MCEVersion, ".")
			if len(releaseParts) >= 2 {
				releaseMinor := releaseParts[0] + "." + releaseParts[1]
				if releaseMinor == expectedMinor {
					// Check if this version was actually released (GA date is in the past)
					if release.GADate.Before(time.Now()) {
						if latestInPrevious == "" || compareMCEVersions(release.MCEVersion, latestInPrevious) > 0 {
							latestInPrevious = release.MCEVersion
						}
					}
				}
			}
		}

		if latestInPrevious != "" {
			logger.Debug("Found latest released version in previous minor series: %s", latestInPrevious)
			return latestInPrevious, nil
		}

		return "", fmt.Errorf("no released previous version found for %s in minor series %s", version, expectedMinor)

	} else {
		// For X.Y.Z versions where Z > 0, look for X.Y.(Z-1) in the same branch
		previousPatch := patch - 1
		if previousPatch < 0 {
			return "", fmt.Errorf("cannot find previous patch version for %s", version)
		}

		previousVersion := fmt.Sprintf("%d.%d.%d", major, minor, previousPatch)
		logger.Debug("Calculated previous patch version: %s", previousVersion)

		// For patch versions, we assume the previous patch exists if we can find snapshots
		// Let's verify the snapshot exists by trying to access the branch
		currentBranch := fmt.Sprintf("mce-%d.%d", major, minor)
		_, err := gitlabClient.FindLatestSnapshot(currentBranch)
		if err != nil {
			return "", fmt.Errorf("failed to find snapshots in branch %s: %w", currentBranch, err)
		}

		logger.Debug("Found snapshots in branch %s, previous version is: %s", currentBranch, previousVersion)
		return previousVersion, nil
	}
}

// compareMCEVersions compares two MCE version strings (e.g., "2.8.1" vs "2.9.0")
func compareMCEVersions(v1, v2 string) int {
	// Parse version parts
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	// Compare each part
	maxParts := len(parts1)
	if len(parts2) > maxParts {
		maxParts = len(parts2)
	}

	for i := 0; i < maxParts; i++ {
		var num1, num2 int

		if i < len(parts1) {
			num1, _ = strconv.Atoi(parts1[i])
		}
		if i < len(parts2) {
			num2, _ = strconv.Atoi(parts2[i])
		}

		if num1 < num2 {
			return -1
		} else if num1 > num2 {
			return 1
		}
	}

	return 0 // versions are equal
}

// getMCESHA extracts the assisted-service SHA from MCE snapshot for given version
func getMCESHA(gitlabClient *gitlab.Client, version string) (string, error) {
	// Calculate MCE branch (e.g., 2.8.1 -> mce-2.8)
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid version format: %s", version)
	}
	mceBranch := fmt.Sprintf("mce-%s.%s", parts[0], parts[1])

	// Find the appropriate snapshot for this version
	// For version comparison, we want the latest snapshot in the branch
	// This is a simplified approach - ideally we'd find the exact snapshot for the version
	snapshot, err := findLatestMCESnapshot(gitlabClient, mceBranch)
	if err != nil {
		return "", fmt.Errorf("failed to find snapshot for MCE %s: %v", version, err)
	}

	// Extract SHA from the snapshot using existing GitLab client method
	sha, err := gitlabClient.ExtractAssistedServiceSHA(mceBranch, snapshot)
	if err != nil {
		return "", fmt.Errorf("failed to extract SHA from snapshot %s: %v", snapshot, err)
	}

	return sha, nil
}

// findLatestMCESnapshot finds the latest snapshot folder for MCE branch in GitLab
func findLatestMCESnapshot(gitlabClient *gitlab.Client, mceBranch string) (string, error) {
	// Use the new GitLab client method to find the latest snapshot
	return gitlabClient.FindLatestSnapshot(mceBranch)
}

// handleSlackSearch searches for PR-related messages in Slack
func handleSlackSearch(owner, repo string, prNumber int) {
	fmt.Printf("=== Slack Search ===\n")
	fmt.Printf("Searching for PR #%d in %s/%s...\n", prNumber, owner, repo)

	// TODO: Implement existing Slack search logic
	fmt.Printf("Feature needs to be migrated from old code!\n")
}

// handleVersionSearch finds latest version message
func handleVersionSearch(channel string) {
	fmt.Printf("=== Version Search ===\n")
	fmt.Printf("Searching in channel: %s\n", channel)

	// TODO: Implement existing version search logic
	fmt.Printf("Feature needs to be migrated from old code!\n")
}

// handleSlackTest tests Slack authentication
func handleSlackTest() {
	fmt.Printf("=== Slack Authentication Test ===\n")

	// TODO: Implement existing Slack test logic
	fmt.Printf("Feature needs to be migrated from old code!\n")
}

// handlePRAnalysis analyzes a PR (existing functionality)
func handlePRAnalysis(prURL string) {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Parse PR number or URL
	prNumber, owner, repo, err := parsePRInput(prURL)
	if err != nil {
		log.Fatalf("Failed to parse PR input '%s': %v", prURL, err)
	}

	// Override config with values from URL if provided
	if owner != "" && repo != "" {
		cfg.Owner = owner
		cfg.Repository = repo
	}

	// Create analyzer and run analysis
	ctx := context.Background()
	analyzer := analyzer.New(ctx, cfg)

	result, err := analyzer.AnalyzePR(prNumber)
	if err != nil {
		log.Fatalf("Failed to analyze PR #%d: %v", prNumber, err)
	}

	// Print results
	analyzer.PrintSummary(result)
}

// parsePRInput parses PR input which can be either a number or a GitHub URL
// Returns: prNumber, owner, repo, error.
func parsePRInput(input string) (int, string, string, error) {
	// First try to parse as a number
	if prNumber, err := strconv.Atoi(input); err == nil {
		return prNumber, "", "", nil
	}

	// Try to parse as a GitHub URL
	if strings.HasPrefix(input, "http") {
		return parsePRURL(input)
	}

	return 0, "", "", fmt.Errorf("invalid input: must be a PR number or GitHub URL")
}

// parsePRURL parses a GitHub PR URL and extracts owner, repo, and PR number.
// Example: https://github.com/openshift/assisted-service/pull/1234
func parsePRURL(prURL string) (int, string, string, error) {
	parsedURL, err := url.Parse(prURL)
	if err != nil {
		return 0, "", "", fmt.Errorf("invalid URL: %w", err)
	}

	if parsedURL.Host != "github.com" {
		return 0, "", "", fmt.Errorf("URL must be from github.com")
	}

	// Use regex to match GitHub PR URL pattern
	// Path should be: /owner/repo/pull/number
	prRegex := regexp.MustCompile(`^/([^/]+)/([^/]+)/pull/(\d+)/?$`)
	matches := prRegex.FindStringSubmatch(parsedURL.Path)

	if len(matches) != 4 {
		return 0, "", "", fmt.Errorf("invalid GitHub PR URL format")
	}

	owner := matches[1]
	repo := matches[2]
	prNumber, err := strconv.Atoi(matches[3])
	if err != nil {
		return 0, "", "", fmt.Errorf("invalid PR number in URL: %w", err)
	}

	return prNumber, owner, repo, nil
}

// handleJiraTicketAnalysis analyzes all PRs related to a JIRA ticket
func handleJiraTicketAnalysis(jiraInput string) {
	fmt.Printf("=== JIRA Ticket Analysis ===\n")

	// Extract ticket ID from input (could be full URL or just ticket ID)
	ticketID := extractJiraTicketID(jiraInput)
	if ticketID == "" {
		log.Fatalf("Invalid JIRA ticket format: %s", jiraInput)
	}

	fmt.Printf("Analyzing JIRA ticket: %s\n", ticketID)

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	if cfg.JiraToken == "" {
		log.Fatalf("JIRA token not configured. Please set PR_BOT_JIRA_TOKEN in your .env file")
	}

	// Create clients
	ctx := context.Background()

	// Create JIRA client for ticket discovery
	jiraClient := jira.NewClient(ctx, cfg.JiraToken)

	// Get all related JIRA tickets (main ticket + cloned tickets)
	fmt.Printf("Finding all related JIRA tickets...\n")
	allTicketIssues, err := jiraClient.GetAllClonedIssues(ticketID)
	if err != nil {
		log.Fatalf("Failed to get related JIRA tickets: %v", err)
	}

	// Extract ticket keys for display
	allTicketKeys := make([]string, len(allTicketIssues))
	for i, ticket := range allTicketIssues {
		allTicketKeys[i] = ticket.Key
	}

	fmt.Printf("Found %d related tickets: %s\n", len(allTicketIssues), strings.Join(allTicketKeys, ", "))

	// Extract all PR URLs from all tickets
	var allPRURLs []string
	for _, ticket := range allTicketIssues {
		// ticket is already a JiraIssue, so we can pass it directly
		prURLs := jiraClient.ExtractGitHubPRsFromIssue(ticket)
		allPRURLs = append(allPRURLs, prURLs...)
	}

	// Remove duplicates and filter for supported repositories
	prURLsMap := make(map[string]bool)
	var uniquePRURLs []string

	// Support assisted-service, assisted-installer, and assisted-installer-agent repositories
	supportedRepos := []string{
		fmt.Sprintf("github.com/%s/assisted-service/pull/", cfg.Owner),
		fmt.Sprintf("github.com/%s/assisted-installer/pull/", cfg.Owner),
		fmt.Sprintf("github.com/%s/assisted-installer-agent/pull/", cfg.Owner),
	}

	for _, prURL := range allPRURLs {
		if !prURLsMap[prURL] {
			// Check if URL matches any supported repository
			for _, repoPattern := range supportedRepos {
				if strings.Contains(prURL, repoPattern) {
					prURLsMap[prURL] = true
					uniquePRURLs = append(uniquePRURLs, prURL)
					break
				}
			}
		}
	}

	if len(uniquePRURLs) == 0 {
		fmt.Printf("No GitHub PRs found for supported repositories (assisted-service, assisted-installer, assisted-installer-agent) in the related JIRA tickets\n")
		return
	}

	fmt.Printf("Found %d unique PRs to analyze:\n", len(uniquePRURLs))
	for _, prURL := range uniquePRURLs {
		fmt.Printf("  • %s\n", prURL)
	}

	// Analyze each PR and collect results using goroutines for parallel processing
	var allResults []*models.PRAnalysisResult
	var resultsMutex sync.Mutex

	// Use a channel to control concurrency
	concurrencyLimit := 5 // Limit concurrent PR analyses to avoid overwhelming APIs
	semaphore := make(chan struct{}, concurrencyLimit)

	// WaitGroup to wait for all goroutines
	var wg sync.WaitGroup

	for _, prURL := range uniquePRURLs {
		wg.Add(1)
		go func(prURL string) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Parse PR URL to get repository information
			prNumber, owner, repo, err := parsePRURL(prURL)
			if err != nil {
				fmt.Printf("Warning: Failed to parse PR URL %s: %v\n", prURL, err)
				return
			}

			// Create a copy of config with the correct repository information
			prCfg := *cfg // Copy the original config
			if owner != "" && repo != "" {
				prCfg.Owner = owner
				prCfg.Repository = repo
			}

			// Create analyzer for this specific repository
			prAnalyzer := analyzer.New(ctx, &prCfg)

			fmt.Printf("\nAnalyzing PR #%d (%s/%s)...\n", prNumber, prCfg.Owner, prCfg.Repository)
			result, err := prAnalyzer.AnalyzePRWithOptions(prNumber, true) // Skip JIRA analysis since we already have the context
			if err != nil {
				fmt.Printf("Error analyzing PR #%d: %v\n", prNumber, err)
				return
			}

			// Thread-safe append to results
			resultsMutex.Lock()
			allResults = append(allResults, result)
			resultsMutex.Unlock()
		}(prURL)
	}

	// Wait for all PR analyses to complete
	wg.Wait()

	// Display combined results
	fmt.Printf("\n" + strings.Repeat("=", 80) + "\n")
	fmt.Printf("=== COMBINED ANALYSIS RESULTS ===\n")
	fmt.Printf("Main JIRA Ticket: %s\n", ticketID)
	fmt.Printf("Related Tickets: %s\n", strings.Join(allTicketKeys[1:], ", "))
	fmt.Printf("Total PRs Analyzed: %d\n", len(allResults))

	// Collect all unique branches across all PRs
	allBranchesMap := make(map[string]models.BranchPresence)
	prSummaries := make([]string, 0)

	for _, result := range allResults {
		// Extract repository from PR URL for display
		_, owner, repo, _ := parsePRURL(result.PR.URL)
		repoDisplay := ""
		if owner != "" && repo != "" {
			repoDisplay = fmt.Sprintf(" [%s/%s]", owner, repo)
		}

		// Add PR summary
		prSummaries = append(prSummaries, fmt.Sprintf("PR #%d%s: %s (Hash: %s)",
			result.PR.Number, repoDisplay, result.PR.Title, result.PR.Hash))

		// Collect all branches from this PR
		for _, branch := range result.ReleaseBranches {
			if branch.Found {
				// If we already have this branch, keep the one with more information
				if existing, exists := allBranchesMap[branch.BranchName]; !exists || len(branch.UpcomingGAs) > len(existing.UpcomingGAs) {
					allBranchesMap[branch.BranchName] = branch
				}
			}
		}
	}

	// Show PR summaries
	fmt.Printf("\n=== PRs Analyzed ===\n")
	for _, summary := range prSummaries {
		fmt.Printf("• %s\n", summary)
	}

	// Convert map back to slice for display
	var allFoundBranches []models.BranchPresence
	for _, branch := range allBranchesMap {
		allFoundBranches = append(allFoundBranches, branch)
	}

	// Display combined branch analysis using the same logic as PR analysis
	fmt.Printf("\n=== Combined Release Branch Analysis ===\n")
	if len(allFoundBranches) > 0 {
		// Group branches by pattern for better organization
		patternGroups := make(map[string][]models.BranchPresence)
		patternOrder := []string{"release-ocm-", "release-", "release-v", "v"}

		for _, branch := range allFoundBranches {
			patternGroups[branch.Pattern] = append(patternGroups[branch.Pattern], branch)
		}

		// Sort branches within each pattern group by version
		for pattern := range patternGroups {
			branches := patternGroups[pattern]
			sort.Slice(branches, func(i, j int) bool {
				// Parse version numbers for proper sorting (e.g., "2.13" < "2.14" < "2.15")
				versionI := parseVersionNumber(branches[i].Version)
				versionJ := parseVersionNumber(branches[j].Version)
				return versionI < versionJ
			})
			patternGroups[pattern] = branches
		}

		fmt.Printf("✓ Found in %d total release branches across all PRs:\n", len(allFoundBranches))

		// Display found branches grouped by pattern (reuse logic from analyzer)
		for _, pattern := range patternOrder {
			branches := patternGroups[pattern]
			if len(branches) > 0 {
				fmt.Printf("\n  %s branches (%d):\n", getPatternDescription(pattern), len(branches))
				for _, branch := range branches {
					isNextVersion := strings.Contains(branch.Version, "Next Version") ||
						(branch.GAStatus.ACM.Status == "Next Version" || branch.GAStatus.MCE.Status == "Next Version")

					nextVersionText := ""
					if isNextVersion {
						nextVersionText = " (Next Version)"
					}

					fmt.Printf("    - %s (v%s)%s", branch.BranchName, branch.Version, nextVersionText)
					if branch.MergedAt != nil {
						fmt.Printf(" - merged at %s", branch.MergedAt.Format("01-02-2006"))
					}

					fmt.Printf("\n")

					// Show release information
					if !isNextVersion && branch.Found {
						now := time.Now()

						// Check if we have content to display
						hasVersionContent := len(branch.ReleasedVersions) > 0 ||
							len(branch.UpcomingGAs) > 0 ||
							branch.Pattern == "release-ocm-"

						if hasVersionContent {
							fmt.Printf("\n      Release Version:")

							// For Version-prefixed branches, show released versions
							if len(branch.ReleasedVersions) > 0 {
								fmt.Printf("\n        %s", strings.Join(branch.ReleasedVersions, ", "))
							}

							if len(branch.UpcomingGAs) == 0 {
								// No upcoming GAs defined - show "Not released yet" only for ACM/MCE branches
								if branch.Pattern == "release-ocm-" {
									fmt.Printf("\n        Not released yet - no GA versions defined for this branch")
								}
							} else {
								// For each product (ACM/MCE), show either:
								// 1. The first released version (if any), OR
								// 2. "Not released yet" for the earliest unreleased version (if no released versions)
								productStatus := make(map[string]bool) // track if we found released version for each product

								// First pass: find released versions
								for _, upcomingGA := range branch.UpcomingGAs {
									if upcomingGA.GADate != nil && upcomingGA.GADate.Before(now) {
										// This is a released version
										if !productStatus[upcomingGA.Product] {
											productStatus[upcomingGA.Product] = true
											fmt.Printf("\n        %s %s: Released (GA: %s)", upcomingGA.Product, upcomingGA.Version,
												models.FormatDate(upcomingGA.GADate))

											// Show the SHA from MCE validation if available
											if upcomingGA.MCEValidation != nil && upcomingGA.MCEValidation.AssistedServiceSHA != "" {
												componentName := upcomingGA.MCEValidation.ComponentName
												if componentName == "" {
													componentName = "assisted-service" // fallback for backward compatibility
												}
												fmt.Printf(" (%s latest commit SHA: %s)", componentName, upcomingGA.MCEValidation.AssistedServiceSHA[:8])
											}
										}
									}
								}

								// Second pass: for products without released versions, show "Not released yet"
								productNotReleased := make(map[string]bool)
								for _, upcomingGA := range branch.UpcomingGAs {
									if !productStatus[upcomingGA.Product] && !productNotReleased[upcomingGA.Product] {
										// No released version found for this product, show first unreleased
										productNotReleased[upcomingGA.Product] = true
										fmt.Printf("\n        %s %s: Not released yet (GA: %s)", upcomingGA.Product, upcomingGA.Version,
											models.FormatDate(upcomingGA.GADate))
									}
								}
							}
							fmt.Printf("\n")

							// Show Latest GA Status (already released versions) from GAStatus
							hasLatestGA := (branch.GAStatus.ACM.Version != "" && branch.GAStatus.ACM.Status == "GA" && branch.GAStatus.ACM.GADate != nil && branch.GAStatus.ACM.GADate.Before(now)) ||
								(branch.GAStatus.MCE.Version != "" && branch.GAStatus.MCE.Status == "GA" && branch.GAStatus.MCE.GADate != nil && branch.GAStatus.MCE.GADate.Before(now))

							if hasLatestGA {
								fmt.Printf("\n      Latest GA Status:")

								if branch.GAStatus.ACM.Version != "" && branch.GAStatus.ACM.Status == "GA" && branch.GAStatus.ACM.GADate != nil && branch.GAStatus.ACM.GADate.Before(now) {
									fmt.Printf("\n        ACM %s: Released (GA: %s)", branch.GAStatus.ACM.Version, models.FormatDate(branch.GAStatus.ACM.GADate))
								}
								if branch.GAStatus.MCE.Version != "" && branch.GAStatus.MCE.Status == "GA" && branch.GAStatus.MCE.GADate != nil && branch.GAStatus.MCE.GADate.Before(now) {
									fmt.Printf("\n        MCE %s: Released (GA: %s)", branch.GAStatus.MCE.Version, models.FormatDate(branch.GAStatus.MCE.GADate))
								}
							}
						}
					}

					fmt.Printf("\n")
					fmt.Printf("\n") // Add spacing between branches
				}
			}
		}
	} else {
		fmt.Printf("No release branches found across all analyzed PRs\n")
	}

	fmt.Printf("\nJIRA ticket analysis completed at: %s\n", time.Now().Format("01-02-2006 15:04:05"))
}

// extractJiraTicketID extracts the ticket ID from a JIRA URL or returns the input if it's already a ticket ID
func extractJiraTicketID(input string) string {
	// If it's already in MGMT-XXXXX format, return as is
	if matched, _ := regexp.MatchString(`^MGMT-\d+$`, input); matched {
		return input
	}

	// If it's a URL, extract the ticket ID
	if strings.Contains(input, "issues.redhat.com/browse/") {
		parts := strings.Split(input, "/")
		if len(parts) > 0 {
			ticketID := parts[len(parts)-1]
			if matched, _ := regexp.MatchString(`^MGMT-\d+$`, ticketID); matched {
				return ticketID
			}
		}
	}

	return ""
}

// extractPRNumberFromURL extracts PR number from GitHub PR URL
func extractPRNumberFromURL(prURL string) (int, error) {
	re := regexp.MustCompile(`github\.com/.+/.+/pull/(\d+)`)
	matches := re.FindStringSubmatch(prURL)
	if len(matches) < 2 {
		return 0, fmt.Errorf("invalid PR URL format")
	}
	return strconv.Atoi(matches[1])
}

// getPatternDescription returns a human-readable description for branch patterns
func getPatternDescription(pattern string) string {
	switch pattern {
	case "release-ocm-":
		return "ACM/MCE"
	case "release-":
		return "OpenShift"
	case "release-v":
		return "Release-v"
	case "v":
		return "Version-prefixed"
	default:
		return pattern
	}
}

// parseVersionNumber extracts and parses version number from version string for sorting.
// Examples: "2.13" -> 2.13, "v2.40" -> 2.40, "Next Version" -> 999.0 (sorts last)
func parseVersionNumber(version string) float64 {
	// Handle special cases
	if strings.Contains(version, "Next Version") {
		return 999.0 // Sort "Next Version" entries last
	}

	// Strip "v" prefix if present
	version = strings.TrimPrefix(version, "v")

	// Parse as float (handles X.Y format)
	if parsed, err := strconv.ParseFloat(version, 64); err == nil {
		return parsed
	}

	// If parsing fails, return 0 (sorts first)
	return 0.0
}
