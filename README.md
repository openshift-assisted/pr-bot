# pr-bot

A Go-based tool to analyze merged pull requests and determine their presence across release branches. Available as both a **CLI tool** for individual use and a **Slack bot server** for team collaboration. This tool helps track the deployment status of changes across different release versions.

## 🌿 Branch Structure

- **`latest`** - Production branch (stable releases)
- **`master`** - Development branch (active development)

## 🚀 Quick Start

### CLI Mode
```bash
# Install latest release
go install github.com/shay23bra/pr-bot@v0.0.3

# Add Go bin to PATH (if needed)
export PATH=$PATH:~/go/bin

# Set up environment variables via export
export PR_BOT_GITHUB_TOKEN="your_github_token_here"
export PR_BOT_GITLAB_TOKEN="your_gitlab_token_here"
export PR_BOT_JIRA_TOKEN="your_jira_token_here"

# Analyze a PR
pr-bot -pr https://github.com/openshift/assisted-service/pull/7788

# Check version
pr-bot -version
```

### Server Mode (Slack Bot)
```bash
# Set up all required tokens in .env file
cat > .env << EOF
# Required tokens for all functionality
PR_BOT_GITHUB_TOKEN=your_github_token_here
PR_BOT_GITLAB_TOKEN=your_gitlab_token_here
PR_BOT_JIRA_TOKEN=your_jira_token_here

# Required for Slack bot server mode
PR_BOT_SLACK_CHANNEL=your-channel
PR_BOT_SLACK_XOXD=xoxd-token
PR_BOT_SLACK_XOXC=xoxc-token
EOF

# Start server
pr-bot -server

# Use in Slack
/prbot pr https://github.com/openshift/assisted-service/pull/7788
```

## Features

### 🔧 Dual Mode Architecture
- **CLI Tool**: Perfect for individual developers and CI/CD pipelines
- **Slack Bot Server**: Team-friendly slash commands for collaborative analysis

### 📊 Analysis Capabilities
- **PR Analysis**: Get detailed information about merged pull requests including commit hash and merge date
- **Release Branch Tracking**: Automatically discover and check all release branches matching patterns
- **Version Detection**: Extract version numbers from branch names and report which versions contain changes
- **JIRA Integration**: Analyze all PRs related to a JIRA ticket including backports
- **MCE Validation**: Verify commits against MCE GitLab snapshots with SHA extraction

### 📅 Release Management
- **GA Status Tracking**: Analyze General Availability status by reading ACM/MCE release schedules from Excel files
- **Release Schedule Integration**: Parse "In Progress" and "ACM MCE Completed" tabs to determine GA dates
- **Version Comparison**: Compare MCE/GitHub versions to track changes between releases

### 🚀 User Experience
- **Auto-Update Notifications**: Automatically checks for newer versions and prompts users to update
- **Multi-Repository Support**: Works with `assisted-service`, `assisted-installer`, `assisted-installer-agent`, and `assisted-installer-ui`
- **Flexible Configuration**: Support for environment variables, config files, and command-line options
- **High Performance**: Optimized with caching and parallel processing to minimize API calls

## Installation

### Option 1: Install via Go (Recommended)
```bash
# Install latest stable release (recommended)
go install github.com/shay23bra/pr-bot@v0.0.3

# Or install from production branch
go install github.com/shay23bra/pr-bot@latest

# Add Go bin directory to PATH (if not already done)
echo 'export PATH=$PATH:~/go/bin' >> ~/.bashrc
source ~/.bashrc

# Verify installation
pr-bot -version
```

### Option 2: Download Binary
```bash
# Download from GitHub releases (replace VERSION with actual version)
wget https://github.com/shay23bra/pr-bot/releases/latest/download/pr-bot-linux-amd64
chmod +x pr-bot-linux-amd64
mv pr-bot-linux-amd64 pr-bot

# Add to PATH
sudo mv pr-bot /usr/local/bin/
```

### Option 3: Build from Source
```bash
# Clone and build
git clone https://github.com/shay23bra/pr-bot.git
cd pr-bot
go build -o pr-bot .
```

### Prerequisites

- Go 1.21 or later (if building from source)
- **GitHub token (REQUIRED)** - For API access
- **GitLab token (REQUIRED)** - For MCE validation  
- **JIRA token (REQUIRED)** - For JIRA ticket analysis
- Excel file `data/ACM - Z Stream Release Schedule.xlsx` with release schedule data

### GitHub Token Setup

