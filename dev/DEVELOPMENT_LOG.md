# PR-Bot Development Log

## Project Overview
**Name**: `pr-bot` (formerly `merged-pr-bot`)
**Purpose**: Analyze merged pull requests and determine their presence across release branches, with version comparison capabilities.

## Current Features

### 1. PR Analysis Mode
```bash
go run main.go <PR_URL>
```
- Analyzes PR presence across multiple branch patterns
- Supports: `release-ocm-*`, `release-*`, `release-v*`, `v*` branches
- Shows GA status from Excel file integration
- Displays exact release versions for Version-prefixed branches

### 2. Version Comparison Mode
```bash
go run main.go -v <version>
```
- **NEW FEATURE**: Compare releases and show exact commits between versions
- Smart previous version detection:
  - `v2.40.1` → finds `v2.40.0` (previous patch)
  - `v2.40.0` → finds `v2.39.3` (latest patch of previous minor)
- Tag validation (checks if version exists)
- Displays commits with: hash, date, title
- Handles version gaps intelligently

### 3. Debug Mode
```bash
go run main.go -d <options>
```
- Detailed logging for troubleshooting
- Shows branch discovery process
- Excel parsing details
- GitHub API interactions

## Recent Major Enhancements

### Version-Prefixed Branches Exact Release Detection
- **Problem Solved**: Originally showed "Not yet released" for all version branches
- **Solution**: Implemented commit history search instead of comparison API
- **Result**: Now shows exact first release version (e.g., "First released in: v2.40.0")

### Version Comparison Tool
- **New CLI**: `go run main.go -v <version>`
- **Smart Logic**: Finds nearest available previous version
- **Validation**: Checks tag existence before processing
- **Output**: Clean commit list with dates and titles

## Technical Architecture

### Core Components
- `main.go` - CLI interface and command routing
- `internal/github/` - GitHub API integration
- `internal/config/` - Configuration management  
- `internal/ga/` - Excel GA status parsing
- `pkg/analyzer/` - Core business logic

### Key Functions Added
- `TagExists()` - Validates release tag existence
- `FindPreviousVersion()` - Smart previous version detection
- `GetCommitsBetweenTags()` - Commit comparison between releases
- `FindCommitInVersionTags()` - Exact release version detection

## Current Usage Examples

### PR Analysis
```bash
# Analyze PR across all release branches
go run main.go https://github.com/openshift/assisted-service/pull/7170

# With debug logging
go run main.go -d https://github.com/openshift/assisted-service/pull/7170
```

### Version Comparison
```bash
# Compare v2.40.0 with previous version (finds v2.39.3)
go run main.go -v v2.40.0

# Compare patch version (finds v2.40.0)  
go run main.go -v v2.40.1

# Handles non-existent versions gracefully
go run main.go -v v2.40.3  # Shows error if tag doesn't exist
```

## Configuration Files
- `.env` - Slack tokens and sensitive config
- `config.yaml` - Repository settings
- `data/ACM - Z Stream Release Schedule.xlsx` - GA status data

## Project Structure
```
pr-bot/
├── main.go                    # CLI entry point
├── internal/
│   ├── config/               # Configuration loading
│   ├── github/               # GitHub API client
│   ├── ga/                   # Excel GA parsing
│   ├── logger/               # Debug logging
│   ├── models/               # Data structures
│   └── slack/                # Slack integration
├── pkg/
│   └── analyzer/             # Core analysis logic
├── data/                     # Excel files
└── README.md                # Documentation
```

## Recent Bug Fixes
1. **GitHub API Rate Limiting**: Handled with proper error messages
2. **Date Parsing**: Fixed timestamp handling for commits
3. **Version Parsing**: Robust semantic version handling
4. **Tag Comparison**: Fixed flawed comparison logic using commit history
5. **Module Renaming**: Successfully renamed from `merged-pr-bot` to `pr-bot`

## Current Status
✅ **Working Features:**
- PR analysis across all branch patterns
- Version-prefixed branch exact release detection
- Version comparison with commit details
- Excel GA status integration
- Debug logging
- Clean CLI interface

⚠️ **Disabled Features:**
- Slack integration commands (commented out in usage)
- Some subcommands need migration

## Next Potential Enhancements
1. **Extend Version Comparison**: Support other branch patterns beyond `v*`
2. **Batch Operations**: Analyze multiple PRs at once
3. **Output Formats**: JSON, CSV export options
4. **Performance**: Caching for large repositories
5. **Slack Integration**: Re-enable and test Slack features

## Build & Run
```bash
# Build binary
go build -o pr-bot

# Run directly
./pr-bot <options>

# Or with Go
go run main.go <options>
```

## Key Learnings
- GitHub comparison API doesn't work for older commits in releases
- Commit history search is more reliable than comparison for release detection
- Smart version detection requires handling gaps in version sequences
- Module renaming requires careful import path updates

## Command Reference
```bash
# Show help
go run main.go

# Analyze PR
go run main.go <PR_URL>

# Compare versions  
go run main.go -v <version>

# Enable debug mode
go run main.go -d <options>
```

---
*Last Updated: July 20, 2025*
*Project renamed from merged-pr-bot to pr-bot* 