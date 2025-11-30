// Package server provides Slack bot server functionality.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shay23bra/pr-bot/internal/github"
	"github.com/shay23bra/pr-bot/internal/jira"
	"github.com/shay23bra/pr-bot/internal/logger"
	"github.com/shay23bra/pr-bot/internal/models"
	"github.com/shay23bra/pr-bot/internal/slack"
	"github.com/shay23bra/pr-bot/pkg/analyzer"
)

// SlackServer handles Slack bot requests
type SlackServer struct {
	config    *models.Config
	analyzer  *analyzer.Analyzer
	botClient *slack.BotClient
	botUserID string
}

// NewSlackServer creates a new Slack server instance
func NewSlackServer(cfg *models.Config) *SlackServer {
	ctx := context.Background()
	analyzer := analyzer.New(ctx, cfg)

	var botClient *slack.BotClient
	if cfg.SlackBotToken != "" {
		botClient = slack.NewBotClient(cfg.SlackBotToken)
		// Test authentication and get bot user ID
		if err := botClient.TestAuth(ctx); err != nil {
			logger.Debug("Failed to authenticate Slack bot: %v", err)
		}
	}

	return &SlackServer{
		config:    cfg,
		analyzer:  analyzer,
		botClient: botClient,
	}
}

// Start starts the Slack bot server
func (s *SlackServer) Start(port int) error {
	http.HandleFunc("/slack/commands", s.handleSlashCommand)
	http.HandleFunc("/slack/events", s.handleEvents)
	http.HandleFunc("/health", s.handleHealth)

	addr := fmt.Sprintf(":%d", port)
	fmt.Printf("🚀 Slack bot server starting on port %d\n", port)
	fmt.Printf("📝 Endpoints:\n")
	fmt.Printf("   POST /slack/commands - Slack slash commands\n")
	fmt.Printf("   POST /slack/events   - Slack event subscriptions\n")
	fmt.Printf("   GET  /health        - Health check\n")

	return http.ListenAndServe(addr, nil)
}