1. Go to [GitHub Settings > Personal Access Tokens](https://github.com/settings/tokens)
2. Click "Generate new token (classic)"
3. Give it a name like "pr-bot"
4. Select scopes: `public_repo` (for public repos) or `repo` (for private repos)
5. Copy the generated token
6. Set it as an environment variable:
   - **For CLI usage:**
     ```bash
     export PR_BOT_GITHUB_TOKEN="your_token_here"
     ```
   - **For Slack server usage (add to .env file):**
     ```bash
     echo "PR_BOT_GITHUB_TOKEN=your_token_here" >> .env
     ```

⚠️ **Important**: Without a GitHub token, you're limited to 60 requests per hour and will get rate limited quickly when analyzing multiple branches.

### Slack Token Setup (Optional)

For Slack integration, you'll need browser tokens (xoxc and xoxd):

1. **Get Browser Tokens**:
   - Open Slack in your browser
   - Open Developer Tools (F12)
   - Go to Network tab
   - Look for requests to `slack.com/api/`
   - Find the `Authorization` header in the requests
   - Copy the token values (they start with `xoxc-` and `xoxd-`)

2. **Set them in .env file** (only needed for server mode):
   ```bash
   echo "PR_BOT_SLACK_XOXD=xoxd-your-browser-token-here" >> .env
   echo "PR_BOT_SLACK_XOXC=xoxc-your-browser-token-here" >> .env
   echo "PR_BOT_SLACK_CHANNEL=team-acm-downstream-notifcation" >> .env
   ```

**Important Notes:**
- Browser tokens are session-based and may expire
- The xoxc token is used to list channels
- The xoxd token is used to read channel messages
- Both tokens must be from the same Slack session
- The channel name must match exactly (case-sensitive)

### JIRA Token Setup (Required for -jt flag)

For JIRA ticket analysis, you'll need a JIRA API token:

1. **Get JIRA API Token**:
   - Go to [Red Hat JIRA API Tokens](https://issues.redhat.com/secure/ViewProfile.jspa?selectedTab=com.atlassian.pats.pats-plugin:jira-user-personal-access-tokens)
   - Click "Create token"
   - Give it a name like "pr-bot"
   - Copy the generated token

2. **Set as environment variable**:
   - **For CLI usage:**
     ```bash
     export PR_BOT_JIRA_TOKEN="your-jira-token-here"
     ```
   - **For Slack server usage (add to .env file):**
     ```bash
     echo "PR_BOT_JIRA_TOKEN=your-jira-token-here" >> .env
     ```

**Important Notes:**
- Required only if you want to use the `-jt` flag for JIRA ticket analysis
- The token should have read access to JIRA issues
- Used to find cloned tickets and extract GitHub PR URLs from JIRA tickets

### GitLab Token Setup (Optional for MCE Validation)

For MCE snapshot validation (used in PR analysis and version comparison), you'll need a GitLab API token:

1. **Get GitLab API Token**:
   - Go to [GitLab Personal Access Tokens](https://gitlab.cee.redhat.com/-/user_settings/personal_access_tokens)
   - Click "Add new token"
   - Give it a name like "pr-bot"
   - Select scope: `read_api` (for reading repository files)
   - Set expiration date (optional)
   - Copy the generated token

2. **Set as environment variable**:
   - **For CLI usage:**
     ```bash
     export PR_BOT_GITLAB_TOKEN="your-gitlab-token-here"
     ```
   - **For Slack server usage (add to .env file):**
     ```bash
     echo "PR_BOT_GITLAB_TOKEN=your-gitlab-token-here" >> .env
     ```

**Important Notes:**
- Required for MCE snapshot validation (SHA extraction from down-sha.yaml)
- Used in PR analysis to validate against MCE snapshots  
- Enables version comparison features (`-v` flag)
- Token should have `read_api` scope to access MCE repository files

### Setup

```bash
git clone https://github.com/shay23bra/pr-bot.git
cd pr-bot
go mod tidy
```

### Environment Configuration

You can configure the application using a `.env` file in the project root:

```bash
# Copy the example file
cp env.example .env

# Edit the .env file with your tokens
nano .env
```

The `.env` file should contain:
```bash
# GitHub Configuration
PR_BOT_GITHUB_TOKEN=your-github-token-here
PR_BOT_GITHUB_OWNER=openshift
PR_BOT_GITHUB_REPOSITORY=assisted-service
PR_BOT_GITHUB_BRANCH_PREFIX=release-ocm-
PR_BOT_GITHUB_DEFAULT_BRANCH=master

# Slack Configuration
PR_BOT_SLACK_XOXD=xoxd-your-browser-token-here
PR_BOT_SLACK_XOXC=xoxc-your-browser-token-here
PR_BOT_SLACK_CHANNEL=team-acm-downstream-notifcation

# GitLab Configuration (for MCE snapshot validation)
PR_BOT_GITLAB_TOKEN=your-gitlab-token-here

# JIRA Configuration (for MGMT ticket analysis)
PR_BOT_JIRA_TOKEN=your-jira-token-here
```

### Optional: Build Binary

```bash
make build
```

## Configuration

The tool can be configured through environment variables, config files, or command-line flags.

### Environment Variables

```bash
export PR_BOT_GITHUB_TOKEN="your-github-token"
export PR_BOT_GITHUB_OWNER="openshift"
export PR_BOT_GITHUB_REPOSITORY="assisted-service"
export PR_BOT_GITHUB_BRANCH_PREFIX="release-ocm-"
export PR_BOT_GITHUB_DEFAULT_BRANCH="master"
export PR_BOT_SLACK_XOXD="xoxd-your-browser-token-here"
export PR_BOT_SLACK_XOXC="xoxc-your-browser-token-here"
export PR_BOT_SLACK_CHANNEL="team-acm-downstream-notifcation"
export PR_BOT_GITLAB_TOKEN="your-gitlab-token-here"
export PR_BOT_JIRA_TOKEN="your-jira-token-here"
```

### Config File

Create a `config.yaml` file:

```yaml
github:
  token: "your-github-token"
  owner: "openshift"
  repository: "assisted-service"
  branch_prefix: "release-ocm-"
  default_branch: "master"
slack:
  xoxd: "xoxd-your-browser-token-here"
  xoxc: "xoxc-your-browser-token-here"
  channel: "team-acm-downstream-notifcation"
gitlab:
  token: "your-gitlab-token-here"
jira:
  token: "your-jira-token-here"
```

### Default Configuration

- **Repository**: `openshift/assisted-service`
- **Branch Prefix**: `release-ocm-`
- **Default Branch**: `master`

## Usage

### 🖥️ CLI Mode

The CLI tool automatically checks for updates on startup and provides comprehensive PR analysis.

#### Basic Commands

```bash
# Show version and check for updates
pr-bot -version

# Show help
pr-bot

# Enable debug logging for any command
pr-bot -d -pr <PR_URL>
```

#### PR Analysis

Analyze merged PRs from any supported repository:

```bash
# Analyze a PR (auto-detects repository)
pr-bot -pr https://github.com/openshift/assisted-service/pull/1234
pr-bot -pr https://github.com/openshift/assisted-installer/pull/100
pr-bot -pr https://github.com/openshift/assisted-installer-agent/pull/200
pr-bot -pr https://github.com/openshift-assisted/assisted-installer-ui/pull/2991

# Just use the PR number if you're in the right repo context
pr-bot -pr 1234
```

#### JIRA Ticket Analysis

Analyze all PRs related to a JIRA ticket (finds backports automatically):

```bash
# Full URL
pr-bot -jt https://issues.redhat.com/browse/MGMT-20662

# Just ticket ID
pr-bot -jt MGMT-20662
```

#### Version Comparison

```bash
# Compare GitHub tag with previous version
pr-bot -v v2.40.1

# Compare MCE versions  
pr-bot -v mce 2.8.1
```

### 🤖 Server Mode (Slack Bot)

Start the server to enable Slack slash commands:

```bash
# Start server (default port 8080)
pr-bot -server
```

#### Slack Commands

Once the server is running, use these slash commands in Slack:

```bash
# Show help
/prbot help

# Analyze a PR
/prbot pr https://github.com/openshift/assisted-service/pull/1234

# Analyze JIRA ticket  
/prbot jira MGMT-20662

# Compare versions
/prbot version v2.40.1
/prbot version mce 2.8.1
```

#### Slack App Setup

1. **Create Slack App**: Go to [Slack Apps](https://api.slack.com/apps) and create a new app
2. **Add Slash Command**: 
   - Command: `/prbot`
   - Request URL: `https://your-server.com/slack/commands`
   - Description: "Analyze PRs and JIRA tickets"
3. **Install App**: Install to your workspace
4. **Configure Tokens**: Set environment variables with your app's tokens

### 📋 Supported Repositories

- `openshift/assisted-service`
- `openshift/assisted-installer` 
- `openshift/assisted-installer-agent`
- `openshift-assisted/assisted-installer-ui`

### 🔧 MCE Validation Components

- **assisted-service**: Direct SHA extraction from `down-sha.yaml`
- **assisted-installer**: Direct SHA extraction from `down-sha.yaml`
- **assisted-installer-agent**: Direct SHA extraction from `down-sha.yaml`  
- **assisted-installer-ui**: Multi-step version extraction via `stolostron/console` → `frontend/package.json` → `@openshift-assisted/ui-lib`

## Auto-Update Feature

The tool automatically checks for updates when running CLI commands:

```bash
# Example output when a new version is available
$ pr-bot -pr https://github.com/openshift/assisted-service/pull/1234

⚠️  A newer version is available: 0.1.0 (current: 0.0.1)
📦 Update with: go install github.com/shay23bra/pr-bot@latest
🔗 Or download from: https://github.com/shay23bra/pr-bot/releases/latest

# Then it continues with normal execution...
```

The update check:
- ✅ **Non-blocking**: Never fails your command if update check fails
- ✅ **Fast**: 5-second timeout, won't slow you down
- ✅ **Informative**: Shows current vs latest version and how to update
- ✅ **Automatic**: No configuration needed

## Troubleshooting

### "command not found: pr-bot"

This happens when `~/go/bin` is not in your PATH. The binary is installed correctly but your shell can't find it.

**Fix:**
```bash
# Temporary fix (for current session)
export PATH=$PATH:~/go/bin

# Permanent fix (add to your shell profile)
echo 'export PATH=$PATH:~/go/bin' >> ~/.bashrc
source ~/.bashrc

# Verify it works
pr-bot -version
```

### "version constraints conflict"

This was fixed in v0.0.3. If you see this error, clean your module cache:

```bash
# Clear module cache and reinstall
go clean -modcache
go install github.com/shay23bra/pr-bot@latest
```

### Rate Limit Issues

If you see `403 API rate limit exceeded`:

```bash
# Make sure you have a GitHub token set
export PR_BOT_GITHUB_TOKEN="your_github_token_here"

# Without a token, you're limited to 60 requests/hour
# With a token, you get 5000 requests/hour
```

## Team Deployment

### For CLI Distribution

1. **Create installation script** for your team:

```bash
#!/bin/bash
# install-pr-bot.sh

echo "🚀 Installing pr-bot..."

# Install latest version
go install github.com/shay23bra/pr-bot@latest

# Verify installation
pr-bot -version

echo "✅ Installation complete!"
echo "📋 Next steps:"
echo "1. Set up your tokens via export:"
echo "   export PR_BOT_GITHUB_TOKEN='your_github_token_here'"
echo "   export PR_BOT_GITLAB_TOKEN='your_gitlab_token_here'"
echo "   export PR_BOT_JIRA_TOKEN='your_jira_token_here'"
echo "2. Test with: pr-bot -pr <PR_URL>"
```

### For Server Deployment

1. **Docker deployment** (recommended):

```dockerfile
# Dockerfile
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o pr-bot .

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /app/pr-bot .
COPY --from=builder /app/VERSION .
COPY --from=builder /app/data ./data
EXPOSE 8080
CMD ["./pr-bot", "-server"]
```

2. **Kubernetes deployment**:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pr-bot
spec:
  replicas: 2
  selector:
    matchLabels:
      app: pr-bot
  template:
    metadata:
      labels:
        app: pr-bot
    spec:
      containers:
      - name: pr-bot
        image: your-registry/pr-bot:latest
        ports:
        - containerPort: 8080
        env:
        - name: PR_BOT_GITHUB_TOKEN
          valueFrom:
            secretKeyRef:
              name: pr-bot-secrets
              key: github-token
        # ... other environment variables
```

### With Debug Output

```bash
./pr-bot -d -pr https://github.com/openshift/assisted-service/pull/1234
```

Debug mode provides detailed logging including:
- Configuration values at startup
- Excel file parsing progress
- GA status analysis steps
- GitHub API request details
- Release branch matching logic

### Show Help

```bash
./pr-bot
```

### Search Slack for PR Messages (Legacy)

```bash
go run main.go -slack
```

### Search Slack with Custom Limit (Legacy)

```bash
go run main.go -slack -slack-limit 200
```

### Search for Latest Version Message (Legacy)

```bash
go run main.go -version 2.13
```

This finds the latest message in the Slack channel that contains the specified version and includes an "Upstream SHA list" link.

### Example Output

```
=== PR Analysis Summary ===
PR #1234: Fix authentication bug in installer
Hash: a1b2c3d4e5f6
Merged to 'master' at: 2024-01-15 10:30:00 +0000 UTC
URL: https://github.com/openshift/assisted-service/pull/1234

=== Release Branch Analysis ===

✓ Found in 3 release branches:
  - release-ocm-2.13 (version 2.13) - merged at 2024-01-16 14:20:00 +0000 UTC
    GA Status:
      ACM 2.13.3: GA (GA: 2024-01-10)
      MCE 2.8.2: GA (GA: 2024-01-10)
  - release-ocm-2.14 (version 2.14) - merged at 2024-01-17 09:15:00 +0000 UTC
    GA Status:
      ACM 2.14.1: Next Version (GA: 2024-02-15)
      MCE 2.9.1: Merged but not GA
  - release-ocm-2.15 (version 2.15) - merged at 2024-01-18 11:45:00 +0000 UTC
    GA Status:
      ACM 2.15.0: Not Found
      MCE 2.10.0: Not Found

✗ Not found in 2 release branches:
  - release-ocm-2.10 (version 2.10)
  - release-ocm-2.11 (version 2.11)

Analysis completed at: 2024-01-20 16:30:00 +0000 UTC
```

### Slack Search Example Output

```
=== Slack Search Results ===
Channel: team-acm-downstream-notifcation
Messages searched: 100
PR-related messages found: 3

PR References Found:
  PR #1234 - 2024-01-15 14:30:00
    Message: Merged PR #1234 to release-ocm-2.13
    User: U1234567890

  PR #1235 - 2024-01-16 09:15:00
    Message: Backported PR #1235 to release-ocm-2.14
    User: U0987654321

  PR #1236 - 2024-01-17 11:45:00
    Message: PR #1236 is ready for review
    User: U1122334455
```

### Version Search Example Output

```
=== Version Search Results ===
Target Version: 2.13
Channel: team-acm-downstream-notifcation
Messages searched: 100

Latest message found:
  Timestamp: 2024-01-15 14:30:00
  User: U1234567890
  Upstream SHA List: https://github.com/openshift/assisted-service/compare/upstream-2.13...release-ocm-2.13
  Message: ACM 2.13.3 is ready for release. Upstream SHA list: https://github.com/openshift/assisted-service/compare/upstream-2.13...release-ocm-2.13
```

### GA Status Meanings

- **GA**: The change is included in a Generally Available release
- **Next Version**: The change was merged after the GA date, so it will be in the next version
- **Merged but not GA**: The change is merged but no GA date is available yet
- **Not Found**: No release information found for this version

## Development

### Project Structure

```
pr-bot/
├── main.go                   # Main application entry point
├── internal/
│   ├── config/              # Configuration management
│   ├── github/              # GitHub API client
│   ├── ga/                  # GA status parsing
│   ├── logger/              # Logging utilities
│   ├── models/              # Data models
│   └── slack/               # Slack API client
├── pkg/
│   └── analyzer/            # Core analysis logic
├── data/                    # Excel files for GA tracking
├── go.mod                   # Go module definition
├── Makefile                 # Build automation
└── README.md               # This file
```

### Building and Testing

```bash
# Show available commands
make help

# Build the application
make build

# Run tests
make test

# Format code
make fmt

# Run linter
make lint

# Run all checks
make check

# Clean build artifacts
make clean
```

### Adding New Components

The project follows Go best practices with a clean architecture:

1. **Models** (`internal/models/`): Define data structures
2. **GitHub Client** (`internal/github/`): API interaction layer
3. **Analyzer** (`pkg/analyzer/`): Business logic
4. **Configuration** (`internal/config/`): Configuration management
5. **CLI** (`main.go`): Command-line interface
6. **GA Parser** (`internal/ga/`): Excel parsing for release tracking

## API Rate Limits

- **Without GitHub token**: 60 requests per hour
- **With GitHub token**: 5,000 requests per hour

For production use, always configure a GitHub token.

## Troubleshooting

### Slack Authentication Issues

If you get `invalid_auth` errors:

1. **Check token types**: Make sure you're using the correct browser tokens:
   - `xoxc` should be your browser token for listing channels
   - `xoxd` should be your browser token for reading channel messages

2. **Verify scopes**: Ensure your Slack app has the required scopes:
   - `channels:read` for the xoxc token
   - `channels:history` for the xoxd token

3. **Check installation**: Make sure the app is installed to your workspace

4. **Verify channel name**: The channel name must match exactly (case-sensitive)

5. **Use debug mode**: Run with `-d` flag to see detailed error messages:
   ```bash
   ./pr-bot -d -pr https://github.com/openshift/assisted-service/pull/1234
   ```

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests
5. Run `make check` to ensure quality
6. Submit a pull request

## License

This project is licensed under the MIT License.

## Roadmap

- [ ] JSON output format
- [ ] Support for multiple repository analysis
- [ ] Batch PR analysis
- [ ] Web interface
- [ ] Database persistence
- [ ] Notification integration (Slack, email)
- [ ] CI/CD pipeline integration 