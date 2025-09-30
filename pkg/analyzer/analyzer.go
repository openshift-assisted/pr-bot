// Package analyzer provides core business logic for analyzing merged pull requests and their presence across release branches.
package analyzer

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"strconv"
	"sync"

	"github.com/shay23bra/pr-bot/internal/ga"
	"github.com/shay23bra/pr-bot/internal/github"
	"github.com/shay23bra/pr-bot/internal/gitlab"
	"github.com/shay23bra/pr-bot/internal/jira"
	"github.com/shay23bra/pr-bot/internal/logger"
	"github.com/shay23bra/pr-bot/internal/models"
)

// Constants for the analyzer package.
const (
	// Excel file path for GA tracking
	ExcelFilePath = "data/ACM - Z Stream Release Schedule.xlsx"

	// Status check strings - these match the constants in ga package
	StatusNotFound = "Not Found"
)

// Analyzer handles PR analysis operations.
type Analyzer struct {
	githubClient *github.Client
	config       *models.Config
	gaParser     *ga.Parser
	gitlabClient *gitlab.Client
	jiraClient   *jira.Client

	// Cache for branch information to avoid repeated API calls
	branchCache    []github.BranchInfo
	branchCacheMux sync.RWMutex
}

// New creates a new analyzer instance.
func New(ctx context.Context, config *models.Config) *Analyzer {
	githubClient := github.NewClient(ctx, config.GitHubToken)
	gaParser := ga.NewParser(ExcelFilePath)
	// Slack integration is now handled by the server component

	var gitlabClient *gitlab.Client
	if config.GitLabToken != "" {
		gitlabClient = gitlab.NewClient(ctx, config.GitLabToken, githubClient)
	}

	var jiraClient *jira.Client
	if config.JiraToken != "" {
		jiraClient = jira.NewClient(ctx, config.JiraToken)
	}

	return &Analyzer{
		githubClient: githubClient,
		config:       config,
		gaParser:     gaParser,
		gitlabClient: gitlabClient,
		jiraClient:   jiraClient,
	}
}

// AnalyzePR performs complete analysis of a pull request.
func (a *Analyzer) AnalyzePR(prNumber int) (*models.PRAnalysisResult, error) {
	return a.AnalyzePRWithOptions(prNumber, false)
}

