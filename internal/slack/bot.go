// Package slack provides Slack Bot API integration for the pr-bot application.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shay23bra/pr-bot/internal/logger"
)

// BotClient represents a Slack Bot API client using OAuth tokens.
type BotClient struct {
	botToken   string
	httpClient *http.Client
}

// SlackEvent represents a Slack event from the Events API.
type SlackEvent struct {
	Token       string   `json:"token"`
	TeamID      string   `json:"team_id"`
	APIAppID    string   `json:"api_app_id"`
	Event       Event    `json:"event"`
	Type        string   `json:"type"`
	EventID     string   `json:"event_id"`
	EventTime   int64    `json:"event_time"`
	AuthedUsers []string `json:"authed_users"`
}

// Event represents the inner event data.
type Event struct {
	Type           string `json:"type"`
	User           string `json:"user"`
	Text           string `json:"text"`
	Timestamp      string `json:"ts"`
	Channel        string `json:"channel"`
	EventTimestamp string `json:"event_ts"`
	ChannelType    string `json:"channel_type"`
	BotID          string `json:"bot_id,omitempty"`
	AppID          string `json:"app_id,omitempty"`
}

// SlackResponse represents a generic Slack API response.
type SlackResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// PostMessageRequest represents a request to post a message.
type PostMessageRequest struct {
	Channel     string       `json:"channel"`
	Text        string       `json:"text,omitempty"`
	Blocks      []Block      `json:"blocks,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	ThreadTS    string       `json:"thread_ts,omitempty"`
}

// Block represents a Slack Block Kit block.
type Block struct {
	Type string      `json:"type"`
	Text *TextObject `json:"text,omitempty"`
}

// TextObject represents a text object in Slack Block Kit.
type TextObject struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Attachment represents a Slack message attachment.
type Attachment struct {
	Color  string            `json:"color,omitempty"`
	Text   string            `json:"text,omitempty"`
	Fields []AttachmentField `json:"fields,omitempty"`
}

// AttachmentField represents a field in a Slack attachment.
type AttachmentField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

// NewBotClient creates a new Slack Bot API client.
func NewBotClient(botToken string) *BotClient {
	return &BotClient{
		botToken:   botToken,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// TestAuth tests the bot token authentication.
func (c *BotClient) TestAuth(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://slack.com/api/auth.test", nil)
	if err != nil {
		return fmt.Errorf("failed to create auth test request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.botToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to make auth test request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		SlackResponse
		User   string `json:"user,omitempty"`
		Team   string `json:"team,omitempty"`
		URL    string `json:"url,omitempty"`
		TeamID string `json:"team_id,omitempty"`
		UserID string `json:"user_id,omitempty"`
		BotID  string `json:"bot_id,omitempty"`
		IsBot  bool   `json:"is_bot,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode auth test response: %w", err)
	}

	if !result.OK {
		return fmt.Errorf("bot token auth failed: %s", result.Error)
	}

	logger.Debug("Bot token auth successful - User: %s, Team: %s, Bot ID: %s", result.User, result.Team, result.BotID)
	return nil
}

// PostMessage posts a message to a Slack channel.
func (c *BotClient) PostMessage(ctx context.Context, req *PostMessageRequest) error {
	jsonData, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://slack.com/api/chat.postMessage", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.botToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	var result SlackResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if !result.OK {
		return fmt.Errorf("slack API error: %s", result.Error)
	}

	return nil
}

// PostSimpleMessage posts a simple text message to a channel.
func (c *BotClient) PostSimpleMessage(ctx context.Context, channel, text string) error {
	return c.PostMessage(ctx, &PostMessageRequest{
		Channel: channel,
		Text:    text,
	})
}

// PostThreadReply posts a message as a thread reply.
func (c *BotClient) PostThreadReply(ctx context.Context, channel, text, threadTS string) error {
	return c.PostMessage(ctx, &PostMessageRequest{
		Channel:  channel,
		Text:     text,
		ThreadTS: threadTS,
	})
}

// IsDirectMessage checks if the event is a direct message to the bot.
func (e *Event) IsDirectMessage() bool {
	return e.ChannelType == "im"
}

// IsMention checks if the event mentions the bot.
func (e *Event) IsMention(botUserID string) bool {
	return strings.Contains(e.Text, fmt.Sprintf("<@%s>", botUserID))
}

// ExtractCommand extracts a command from the message text, removing bot mentions.
func (e *Event) ExtractCommand(botUserID string) string {
	text := e.Text

	// Remove bot mention
	mentionPattern := fmt.Sprintf("<@%s>", botUserID)
	text = strings.ReplaceAll(text, mentionPattern, "")

	// Trim whitespace
	text = strings.TrimSpace(text)

	return text
}

// FormatPRAnalysisMessage formats PR analysis results for Slack using Block Kit.
func FormatPRAnalysisMessage(prNumber int, prURL, title, mergedInto string, mergedAt time.Time, branches []BranchInfo) *PostMessageRequest {
	// Create header block
	headerText := fmt.Sprintf("📋 *PR Analysis: #%d*\n🔗 <%s|%s>\n🔨 Merged to `%s` at %s",
		prNumber, prURL, title, mergedInto, mergedAt.Format("2006-01-02 15:04"))

	blocks := []Block{
		{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: headerText,
			},
		},
	}

	// Add divider
	blocks = append(blocks, Block{Type: "divider"})

	if len(branches) == 0 {
		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: "❌ No release branches found containing this PR",
			},
		})
	} else {
		// Group branches by pattern
		branchGroups := make(map[string][]BranchInfo)
		for _, branch := range branches {
			branchGroups[branch.Pattern] = append(branchGroups[branch.Pattern], branch)
		}

		branchText := "✅ *Found in release branches:*\n"
		for pattern, patternBranches := range branchGroups {
			branchText += fmt.Sprintf("📂 *%s branches:*\n", getPatternDisplayName(pattern))
			for _, branch := range patternBranches {
				branchText += fmt.Sprintf("  • `%s` (v%s)", branch.Name, branch.Version)
				if !branch.MergedAt.IsZero() {
					branchText += fmt.Sprintf(" - merged %s", branch.MergedAt.Format("2006-01-02"))
				}
				branchText += "\n"
			}
			branchText += "\n"
		}

		blocks = append(blocks, Block{
			Type: "section",
			Text: &TextObject{
				Type: "mrkdwn",
				Text: branchText,
			},
		})
	}

	return &PostMessageRequest{
		Blocks: blocks,
	}
}

// BranchInfo represents information about a branch containing the PR.
type BranchInfo struct {
	Name     string
	Version  string
	Pattern  string
	MergedAt time.Time
}

// getPatternDisplayName returns a user-friendly name for branch patterns.
func getPatternDisplayName(pattern string) string {
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
