// Package slack provides Slack API integration for the merged-pr-bot application.
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shay23bra/pr-bot/internal/logger"
)

// Client represents a Slack API client.
type Client struct {
	xoxdToken  string
	xoxcToken  string
	httpClient *http.Client
}

// Message represents a Slack message.
type Message struct {
	Type      string    `json:"type"`
	User      string    `json:"user,omitempty"`
	Text      string    `json:"text"`
	Timestamp string    `json:"ts"`
	Time      time.Time `json:"-"`
}

// ChannelInfo represents information about a Slack channel.
type ChannelInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ConversationHistoryResponse represents the response from conversations.history API.
type ConversationHistoryResponse struct {
	OK       bool      `json:"ok"`
	Messages []Message `json:"messages"`
	HasMore  bool      `json:"has_more"`
	Latest   string    `json:"latest"`
	Oldest   string    `json:"oldest"`
}

// SearchResult represents a search result for PR-related messages.
type SearchResult struct {
	Message   Message   `json:"message"`
	Channel   string    `json:"channel"`
	PRNumber  int       `json:"pr_number"`
	Timestamp time.Time `json:"timestamp"`
}

// VersionMessage represents a message containing version information and Upstream SHA list.
type VersionMessage struct {
	Message         Message   `json:"message"`
	Channel         string    `json:"channel"`
	Version         string    `json:"version"`
	UpstreamSHALink string    `json:"upstream_sha_link"`
	Timestamp       time.Time `json:"timestamp"`
}

