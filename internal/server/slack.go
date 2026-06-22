// Package server provides Slack bot server functionality.
package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shay23bra/pr-bot/internal/ga"
	"github.com/shay23bra/pr-bot/internal/github"
	"github.com/shay23bra/pr-bot/internal/gitlocal"
	"github.com/shay23bra/pr-bot/internal/jira"
	"github.com/shay23bra/pr-bot/internal/logger"
	"github.com/shay23bra/pr-bot/internal/models"
	"github.com/shay23bra/pr-bot/internal/slack"
	"github.com/shay23bra/pr-bot/pkg/analyzer"
)

// SlackServer handles Slack bot requests
type SlackServer struct {
	config      *models.Config
	analyzer    *analyzer.Analyzer
	repoManager *gitlocal.RepoManager
	botClient   *slack.BotClient
	botUserID   string
}

// NewSlackServer creates a new Slack server instance
func NewSlackServer(cfg *models.Config, repoManager *gitlocal.RepoManager) (*SlackServer, error) {
	ctx := context.Background()
	a, err := analyzer.New(ctx, cfg, repoManager)
	if err != nil {
		return nil, fmt.Errorf("failed to create analyzer: %w", err)
	}

	var botClient *slack.BotClient
	if cfg.SlackBotToken != "" {
		botClient = slack.NewBotClient(cfg.SlackBotToken)
		if err := botClient.TestAuth(ctx); err != nil {
			logger.Debug("Failed to authenticate Slack bot: %v", err)
		}
	}

	return &SlackServer{
		config:      cfg,
		repoManager: repoManager,
		analyzer:    a,
		botClient:   botClient,
	}, nil
}

