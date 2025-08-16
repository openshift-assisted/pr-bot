// Package config provides configuration loading and management for the merged-pr-bot application.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/shay23bra/pr-bot/internal/models"
	"github.com/spf13/viper"
)

// Load loads configuration from environment variables and config files.
func Load() (*models.Config, error) {
	// Load .env file if it exists
	if err := godotenv.Load(); err != nil {
		// .env file not found is OK, we'll use system environment variables
	}

	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("./config")
	viper.AddConfigPath("$HOME/.merged-pr-bot")

	// Set environment variable prefix
	viper.SetEnvPrefix("PR_BOT")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// Set default values
	setDefaults()

	// Read config file if it exists
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		// Config file not found is OK, we'll use defaults and env vars
	}

	// Handle special case for GitLab token which uses different env var name
	gitlabToken := viper.GetString("gitlab_token")
	if gitlabToken == "" {
		gitlabToken = os.Getenv("PR_BOT_GITLAB_TOKEN")
	}

	// Handle special case for Jira token which uses different env var name
	jiraToken := viper.GetString("jira_token")
	if jiraToken == "" {
		jiraToken = os.Getenv("PR_BOT_JIRA_TOKEN")
	}

	config := &models.Config{
		GitHubToken:   viper.GetString("github.token"),
		Repository:    viper.GetString("github.repository"),
		Owner:         viper.GetString("github.owner"),
		BranchPrefix:  viper.GetString("github.branch_prefix"),
		DefaultBranch: viper.GetString("github.default_branch"),
		SlackXOXD:     viper.GetString("slack.xoxd"),
		SlackXOXC:     viper.GetString("slack.xoxc"),
		SlackChannel:  viper.GetString("slack.channel"),
		GitLabToken:   gitlabToken,
		JiraToken:     jiraToken,
	}

	// Validate required fields
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	return config, nil
}

// PrintConfig prints the current configuration (excluding sensitive data).
func PrintConfig(config *models.Config) {
	fmt.Printf("Configuration:\n")
	fmt.Printf("  Repository: %s/%s\n", config.Owner, config.Repository)
	fmt.Printf("  Branch Prefix: %s\n", config.BranchPrefix)
	fmt.Printf("  Default Branch: %s\n", config.DefaultBranch)
	if config.GitHubToken != "" {
		fmt.Printf("  GitHub Token: ********** (configured)\n")
	} else {
		fmt.Printf("  GitHub Token: (not configured)\n")
	}
	if config.SlackXOXD != "" {
		fmt.Printf("  Slack XOXD Token: ********** (configured)\n")
	} else {
		fmt.Printf("  Slack XOXD Token: (not configured)\n")
	}
	if config.SlackXOXC != "" {
		fmt.Printf("  Slack XOXC Token: ********** (configured)\n")
	} else {
		fmt.Printf("  Slack XOXC Token: (not configured)\n")
	}
	if config.SlackChannel != "" {
		fmt.Printf("  Slack Channel: %s\n", config.SlackChannel)
	} else {
		fmt.Printf("  Slack Channel: (not configured)\n")
	}
}

// setDefaults sets default configuration values.
func setDefaults() {
	viper.SetDefault("github.token", "")
	viper.SetDefault("github.repository", "assisted-service")
	viper.SetDefault("github.owner", "openshift")
	viper.SetDefault("github.branch_prefix", "release-ocm-")
	viper.SetDefault("github.default_branch", "master")
	viper.SetDefault("slack.xoxd", "")
	viper.SetDefault("slack.xoxc", "")
	viper.SetDefault("slack.channel", "team-acm-downstream-notifcation")
	viper.SetDefault("gitlab_token", "")
	viper.SetDefault("jira_token", "")
}

// validateConfig validates the configuration.
func validateConfig(config *models.Config) error {
	if config.Repository == "" {
		return fmt.Errorf("repository name is required")
	}

	if config.Owner == "" {
		return fmt.Errorf("repository owner is required")
	}

	if config.BranchPrefix == "" {
		return fmt.Errorf("branch prefix is required")
	}

	// GitHub token is optional for public repositories but recommended
	if config.GitHubToken == "" {
		fmt.Fprintf(os.Stderr, "Warning: No GitHub token provided. API rate limits will be lower.\n")
		fmt.Fprintf(os.Stderr, "Set PR_BOT_GITHUB_TOKEN environment variable for higher rate limits.\n")
	}

	return nil
}