// New creates a new Slack client.
func New(xoxdToken, xoxcToken string) *Client {
	return &Client{
		xoxdToken:  xoxdToken,
		xoxcToken:  xoxcToken,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// TestAuth tests the authentication and returns available scopes
func (c *Client) TestAuth(ctx context.Context) error {
	// Test xoxc token first
	req, err := http.NewRequestWithContext(ctx, "GET", "https://slack.com/api/auth.test", nil)
	if err != nil {
		return fmt.Errorf("failed to create auth test request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.xoxcToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to make auth test request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error,omitempty"`
		User   string `json:"user,omitempty"`
		Team   string `json:"team,omitempty"`
		URL    string `json:"url,omitempty"`
		TeamID string `json:"team_id,omitempty"`
		UserID string `json:"user_id,omitempty"`
		BotID  string `json:"bot_id,omitempty"`
		IsBot  bool   `json:"is_bot,omitempty"`
		Scope  string `json:"scope,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode auth test response: %w", err)
	}

	if !result.OK {
		return fmt.Errorf("xoxc token auth failed: %s", result.Error)
	}

	logger.Debug("xoxc token auth successful - User: %s, Team: %s, Scopes: %s", result.User, result.Team, result.Scope)

	// Test xoxd token
	req, err = http.NewRequestWithContext(ctx, "GET", "https://slack.com/api/auth.test", nil)
	if err != nil {
		return fmt.Errorf("failed to create auth test request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.xoxdToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err = c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to make auth test request: %w", err)
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode auth test response: %w", err)
	}

	if !result.OK {
		return fmt.Errorf("xoxd token auth failed: %s", result.Error)
	}

	logger.Debug("xoxd token auth successful - User: %s, Team: %s, Scopes: %s", result.User, result.Team, result.Scope)

	return nil
}

// GetChannelID retrieves the channel ID for a given channel name.
func (c *Client) GetChannelID(ctx context.Context, channelName string) (string, error) {
	// Use xoxc token (browser token) for users.conversations
	req, err := http.NewRequestWithContext(ctx, "GET", "https://slack.com/api/users.conversations", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.xoxcToken)
	req.Header.Set("Content-Type", "application/json")

	q := req.URL.Query()
	q.Add("types", "public_channel,private_channel")
	q.Add("limit", "1000")
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		OK       bool `json:"ok"`
		Channels []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"channels"`
		Error   string `json:"error,omitempty"`
		Warning string `json:"warning,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if !result.OK {
		if result.Error == "invalid_auth" {
			return "", fmt.Errorf("slack API error: %s - check if xoxc token (browser token) is valid", result.Error)
		}
		return "", fmt.Errorf("slack API error: %s", result.Error)
	}

	if result.Warning != "" {
		logger.Debug("Slack API warning: %s", result.Warning)
	}

	logger.Debug("Found %d channels in workspace", len(result.Channels))

	for _, channel := range result.Channels {
		if channel.Name == channelName {
			logger.Debug("Found channel '%s' with ID: %s", channelName, channel.ID)
			return channel.ID, nil
		}
	}

	// List available channels for debugging
	var channelNames []string
	for _, channel := range result.Channels {
		channelNames = append(channelNames, channel.Name)
	}
	logger.Debug("Available channels: %v", channelNames)

	return "", fmt.Errorf("channel '%s' not found. Available channels: %v", channelName, channelNames)
}

// GetChannelMessages retrieves messages from a channel.
func (c *Client) GetChannelMessages(ctx context.Context, channelID string, limit int) ([]Message, error) {
	// Use xoxd token (browser token) for conversations.history
	req, err := http.NewRequestWithContext(ctx, "GET", "https://slack.com/api/conversations.history", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.xoxdToken)
	req.Header.Set("Content-Type", "application/json")

	q := req.URL.Query()
	q.Add("channel", channelID)
	q.Add("limit", strconv.Itoa(limit))
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	var result ConversationHistoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if !result.OK {
		return nil, fmt.Errorf("slack API error - check if xoxd token (browser token) is valid")
	}

	// Parse timestamps
	for i := range result.Messages {
		if ts, err := strconv.ParseFloat(result.Messages[i].Timestamp, 64); err == nil {
			result.Messages[i].Time = time.Unix(int64(ts), 0)
		}
	}

	return result.Messages, nil
}

// SearchPRMessages searches for PR-related messages in the given messages.
func (c *Client) SearchPRMessages(messages []Message, channelName string) []SearchResult {
	var results []SearchResult

	for _, msg := range messages {
		// Look for PR numbers in the message text
		prNumbers := extractPRNumbers(msg.Text)
		for _, prNum := range prNumbers {
			results = append(results, SearchResult{
				Message:   msg,
				Channel:   channelName,
				PRNumber:  prNum,
				Timestamp: msg.Time,
			})
		}
	}

	return results
}

// FindLatestVersionMessage finds the latest message containing a specific version and Upstream SHA list link.
func (c *Client) FindLatestVersionMessage(messages []Message, channelName, targetVersion string) *VersionMessage {
	var latestMessage *VersionMessage

	for _, msg := range messages {
		// Check if message contains the target version
		if !strings.Contains(msg.Text, targetVersion) {
			continue
		}

		// Look for Upstream SHA list link
		upstreamLink := extractUpstreamSHALink(msg.Text)
		if upstreamLink == "" {
			continue
		}

		// If this is the first match or it's newer than the current latest
		if latestMessage == nil || msg.Time.After(latestMessage.Timestamp) {
			latestMessage = &VersionMessage{
				Message:         msg,
				Channel:         channelName,
				Version:         targetVersion,
				UpstreamSHALink: upstreamLink,
				Timestamp:       msg.Time,
			}
		}
	}

	return latestMessage
}

// extractUpstreamSHALink extracts Upstream SHA list links from text.
func extractUpstreamSHALink(text string) string {
	// Look for various patterns of Upstream SHA list links
	patterns := []string{
		`Upstream SHA list[:\s]*<([^>]+)>`,         // "Upstream SHA list: <link>"
		`Upstream SHA list[:\s]*(https?://[^\s]+)`, // "Upstream SHA list: http://..."
		`<([^>]*upstream[^>]*)>`,                   // Any link containing "upstream"
		`(https?://[^\s]*upstream[^\s]*)`,          // Any URL containing "upstream"
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(text)
		if len(matches) > 1 {
			// Return the first non-empty match
			for _, match := range matches[1:] {
				if match != "" {
					return match
				}
			}
		}
	}

	return ""
}

// extractPRNumbers extracts PR numbers from text.
func extractPRNumbers(text string) []int {
	var numbers []int
	words := strings.Fields(text)

	for _, word := range words {
		// Look for patterns like #1234, PR#1234, pull request #1234, etc.
		if strings.Contains(word, "#") {
			parts := strings.Split(word, "#")
			if len(parts) > 1 {
				if num, err := strconv.Atoi(parts[1]); err == nil && num > 0 {
					numbers = append(numbers, num)
				}
			}
		}
	}

	return numbers
}
