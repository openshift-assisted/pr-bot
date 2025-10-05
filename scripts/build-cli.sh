#!/bin/bash

# Build script for pr-bot CLI distribution
# This creates a clean build that uses Google Sheets API for GA data

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🚀 Building pr-bot CLI for distribution...${NC}"

# Get version from VERSION file or git
if [ -f "VERSION" ]; then
    VERSION=$(cat VERSION)
else
    VERSION=$(git describe --tags --abbrev=0 2>/dev/null || echo "dev")
fi

# Get build info
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

echo -e "${YELLOW}📦 Version: ${VERSION}${NC}"
echo -e "${YELLOW}📝 Commit: ${COMMIT}${NC}"
echo -e "${YELLOW}📅 Build Date: ${BUILD_DATE}${NC}"

# Build flags
LDFLAGS="-X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildDate=${BUILD_DATE}"

# Clean previous builds
echo -e "${BLUE}🧹 Cleaning previous builds...${NC}"
rm -f pr-bot

# Build the binary
echo -e "${BLUE}🔨 Building binary...${NC}"
go build -ldflags "${LDFLAGS}" -o pr-bot .

# Verify the build
if [ -f "pr-bot" ]; then
    echo -e "${GREEN}✅ Build successful!${NC}"
    
    # Show binary info
    echo -e "${BLUE}📊 Binary information:${NC}"
    ls -lh pr-bot
    
    # Test the binary
    echo -e "${BLUE}🧪 Testing binary...${NC}"
    ./pr-bot --version 2>/dev/null || echo -e "${YELLOW}⚠️  Version flag not implemented yet${NC}"
    
    echo -e "${GREEN}🎉 CLI build complete!${NC}"
    echo -e "${YELLOW}📋 Next steps:${NC}"
    echo -e "   1. Configure Google Sheets API:"
    echo -e "      export PR_BOT_GOOGLE_API_KEY=\"your-api-key\""
    echo -e "      export PR_BOT_GOOGLE_SHEET_ID=\"your-sheet-id\""
    echo -e "   2. Test with: ./pr-bot --config"
    echo -e "   3. Use: ./pr-bot -pr <PR_URL>"
else
    echo -e "${RED}❌ Build failed!${NC}"
    exit 1
fi