// handleHealth provides a health check endpoint
func (s *SlackServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"healthy","service":"pr-bot"}`))
}

// handleSlashCommand processes Slack slash commands
func (s *SlackServer) handleSlashCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	// Extract Slack command data
	command := r.FormValue("command")
	text := strings.TrimSpace(r.FormValue("text"))
	userID := r.FormValue("user_id")
	channelID := r.FormValue("channel_id")

	logger.Debug("=== RECEIVED SLACK COMMAND: %s, text: %s, user: %s, channel: %s ===", command, text, userID, channelID)

	// Route command
	var response string
	var err error

	switch command {
	case "/info":
		response = s.getHelpMessage()
	case "/pr":
		if text == "" {
			response = "❌ Usage: `/pr <PR_URL>`"
		} else {
			// Send immediate response and process async
			go s.analyzePRAsync(text, r.FormValue("response_url"))
			response = "🔍 Analyzing PR... This may take a moment. Results will appear shortly."
		}
	case "/jt":
		if text == "" {
			response = "❌ Usage: `/jt <JIRA_TICKET>`"
		} else {
			// Send immediate response and process async
			go s.analyzeJiraTicketAsync(text, r.FormValue("response_url"))
			response = "🔍 Analyzing JIRA ticket... This may take a moment. Results will appear shortly."
		}
	case "/version":
		if text == "" {
			response = "❌ Usage: `/version <COMPONENT> <VERSION>` or `/version mce <COMPONENT> <VERSION>`"
		} else {
			response, err = s.handleVersionCommand(text)
		}
	default:
		response = fmt.Sprintf("Unknown command: %s\n\nUse `/info` to see available commands.", command)
	}

	if err != nil {
		logger.Debug("Error processing command: %v", err)
		response = fmt.Sprintf("❌ Error: %v", err)
	}

	// Send response
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(response))
}

// analyzePR analyzes a PR via Slack
func (s *SlackServer) analyzePR(prURL string) (string, error) {
	// Parse PR number and repository
	prNumber, owner, repo, err := parsePRURL(prURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse PR URL: %w", err)
	}

	// Update config with repository info
	if owner != "" && repo != "" {
		s.config.Owner = owner
		s.config.Repository = repo
		// Recreate analyzer with updated config
		ctx := context.Background()
		s.analyzer = analyzer.New(ctx, s.config)
	}

	// Analyze PR
	result, err := s.analyzer.AnalyzePR(prNumber)
	if err != nil {
		return "", fmt.Errorf("failed to analyze PR: %w", err)
	}

	// If JIRA analysis was performed and found related PRs, enhance the response
	if result.JiraAnalysis != nil && len(result.RelatedPRs) > 0 {
		// Find unmerged related PRs from the same JIRA tickets
		var unmergedPRs []models.UnmergedPR

		// Get all unique PR URLs from JIRA analysis
		for _, prURL := range result.JiraAnalysis.RelatedPRURLs {
			// Skip the current PR
			if strings.Contains(prURL, fmt.Sprintf("/pull/%d", prNumber)) {
				continue
			}

			// Parse PR URL to get details
			relatedPRNumber, relatedOwner, relatedRepo, parseErr := parsePRURL(prURL)
			if parseErr != nil {
				logger.Debug("Failed to parse related PR URL %s: %v", prURL, parseErr)
				continue
			}

			// Check if this PR is already in the RelatedPRs (merged PRs)
			found := false
			for _, relatedPR := range result.RelatedPRs {
				if relatedPR.Number == relatedPRNumber {
					found = true
					break
				}
			}

			// If not found in merged PRs, check if it's unmerged
			if !found {
				ctx := context.Background()
				githubClient := github.NewClient(ctx, s.config.GitHubToken)
				prInfo, prErr := githubClient.GetBasicPRInfo(relatedOwner, relatedRepo, relatedPRNumber)
				if prErr != nil {
					logger.Debug("Failed to get basic info for related PR %d: %v", relatedPRNumber, prErr)
				} else if prInfo.MergedAt == nil {
					// It's an unmerged PR
					unmergedPR := models.UnmergedPR{
						Number: prInfo.Number,
						Title:  prInfo.Title,
						URL:    prInfo.URL,
						Status: "In Review",
					}
					unmergedPRs = append(unmergedPRs, unmergedPR)
					logger.Debug("Found unmerged related PR #%d: %s", relatedPRNumber, prInfo.Title)
				}
			}
		}

		// Use enhanced formatting that shows related PRs and unmerged PRs
		return s.formatEnhancedPRAnalysisForSlack(result, unmergedPRs), nil
	}

	// Format response for Slack (standard format for PRs without JIRA analysis)
	return s.formatPRAnalysisForSlack(result), nil
}

// analyzeJiraTicket analyzes a JIRA ticket via Slack
func (s *SlackServer) analyzeJiraTicket(ticketURL string) (string, error) {
	logger.Debug("=== STARTING JIRA TICKET ANALYSIS FOR: %s ===", ticketURL)
	// Extract JIRA ticket ID (supports any project prefix like ACM, MGMT, etc.)
	ticketID := jira.ExtractJiraTicketFromText(ticketURL)
	if ticketID == "" {
		return "", fmt.Errorf("failed to extract JIRA ticket ID from: %s", ticketURL)
	}

	// Check if JIRA token is configured
	if s.config.JiraToken == "" {
		return "", fmt.Errorf("JIRA token not configured. Please set PR_BOT_JIRA_TOKEN in your .env file")
	}

	// Create JIRA client
	ctx := context.Background()
	jiraClient := jira.NewClient(ctx, s.config.JiraToken)

	// Get all related JIRA tickets (main ticket + cloned tickets)
	allTicketIssues, err := jiraClient.GetAllClonedIssues(ticketID)
	if err != nil {
		return "", fmt.Errorf("failed to get related JIRA tickets: %w", err)
	}

	// Extract ticket keys for display
	allTicketKeys := make([]string, len(allTicketIssues))
	for i, ticket := range allTicketIssues {
		allTicketKeys[i] = ticket.Key
	}

	// Extract all PR URLs from all tickets
	var allPRURLs []string
	for _, ticket := range allTicketIssues {
		prURLs := jiraClient.ExtractGitHubPRsFromIssue(ticket)
		allPRURLs = append(allPRURLs, prURLs...)
	}

	// Remove duplicates and filter for supported repositories
	prURLsMap := make(map[string]bool)
	var uniquePRURLs []string

	// Support assisted-service, assisted-installer, assisted-installer-agent, and assisted-installer-ui repositories
	supportedRepos := []string{
		fmt.Sprintf("github.com/%s/assisted-service/pull/", s.config.Owner),
		fmt.Sprintf("github.com/%s/assisted-installer/pull/", s.config.Owner),
		fmt.Sprintf("github.com/%s/assisted-installer-agent/pull/", s.config.Owner),
		fmt.Sprintf("github.com/openshift-assisted/assisted-installer-ui/pull/"), // Different owner
	}

	logger.Debug("Found %d total PR URLs from JIRA tickets", len(allPRURLs))
	logger.Debug("Supported repos: %v", supportedRepos)

	for _, prURL := range allPRURLs {
		if prURLsMap[prURL] {
			continue // Skip duplicates
		}

		// Check if PR is from a supported repository
		isSupported := false
		for _, supportedRepo := range supportedRepos {
			if strings.Contains(prURL, supportedRepo) {
				isSupported = true
				logger.Debug("PR %s matches supported repo pattern: %s", prURL, supportedRepo)
				break
			}
		}

		if isSupported {
			prURLsMap[prURL] = true
			uniquePRURLs = append(uniquePRURLs, prURL)
		} else {
			logger.Debug("PR %s does not match any supported repo pattern", prURL)
		}
	}

	logger.Debug("After filtering: %d unique PR URLs", len(uniquePRURLs))

	// Create JIRA analysis result
	jiraAnalysis := &models.JiraAnalysis{
		MainTicket:      ticketID,
		AllTickets:      allTicketKeys,
		RelatedPRURLs:   uniquePRURLs,
		AnalysisSuccess: true,
	}

	// Analyze each related PR (both merged and unmerged)
	var relatedPRs []models.RelatedPR
	var unmergedPRs []models.UnmergedPR

	logger.Debug("Starting analysis of %d unique PR URLs", len(uniquePRURLs))
	for i, prURL := range uniquePRURLs {
		logger.Debug("Processing PR %d/%d: %s", i+1, len(uniquePRURLs), prURL)
		// Parse PR URL to get number, owner, repo
		prNumber, owner, repo, err := parsePRURL(prURL)
		if err != nil {
			logger.Debug("Failed to parse PR URL %s: %v", prURL, err)
			continue
		}

		// Update config with repository info for this PR
		originalOwner := s.config.Owner
		originalRepo := s.config.Repository
		s.config.Owner = owner
		s.config.Repository = repo

		// Create analyzer for this specific repository
		analyzer := analyzer.New(ctx, s.config)
		if analyzer == nil {
			logger.Debug("Failed to create analyzer for PR %d, checking if it's unmerged", prNumber)
			// If analyzer creation fails, still try to get basic PR info to check if it's unmerged
			ctx := context.Background()
			githubClient := github.NewClient(ctx, s.config.GitHubToken)
			prInfo, prErr := githubClient.GetBasicPRInfo(owner, repo, prNumber)
			if prErr != nil {
				logger.Debug("Failed to get basic info for PR %d: %v", prNumber, prErr)
			} else if prInfo.MergedAt == nil {
				// It's an unmerged PR
				unmergedPR := models.UnmergedPR{
					Number: prInfo.Number,
					Title:  prInfo.Title,
					URL:    prInfo.URL,
					Status: "In Review",
				}
				unmergedPRs = append(unmergedPRs, unmergedPR)
				logger.Debug("Added unmerged PR #%d to results (analyzer creation failed): %s", prNumber, prInfo.Title)
			}
			s.config.Owner = originalOwner
			s.config.Repository = originalRepo
			continue
		}

		// Try to analyze the PR
		result, err := analyzer.AnalyzePR(prNumber)
		if err != nil {
			logger.Debug("PR %d analysis failed with error: %s", prNumber, err.Error())
			// Check if it's an unmerged PR
			if strings.Contains(err.Error(), "is not merged") || strings.Contains(err.Error(), "not merged") {
				logger.Debug("Detected unmerged PR %d, getting basic info", prNumber)
				// Get basic PR info for unmerged PR
				ctx := context.Background()
				githubClient := github.NewClient(ctx, s.config.GitHubToken)
				prInfo, prErr := githubClient.GetBasicPRInfo(owner, repo, prNumber)
				if prErr != nil {
					logger.Debug("Failed to get basic info for unmerged PR %d: %v", prNumber, prErr)
					// Even if we can't get basic info, still add it as an unmerged PR with minimal info
					unmergedPR := models.UnmergedPR{
						Number: prNumber,
						Title:  fmt.Sprintf("PR #%d (unmerged)", prNumber),
						URL:    prURL,
						Status: "In Review",
					}
					unmergedPRs = append(unmergedPRs, unmergedPR)
					logger.Debug("Added unmerged PR #%d to results (basic info failed): %s", prNumber, prURL)
				} else {
					unmergedPR := models.UnmergedPR{
						Number: prInfo.Number,
						Title:  prInfo.Title,
						URL:    prInfo.URL,
						Status: "In Review",
					}
					unmergedPRs = append(unmergedPRs, unmergedPR)
					logger.Debug("Added unmerged PR #%d to results: %s", prNumber, prInfo.Title)
				}
			} else {
				logger.Debug("Failed to analyze PR %d (not unmerged), getting basic info", prNumber)
				// For other analysis failures, still try to get basic PR info
				ctx := context.Background()
				githubClient := github.NewClient(ctx, s.config.GitHubToken)
				prInfo, prErr := githubClient.GetBasicPRInfo(owner, repo, prNumber)
				if prErr != nil {
					logger.Debug("Failed to get basic info for PR %d: %v", prNumber, prErr)
					// Even if we can't get basic info, still add it as an unmerged PR with minimal info
					unmergedPR := models.UnmergedPR{
						Number: prNumber,
						Title:  fmt.Sprintf("PR #%d (analysis failed)", prNumber),
						URL:    prURL,
						Status: "Analysis Failed",
					}
					unmergedPRs = append(unmergedPRs, unmergedPR)
					logger.Debug("Added PR #%d to results (analysis failed): %s", prNumber, prURL)
				} else if prInfo.MergedAt == nil {
					// It's an unmerged PR
					unmergedPR := models.UnmergedPR{
						Number: prInfo.Number,
						Title:  prInfo.Title,
						URL:    prInfo.URL,
						Status: "In Review",
					}
					unmergedPRs = append(unmergedPRs, unmergedPR)
					logger.Debug("Added unmerged PR #%d to results (analysis failed): %s", prNumber, prInfo.Title)
				} else {
					// It's a merged PR but analysis failed - add it as unmerged with error status
					unmergedPR := models.UnmergedPR{
						Number: prInfo.Number,
						Title:  prInfo.Title,
						URL:    prInfo.URL,
						Status: "Analysis Failed",
					}
					unmergedPRs = append(unmergedPRs, unmergedPR)
					logger.Debug("Added merged PR #%d to results (analysis failed): %s", prNumber, prInfo.Title)
				}
			}
			// Restore original config
			s.config.Owner = originalOwner
			s.config.Repository = originalRepo
			continue
		}

		// Create RelatedPR entry for merged PR
		relatedPR := models.RelatedPR{
			Number:          result.PR.Number,
			Title:           result.PR.Title,
			URL:             result.PR.URL,
			Hash:            result.PR.Hash,
			JiraTickets:     []string{ticketID}, // Associate with the main ticket
			ReleaseBranches: result.ReleaseBranches,
		}
		relatedPRs = append(relatedPRs, relatedPR)

		// Restore original config
		s.config.Owner = originalOwner
		s.config.Repository = originalRepo
	}

	// Format response for Slack
	return s.formatJiraAnalysisForSlack(jiraAnalysis, relatedPRs, unmergedPRs), nil
}

// handleVersionCommand handles version comparison commands
func (s *SlackServer) handleVersionCommand(text string) (string, error) {
	args := strings.Fields(text)
	if len(args) < 2 {
		return "❌ Usage: `/version <COMPONENT> <VERSION>` or `/version mce <COMPONENT> <VERSION>`\n\nAvailable components: assisted-service, assisted-installer, assisted-installer-agent, assisted-installer-ui", nil
	}

	if len(args) >= 3 && args[0] == "mce" {
		// MCE version comparison: /version mce assisted-service 2.8.0
		component := args[1]
		version := args[2]
		return s.compareMCEVersionWithComponent(component, version)
	} else {
		// Regular version comparison: /version assisted-service v2.40.1
		component := args[0]
		version := args[1]
		return s.compareVersionWithComponent(component, version)
	}
}

// compareVersionWithComponent compares regular versions with component
func (s *SlackServer) compareVersionWithComponent(component, version string) (string, error) {
	// TODO: Implement regular version comparison for Slack with component
	return fmt.Sprintf("🚧 Version comparison for %s %s is not yet implemented in Slack mode", component, version), nil
}

// compareMCEVersionWithComponent compares MCE versions with component
func (s *SlackServer) compareMCEVersionWithComponent(component, version string) (string, error) {
	// TODO: Implement MCE version comparison for Slack with component
	return fmt.Sprintf("🚧 MCE version comparison for %s %s is not yet implemented in Slack mode", component, version), nil
}

// formatPRAnalysisForSlack formats PR analysis results for Slack
func (s *SlackServer) formatPRAnalysisForSlack(result *models.PRAnalysisResult) string {
	var response strings.Builder

	response.WriteString(fmt.Sprintf("📋 *PR Analysis: #%d*\n", result.PR.Number))
	response.WriteString(fmt.Sprintf("🔗 %s\n", result.PR.URL))
	response.WriteString(fmt.Sprintf("📝 %s\n", result.PR.Title))
	response.WriteString(fmt.Sprintf("🔨 Merged to `%s` at %s\n\n", result.PR.MergedInto, models.FormatDate(result.PR.MergedAt)))

	if len(result.ReleaseBranches) == 0 {
		response.WriteString("❌ No release branches found containing this PR\n")
		return response.String()
	}

	// Group branches by pattern
	branchGroups := make(map[string][]models.BranchPresence)
	for _, branch := range result.ReleaseBranches {
		if branch.Found {
			branchGroups[branch.Pattern] = append(branchGroups[branch.Pattern], branch)
		}
	}

	response.WriteString("✅ *Found in release branches:*\n")
	for pattern, branches := range branchGroups {
		response.WriteString(fmt.Sprintf("📂 *%s branches:*\n", getPatternName(pattern)))
		for _, branch := range branches {
			response.WriteString(fmt.Sprintf("  • `%s` (v%s)", branch.BranchName, branch.Version))
			if branch.MergedAt != nil {
				response.WriteString(fmt.Sprintf(" - merged %s", models.FormatDate(branch.MergedAt)))
			}

			// Add GA release information for ACM/MCE branches
			if pattern == "release-ocm-" {
				s.addGAInfoToSlackResponse(&response, branch)
			}

			// Add version tag information for version-prefixed branches
			if len(branch.ReleasedVersions) > 0 {
				releasedVersionsText := strings.Join(branch.ReleasedVersions, ", ")
				// Add badge for SaaS versions
				if pattern == "v" && s.analyzer != nil && s.analyzer.GetGitLabClient() != nil {
					// Get badge for the first released version (or all if multiple)
					badge := s.getSaaSVersionBadge(branch.ReleasedVersions[0])
					releasedVersionsText += badge
				}
				response.WriteString(fmt.Sprintf("\n    📦 Released in: %s", releasedVersionsText))
			}

			response.WriteString("\n")
		}
		response.WriteString("\n")
	}

	return response.String()
}

// formatEnhancedPRAnalysisForSlack formats PR analysis results with related PRs for Slack
func (s *SlackServer) formatEnhancedPRAnalysisForSlack(result *models.PRAnalysisResult, unmergedPRs []models.UnmergedPR) string {
	var response strings.Builder

	// Main PR header
	response.WriteString(fmt.Sprintf("📋 *PR Analysis: #%d*\n", result.PR.Number))
	response.WriteString(fmt.Sprintf("🔗 %s\n", result.PR.URL))
	response.WriteString(fmt.Sprintf("📝 %s\n", result.PR.Title))
	response.WriteString(fmt.Sprintf("🔨 Merged to `%s` at %s\n\n", result.PR.MergedInto, models.FormatDate(result.PR.MergedAt)))

	// JIRA information
	if result.JiraAnalysis != nil {
		response.WriteString(fmt.Sprintf("🎫 *JIRA Ticket: %s*\n", result.JiraAnalysis.MainTicket))
		if len(result.JiraAnalysis.AllTickets) > 1 {
			response.WriteString(fmt.Sprintf("🔗 Related tickets: %s\n", strings.Join(result.JiraAnalysis.AllTickets[1:], ", ")))
		}

		totalRelatedPRs := len(result.RelatedPRs) + len(unmergedPRs)
		if totalRelatedPRs > 0 {
			response.WriteString(fmt.Sprintf("📊 Found %d related PRs", totalRelatedPRs))
			if len(result.RelatedPRs) > 0 && len(unmergedPRs) > 0 {
				response.WriteString(fmt.Sprintf(" (%d merged, %d in review)", len(result.RelatedPRs), len(unmergedPRs)))
			} else if len(unmergedPRs) > 0 {
				response.WriteString(fmt.Sprintf(" (%d in review)", len(unmergedPRs)))
			}
			response.WriteString("\n\n")
		}
	}

	// Main PR release branch analysis
	if len(result.ReleaseBranches) == 0 {
		response.WriteString("❌ No release branches found containing this PR\n")
	} else {
		// Group branches by pattern
		branchGroups := make(map[string][]models.BranchPresence)
		for _, branch := range result.ReleaseBranches {
			if branch.Found {
				branchGroups[branch.Pattern] = append(branchGroups[branch.Pattern], branch)
			}
		}

		response.WriteString("✅ *Found in release branches:*\n")
		for pattern, branches := range branchGroups {
			response.WriteString(fmt.Sprintf("📂 *%s branches:*\n", getPatternName(pattern)))
			for _, branch := range branches {
				response.WriteString(fmt.Sprintf("  • `%s` (v%s)", branch.BranchName, branch.Version))
				if branch.MergedAt != nil {
					response.WriteString(fmt.Sprintf(" - merged %s", models.FormatDate(branch.MergedAt)))
				}

				// Add GA release information for ACM/MCE branches
				if pattern == "release-ocm-" {
					s.addGAInfoToSlackResponse(&response, branch)
				}

				// Add version tag information for version-prefixed branches
				if len(branch.ReleasedVersions) > 0 {
					releasedVersionsText := strings.Join(branch.ReleasedVersions, ", ")
					// Add badge for SaaS versions
					if pattern == "v" && s.analyzer != nil && s.analyzer.GetGitLabClient() != nil {
						// Get badge for the first released version (or all if multiple)
						badge := s.getSaaSVersionBadge(branch.ReleasedVersions[0])
						releasedVersionsText += badge
					}
					response.WriteString(fmt.Sprintf("\n    📦 Released in: %s", releasedVersionsText))
				}

				response.WriteString("\n")
			}
			response.WriteString("\n")
		}
	}

	// Show related PRs if any exist
	if len(result.RelatedPRs) > 0 || len(unmergedPRs) > 0 {
		response.WriteString("🔗 *Related PRs:*\n")

		// Show merged related PRs
		for i, relatedPR := range result.RelatedPRs {
			// Skip the main PR if it appears in related PRs
			if relatedPR.Number == result.PR.Number {
				continue
			}

			response.WriteString(fmt.Sprintf("*%d. PR #%d*\n", i+1, relatedPR.Number))
			response.WriteString(fmt.Sprintf("🔗 %s\n", relatedPR.URL))
			response.WriteString(fmt.Sprintf("📝 %s\n", relatedPR.Title))

			// Check if PR is in any release branches
			foundBranches := []models.BranchPresence{}
			for _, branch := range relatedPR.ReleaseBranches {
				if branch.Found {
					foundBranches = append(foundBranches, branch)
				}
			}

			if len(foundBranches) == 0 {
				response.WriteString("❌ Not found in any release branches\n")
			} else {
				response.WriteString("✅ *Found in release branches:*\n")

				// Group branches by pattern
				branchGroups := make(map[string][]models.BranchPresence)
				for _, branch := range foundBranches {
					branchGroups[branch.Pattern] = append(branchGroups[branch.Pattern], branch)
				}

				for pattern, branches := range branchGroups {
					response.WriteString(fmt.Sprintf("  📂 *%s branches:*\n", getPatternName(pattern)))
					for _, branch := range branches {
						response.WriteString(fmt.Sprintf("    • `%s` (v%s)", branch.BranchName, branch.Version))
						if branch.MergedAt != nil {
							response.WriteString(fmt.Sprintf(" - merged %s", models.FormatDate(branch.MergedAt)))
						}

						// Add GA release information for ACM/MCE branches
						if pattern == "release-ocm-" {
							s.addGAInfoToSlackResponse(&response, branch)
						}

						// Add version tag information for version-prefixed branches
						if len(branch.ReleasedVersions) > 0 {
							releasedVersionsText := strings.Join(branch.ReleasedVersions, ", ")
							// Add badge for SaaS versions
							if pattern == "v" && s.analyzer != nil && s.analyzer.GetGitLabClient() != nil {
								// Get badge for the first released version (or all if multiple)
								badge := s.getSaaSVersionBadge(branch.ReleasedVersions[0])
								releasedVersionsText += badge
							}
							response.WriteString(fmt.Sprintf("\n      📦 Released in: %s", releasedVersionsText))
						}

						response.WriteString("\n")
					}
				}
			}
			response.WriteString("\n")
		}

		// Add unmerged PRs section if any exist
		if len(unmergedPRs) > 0 {
			response.WriteString("🔄 *PRs In Review:*\n")
			for i, unmergedPR := range unmergedPRs {
				prIndex := len(result.RelatedPRs) + i + 1
				// Display status only if it's not a standard "In Review" status
				statusDisplay := ""
				if unmergedPR.Status != "In Review" {
					statusDisplay = fmt.Sprintf(" *%s*", unmergedPR.Status)
				}
				response.WriteString(fmt.Sprintf("*%d. PR #%d*%s\n", prIndex, unmergedPR.Number, statusDisplay))
				response.WriteString(fmt.Sprintf("🔗 %s\n", unmergedPR.URL))
				response.WriteString(fmt.Sprintf("📝 %s\n", unmergedPR.Title))
				response.WriteString("⏳ Cannot analyze release branches until merged\n\n")
			}
		}

		// Summary for related PRs
		totalBranches := 0
		for _, relatedPR := range result.RelatedPRs {
			for _, branch := range relatedPR.ReleaseBranches {
				if branch.Found {
					totalBranches++
				}
			}
		}

		totalRelatedPRs := len(result.RelatedPRs) + len(unmergedPRs)
		summaryText := fmt.Sprintf("📋 *Summary:* 1 main PR + %d related PRs", totalRelatedPRs)
		if len(result.RelatedPRs) > 0 {
			summaryText += fmt.Sprintf(" (%d analyzed, %d release branch entries)", len(result.RelatedPRs), totalBranches)
		}
		if len(unmergedPRs) > 0 {
			summaryText += fmt.Sprintf(" (%d in review)", len(unmergedPRs))
		}
		response.WriteString(summaryText + "\n")
	}

	return response.String()
}

// addGAInfoToSlackResponse adds GA release information to the Slack response
func (s *SlackServer) addGAInfoToSlackResponse(response *strings.Builder, branch models.BranchPresence) {
	now := time.Now()

	// Show upcoming GA versions (including released ones)
	if len(branch.UpcomingGAs) > 0 {
		// Track products to avoid duplicates
		productStatus := make(map[string]bool)

		// First pass: show released versions
		for _, upcomingGA := range branch.UpcomingGAs {
			if upcomingGA.GADate != nil && upcomingGA.GADate.Before(now) {
				if !productStatus[upcomingGA.Product] {
					productStatus[upcomingGA.Product] = true
					response.WriteString(fmt.Sprintf("\n    🚀 %s %s: Released (GA: %s)",
						upcomingGA.Product, upcomingGA.Version, models.FormatDate(upcomingGA.GADate)))
				}
			}
		}

		// Second pass: show upcoming releases for products without released versions
		productNotReleased := make(map[string]bool)
		for _, upcomingGA := range branch.UpcomingGAs {
			if !productStatus[upcomingGA.Product] && !productNotReleased[upcomingGA.Product] {
				productNotReleased[upcomingGA.Product] = true
				response.WriteString(fmt.Sprintf("\n    ⏳ %s %s: Upcoming (GA: %s)",
					upcomingGA.Product, upcomingGA.Version, models.FormatDate(upcomingGA.GADate)))
			}
		}
	}

	// Show latest GA status (already released versions from GAStatus)
	hasLatestGA := (branch.GAStatus.ACM.Version != "" && branch.GAStatus.ACM.Status == "GA" &&
		branch.GAStatus.ACM.GADate != nil && branch.GAStatus.ACM.GADate.Before(now)) ||
		(branch.GAStatus.MCE.Version != "" && branch.GAStatus.MCE.Status == "GA" &&
			branch.GAStatus.MCE.GADate != nil && branch.GAStatus.MCE.GADate.Before(now))

	if hasLatestGA {
		if branch.GAStatus.ACM.Version != "" && branch.GAStatus.ACM.Status == "GA" &&
			branch.GAStatus.ACM.GADate != nil && branch.GAStatus.ACM.GADate.Before(now) {
			response.WriteString(fmt.Sprintf("\n    ✅ ACM %s: Released (GA: %s)",
				branch.GAStatus.ACM.Version, models.FormatDate(branch.GAStatus.ACM.GADate)))
		}
		if branch.GAStatus.MCE.Version != "" && branch.GAStatus.MCE.Status == "GA" &&
			branch.GAStatus.MCE.GADate != nil && branch.GAStatus.MCE.GADate.Before(now) {
			response.WriteString(fmt.Sprintf("\n    ✅ MCE %s: Released (GA: %s)",
				branch.GAStatus.MCE.Version, models.FormatDate(branch.GAStatus.MCE.GADate)))
		}
	}
}

// formatJiraAnalysisForSlack formats JIRA analysis results for Slack
func (s *SlackServer) formatJiraAnalysisForSlack(jiraAnalysis *models.JiraAnalysis, relatedPRs []models.RelatedPR, unmergedPRs []models.UnmergedPR) string {
	var response strings.Builder

	response.WriteString(fmt.Sprintf("🎫 *JIRA Ticket Analysis: %s*\n", jiraAnalysis.MainTicket))

	if len(jiraAnalysis.AllTickets) > 1 {
		response.WriteString(fmt.Sprintf("🔗 Related tickets: %s\n", strings.Join(jiraAnalysis.AllTickets[1:], ", ")))
	}

	totalPRs := len(relatedPRs) + len(unmergedPRs)
	response.WriteString(fmt.Sprintf("📊 Found %d related PRs", totalPRs))
	if len(relatedPRs) > 0 && len(unmergedPRs) > 0 {
		response.WriteString(fmt.Sprintf(" (%d merged, %d in review)", len(relatedPRs), len(unmergedPRs)))
	} else if len(unmergedPRs) > 0 {
		response.WriteString(fmt.Sprintf(" (%d in review)", len(unmergedPRs)))
	}
	response.WriteString("\n\n")

	if totalPRs == 0 {
		response.WriteString("❌ No related PRs found in supported repositories\n")
		response.WriteString("💡 Supported repos: assisted-service, assisted-installer, assisted-installer-agent, assisted-installer-ui\n")
		return response.String()
	}

	response.WriteString("🔗 *Related PRs:*\n")
	for i, relatedPR := range relatedPRs {
		response.WriteString(fmt.Sprintf("*%d. PR #%d*\n", i+1, relatedPR.Number))
		response.WriteString(fmt.Sprintf("🔗 %s\n", relatedPR.URL))
		response.WriteString(fmt.Sprintf("📝 %s\n", relatedPR.Title))

		// Check if PR is in any release branches
		foundBranches := []models.BranchPresence{}
		for _, branch := range relatedPR.ReleaseBranches {
			if branch.Found {
				foundBranches = append(foundBranches, branch)
			}
		}

		if len(foundBranches) == 0 {
			response.WriteString("❌ Not found in any release branches\n")
		} else {
			response.WriteString("✅ *Found in release branches:*\n")

			// Group branches by pattern
			branchGroups := make(map[string][]models.BranchPresence)
			for _, branch := range foundBranches {
				branchGroups[branch.Pattern] = append(branchGroups[branch.Pattern], branch)
			}

			for pattern, branches := range branchGroups {
				response.WriteString(fmt.Sprintf("  📂 *%s branches:*\n", getPatternName(pattern)))
				for _, branch := range branches {
					response.WriteString(fmt.Sprintf("    • `%s` (v%s)", branch.BranchName, branch.Version))
					if branch.MergedAt != nil {
						response.WriteString(fmt.Sprintf(" - merged %s", models.FormatDate(branch.MergedAt)))
					}

					// Add GA release information for ACM/MCE branches
					if pattern == "release-ocm-" {
						s.addGAInfoToSlackResponse(&response, branch)
					}

					// Add version tag information for version-prefixed branches
					if len(branch.ReleasedVersions) > 0 {
						releasedVersionsText := strings.Join(branch.ReleasedVersions, ", ")
						// Add badge for SaaS versions
						if pattern == "v" && s.analyzer != nil && s.analyzer.GetGitLabClient() != nil {
							// Get badge for the first released version (or all if multiple)
							badge := s.getSaaSVersionBadge(branch.ReleasedVersions[0])
							releasedVersionsText += badge
						}
						response.WriteString(fmt.Sprintf("\n      📦 Released in: %s", releasedVersionsText))
					}

					response.WriteString("\n")
				}
			}
		}
		response.WriteString("\n")
	}

	// Add unmerged PRs section if any exist
	if len(unmergedPRs) > 0 {
		response.WriteString("🔄 *PRs In Review:*\n")
		for i, unmergedPR := range unmergedPRs {
			prIndex := len(relatedPRs) + i + 1
			// Display status only if it's not a standard "In Review" status
			statusDisplay := ""
			if unmergedPR.Status != "In Review" {
				statusDisplay = fmt.Sprintf(" *%s*", unmergedPR.Status)
			}
			response.WriteString(fmt.Sprintf("*%d. PR #%d*%s\n", prIndex, unmergedPR.Number, statusDisplay))
			response.WriteString(fmt.Sprintf("🔗 %s\n", unmergedPR.URL))
			response.WriteString(fmt.Sprintf("📝 %s\n", unmergedPR.Title))
			response.WriteString("⏳ Cannot analyze release branches until merged\n\n")
		}
	}

	// Summary
	totalBranches := 0
	for _, relatedPR := range relatedPRs {
		for _, branch := range relatedPR.ReleaseBranches {
			if branch.Found {
				totalBranches++
			}
		}
	}

	summaryText := fmt.Sprintf("📋 *Summary:* %d total PRs", totalPRs)
	if len(relatedPRs) > 0 {
		summaryText += fmt.Sprintf(" (%d analyzed, %d release branch entries)", len(relatedPRs), totalBranches)
	}
	if len(unmergedPRs) > 0 {
		summaryText += fmt.Sprintf(" (%d in review)", len(unmergedPRs))
	}
	response.WriteString(summaryText + "\n")

	return response.String()
}

// analyzePRAsync analyzes a PR asynchronously and sends result via response_url
func (s *SlackServer) analyzePRAsync(prURL, responseURL string) {
	// Perform the analysis
	result, err := s.analyzePR(prURL)

	var message string
	if err != nil {
		message = fmt.Sprintf("❌ Error analyzing PR: %v", err)
	} else {
		message = result
	}

	// Send the result back to Slack using response_url
	s.sendDelayedResponse(responseURL, message)
}

// analyzeJiraTicketAsync analyzes a JIRA ticket asynchronously and sends result via response_url
func (s *SlackServer) analyzeJiraTicketAsync(ticketURL, responseURL string) {
	logger.Debug("=== ASYNC JIRA ANALYSIS STARTED: %s (response_url: %s) ===", ticketURL, responseURL)
	// Perform the analysis
	result, err := s.analyzeJiraTicket(ticketURL)
	logger.Debug("=== ASYNC JIRA ANALYSIS COMPLETED: err=%v ===", err)

	var message string
	if err != nil {
		message = fmt.Sprintf("❌ Error analyzing JIRA ticket: %v", err)
	} else {
		message = result
	}

	// Send the result back to Slack using response_url
	s.sendDelayedResponse(responseURL, message)
}

// sendDelayedResponse sends a delayed response to Slack using response_url
func (s *SlackServer) sendDelayedResponse(responseURL, message string) {
	if responseURL == "" {
		logger.Debug("No response URL provided for delayed response")
		return
	}

	payload := map[string]interface{}{
		"text":          message,
		"response_type": "in_channel", // or "ephemeral" for private response
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		logger.Debug("Failed to marshal delayed response: %v", err)
		return
	}

	resp, err := http.Post(responseURL, "application/json", strings.NewReader(string(jsonData)))
	if err != nil {
		logger.Debug("Failed to send delayed response: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Debug("Delayed response failed with status: %d", resp.StatusCode)
	}
}

// getHelpMessage returns the help message for Slack commands
func (s *SlackServer) getHelpMessage() string {
	return `🤖 *PR Bot Commands*

