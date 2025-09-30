# Slack Bot Setup Checklist

## ✅ Bot Token Scopes (Required)
Go to **OAuth & Permissions** → **Bot Token Scopes**:

- [ ] `app_mentions:read` - Respond to @mentions
- [ ] `chat:write` - Send messages  
- [ ] `commands` - Handle slash commands
- [ ] `im:read` - Read direct messages
- [ ] `im:write` - Send direct messages
- [ ] `channels:read` - Read channel info (optional)

## ✅ App Home Settings
Go to **App Home**:

- [ ] Enable "Always Show My Bot as Online"
- [ ] Enable "Allow users to send Slash commands and messages from the messages tab"

## ✅ Event Subscriptions
Go to **Event Subscriptions**:

- [ ] Enable Events: ON
- [ ] Request URL: `https://your-tunnel-url.loca.lt/slack/events`
- [ ] Subscribe to Bot Events:
  - [ ] `app_mention`
  - [ ] `message.im`

## ✅ Slash Commands
Create these 4 commands (all use same URL):

Request URL for all: `https://your-tunnel-url.loca.lt/slack/commands`

- [ ] `/info` - Show bot help
- [ ] `/pr` - Analyze PR (Usage: `<PR_URL>`)
- [ ] `/jt` - Analyze JIRA ticket (Usage: `<JIRA_TICKET>`)
- [ ] `/version` - Compare versions (Usage: `<COMPONENT> <VERSION>`)

## ✅ Installation
- [ ] Install App to Workspace
- [ ] Copy Bot User OAuth Token (starts with `xoxb-`)
- [ ] Add token to .env: `PR_BOT_SLACK_BOT_TOKEN=xoxb-...`
- [ ] Restart pr-bot server

## ✅ Testing
- [ ] `/info` works in channel
- [ ] Can mention bot: `@pr-bot info`
- [ ] Can send DM to bot: `info`
- [ ] Bot responds to all interaction methods
