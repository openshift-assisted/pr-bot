// Package models defines the data structures used throughout the merged-pr-bot application.
package models

import (
	"strconv"
	"strings"
	"time"
)

// DateFormat is the standard date format used throughout the application.
const DateFormat = "01-02-2006"

// FormatDate formats a time pointer to mm-dd-yyyy format.
// Returns empty string for nil dates.
func FormatDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(DateFormat)
}

// FormatDateWithNil formats a time pointer to mm-dd-yyyy format.
// Returns "<nil>" for nil dates (useful for logging).
func FormatDateWithNil(t *time.Time) string {
	if t == nil {
		return "<nil>"
	}
	return t.Format(DateFormat)
}

// PRInfo represents information about a pull request.
type PRInfo struct {
	Number     int        `json:"number"`
	Title      string     `json:"title"`
	Hash       string     `json:"hash"`
	MergedAt   *time.Time `json:"merged_at,omitempty"`
	MergedInto string     `json:"merged_into"`
	URL        string     `json:"url"`
}

// BranchPresence represents PR presence in a release branch.
type BranchPresence struct {
	BranchName       string       `json:"branch_name"`
	Pattern          string       `json:"pattern"` // "release-ocm-", "release-", "release-v", or "v"
	Version          string       `json:"version"`
	MergedAt         *time.Time   `json:"merged_at,omitempty"`
	Found            bool         `json:"found"`
	ReleasedVersions []string     `json:"released_versions,omitempty"` // Exact release versions (e.g., v2.40.1, v2.40.2)
	GAStatus         GAStatus     `json:"ga_status"`
	UpcomingGAs      []UpcomingGA `json:"upcoming_gas,omitempty"`
}

// GAStatus represents GA status for both ACM and MCE.
type GAStatus struct {
	ACM     GAInfo `json:"acm"`
	MCE     GAInfo `json:"mce"`
	NextACM GAInfo `json:"next_acm"`
	NextMCE GAInfo `json:"next_mce"`
}

// GAInfo represents GA information for a specific product.
type GAInfo struct {
	Version  string     `json:"version"`
	GADate   *time.Time `json:"ga_date,omitempty"`
	IsGA     bool       `json:"is_ga"`
	IsInNext bool       `json:"is_in_next"`
	Status   string     `json:"status"` // "GA", "Next Version", "Not Found", "Merged but not GA"
}

// UpcomingGA represents upcoming GA versions after a merge date.
type UpcomingGA struct {
	Product       string                 `json:"product"` // "ACM" or "MCE"
	Version       string                 `json:"version"`
	GADate        *time.Time             `json:"ga_date"`
	MCEValidation *MCESnapshotValidation `json:"mce_validation,omitempty"` // MCE snapshot validation result
}

// MCESnapshotValidation represents the result of MCE snapshot validation.
type MCESnapshotValidation struct {
	Product            string     `json:"product"`              // "ACM" or "MCE"
	Version            string     `json:"version"`              // e.g., "2.8.1"
	GADate             *time.Time `json:"ga_date"`              // GA date
	MCEBranch          string     `json:"mce_branch"`           // e.g., "mce-2.8"
	SnapshotFolder     string     `json:"snapshot_folder"`      // e.g., "2025-03-14-18-55-26"
	ValidationSuccess  bool       `json:"validation_success"`   // Whether validation passed
	ComponentName      string     `json:"component_name"`       // e.g., "assisted-service", "assisted-installer", "assisted-installer-agent", "assisted-installer-ui"
	AssistedServiceSHA string     `json:"assisted_service_sha"` // SHA from down-sha.yaml
	PRCommitBeforeSHA  bool       `json:"pr_commit_before_sha"` // Whether PR commit is before the SHA
	ErrorMessage       string     `json:"error_message"`        // Error details if validation failed
}

// PRAnalysisResult represents the complete analysis result.
type PRAnalysisResult struct {
	PR                   PRInfo           `json:"pr"`
	ReleaseBranches      []BranchPresence `json:"release_branches"`
	AnalyzedAt           time.Time        `json:"analyzed_at"`
	JiraAnalysis         *JiraAnalysis    `json:"jira_analysis,omitempty"`
	RelatedPRs           []RelatedPR      `json:"related_prs,omitempty"`
	SheetsUnavailable    bool             `json:"sheets_unavailable,omitempty"`
}