*Available Slash Commands:*
• ` + "`" + `/info` + "`" + ` - Show this help message
• ` + "`" + `/pr <PR_URL>` + "`" + ` - Analyze a PR across release branches
• ` + "`" + `/jt <JIRA_TICKET>` + "`" + ` - Analyze all PRs related to a JIRA ticket
• ` + "`" + `/version <COMPONENT> <VERSION>` + "`" + ` - Compare GitHub tag with previous version
• ` + "`" + `/version mce <COMPONENT> <VERSION>` + "`" + ` - Compare MCE version with previous version

*Examples:*
• ` + "`" + `/pr https://github.com/openshift/assisted-service/pull/7788` + "`" + `
• ` + "`" + `/jt MGMT-20662` + "`" + ` or ` + "`" + `/jt ACM-22787` + "`" + `
• ` + "`" + `/jt https://issues.redhat.com/browse/ACM-22787` + "`" + `
• ` + "`" + `/version assisted-service v2.40.1` + "`" + `
• ` + "`" + `/version mce assisted-service 2.8.0` + "`" + `

*Available Components:*
• assisted-service, assisted-installer, assisted-installer-agent, assisted-installer-ui

*Alternative Usage:*
You can also mention the bot (` + "`" + `@pr-bot` + "`" + `) or send direct messages using the same command syntax without the slash.`
}

// getPatternName returns a user-friendly name for branch patterns
func getPatternName(pattern string) string {
	switch pattern {
	case "release-ocm-":
		return "ACM/MCE Release"
	case "release-":
		return "OpenShift Release"
	case "release-v":
		return "Release-v"
	case "v":
		return "SaaS versions"
	case "releases/v":
		return "UI Release"
	default:
		return pattern
	}
}

// parsePRURL parses a GitHub PR URL and extracts owner, repo, and PR number
func parsePRURL(prURL string) (int, string, string, error) {
	// First try to parse as a number
	if prNumber, err := strconv.Atoi(prURL); err == nil {
		return prNumber, "", "", nil
	}

	// Try to parse as a GitHub URL
	if !strings.HasPrefix(prURL, "http") {
		return 0, "", "", fmt.Errorf("invalid input: must be a PR number or GitHub URL")
	}

	parsedURL, err := url.Parse(prURL)
	if err != nil {
		return 0, "", "", fmt.Errorf("invalid URL: %w", err)
	}

	if parsedURL.Host != "github.com" {
		return 0, "", "", fmt.Errorf("URL must be from github.com")
	}

	// Extract path components: /owner/repo/pull/number
	pathParts := strings.Split(strings.Trim(parsedURL.Path, "/"), "/")
	if len(pathParts) != 4 || pathParts[2] != "pull" {
		return 0, "", "", fmt.Errorf("invalid GitHub PR URL format")
	}

	owner := pathParts[0]
	repo := pathParts[1]
	prNumber, err := strconv.Atoi(pathParts[3])
	if err != nil {
		return 0, "", "", fmt.Errorf("invalid PR number: %s", pathParts[3])
	}

	return prNumber, owner, repo, nil
}

// handleEvents handles Slack event subscriptions
func (s *SlackServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload struct {
		Token     string       `json:"token"`
		Challenge string       `json:"challenge"`
		Type      string       `json:"type"`
		Event     *slack.Event `json:"event,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		logger.Debug("Failed to decode event payload: %v", err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Handle URL verification challenge
	if payload.Type == "url_verification" {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(payload.Challenge))
		return
	}

	// Handle event callbacks
	if payload.Type == "event_callback" && payload.Event != nil {
		go s.processSlackEvent(payload.Event)
	}

	// Acknowledge the event
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// processSlackEvent processes incoming Slack events
func (s *SlackServer) processSlackEvent(event *slack.Event) {
	if s.botClient == nil {
		logger.Debug("Bot client not configured, ignoring event")
		return
	}

	ctx := context.Background()

	// Ignore bot messages to prevent loops
	if event.BotID != "" {
		return
	}

	// Handle different event types
	switch event.Type {
	case "app_mention":
		s.handleMention(ctx, event)
	case "message":
		// Only handle direct messages
		if event.IsDirectMessage() {
			s.handleDirectMessage(ctx, event)
		}
	}
}