// AnalyzePRWithOptions performs complete analysis of a pull request with optional settings.
func (a *Analyzer) AnalyzePRWithOptions(prNumber int, skipJiraAnalysis bool) (*models.PRAnalysisResult, error) {
	logger.Debug("Starting analysis of PR #%d (skipJiraAnalysis: %v)", prNumber, skipJiraAnalysis)

	// Get PR information
	prInfo, err := a.githubClient.GetPRInfo(a.config.Owner, a.config.Repository, prNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get PR info: %w", err)
	}

	logger.Debug("PR #%d: %s (merged at %v)", prInfo.Number, prInfo.Title, prInfo.MergedAt)
	logger.Debug("Commit hash: %s", prInfo.Hash)

	// Get all release branches with different patterns (using cache)
	branchInfos, err := a.getBranches()
	if err != nil {
		return nil, fmt.Errorf("failed to get release branches: %w", err)
	}

	logger.Debug("Found %d release branches across all patterns", len(branchInfos))

	// Group branches by pattern for logging
	patternCounts := make(map[string]int)
	for _, branchInfo := range branchInfos {
		patternCounts[branchInfo.Pattern]++
	}

	for pattern, count := range patternCounts {
		logger.Debug("  %s: %d branches", pattern, count)
	}

	// Check PR presence in each release branch using goroutines for parallel processing
	branchPresences := make([]models.BranchPresence, len(branchInfos))

	// Use a channel to control concurrency (limit to avoid overwhelming GitHub API)
	concurrencyLimit := 10
	semaphore := make(chan struct{}, concurrencyLimit)

	// WaitGroup to wait for all goroutines
	var wg sync.WaitGroup

	for i, branchInfo := range branchInfos {
		wg.Add(1)
		go func(index int, branch github.BranchInfo) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			logger.Debug("Checking branch: %s (%s)", branch.Name, branch.Pattern)

			found, mergedAt, err := a.githubClient.CheckCommitInBranch(
				a.config.Owner,
				a.config.Repository,
				prInfo.Hash,
				branch.Name,
			)

			if err != nil {
				logger.Debug("Warning: failed to check commit in branch %s: %v", branch.Name, err)
				// Continue with other branches even if one fails
			}

			gaStatus := models.GAStatus{}
			var upcomingGAs []models.UpcomingGA
			var releasedVersions []string

			// Always calculate GA status for ACM/MCE branches to provide context
			if branch.Pattern == "release-ocm-" {
				var gaErr error
				gaStatus, gaErr = a.gaParser.GetGAStatus(branch.Name, mergedAt)
				if gaErr != nil {
					logger.Debug("Warning: failed to get GA status for %s: %v", branch.Name, gaErr)
				}

				// Get upcoming GA versions
				upcomingGAs, gaErr = a.gaParser.GetUpcomingGAVersions(branch.Name, mergedAt)
				if gaErr != nil {
					logger.Debug("Warning: failed to get upcoming GA versions for %s: %v", branch.Name, gaErr)
				}
			} else if branch.Pattern == "releases/v" && a.config.Repository == "assisted-installer-ui" {
				// TEMPORARILY DISABLED: For UI release branches, find the corresponding ACM/MCE versions
				// TODO: Fix performance issue - this causes 1000+ API calls
				// upcomingGAs = a.findACMMCEVersionsForUIRelease(branch.Version, mergedAt)
			}

			if found {
				// For Version-prefixed branches (v*) and UI release branches (releases/v*), find the exact release versions
				if branch.Pattern == "v" || (branch.Pattern == "releases/v" && a.config.Repository != "assisted-installer-ui") {
					logger.Debug("Finding exact release versions for %s (%s)", branch.Name, branch.Version)
					foundTags, tagErr := a.githubClient.FindCommitInVersionTags(
						a.config.Owner,
						a.config.Repository,
						prInfo.Hash,
						branch.Name, // e.g., "v2.40" for branch v2.40 or "releases/v2.15-cim" for releases/v branch
					)
					if tagErr != nil {
						logger.Debug("Warning: failed to find release versions for %s: %v", branch.Name, tagErr)
					} else {
						releasedVersions = foundTags
						if len(foundTags) > 0 {
							logger.Debug("Found in release versions: %v", foundTags)
						} else {
							logger.Debug("Not found in any release versions for %s", branch.Name)
						}
					}
				}

				// Perform MCE snapshot validation if GitLab client is available and not all GAs are in future
				// Only validate if the PR is actually in this branch
				if a.gitlabClient != nil && len(upcomingGAs) > 0 && (branch.Pattern == "release-ocm-" || (branch.Pattern == "releases/v" && a.config.Repository == "assisted-installer-ui")) {
					// Check if all GA dates are in the future
					allGAsInFuture := true
					now := time.Now()
					for _, upcomingGA := range upcomingGAs {
						if upcomingGA.GADate != nil && upcomingGA.GADate.Before(now) {
							allGAsInFuture = false
							break
						}
					}

					// Only perform validation if not all GAs are in the future
					if !allGAsInFuture {
						upcomingGAs = a.performMCEValidation(upcomingGAs, prInfo.Hash)
					}
				}
			}

			presence := models.BranchPresence{
				BranchName:       branch.Name,
				Pattern:          branch.Pattern,
				Version:          branch.Version,
				MergedAt:         mergedAt,
				Found:            found,
				ReleasedVersions: releasedVersions,
				GAStatus:         gaStatus,
				UpcomingGAs:      upcomingGAs,
			}

			branchPresences[index] = presence

			if found {
				logger.Debug("✓ Found in %s (%s, version %s)", branch.Name, branch.Pattern, branch.Version)
				if mergedAt != nil {
					logger.Debug("  Merged at: %v", mergedAt)
				}
			} else {
				logger.Debug("✗ Not found in %s (%s, version %s)", branch.Name, branch.Pattern, branch.Version)
			}
		}(i, branchInfo)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	result := &models.PRAnalysisResult{
		PR:              *prInfo,
		ReleaseBranches: branchPresences,
		AnalyzedAt:      time.Now(),
	}

	// Perform JIRA analysis if JIRA client is available and PR title contains MGMT ticket
	if a.jiraClient != nil && !skipJiraAnalysis {
		mgmtTicket := jira.ExtractMGMTTicketFromTitle(prInfo.Title)
		if mgmtTicket != "" {
			logger.Debug("Found MGMT ticket in PR title: %s", mgmtTicket)
			jiraAnalysis, relatedPRs := a.performJiraAnalysis(mgmtTicket, prInfo)
			result.JiraAnalysis = jiraAnalysis
			result.RelatedPRs = relatedPRs
		}
	}

	return result, nil
}

