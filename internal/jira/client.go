package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/shay23bra/pr-bot/internal/logger"
)

// Client represents a Jira API client.
type Client struct {
	baseURL    string
	httpClient *http.Client
	token      string
	ctx        context.Context
}

// JiraIssue represents a Jira issue/ticket.
type JiraIssue struct {
	Key    string     `json:"key"`
	Fields JiraFields `json:"fields"`
}

// JiraFields represents the fields of a Jira issue.
type JiraFields struct {
	Summary     string       `json:"summary"`
	Description string       `json:"description"`
	IssueLinks  []IssueLink  `json:"issuelinks"`
	RemoteLinks []RemoteLink `json:"remotelinks"`
}

// IssueLink represents a link between Jira issues.
type IssueLink struct {
	Type         LinkType     `json:"type"`
	OutwardIssue *LinkedIssue `json:"outwardIssue,omitempty"`
	InwardIssue  *LinkedIssue `json:"inwardIssue,omitempty"`
}

// LinkType represents the type of link between issues.
type LinkType struct {
	Name    string `json:"name"`
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

// LinkedIssue represents a linked Jira issue.
type LinkedIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
	} `json:"fields"`
}

// RemoteLink represents a remote link (web link) in a Jira issue.
type RemoteLink struct {
	ID     int    `json:"id"`
	Self   string `json:"self"`
	Object struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	} `json:"object"`
}

// JiraSearchResponse represents the response from Jira search API.
type JiraSearchResponse struct {
	Issues []JiraIssue `json:"issues"`
	Total  int         `json:"total"`
}

// NewClient creates a new Jira client.
func NewClient(ctx context.Context, token string) *Client {
	if token == "" {
		return nil
	}

	return &Client{
		baseURL: "https://issues.redhat.com",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		token: token,
		ctx:   ctx,
	}
}

// GetIssue retrieves a Jira issue by key.
func (c *Client) GetIssue(issueKey string) (*JiraIssue, error) {
	logger.Debug("Getting Jira issue: %s", issueKey)

	url := fmt.Sprintf("%s/rest/api/2/issue/%s?expand=names&fields=summary,description,issuelinks,remotelinks", c.baseURL, issueKey)

	req, err := http.NewRequestWithContext(c.ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("issue %s not found", issueKey)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get issue %s, status: %d, body: %s", issueKey, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var issue JiraIssue
	if err := json.Unmarshal(body, &issue); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	logger.Debug("Found issue: %s - %s", issue.Key, issue.Fields.Summary)

	// Get remote links separately as they require a different API endpoint
	remoteLinks, err := c.getRemoteLinks(issueKey)
	if err != nil {
		logger.Debug("Warning: failed to get remote links for %s: %v", issueKey, err)
	} else {
		issue.Fields.RemoteLinks = remoteLinks
		logger.Debug("Found %d remote links for issue %s", len(remoteLinks), issueKey)
	}

	return &issue, nil
}

// getRemoteLinks retrieves remote links for a JIRA issue.
func (c *Client) getRemoteLinks(issueKey string) ([]RemoteLink, error) {
	url := fmt.Sprintf("%s/rest/api/2/issue/%s/remotelink", c.baseURL, issueKey)

	req, err := http.NewRequestWithContext(c.ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get remote links for %s, status: %d, body: %s", issueKey, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var remoteLinks []RemoteLink
	if err := json.Unmarshal(body, &remoteLinks); err != nil {
		return nil, fmt.Errorf("failed to unmarshal remote links response: %w", err)
	}

	return remoteLinks, nil
}

// GetAllClonedIssues finds all cloned issues related to the given issue.
func (c *Client) GetAllClonedIssues(issueKey string) ([]JiraIssue, error) {
	logger.Debug("Getting cloned issues for: %s", issueKey)

	var allIssues []JiraIssue
	visited := make(map[string]bool)
	toProcess := []string{issueKey}

	for len(toProcess) > 0 {
		currentKey := toProcess[0]
		toProcess = toProcess[1:]

		if visited[currentKey] {
			continue
		}
		visited[currentKey] = true

		issue, err := c.GetIssue(currentKey)
		if err != nil {
			logger.Debug("Failed to get issue %s: %v", currentKey, err)
			continue
		}

		allIssues = append(allIssues, *issue)

		// Look for cloned issues in links
		for _, link := range issue.Fields.IssueLinks {
			if strings.Contains(strings.ToLower(link.Type.Name), "clone") {
				if link.OutwardIssue != nil && !visited[link.OutwardIssue.Key] {
					toProcess = append(toProcess, link.OutwardIssue.Key)
				}
				if link.InwardIssue != nil && !visited[link.InwardIssue.Key] {
					toProcess = append(toProcess, link.InwardIssue.Key)
				}
			}
		}
	}

	logger.Debug("Found %d total issues (including original and clones)", len(allIssues))
	return allIssues, nil
}

// ExtractGitHubPRsFromIssue extracts GitHub PR URLs from a Jira issue.
func (c *Client) ExtractGitHubPRsFromIssue(issue JiraIssue) []string {
	var prURLs []string

	// GitHub PR URL pattern
	prPattern := regexp.MustCompile(`https://github\.com/[^/]+/[^/]+/pull/\d+`)

	// Check summary
	matches := prPattern.FindAllString(issue.Fields.Summary, -1)
	prURLs = append(prURLs, matches...)

	// Check description
	matches = prPattern.FindAllString(issue.Fields.Description, -1)
	prURLs = append(prURLs, matches...)

	// Check remote links - these are where "links to" URLs are typically stored
	for _, remoteLink := range issue.Fields.RemoteLinks {
		matches := prPattern.FindAllString(remoteLink.Object.URL, -1)
		prURLs = append(prURLs, matches...)
		logger.Debug("Checked remote link: %s (title: %s)", remoteLink.Object.URL, remoteLink.Object.Title)
	}

	// Remove duplicates
	seen := make(map[string]bool)
	var uniquePRs []string
	for _, pr := range prURLs {
		if !seen[pr] {
			seen[pr] = true
			uniquePRs = append(uniquePRs, pr)
		}
	}

	logger.Debug("Found %d GitHub PRs in issue %s (checked summary, description, and %d remote links)", len(uniquePRs), issue.Key, len(issue.Fields.RemoteLinks))
	return uniquePRs
}

// ExtractMGMTTicketFromTitle extracts MGMT ticket number from PR title.
func ExtractMGMTTicketFromTitle(title string) string {
	re := regexp.MustCompile(`MGMT-(\d+)`)
	matches := re.FindStringSubmatch(title)
	if len(matches) >= 2 {
		return "MGMT-" + matches[1]
	}
	return ""
}