// handleMention handles when the bot is mentioned in a channel
func (s *SlackServer) handleMention(ctx context.Context, event *slack.Event) {
	command := event.ExtractCommand(s.botUserID)
	response, err := s.handleTextCommand(command)

	if err != nil {
		response = fmt.Sprintf("❌ Error: %v", err)
	}

	// Post response in thread
	if err := s.botClient.PostThreadReply(ctx, event.Channel, response, event.Timestamp); err != nil {
		logger.Debug("Failed to post thread reply: %v", err)
	}
}

// handleDirectMessage handles direct messages to the bot
func (s *SlackServer) handleDirectMessage(ctx context.Context, event *slack.Event) {
	response, err := s.handleTextCommand(event.Text)

	if err != nil {
		response = fmt.Sprintf("❌ Error: %v", err)
	}

	// Post response in DM
	if err := s.botClient.PostSimpleMessage(ctx, event.Channel, response); err != nil {
		logger.Debug("Failed to post DM response: %v", err)
	}
}

// handleTextCommand handles text-based commands (from mentions or DMs)
func (s *SlackServer) handleTextCommand(text string) (string, error) {
	text = strings.TrimSpace(text)

	if text == "" || text == "help" || text == "info" {
		return s.getHelpMessage(), nil
	}

	args := strings.Fields(text)
	if len(args) == 0 {
		return s.getHelpMessage(), nil
	}

	command := args[0]
	commandText := ""
	if len(args) > 1 {
		commandText = strings.Join(args[1:], " ")
	}

	switch command {
	case "pr":
		if commandText == "" {
			return "❌ Usage: `pr <PR_URL>`", nil
		}
		return s.analyzePR(commandText)

	case "jt", "jira":
		if commandText == "" {
			return "❌ Usage: `jt <JIRA_TICKET>`", nil
		}
		return s.analyzeJiraTicket(commandText)

	case "version", "v":
		if commandText == "" {
			return "❌ Usage: `version <COMPONENT> <VERSION>` or `version mce <COMPONENT> <VERSION>`", nil
		}
		return s.handleVersionCommand(commandText)

	default:
		return fmt.Sprintf("❌ Unknown command: %s\n\nUse `info` or `help` to see available commands.", command), nil
	}
}

// getSaaSVersionBadge returns the badge text for a SaaS version
func (s *SlackServer) getSaaSVersionBadge(releasedVersion string) string {
	if s.analyzer == nil {
		return ""
	}
	gitlabClient := s.analyzer.GetGitLabClient()
	if gitlabClient == nil {
		return ""
	}
	return gitlabClient.GetSaaSVersionBadge(releasedVersion)
}
