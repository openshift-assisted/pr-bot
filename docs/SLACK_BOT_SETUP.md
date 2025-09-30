# Slack Bot Setup Guide

This guide explains how to set up the pr-bot as a proper Slack bot using OAuth tokens instead of browser tokens.

## Overview

The pr-bot uses proper OAuth bot tokens with event subscriptions for reliable Slack integration.

## Slack Bot Setup

### 1. Create a Slack App

1. Go to [Slack API Apps](https://api.slack.com/apps)
2. Click "Create New App" → "From scratch"
3. Enter app name: `pr-bot`
4. Select your workspace
5. Click "Create App"

### 2. Configure Bot User

1. In your app settings, go to **"OAuth & Permissions"**
2. Scroll down to **"Scopes"** → **"Bot Token Scopes"**
3. Add the following scopes:
   - `app_mentions:read` - To respond when mentioned
   - `channels:read` - To read channel information
   - `chat:write` - To post messages
   - `im:read` - To read direct messages
   - `im:write` - To send direct messages
   - `commands` - To handle slash commands

### 3. Install App to Workspace

1. Scroll up to **"OAuth Tokens for Your Workspace"**
2. Click **"Install to Workspace"**
3. Review permissions and click **"Allow"**
4. Copy the **"Bot User OAuth Token"** (starts with `xoxb-`)

### 4. Set Up Event Subscriptions

1. Go to **"Event Subscriptions"** in your app settings
2. Turn on **"Enable Events"**
3. Set **Request URL** to: `https://your-server.com/slack/events`
4. Under **"Subscribe to bot events"**, add:
   - `app_mention` - When someone mentions your bot
   - `message.im` - Direct messages to your bot

### 5. Set Up Slash Commands

1. Go to **"Slash Commands"** in your app settings
2. Create the following commands by clicking **"Create New Command"** for each:

#### Command: `/info`
- **Request URL**: `https://your-server.com/slack/commands`
- **Short Description**: `Show bot help and available commands`
- **Usage Hint**: `(no parameters)`

#### Command: `/pr`
- **Request URL**: `https://your-server.com/slack/commands`
- **Short Description**: `Analyze a PR across release branches`
- **Usage Hint**: `<PR_URL>`

#### Command: `/jt`
- **Request URL**: `https://your-server.com/slack/commands`
- **Short Description**: `Analyze all PRs related to a JIRA ticket`
- **Usage Hint**: `<JIRA_TICKET>`

#### Command: `/version`
- **Request URL**: `https://your-server.com/slack/commands`
- **Short Description**: `Compare GitHub tag or MCE version`
- **Usage Hint**: `<COMPONENT> <VERSION> | mce <COMPONENT> <VERSION>`

### 6. Configure Environment Variables

Add to your `.env` file:

```bash
# Slack Bot Configuration (recommended)
PR_BOT_SLACK_BOT_TOKEN=xoxb-your-bot-token-here
```

### 7. Start the Server

```bash
# Start the pr-bot server
pr-bot -server

# Or specify a custom port
pr-bot -server -port 3000
```

## Usage

### Slash Commands (Primary Method)

Use the individual slash commands:

```
/info
/pr https://github.com/openshift/assisted-service/pull/7788
/jt MGMT-20662
/version assisted-service v2.40.1
/version mce assisted-service 2.8.0
```

### In Slack Channels

Mention the bot with commands:

```
@pr-bot info
@pr-bot pr https://github.com/openshift/assisted-service/pull/7788
@pr-bot jt MGMT-20662
@pr-bot version assisted-service v2.40.1
```

### Direct Messages

Send commands directly to the bot:

```
info
pr https://github.com/openshift/assisted-service/pull/7788
jt MGMT-20662
version assisted-service v2.40.1
```

## Available Commands

| Command | Description | Example |
|---------|-------------|---------|
| `/info` | Show help and available commands | `/info` |
| `/pr <URL>` | Analyze a PR across release branches | `/pr https://github.com/openshift/assisted-service/pull/7788` |
| `/jt <TICKET>` | Analyze all PRs related to a JIRA ticket | `/jt MGMT-20662` |
| `/version <COMPONENT> <VERSION>` | Compare GitHub tag with previous version | `/version assisted-service v2.40.1` |
| `/version mce <COMPONENT> <VERSION>` | Compare MCE version with previous version | `/version mce assisted-service 2.8.0` |

## Server Endpoints

The pr-bot server exposes these endpoints:

- `POST /slack/events` - Slack event subscriptions (mentions, DMs)
- `POST /slack/commands` - Slack slash commands
- `GET /health` - Health check endpoint

## Troubleshooting

### Bot Not Responding

1. **Check bot token**: Ensure `PR_BOT_SLACK_BOT_TOKEN` is set correctly
2. **Verify scopes**: Make sure all required scopes are added to your bot
3. **Check server logs**: Look for authentication errors in server output
4. **Test endpoint**: Verify your server is accessible at the configured URL

### Event Subscriptions Not Working

1. **URL verification**: Slack will send a challenge request to verify your endpoint
2. **HTTPS required**: Event subscriptions require HTTPS endpoints
3. **Response time**: Your server must respond within 3 seconds
4. **Check logs**: Look for event processing errors in server output

### Slash Commands Not Working

1. **Verify command setup**: Ensure `/prbot` command is configured correctly
2. **Check request URL**: Must point to `/slack/commands` endpoint
3. **Response format**: Commands must respond with plain text or JSON


## Security Notes

- **Bot tokens are more secure** than browser tokens as they don't expire with user sessions
- **Scopes are limited** to only what the bot needs to function
- **Audit trail** - All bot actions are logged and attributable
- **Workspace control** - Admins can easily manage bot permissions

## Development

For local development with ngrok:

1. Install ngrok: `npm install -g ngrok`
2. Start your server: `pr-bot -server -port 3000`
3. Expose with ngrok: `ngrok http 3000`
4. Use the ngrok URL in your Slack app configuration
5. Update Request URLs to use the ngrok domain

Example ngrok URLs:
- Events: `https://abc123.ngrok.io/slack/events`
- Commands: `https://abc123.ngrok.io/slack/commands`
