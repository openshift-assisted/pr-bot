#!/bin/bash

# Test script for pr-bot endpoints
# Usage: ./test-endpoints.sh [ngrok-url]

NGROK_URL=${1:-"http://localhost:3000"}

echo "🧪 Testing pr-bot endpoints..."
echo "Base URL: $NGROK_URL"
echo

# Test health endpoint
echo "1. Testing health endpoint..."
curl -s "$NGROK_URL/health" | jq . || echo "Health check failed"
echo

# Test slash command endpoint (simulate Slack request)
echo "2. Testing /info slash command..."
curl -s -X POST "$NGROK_URL/slack/commands" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "command=/info&text=&user_id=U123&channel_id=C123" || echo "Slash command test failed"
echo

echo "✅ Endpoint tests completed!"
echo
echo "📝 To test with Slack:"
echo "1. Make sure ngrok is running: ngrok http 3000"
echo "2. Update Slack app URLs with your ngrok URL"
echo "3. Try: /info in Slack"