// getBranches returns cached branch information or fetches it if not cached
func (a *Analyzer) getBranches() ([]github.BranchInfo, error) {
	// Check cache first (read lock)
	a.branchCacheMux.RLock()
	if len(a.branchCache) > 0 {
		cached := make([]github.BranchInfo, len(a.branchCache))
		copy(cached, a.branchCache)
		a.branchCacheMux.RUnlock()
		logger.Debug("Using cached branch information (%d branches)", len(cached))
		return cached, nil
	}
	a.branchCacheMux.RUnlock()

	// Not cached, fetch and cache (write lock)
	a.branchCacheMux.Lock()
	defer a.branchCacheMux.Unlock()

	// Double-check cache in case another goroutine populated it
	if len(a.branchCache) > 0 {
		cached := make([]github.BranchInfo, len(a.branchCache))
		copy(cached, a.branchCache)
		logger.Debug("Using newly cached branch information (%d branches)", len(cached))
		return cached, nil
	}

	// Fetch branches from GitHub API
	logger.Debug("Fetching branch information from GitHub API")
	branchInfos, err := a.githubClient.GetAllReleaseBranches(a.config.Owner, a.config.Repository)
	if err != nil {
		return nil, fmt.Errorf("failed to get release branches: %w", err)
	}

	// Cache the results
	a.branchCache = make([]github.BranchInfo, len(branchInfos))
	copy(a.branchCache, branchInfos)

	logger.Debug("Cached %d release branches", len(branchInfos))
	return branchInfos, nil
}

