.PHONY: build test clean run help tidy fmt lint

# Build variables
BINARY_NAME=pr-bot
BUILD_DIR=bin
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# Go variables
GOFLAGS ?= 

help: ## Show this help message
	@echo "Available commands:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

build: ## Build the application
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	go build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) .

test: ## Run tests
	go test -v ./...

tidy: ## Run go mod tidy
	go mod tidy

fmt: ## Format Go code
	go fmt ./...

lint: ## Run golangci-lint
	golangci-lint run

clean: ## Clean build artifacts
	rm -rf $(BUILD_DIR)

run: ## Run the application directly
	go run main.go

run-help: ## Show help
	go run main.go -h

run-config: ## Show configuration
	go run main.go -config

run-example: ## Run example analysis (change PR number as needed)
	go run main.go 1234

run-debug: ## Run example analysis with debug output
	go run main.go -d 1234

# Development targets
dev-deps: ## Install development dependencies
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

check: tidy fmt lint test ## Run all checks

.DEFAULT_GOAL := help 