// Start starts the Slack bot server with graceful shutdown.
func (s *SlackServer) Start(port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/slack/commands", s.verifySlackRequest(s.handleSlashCommand))
	mux.HandleFunc("/slack/events", s.verifySlackRequest(s.handleEvents))
	mux.HandleFunc("/health", s.handleHealth)

	addr := fmt.Sprintf(":%d", port)
	fmt.Printf("🚀 Slack bot server starting on port %d\n", port)
	fmt.Printf("📝 Endpoints:\n")
	fmt.Printf("   POST /slack/commands - Slack slash commands\n")
	fmt.Printf("   POST /slack/events   - Slack event subscriptions\n")
	fmt.Printf("   GET  /health        - Health check\n")

	if s.config.SlackSigningSecret == "" {
		logger.Debug("⚠️  Slack signing secret not configured — requests will not be verified")
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		fmt.Println("\n🛑 Shutting down server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	return srv.ListenAndServe()
}

// verifySlackRequest wraps a handler with Slack request signature verification.
func (s *SlackServer) verifySlackRequest(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.config.SlackSigningSecret == "" {
			next(w, r)
			return
		}

		timestamp := r.Header.Get("X-Slack-Request-Timestamp")
		signature := r.Header.Get("X-Slack-Signature")

		if timestamp == "" || signature == "" {
			http.Error(w, "Missing Slack signature headers", http.StatusUnauthorized)
			return
		}

		// Reject requests older than 5 minutes to prevent replay attacks
		ts, err := strconv.ParseInt(timestamp, 10, 64)
		if err != nil || math.Abs(float64(time.Now().Unix()-ts)) > 300 {
			http.Error(w, "Request too old", http.StatusUnauthorized)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read body", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))

		baseString := fmt.Sprintf("v0:%s:%s", timestamp, string(body))
		mac := hmac.New(sha256.New, []byte(s.config.SlackSigningSecret))
		mac.Write([]byte(baseString))
		expected := "v0=" + hex.EncodeToString(mac.Sum(nil))

		if !hmac.Equal([]byte(expected), []byte(signature)) {
			http.Error(w, "Invalid signature", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
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
			go s.analyzePRAsync(text, r.FormValue("response_url"), userID)
			response = "🔍 Analyzing PR... This may take a moment. Results will appear shortly."
		}
	case "/jt":
		if text == "" {
			response = "❌ Usage: `/jt <JIRA_TICKET>`"
		} else {
			// Send immediate response and process async
			go s.analyzeJiraTicketAsync(text, r.FormValue("response_url"), userID)
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
func (s *SlackServer) analyzePR(prURL, userID string) (string, error) {
	// Parse PR number and repository
	prNumber, owner, repo, err := github.ParsePRInput(prURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse PR URL: %w", err)
	}

	// Create analyzer with correct repository info
	cfg := *s.config
	if owner != "" && repo != "" {
		cfg.Owner = owner
		cfg.Repository = repo
	}
	ctx := context.Background()
	a, err := analyzer.New(ctx, &cfg, s.repoManager)
	if err != nil {
		return "", fmt.Errorf("failed to create analyzer: %w", err)
	}

	// Analyze PR
	result, err := a.AnalyzePR(prNumber)
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
			relatedPRNumber, relatedOwner, relatedRepo, parseErr := github.ParsePRInput(prURL)
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
		response := s.formatEnhancedPRAnalysisForSlack(result, unmergedPRs, userID)
		if result.SheetsUnavailable {
			response += "\n" + sheetsUnavailableSlackMessage()
		}
		return response, nil
	}

	// Format response for Slack (standard format for PRs without JIRA analysis)
	response := s.formatPRAnalysisForSlack(result, userID)
	if result.SheetsUnavailable {
		response += "\n" + sheetsUnavailableSlackMessage()
	}
	return response, nil
}

// analyzeJiraTicket analyzes a JIRA ticket via Slack
func (s *SlackServer) analyzeJiraTicket(ticketURL, userID string) (string, error) {
	logger.Debug("=== STARTING JIRA TICKET ANALYSIS FOR: %s ===", ticketURL)
	// Extract JIRA ticket ID (supports any project prefix like ACM, MGMT, etc.)
	ticketID := jira.ExtractJiraTicketFromText(ticketURL)
	if ticketID == "" {
		return "", fmt.Errorf("failed to extract JIRA ticket ID from: %s", ticketURL)
	}

	if s.config.JiraToken == "" || s.config.JiraEmail == "" {
		return "", fmt.Errorf("JIRA not configured. Please set PR_BOT_JIRA_TOKEN and PR_BOT_JIRA_EMAIL in your .env file")
	}

	// Create JIRA client
	ctx := context.Background()
	jiraClient := jira.NewClient(ctx, s.config.JiraEmail, s.config.JiraToken)

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

	// Pre-create one analyzer per unique repo to share branch cache and reduce API calls
	analyzerCache := make(map[string]*analyzer.Analyzer)
	var analyzerMu sync.Mutex
	getAnalyzer := func(owner, repo string) (*analyzer.Analyzer, error) {
		key := owner + "/" + repo
		analyzerMu.Lock()
		defer analyzerMu.Unlock()
		if a, ok := analyzerCache[key]; ok {
			return a, nil
		}
		cfg := *s.config
		cfg.Owner = owner
		cfg.Repository = repo
		a, err := analyzer.New(ctx, &cfg, s.repoManager)
		if err != nil {
			return nil, err
		}
		analyzerCache[key] = a
		return a, nil
	}

	var relatedPRs []models.RelatedPR
	var unmergedPRs []models.UnmergedPR
	var mu sync.Mutex

	logger.Debug("Starting parallel analysis of %d unique PR URLs", len(uniquePRURLs))

	const maxWorkers = 3
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup

	for i, prURL := range uniquePRURLs {
		wg.Add(1)
		go func(index int, url string) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			logger.Debug("Processing PR %d/%d: %s", index+1, len(uniquePRURLs), url)

			prNumber, owner, repo, err := github.ParsePRInput(url)
			if err != nil {
				logger.Debug("Failed to parse PR URL %s: %v", url, err)
				return
			}

			a, aErr := getAnalyzer(owner, repo)
			if aErr != nil {
				logger.Debug("Failed to create analyzer for PR %d: %v", prNumber, aErr)
				return
			}

			result, err := a.AnalyzePR(prNumber)
			if err != nil {
				logger.Debug("PR %d analysis failed with error: %s", prNumber, err.Error())
				// Check if it's an unmerged PR
				if strings.Contains(err.Error(), "is not merged") || strings.Contains(err.Error(), "not merged") {
					logger.Debug("Detected unmerged PR %d, getting basic info", prNumber)
					// Get basic PR info for unmerged PR
					githubClient := github.NewClient(ctx, s.config.GitHubToken)
					prInfo, prErr := githubClient.GetBasicPRInfo(owner, repo, prNumber)
					if prErr != nil {
						logger.Debug("Failed to get basic info for unmerged PR %d: %v", prNumber, prErr)
						// Even if we can't get basic info, still add it as an unmerged PR with minimal info
						unmergedPR := models.UnmergedPR{
							Number: prNumber,
							Title:  fmt.Sprintf("PR #%d (unmerged)", prNumber),
							URL:    url,
							Status: "In Review",
						}
						mu.Lock()
						unmergedPRs = append(unmergedPRs, unmergedPR)
						mu.Unlock()
						logger.Debug("Added unmerged PR #%d to results (basic info failed): %s", prNumber, url)
					} else {
						unmergedPR := models.UnmergedPR{
							Number: prInfo.Number,
							Title:  prInfo.Title,
							URL:    prInfo.URL,
							Status: "In Review",
						}
						mu.Lock()
						unmergedPRs = append(unmergedPRs, unmergedPR)
						mu.Unlock()
						logger.Debug("Added unmerged PR #%d to results: %s", prNumber, prInfo.Title)
					}
				} else {
					logger.Debug("Failed to analyze PR %d (not unmerged), getting basic info", prNumber)
					// For other analysis failures, still try to get basic PR info
					githubClient := github.NewClient(ctx, s.config.GitHubToken)
					prInfo, prErr := githubClient.GetBasicPRInfo(owner, repo, prNumber)
					if prErr != nil {
						logger.Debug("Failed to get basic info for PR %d: %v", prNumber, prErr)
						// Even if we can't get basic info, still add it as an unmerged PR with minimal info
						unmergedPR := models.UnmergedPR{
							Number: prNumber,
							Title:  fmt.Sprintf("PR #%d (analysis failed)", prNumber),
							URL:    url,
							Status: "Analysis Failed",
						}
						mu.Lock()
						unmergedPRs = append(unmergedPRs, unmergedPR)
						mu.Unlock()
						logger.Debug("Added PR #%d to results (analysis failed): %s", prNumber, url)
					} else if prInfo.MergedAt == nil {
						// It's an unmerged PR
						unmergedPR := models.UnmergedPR{
							Number: prInfo.Number,
							Title:  prInfo.Title,
							URL:    prInfo.URL,
							Status: "In Review",
						}
						mu.Lock()
						unmergedPRs = append(unmergedPRs, unmergedPR)
						mu.Unlock()
						logger.Debug("Added unmerged PR #%d to results (analysis failed): %s", prNumber, prInfo.Title)
					} else {
						// It's a merged PR but analysis failed - add it as unmerged with error status
						unmergedPR := models.UnmergedPR{
							Number: prInfo.Number,
							Title:  prInfo.Title,
							URL:    prInfo.URL,
							Status: "Analysis Failed",
						}
						mu.Lock()
						unmergedPRs = append(unmergedPRs, unmergedPR)
						mu.Unlock()
						logger.Debug("Added merged PR #%d to results (analysis failed): %s", prNumber, prInfo.Title)
					}
				}
				return
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

			mu.Lock()
			relatedPRs = append(relatedPRs, relatedPR)
			mu.Unlock()

			logger.Debug("Completed analysis for PR #%d: %s", prNumber, result.PR.Title)
		}(i, prURL)
	}

	// Wait for all workers to complete
	wg.Wait()
	logger.Debug("Parallel PR analysis completed: %d merged, %d unmerged", len(relatedPRs), len(unmergedPRs))

	// Format response for Slack
	return s.formatJiraAnalysisForSlack(jiraAnalysis, relatedPRs, unmergedPRs, userID), nil
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
	ctx := context.Background()
	cfg := *s.config
	a, err := analyzer.New(ctx, &cfg, s.repoManager)
	if err != nil {
		return "", fmt.Errorf("failed to create analyzer: %w", err)
	}

	result, err := a.CompareVersions(component, version)
	if err != nil {
		return "", err
	}

	return s.formatVersionComparisonForSlack(result), nil
}

// compareMCEVersionWithComponent compares MCE versions with component
func (s *SlackServer) compareMCEVersionWithComponent(component, version string) (string, error) {
	return "", fmt.Errorf("MCE version comparison is not yet available in Slack mode — use CLI: `pr-bot -v mce %s %s`", component, version)
}

func (s *SlackServer) formatVersionComparisonForSlack(result *models.VersionComparisonResult) string {
	var response strings.Builder

	response.WriteString(fmt.Sprintf("📦 *Version Comparison: %s*\n", result.TargetVersion))
	response.WriteString(fmt.Sprintf("Component: `%s` (%s/%s)\n", result.Component, result.Owner, result.Repository))
	response.WriteString(fmt.Sprintf("Comparing: `%s` → `%s`\n", result.PreviousVersion, result.TargetVersion))
	response.WriteString(fmt.Sprintf("Total commits: %d\n\n", len(result.Commits)))

	if len(result.Commits) == 0 {
		response.WriteString("No commits found between versions\n")
		return response.String()
	}

	for _, c := range result.Commits {
		response.WriteString(fmt.Sprintf("`%s`  %s  %s\n", c.ShortHash, c.Date, c.Title))
	}

	return response.String()
}

// formatPRAnalysisForSlack formats PR analysis results for Slack
func (s *SlackServer) formatPRAnalysisForSlack(result *models.PRAnalysisResult, userID string) string {
	var response strings.Builder

	if userID != "" {
		response.WriteString(fmt.Sprintf("Hi <@%s>, here's the analysis for pull request %s #%d\n\n", userID, result.PR.URL, result.PR.Number))
	} else {
		response.WriteString(fmt.Sprintf("PR Analysis for %s #%d\n\n", result.PR.URL, result.PR.Number))
	}

	response.WriteString(fmt.Sprintf("📋 *PR Analysis: #%d*\n", result.PR.Number))
	response.WriteString(fmt.Sprintf("🔗 %s\n", result.PR.URL))
	response.WriteString(fmt.Sprintf("📝 %s\n", result.PR.Title))
	response.WriteString(fmt.Sprintf("🔨 Merged to `%s` at %s\n\n", result.PR.MergedInto, models.FormatDate(result.PR.MergedAt)))

	allBranchesMap := make(map[string]models.BranchPresence)
	for _, branch := range result.ReleaseBranches {
		if branch.Found {
			allBranchesMap[branch.BranchName] = branch
		}
	}

	if len(allBranchesMap) == 0 {
		response.WriteString("❌ No release branches found containing this PR\n")
	} else {
		s.writeSlackBranchList(&response, allBranchesMap)
	}

	return response.String()
}

// formatEnhancedPRAnalysisForSlack formats PR analysis results with related PRs for Slack,
// combining all branches from the main PR and backports into one unified view (matching CLI output).
func (s *SlackServer) formatEnhancedPRAnalysisForSlack(result *models.PRAnalysisResult, unmergedPRs []models.UnmergedPR, userID string) string {
	var response strings.Builder

	if userID != "" {
		response.WriteString(fmt.Sprintf("Hi <@%s>, here's the analysis for pull request %s #%d\n\n", userID, result.PR.URL, result.PR.Number))
	} else {
		response.WriteString(fmt.Sprintf("PR Analysis for %s #%d\n\n", result.PR.URL, result.PR.Number))
	}

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

		backportCount := 0
		for _, rp := range result.RelatedPRs {
			if rp.Number != result.PR.Number {
				backportCount++
			}
		}
		if backportCount > 0 {
			response.WriteString(fmt.Sprintf("📊 Found %d related backport PRs:\n", backportCount))
			for _, rp := range result.RelatedPRs {
				if rp.Number != result.PR.Number {
					response.WriteString(fmt.Sprintf("  • PR #%d: %s\n", rp.Number, rp.Title))
				}
			}
		}
		if len(unmergedPRs) > 0 {
			response.WriteString(fmt.Sprintf("🔄 %d PRs in review:\n", len(unmergedPRs)))
			for _, up := range unmergedPRs {
				response.WriteString(fmt.Sprintf("  • PR #%d: %s\n", up.Number, up.Title))
			}
		}
		response.WriteString("\n")
	}

	// Combine all branches from main PR and related PRs (same as CLI)
	allBranchesMap := make(map[string]models.BranchPresence)
	for _, branch := range result.ReleaseBranches {
		if branch.Found {
			allBranchesMap[branch.BranchName] = branch
		}
	}
	for _, relatedPR := range result.RelatedPRs {
		if relatedPR.Number != result.PR.Number {
			for _, branch := range relatedPR.ReleaseBranches {
				if branch.Found {
					if _, exists := allBranchesMap[branch.BranchName]; !exists {
						allBranchesMap[branch.BranchName] = branch
					}
				}
			}
		}
	}

	if len(allBranchesMap) == 0 {
		response.WriteString("❌ No release branches found\n")
	} else {
		s.writeSlackBranchList(&response, allBranchesMap)
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

// formatJiraAnalysisForSlack formats JIRA analysis results for Slack with combined branch view.
func (s *SlackServer) formatJiraAnalysisForSlack(jiraAnalysis *models.JiraAnalysis, relatedPRs []models.RelatedPR, unmergedPRs []models.UnmergedPR, userID string) string {
	var response strings.Builder

	if userID != "" {
		response.WriteString(fmt.Sprintf("Hi <@%s>, here's the analysis for JIRA ticket %s\n\n", userID, jiraAnalysis.MainTicket))
	} else {
		response.WriteString(fmt.Sprintf("JIRA Ticket Analysis for %s\n\n", jiraAnalysis.MainTicket))
	}

	response.WriteString(fmt.Sprintf("🎫 *JIRA Ticket Analysis: %s*\n", jiraAnalysis.MainTicket))

	if len(jiraAnalysis.AllTickets) > 1 {
		response.WriteString(fmt.Sprintf("🔗 Related tickets: %s\n", strings.Join(jiraAnalysis.AllTickets[1:], ", ")))
	}

	totalPRs := len(relatedPRs) + len(unmergedPRs)
	if totalPRs == 0 {
		response.WriteString("\n❌ No related PRs found in supported repositories\n")
		response.WriteString("💡 Supported repos: assisted-service, assisted-installer, assisted-installer-agent, assisted-installer-ui\n")
		return response.String()
	}

	// List all PRs
	response.WriteString(fmt.Sprintf("📊 Found %d related PRs:\n", totalPRs))
	for _, rp := range relatedPRs {
		response.WriteString(fmt.Sprintf("  • PR #%d: %s\n", rp.Number, rp.Title))
	}
	for _, up := range unmergedPRs {
		response.WriteString(fmt.Sprintf("  • PR #%d: %s _(in review)_\n", up.Number, up.Title))
	}
	response.WriteString("\n")

	// Combine all branches from all PRs into one unified view
	allBranchesMap := make(map[string]models.BranchPresence)
	for _, rp := range relatedPRs {
		for _, branch := range rp.ReleaseBranches {
			if branch.Found {
				if existing, exists := allBranchesMap[branch.BranchName]; !exists || len(branch.UpcomingGAs) > len(existing.UpcomingGAs) {
					allBranchesMap[branch.BranchName] = branch
				}
			}
		}
	}

	if len(allBranchesMap) == 0 {
		response.WriteString("❌ No release branches found across all analyzed PRs\n")
	} else {
		s.writeSlackBranchList(&response, allBranchesMap)
	}

	return response.String()
}

// analyzePRAsync analyzes a PR asynchronously and sends result via response_url
func (s *SlackServer) analyzePRAsync(prURL, responseURL, userID string) {
	// Perform the analysis
	result, err := s.analyzePR(prURL, userID)

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
func (s *SlackServer) analyzeJiraTicketAsync(ticketURL, responseURL, userID string) {
	logger.Debug("=== ASYNC JIRA ANALYSIS STARTED: %s (response_url: %s) ===", ticketURL, responseURL)
	// Perform the analysis
	result, err := s.analyzeJiraTicket(ticketURL, userID)
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
	response, err := s.handleTextCommand(command, event.User)

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
	response, err := s.handleTextCommand(event.Text, event.User)

	if err != nil {
		response = fmt.Sprintf("❌ Error: %v", err)
	}

	// Post response in DM
	if err := s.botClient.PostSimpleMessage(ctx, event.Channel, response); err != nil {
		logger.Debug("Failed to post DM response: %v", err)
	}
}

// handleTextCommand handles text-based commands (from mentions or DMs)
func (s *SlackServer) handleTextCommand(text, userID string) (string, error) {
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
		return s.analyzePR(commandText, userID)

	case "jt", "jira":
		if commandText == "" {
			return "❌ Usage: `jt <JIRA_TICKET>`", nil
		}
		return s.analyzeJiraTicket(commandText, userID)

	case "version", "v":
		if commandText == "" {
			return "❌ Usage: `version <COMPONENT> <VERSION>` or `version mce <COMPONENT> <VERSION>`", nil
		}
		return s.handleVersionCommand(commandText)

	default:
		return fmt.Sprintf("❌ Unknown command: %s\n\nUse `info` or `help` to see available commands.", command), nil
	}
}

// writeSlackBranchList writes a combined branch list to the response, grouped by pattern and sorted.
func (s *SlackServer) writeSlackBranchList(response *strings.Builder, allBranchesMap map[string]models.BranchPresence) {
	// Group by pattern
	branchGroups := make(map[string][]models.BranchPresence)
	for _, branch := range allBranchesMap {
		branchGroups[branch.Pattern] = append(branchGroups[branch.Pattern], branch)
	}

	// Sort within each group
	patternOrder := []string{"release-ocm-", "releases/v", "release-", "release-v", "v"}
	for _, branches := range branchGroups {
		sort.Slice(branches, func(i, j int) bool {
			return models.ParseVersionNumber(branches[i].Version) < models.ParseVersionNumber(branches[j].Version)
		})
	}

	totalBranches := len(allBranchesMap)
	response.WriteString(fmt.Sprintf("✅ *Found in %d release branches:*\n", totalBranches))

	for _, pattern := range patternOrder {
		branches := branchGroups[pattern]
		if len(branches) == 0 {
			continue
		}
		response.WriteString(fmt.Sprintf("📂 *%s branches (%d):*\n", models.PatternDisplayName(pattern), len(branches)))
		for _, branch := range branches {
			response.WriteString(fmt.Sprintf("  • `%s` (v%s)", branch.BranchName, branch.Version))
			if branch.MergedAt != nil {
				response.WriteString(fmt.Sprintf(" - merged %s", models.FormatDate(branch.MergedAt)))
			}
			if pattern == "release-ocm-" {
				s.addGAInfoToSlackResponse(response, branch)
			}
			if len(branch.ReleasedVersions) > 0 {
				releasedVersionsText := strings.Join(branch.ReleasedVersions, ", ")
				if pattern == "v" && s.analyzer != nil && s.analyzer.GetGitLabClient() != nil {
					releasedVersionsText += s.getSaaSVersionBadge(branch.ReleasedVersions[0])
				}
				response.WriteString(fmt.Sprintf("\n    📦 Released in: %s", releasedVersionsText))
			}
			response.WriteString("\n")
		}
		response.WriteString("\n")
	}
}

func sheetsUnavailableSlackMessage() string {
	return fmt.Sprintf("⚠️ Release schedule data is currently unavailable (Google Sheets API error).\n📊 View the release schedule directly: <%s|Release Schedule>", ga.ReleaseScheduleURL)
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