// performJiraAnalysis analyzes JIRA tickets and finds related PRs.
func (a *Analyzer) performJiraAnalysis(mainTicket string, originalPR *models.PRInfo) (*models.JiraAnalysis, []models.RelatedPR) {
	logger.Debug("Starting JIRA analysis for ticket: %s", mainTicket)

	// Get all cloned issues related to the main ticket
	allIssues, err := a.jiraClient.GetAllClonedIssues(mainTicket)
	if err != nil {
		logger.Debug("Failed to get cloned issues for %s: %v", mainTicket, err)
		return &models.JiraAnalysis{
			MainTicket:      mainTicket,
			AnalysisSuccess: false,
			ErrorMessage:    fmt.Sprintf("Failed to get cloned issues: %v", err),
		}, nil
	}

	var allTickets []string
	var allPRURLs []string
	var uniqueRelatedPRs []models.RelatedPR
	processedPRs := make(map[string]bool)

	// Extract PR URLs from all issues
	for _, issue := range allIssues {
		allTickets = append(allTickets, issue.Key)
		prURLs := a.jiraClient.ExtractGitHubPRsFromIssue(issue)
		allPRURLs = append(allPRURLs, prURLs...)

		// Process each PR URL
		for _, prURL := range prURLs {
			if processedPRs[prURL] {
				continue
			}
			processedPRs[prURL] = true

			// Skip the original PR
			if strings.Contains(prURL, fmt.Sprintf("/pull/%d", originalPR.Number)) {
				continue
			}

			// Check if this PR is from the current repository
			if !strings.Contains(prURL, fmt.Sprintf("github.com/%s/%s", a.config.Owner, a.config.Repository)) {
				continue
			}

			// Extract PR number from URL
			prNumber := extractPRNumberFromURL(prURL)
			if prNumber == 0 {
				continue
			}

			// Analyze this related PR
			relatedPRInfo, err := a.githubClient.GetPRInfo(a.config.Owner, a.config.Repository, prNumber)
			if err != nil {
				logger.Debug("Failed to get info for related PR #%d: %v", prNumber, err)
				continue
			}

			// Get branch presence for this related PR
			branchInfos, err := a.getBranches()
			if err != nil {
				logger.Debug("Failed to get release branches for related PR #%d: %v", prNumber, err)
				continue
			}

			var branchPresences []models.BranchPresence
			for _, branchInfo := range branchInfos {
				found, mergedAt, err := a.githubClient.CheckCommitInBranch(a.config.Owner, a.config.Repository, relatedPRInfo.Hash, branchInfo.Name)
				if err != nil {
					continue
				}

				// Get release information for ACM/MCE branches
				var releasedVersions []string
				gaStatus := models.GAStatus{}
				var upcomingGAs []models.UpcomingGA

				// Always calculate GA status for ACM/MCE branches to provide context
				if branchInfo.Pattern == "release-ocm-" {
					var gaErr error
					gaStatus, gaErr = a.gaParser.GetGAStatus(branchInfo.Name, mergedAt)
					if gaErr != nil {
						logger.Debug("Warning: failed to get GA status for related PR #%d: %v", prNumber, gaErr)
					}

					// Get upcoming GA versions
					upcomingGAs, gaErr = a.gaParser.GetUpcomingGAVersions(branchInfo.Name, mergedAt)
					if gaErr != nil {
						logger.Debug("Warning: failed to get upcoming GA versions for related PR #%d: %v", prNumber, gaErr)
					}
				} else if branchInfo.Pattern == "releases/v" && a.config.Repository == "assisted-installer-ui" {
					// TEMPORARILY DISABLED: For UI release branches, find the corresponding ACM/MCE versions
					// TODO: Fix performance issue - this causes 1000+ API calls
					// upcomingGAs = a.findACMMCEVersionsForUIRelease(branchInfo.Version, mergedAt)
				}

				if found {
					// For Version-prefixed branches (v*) and UI release branches (releases/v*), find the exact release versions
					if branchInfo.Pattern == "v" || (branchInfo.Pattern == "releases/v" && a.config.Repository != "assisted-installer-ui") {
						foundTags, err := a.githubClient.FindCommitInVersionTags(
							a.config.Owner,
							a.config.Repository,
							relatedPRInfo.Hash,
							branchInfo.Name,
						)
						if err != nil {
							logger.Debug("Warning: failed to find release versions for related PR #%d: %v", prNumber, err)
						} else {
							releasedVersions = foundTags
						}
					}
				}

				presence := models.BranchPresence{
					BranchName:       branchInfo.Name,
					Pattern:          branchInfo.Pattern,
					Version:          branchInfo.Version,
					MergedAt:         mergedAt,
					Found:            found,
					ReleasedVersions: releasedVersions,
					GAStatus:         gaStatus,
					UpcomingGAs:      upcomingGAs,
				}

				branchPresences = append(branchPresences, presence)
			}

			// Find which JIRA tickets are associated with this PR
			var associatedTickets []string
			for _, ticket := range allTickets {
				// Check if this PR URL is mentioned in the ticket
				for _, ticketPRURL := range allPRURLs {
					if ticketPRURL == prURL {
						associatedTickets = append(associatedTickets, ticket)
						break
					}
				}
			}

			relatedPR := models.RelatedPR{
				Number:          prNumber,
				Title:           relatedPRInfo.Title,
				URL:             prURL,
				Hash:            relatedPRInfo.Hash,
				JiraTickets:     associatedTickets,
				ReleaseBranches: branchPresences,
			}

			uniqueRelatedPRs = append(uniqueRelatedPRs, relatedPR)
		}
	}

	// Remove duplicates from allPRURLs
	seen := make(map[string]bool)
	var uniquePRURLs []string
	for _, prURL := range allPRURLs {
		if !seen[prURL] {
			seen[prURL] = true
			uniquePRURLs = append(uniquePRURLs, prURL)
		}
	}

	jiraAnalysis := &models.JiraAnalysis{
		MainTicket:      mainTicket,
		AllTickets:      allTickets,
		RelatedPRURLs:   uniquePRURLs,
		AnalysisSuccess: true,
	}

	return jiraAnalysis, uniqueRelatedPRs
}

// extractPRNumberFromURL extracts PR number from GitHub PR URL.
func extractPRNumberFromURL(url string) int {
	parts := strings.Split(url, "/")
	if len(parts) < 2 {
		return 0
	}

	// URL format: https://github.com/owner/repo/pull/123
	for i, part := range parts {
		if part == "pull" && i+1 < len(parts) {
			var prNumber int
			if _, err := fmt.Sscanf(parts[i+1], "%d", &prNumber); err == nil {
				return prNumber
			}
		}
	}
	return 0
}

