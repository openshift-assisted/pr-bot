#!/bin/bash
set -e

echo "🔧 Building pr-bot with embedded private data..."

# Configuration
DATA_FILE="data/ACM - Z Stream Release Schedule.xlsx"
EMBED_FILE="internal/embedded/schedule.xlsx"
OUTPUT_BINARY="pr-bot"

# Check if data file exists
if [ ! -f "$DATA_FILE" ]; then
    echo "❌ Error: $DATA_FILE not found"
    echo "💡 Make sure you have access to the private data repository or file"
    echo "📂 Expected location: $(pwd)/$DATA_FILE"
    exit 1
fi

# Create embedded directory if it doesn't exist
mkdir -p "$(dirname "$EMBED_FILE")"

# Copy data file to embedded location for go:embed
echo "📋 Copying data file for embedding..."
cp "$DATA_FILE" "$EMBED_FILE"
echo "✅ Data file copied to: $EMBED_FILE"

# Verify embedded file
if [ ! -f "$EMBED_FILE" ]; then
    echo "❌ Error: Failed to copy data file for embedding"
    exit 1
fi

# Build with embedded data (default build)
echo "🚀 Building binary with embedded data..."
echo "📦 Build flags: -ldflags='-s -w' (embedded is default)"
go build -ldflags="-s -w" -o "$OUTPUT_BINARY" .

# Verify build succeeded
if [ ! -f "$OUTPUT_BINARY" ]; then
    echo "❌ Error: Build failed - binary not created"
    rm -f "$EMBED_FILE"
    exit 1
fi

# Clean up the embedded file (keep source clean)
echo "🧹 Cleaning up temporary files..."
rm -f "$EMBED_FILE"

# Test the binary
echo "🔍 Testing binary..."
DATA_INFO=$(./"$OUTPUT_BINARY" --data-source 2>/dev/null || echo "Data source check failed")

echo ""
echo "✅ Build completed successfully!"
echo "📊 Binary: $(pwd)/$OUTPUT_BINARY"
echo "📈 Size: $(ls -lh $OUTPUT_BINARY | awk '{print $5}')"
echo "🔗 Data: $DATA_INFO"
echo ""
echo "💡 This binary includes embedded Excel data and can be distributed"
echo "   without requiring access to the original data file." 