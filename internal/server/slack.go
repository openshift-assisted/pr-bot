// Package server provides Slack bot server functionality.
package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sbratsla/pr-bot/internal/jira"
	"github.com/sbratsla/pr-bot/internal/logger"
	"github.com/sbratsla/pr-bot/internal/models"
	"github.com/sbratsla/pr-bot/pkg/analyzer"
)

// SlackServer handles Slack bot requests
type SlackServer struct {
	config   *models.Config
	analyzer *analyzer.Analyzer
}

// NewSlackServer creates a new Slack server instance
func NewSlackServer(cfg *models.Config) *SlackServer {
	ctx := context.Background()
	analyzer := analyzer.New(ctx, cfg)

	return &SlackServer{
		config:   cfg,
		analyzer: analyzer,
	}
}

// Start starts the Slack bot server
func (s *SlackServer) Start(port int) error {
	http.HandleFunc("/slack/commands", s.handleSlashCommand)
	http.HandleFunc("/health", s.handleHealth)

	addr := fmt.Sprintf(":%d", port)
	fmt.Printf("🚀 Slack bot server starting on port %d\n", port)
	fmt.Printf("📝 Endpoints:\n")
	fmt.Printf("   POST /slack/commands - Slack slash commands\n")
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

	logger.Debug("Received Slack command: %s, text: %s, user: %s, channel: %s", command, text, userID, channelID)

	// Route command
	var response string
	var err error

	switch command {
	case "/pr-bot", "/prbot":
		response, err = s.handlePRBotCommand(text)
	default:
		response = fmt.Sprintf("Unknown command: %s", command)
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

// handlePRBotCommand processes pr-bot slash commands
func (s *SlackServer) handlePRBotCommand(text string) (string, error) {
	if text == "" || text == "help" {
		return s.getHelpMessage(), nil
	}

	args := strings.Fields(text)
	if len(args) == 0 {
		return s.getHelpMessage(), nil
	}

	command := args[0]

	switch command {
	case "pr":
		if len(args) < 2 {
			return "❌ Usage: `/prbot pr <PR_URL>`", nil
		}
		return s.analyzePR(args[1])

	case "jira", "jt":
		if len(args) < 2 {
			return "❌ Usage: `/prbot jira <JIRA_TICKET>`", nil
		}
		return s.analyzeJiraTicket(args[1])

	case "version", "v":
		if len(args) < 2 {
			return "❌ Usage: `/prbot version <VERSION>` or `/prbot version mce <VERSION>`", nil
		}
		if len(args) >= 3 && args[1] == "mce" {
			return s.compareMCEVersion(args[2])
		}
		return s.compareVersion(args[1])

	default:
		return fmt.Sprintf("❌ Unknown command: %s\n\n%s", command, s.getHelpMessage()), nil
	}
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

	// Format response for Slack
	return s.formatPRAnalysisForSlack(result), nil
}

// analyzeJiraTicket analyzes a JIRA ticket via Slack
func (s *SlackServer) analyzeJiraTicket(ticketURL string) (string, error) {
	// Extract JIRA ticket ID
	ticketID := jira.ExtractMGMTTicketFromTitle(ticketURL)
	if ticketID == "" {
		return "", fmt.Errorf("failed to extract JIRA ticket ID from: %s", ticketURL)
	}

	// TODO: Implement JIRA ticket analysis for Slack
	return fmt.Sprintf("🚧 JIRA ticket analysis for %s is not yet implemented in Slack mode", ticketID), nil
}

// compareVersion compares versions via Slack
func (s *SlackServer) compareVersion(version string) (string, error) {
	// TODO: Implement version comparison for Slack
	return fmt.Sprintf("🚧 Version comparison for %s is not yet implemented in Slack mode", version), nil
}

// compareMCEVersion compares MCE versions via Slack
func (s *SlackServer) compareMCEVersion(version string) (string, error) {
	// TODO: Implement MCE version comparison for Slack
	return fmt.Sprintf("🚧 MCE version comparison for %s is not yet implemented in Slack mode", version), nil
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
			response.WriteString("\n")
		}
		response.WriteString("\n")
	}

	return response.String()
}

// getHelpMessage returns the help message for Slack commands
func (s *SlackServer) getHelpMessage() string {
	return `🤖 *PR Bot Help*

*Commands:*
• ` + "`" + `/prbot pr <PR_URL>` + "`" + ` - Analyze a PR across release branches
• ` + "`" + `/prbot jira <JIRA_TICKET>` + "`" + ` - Analyze all PRs related to a JIRA ticket
• ` + "`" + `/prbot version <VERSION>` + "`" + ` - Compare GitHub tag with previous version
• ` + "`" + `/prbot version mce <VERSION>` + "`" + ` - Compare MCE version with previous version
• ` + "`" + `/prbot help` + "`" + ` - Show this help message

*Examples:*
• ` + "`" + `/prbot pr https://github.com/openshift/assisted-service/pull/7788` + "`" + `
• ` + "`" + `/prbot jira MGMT-20662` + "`" + `
• ` + "`" + `/prbot version v2.40.1` + "`" + `
• ` + "`" + `/prbot version mce 2.8.1` + "`"
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
		return "Version-prefixed"
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