// PrintSummary prints a formatted summary of the analysis result.
func (a *Analyzer) PrintSummary(result *models.PRAnalysisResult) {
	fmt.Printf("\n=== PR Analysis Summary ===\n")
	fmt.Printf("PR #%d: %s\n", result.PR.Number, result.PR.Title)
	fmt.Printf("Hash: %s\n", result.PR.Hash)
	fmt.Printf("Merged to '%s' at: %s\n", result.PR.MergedInto, models.FormatDate(result.PR.MergedAt))
	fmt.Printf("URL: %s\n", result.PR.URL)

	// Add JIRA analysis to the summary if available
	if result.JiraAnalysis != nil && result.JiraAnalysis.AnalysisSuccess {
		// Count backport PRs (exclude the original PR)
		backportCount := 0
		for _, relatedPR := range result.RelatedPRs {
			if relatedPR.Number != result.PR.Number {
				backportCount++
			}
		}

		if backportCount > 0 {
			pluralS := "s"
			if backportCount == 1 {
				pluralS = ""
			}
			fmt.Printf("\n📋 JIRA Ticket: %s\n", result.JiraAnalysis.MainTicket)
			fmt.Printf("🔗 Found %d related backport PR%s:\n", backportCount, pluralS)

			for _, relatedPR := range result.RelatedPRs {
				if relatedPR.Number != result.PR.Number {
					fmt.Printf("  • PR #%d: %s\n", relatedPR.Number, relatedPR.Title)
					fmt.Printf("    URL: %s\n", relatedPR.URL)
					fmt.Printf("    Hash: %s\n", relatedPR.Hash)
				}
			}
		}
	}

	fmt.Printf("\n=== Release Branch Analysis ===\n")

	// Collect all branches from original PR and related PRs
	allBranchesMap := make(map[string]models.BranchPresence)

	// Add original PR branches
	for _, branch := range result.ReleaseBranches {
		if branch.Found {
			allBranchesMap[branch.BranchName] = branch
		}
	}

	// Add related PR branches
	if result.JiraAnalysis != nil && result.JiraAnalysis.AnalysisSuccess {
		for _, relatedPR := range result.RelatedPRs {
			if relatedPR.Number != result.PR.Number {
				for _, branch := range relatedPR.ReleaseBranches {
					if branch.Found {
						// If we already have this branch from original PR, prefer the related PR data
						// if the original PR is not actually found in that branch
						if existing, exists := allBranchesMap[branch.BranchName]; exists {
							// If original PR is not found in this branch, use related PR data instead
							if !existing.Found {
								allBranchesMap[branch.BranchName] = branch
							}
						} else {
							// Branch not in map yet, add it
							allBranchesMap[branch.BranchName] = branch
						}
					}
				}
			}
		}
	}

	// Convert map back to slice for consistent ordering
	var allFoundBranches []models.BranchPresence
	for _, branch := range allBranchesMap {
		allFoundBranches = append(allFoundBranches, branch)
	}

	// Group branches by pattern for better organization
	patternGroups := make(map[string][]models.BranchPresence)
	patternOrder := []string{"release-ocm-", "releases/v", "release-", "release-v", "v"}

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

	if len(allFoundBranches) > 0 {
		fmt.Printf("\n✓ Found in %d release branches:\n", len(allFoundBranches))

		// Display found branches grouped by pattern
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
	}

	// Temporarily commented out - not showing branches where PR is not found
	/*
		if len(notFoundBranches) > 0 {
			fmt.Printf("\n✗ Not found in %d release branches:\n", len(notFoundBranches))

			// Display not found branches grouped by pattern
			for _, pattern := range patternOrder {
				branches := patternNotFound[pattern]
				if len(branches) > 0 {
					fmt.Printf("\n  %s branches (%d):\n", getPatternDescription(pattern), len(branches))
					for _, branch := range branches {
						fmt.Printf("    - %s (v%s)\n", branch.BranchName, branch.Version)
					}
				}
			}
		}
	*/

	fmt.Printf("\nAnalysis completed at: %s\n", result.AnalyzedAt.Format("01-02-2006 15:04:05"))
}

