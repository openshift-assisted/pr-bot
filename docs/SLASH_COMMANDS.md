# Slack Slash Commands Reference

This document provides a quick reference for all available Slack slash commands in pr-bot.

## Available Commands

### `/info`
**Description**: Show help and available commands  
**Usage**: `/info`  
**Example**: `/info`

### `/pr <PR_URL>`
**Description**: Analyze a PR across all release branches  
**Usage**: `/pr <PR_URL>`  
**Examples**:
- `/pr https://github.com/openshift/assisted-service/pull/7788`
- `/pr https://github.com/openshift/assisted-installer/pull/100`

### `/jt <JIRA_TICKET>`
**Description**: Analyze all PRs related to a JIRA ticket  
**Usage**: `/jt <JIRA_TICKET>`  
**Examples**:
- `/jt MGMT-20662`
- `/jt https://issues.redhat.com/browse/MGMT-20662`

### `/version <COMPONENT> <VERSION>`
**Description**: Compare GitHub tag with previous version for a specific component  
**Usage**: `/version <COMPONENT> <VERSION>`  
**Examples**:
- `/version assisted-service v2.40.1`
- `/version assisted-installer v2.44.0`

### `/version mce <COMPONENT> <VERSION>`
**Description**: Compare MCE version with previous version for a specific component  
**Usage**: `/version mce <COMPONENT> <VERSION>`  
**Examples**:
- `/version mce assisted-service 2.8.0`
- `/version mce assisted-installer 2.8.0`

## Available Components

For version comparison commands, use one of these components:
- `assisted-service`
- `assisted-installer`
- `assisted-installer-agent`
- `assisted-installer-ui`

## Alternative Usage Methods

### Bot Mentions
You can also mention the bot in channels using the same command syntax (without the slash):
```
@pr-bot info
@pr-bot pr https://github.com/openshift/assisted-service/pull/7788
@pr-bot jt MGMT-20662
@pr-bot version assisted-service v2.40.1
```

### Direct Messages
Send commands directly to the bot via DM (without the slash):
```
info
pr https://github.com/openshift/assisted-service/pull/7788
jt MGMT-20662
version assisted-service v2.40.1
```

## Setup Requirements

To use these slash commands, you need to:

1. **Create each slash command** in your Slack app settings
2. **Set the Request URL** for all commands to: `https://your-server.com/slack/commands`
3. **Configure bot token** in your environment: `PR_BOT_SLACK_BOT_TOKEN=xoxb-...`
4. **Start the server**: `pr-bot -server`

See [SLACK_BOT_SETUP.md](SLACK_BOT_SETUP.md) for detailed setup instructions.

## Response Format

All commands return formatted responses with:
- **PR Analysis**: Branch presence information, merge dates, version details
- **JIRA Analysis**: Related PRs and their status across branches
- **Version Comparison**: Changes between versions with commit details
- **Error Messages**: Clear error descriptions with usage hints