// JiraAnalysis represents the JIRA ticket analysis result.
type JiraAnalysis struct {
	MainTicket      string   `json:"main_ticket"`      // The main MGMT ticket (e.g., "MGMT-20662")
	AllTickets      []string `json:"all_tickets"`      // All related tickets including clones
	RelatedPRURLs   []string `json:"related_pr_urls"`  // All PR URLs found in tickets
	AnalysisSuccess bool     `json:"analysis_success"` // Whether analysis completed
	ErrorMessage    string   `json:"error_message"`    // Error details if analysis failed
}

// RelatedPR represents a merged PR found through JIRA ticket analysis.
type RelatedPR struct {
	Number          int              `json:"number"`
	Title           string           `json:"title"`
	URL             string           `json:"url"`
	Hash            string           `json:"hash"`             // Commit hash
	JiraTickets     []string         `json:"jira_tickets"`     // JIRA tickets associated with this PR
	ReleaseBranches []BranchPresence `json:"release_branches"` // Branch analysis for this PR
}

// UnmergedPR represents an unmerged PR found through JIRA ticket analysis.
type UnmergedPR struct {
	Number int    `json:"number"` // PR number
	Title  string `json:"title"`  // PR title
	URL    string `json:"url"`    // PR URL
	Status string `json:"status"` // PR status (e.g., "In Review", "Draft", "Pending")
}

// Config represents the application configuration.
type Config struct {
	GitHubToken              string `json:"github_token"`
	Repository               string `json:"repository"`
	Owner                    string `json:"owner"`
	BranchPrefix             string `json:"branch_prefix"`
	DefaultBranch            string `json:"default_branch"`
	SlackBotToken            string `json:"slack_bot_token"`
	SlackSigningSecret       string `json:"slack_signing_secret"`
	GitLabToken              string `json:"gitlab_token"`
	JiraToken                string `json:"jira_token"`
	JiraEmail                string `json:"jira_email"`
	GoogleSheetID            string `json:"google_sheet_id"`
	GoogleServiceAccountJSON string `json:"google_service_account_json"`
	RepoCacheDir             string `json:"repo_cache_dir"`
}

// PatternDescription returns a human-readable description for branch patterns.
func PatternDescription(pattern string) string {
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
		return "SaaS versions"
	default:
		return pattern
	}
}

// PatternDisplayName returns a user-friendly name for branch patterns (Slack formatting).
func PatternDisplayName(pattern string) string {
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

// ParseVersionNumber extracts and parses version number from version string for sorting.
func ParseVersionNumber(version string) float64 {
	if strings.Contains(version, "Next Version") {
		return 999.0
	}
	version = strings.TrimPrefix(version, "v")
	if parsed, err := strconv.ParseFloat(version, 64); err == nil {
		return parsed
	}
	return 0.0
}

// VersionComparisonResult holds the result of comparing two versions.
type VersionComparisonResult struct {
	Component       string
	Owner           string
	Repository      string
	TargetVersion   string
	PreviousVersion string
	Commits         []CommitInfo
}

// CommitInfo holds basic commit information for version comparison display.
type CommitInfo struct {
	ShortHash string
	Date      string
	Title     string
}

// CompareSemanticVersions compares two version strings numerically (e.g., "v2.9.0" vs "v2.40.0").
// Returns -1 if v1 < v2, 0 if equal, 1 if v1 > v2.
func CompareSemanticVersions(v1, v2 string) int {
	v1 = strings.TrimPrefix(v1, "v")
	v2 = strings.TrimPrefix(v2, "v")

	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := range maxLen {
		var n1, n2 int
		if i < len(parts1) {
			n1, _ = strconv.Atoi(parts1[i])
		}
		if i < len(parts2) {
			n2, _ = strconv.Atoi(parts2[i])
		}
		if n1 < n2 {
			return -1
		} else if n1 > n2 {
			return 1
		}
	}
	return 0
}