// getPatternDescription returns a human-readable description for branch patterns.
func getPatternDescription(pattern string) string {
	switch pattern {
	case "release-ocm-":
		return "ACM/MCE"
	case "releases/v":
		return "UI Release"
	case "release-":
		return "OpenShift"
	case "release-v":
		return "Version-tagged"
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

// performMCEValidation performs MCE snapshot validation for released GAs only.
func (a *Analyzer) performMCEValidation(upcomingGAs []models.UpcomingGA, prCommitSHA string) []models.UpcomingGA {
	if len(upcomingGAs) == 0 {
		return upcomingGAs
	}

	now := time.Now()

	// Count how many GAs are already released (can be validated)
	releasedCount := 0
	for _, ga := range upcomingGAs {
		if ga.GADate != nil && ga.GADate.Before(now) {
			releasedCount++
		}
	}

	logger.Debug("Starting MCE validation for %d released GAs out of %d total GAs", releasedCount, len(upcomingGAs))

	// Create a copy of the slice to modify with validation results
	validatedGAs := make([]models.UpcomingGA, len(upcomingGAs))
	copy(validatedGAs, upcomingGAs)

	// Use goroutines to parallelize MCE validation
	var wg sync.WaitGroup

	for i := range validatedGAs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			ga := &validatedGAs[index]

			// Only validate versions that are already released
			if ga.GADate == nil || !ga.GADate.Before(now) {
				logger.Debug("Skipping MCE validation for %s %s - not yet released (GA: %s)", ga.Product, ga.Version, models.FormatDateWithNil(ga.GADate))
				return
			}

			logger.Debug("Validating MCE snapshot for %s %s (released)", ga.Product, ga.Version)

			// Determine component name based on repository
			componentName := "assisted-service" // default
			if a.config.Repository == "assisted-installer" {
				componentName = "assisted-installer"
			} else if a.config.Repository == "assisted-installer-agent" {
				componentName = "assisted-installer-agent"
			} else if a.config.Repository == "assisted-installer-ui" {
				componentName = "assisted-installer-ui"
			}

			validation, err := a.gitlabClient.ValidateMCESnapshotForComponent(ga.Product, ga.Version, ga.GADate, prCommitSHA, componentName)
			if err != nil {
				logger.Debug("Failed to validate MCE snapshot for %s %s: %v", ga.Product, ga.Version, err)
				ga.MCEValidation = &models.MCESnapshotValidation{
					Product:           ga.Product,
					Version:           ga.Version,
					GADate:            ga.GADate,
					ComponentName:     componentName,
					ValidationSuccess: false,
					ErrorMessage:      fmt.Sprintf("Validation failed: %v", err),
				}
			} else if validation != nil && validation.ValidationSuccess {
				// If validation succeeded, now compare PR commit with extracted SHA
				prBeforeSnapshot, err := a.comparePRCommitWithSnapshot(prCommitSHA, validation.AssistedServiceSHA)
				if err != nil {
					logger.Debug("Failed to compare PR commit with snapshot SHA: %v", err)
					validation.ErrorMessage = fmt.Sprintf("Failed to compare commits: %v", err)
					validation.ValidationSuccess = false
				} else {
					validation.PRCommitBeforeSHA = prBeforeSnapshot
					logger.Debug("PR commit before snapshot SHA: %v", prBeforeSnapshot)
				}
				ga.MCEValidation = validation
			} else {
				ga.MCEValidation = validation
			}
		}(i)
	}

	// Wait for all validations to complete
	wg.Wait()

	logger.Debug("Completed MCE validation for all GAs")
	return validatedGAs
}

