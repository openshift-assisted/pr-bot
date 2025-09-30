#!/bin/bash

# Development workflow script for pr-bot
# This script helps set up the complete local development environment

set -e

echo "🚀 PR-Bot Local Development Setup"
echo "=================================="

# Check if .env exists
if [ ! -f .env ]; then
    echo "📝 Creating .env file from template..."
    cp env.example .env
    echo "⚠️  Please edit .env with your actual tokens before continuing!"
    echo "   Required: PR_BOT_GITHUB_TOKEN, PR_BOT_GITLAB_TOKEN, PR_BOT_JIRA_TOKEN"
    echo "   For Slack: PR_BOT_SLACK_BOT_TOKEN (get from Slack app)"
    exit 1
fi

# Build the project
echo "🔨 Building pr-bot..."
go build -o pr-bot .

# Check if ngrok is available
if ! command -v ngrok &> /dev/null; then
    echo "❌ ngrok not found!"
    echo "📥 Install ngrok:"
    echo "   Option 1: npm install -g ngrok"
    echo "   Option 2: Download from https://ngrok.com/download"
    echo "   Option 3: wget https://bin.equinox.io/c/bNyj1mQVY4c/ngrok-v3-stable-linux-amd64.tgz"
    exit 1
fi

echo "✅ Setup complete!"
echo
echo "🎯 Next steps:"
echo "1. Edit .env with your tokens"
echo "2. Run: ./pr-bot -server -port 3000"
echo "3. In another terminal: ngrok http 3000"
echo "4. Configure Slack app with ngrok URL"
echo "5. Test with: /info in Slack"
echo
echo "🧪 Test endpoints locally:"
echo "   ./test-endpoints.sh"
echo
echo "📚 Full setup guide:"
echo "   docs/SLACK_BOT_SETUP.md"
