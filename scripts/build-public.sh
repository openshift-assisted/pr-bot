#!/bin/bash
set -e

echo "🔧 Building pr-bot for public distribution (no embedded data)..."

# Configuration
EMBED_FILE="internal/embedded/schedule.xlsx"
OUTPUT_BINARY="pr-bot"

# Ensure no embedded data file exists (clean public build)
echo "🧹 Ensuring no embedded data files exist..."
rm -f "$EMBED_FILE"

# Verify clean state
if [ -f "$EMBED_FILE" ]; then
    echo "❌ Error: Embedded data file still exists after cleanup"
    exit 1
fi

# Build without embedded data (default build)
echo "🚀 Building binary without embedded data..."
echo "📦 Build flags: -ldflags='-s -w' (filesystem is default)"
go build -ldflags="-s -w" -o "$OUTPUT_BINARY" .

# Verify build succeeded
if [ ! -f "$OUTPUT_BINARY" ]; then
    echo "❌ Error: Build failed - binary not created"
    exit 1
fi

# Test the binary
echo "🔍 Testing binary..."
DATA_INFO=$(./"$OUTPUT_BINARY" --data-source 2>/dev/null || echo "Data source check failed")

echo ""
echo "✅ Build completed successfully!"
echo "📊 Binary: $(pwd)/$OUTPUT_BINARY"
echo "📈 Size: $(ls -lh $OUTPUT_BINARY | awk '{print $5}')"
echo "🔗 Data: $DATA_INFO"
echo ""
echo "💡 This binary requires external Excel data file:"
echo "   • Place your Excel file at: data/ACM - Z Stream Release Schedule.xlsx"
echo "   • Or specify custom path with --data-file flag (if implemented)"
echo ""
echo "🌐 This is the public distribution version suitable for:"
echo "   • Open source contributions"
echo "   • Users with their own data files" 
echo "   • CI/CD pipelines with external data sources" 