// findACMMCEVersionsForUIRelease finds ACM/MCE versions that contain a specific UI version.
func (a *Analyzer) findACMMCEVersionsForUIRelease(uiVersion string, mergedAt *time.Time) []models.UpcomingGA {
	if a.gitlabClient == nil {
		logger.Debug("No GitLab client available for UI version lookup")
		return nil
	}

	logger.Debug("Finding ACM/MCE versions containing UI version %s", uiVersion)

	// Get all MCE releases
	allReleases, err := a.gaParser.GetAllMCEReleases()
	if err != nil {
		logger.Debug("Failed to get MCE releases: %v", err)
		return nil
	}

	var matchingVersions []models.UpcomingGA

	// Only check recent releases (within last 12 months) to avoid excessive API calls
	now := time.Now()
	cutoffDate := now.AddDate(-1, 0, 0) // 12 months ago

	logger.Debug("Limiting search to MCE releases after %s", cutoffDate.Format("2006-01-02"))

	// Search through recent MCE versions to find matches
	for _, release := range allReleases {
		// Skip old releases to reduce API calls
		if release.GADate == nil || release.GADate.Before(cutoffDate) {
			continue
		}

		// Try both ACM and MCE versions
		if release.ACMVersion != "" {
			logger.Debug("Checking ACM version %s for UI version %s", release.ACMVersion, uiVersion)
			if a.checkUIVersionInMCERelease("ACM", release.ACMVersion, release.GADate, uiVersion) {
				matchingVersions = append(matchingVersions, models.UpcomingGA{
					Product: "ACM",
					Version: release.ACMVersion,
					GADate:  release.GADate,
				})
			}
		}

		if release.MCEVersion != "" {
			logger.Debug("Checking MCE version %s for UI version %s", release.MCEVersion, uiVersion)
			if a.checkUIVersionInMCERelease("MCE", release.MCEVersion, release.GADate, uiVersion) {
				matchingVersions = append(matchingVersions, models.UpcomingGA{
					Product: "MCE",
					Version: release.MCEVersion,
					GADate:  release.GADate,
				})
			}
		}
	}

	logger.Debug("Found %d matching ACM/MCE versions for UI version %s", len(matchingVersions), uiVersion)

	// If no matches found, create a placeholder indicating the search was performed
	if len(matchingVersions) == 0 {
		logger.Debug("No MCE releases found containing UI version %s - this may be a newer version not yet in any MCE release", uiVersion)
		// Return a placeholder to show that we looked but didn't find it
		return []models.UpcomingGA{
			{
				Product: "UI",
				Version: fmt.Sprintf("%s not yet in released ACM/MCE versions", uiVersion),
				GADate:  nil,
			},
		}
	}

	return matchingVersions
}

// checkUIVersionInMCERelease checks if a specific UI version exists in an MCE release.
func (a *Analyzer) checkUIVersionInMCERelease(product, version string, gaDate *time.Time, targetUIVersion string) bool {
	if gaDate == nil {
		return false
	}

	// Only check released versions to avoid unnecessary API calls
	now := time.Now()
	if gaDate.After(now) {
		logger.Debug("Skipping future release %s %s", product, version)
		return false
	}

	logger.Debug("Extracting UI version from %s %s", product, version)

	// Use MCE validation logic to extract UI version from snapshot
	validation, err := a.gitlabClient.ValidateMCESnapshotForComponent(product, version, gaDate, "", "assisted-installer-ui")
	if err != nil {
		logger.Debug("Failed to validate MCE snapshot for %s %s: %v", product, version, err)
		return false
	}

	if validation == nil || !validation.ValidationSuccess {
		logger.Debug("MCE validation failed for %s %s", product, version)
		return false
	}

	// The validation returns the UI version in AssistedServiceSHA field
	extractedUIVersion := validation.AssistedServiceSHA

	// Clean up version strings for comparison
	cleanTarget := strings.TrimPrefix(targetUIVersion, "v")
	cleanExtracted := strings.TrimPrefix(extractedUIVersion, "v")

	matches := cleanTarget == cleanExtracted
	logger.Debug("UI version comparison: target=%s, extracted=%s, matches=%v", cleanTarget, cleanExtracted, matches)

	return matches
}

// comparePRCommitWithSnapshot compares if PR commit is before the snapshot commit.
func (a *Analyzer) comparePRCommitWithSnapshot(prCommitSHA, snapshotCommitSHA string) (bool, error) {
	if prCommitSHA == "" || snapshotCommitSHA == "" {
		return false, fmt.Errorf("both commit SHAs are required")
	}

	logger.Debug("Comparing PR commit %s with snapshot commit %s", prCommitSHA[:8], snapshotCommitSHA[:8])

	// Get PR commit information
	prCommit, _, err := a.githubClient.GetCommit(a.config.Owner, a.config.Repository, prCommitSHA)
	if err != nil {
		return false, fmt.Errorf("failed to get PR commit: %w", err)
	}

	// Get snapshot commit information
	snapshotCommit, _, err := a.githubClient.GetCommit(a.config.Owner, a.config.Repository, snapshotCommitSHA)
	if err != nil {
		return false, fmt.Errorf("failed to get snapshot commit: %w", err)
	}

	// Compare commit dates
	prCommitDate := prCommit.GetCommit().GetCommitter().GetDate()
	snapshotCommitDate := snapshotCommit.GetCommit().GetCommitter().GetDate()

	prBefore := prCommitDate.Time.Before(snapshotCommitDate.Time)

	logger.Debug("PR commit date: %v, Snapshot commit date: %v, PR before: %v",
		prCommitDate.Time, snapshotCommitDate.Time, prBefore)

	return prBefore, nil
}
