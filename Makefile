# NOFX Backtest Makefile

# Variables
BINARY_NAME=backtest
MAIN_DIR=cmd/backtest
OUTPUT_DIR=backtest_results

# Build variables
BUILD_FLAGS=-ldflags="-s -w"

# Default target
.PHONY: all
all: build

# Build the backtest binary
.PHONY: build
build:
	@echo "Building backtest binary..."
	go build $(BUILD_FLAGS) -o $(BINARY_NAME) $(MAIN_DIR)/main.go
	@echo "Build complete!"

# Run backtest with default parameters
.PHONY: run
run: build
	@echo "Running backtest..."
	./$(BINARY_NAME) -symbol=BTCUSDT -start=2025-10-01 -end=2025-10-31 -balance=10000.0 -output=$(OUTPUT_DIR)

# Run backtest with custom parameters
.PHONY: run-custom
run-custom: build
	@echo "Running custom backtest..."
	./$(BINARY_NAME) $(ARGS)

# Clean build artifacts
.PHONY: clean
clean:
	@echo "Cleaning build artifacts..."
	rm -f $(BINARY_NAME)
	rm -rf $(OUTPUT_DIR)
	@echo "Clean complete!"

# Install dependencies
.PHONY: deps
deps:
	@echo "Installing dependencies..."
	go mod tidy
	@echo "Dependencies installed!"

# Help
.PHONY: help
help:
	@echo "NOFX Backtest Makefile"
	@echo ""
	@echo "Usage:"
	@echo "  make              - Build the backtest binary"
	@echo "  make build        - Build the backtest binary"
	@echo "  make run          - Run backtest with default parameters"
	@echo "  make run-custom   - Run backtest with custom parameters (use ARGS='...' to pass arguments)"
	@echo "  make clean        - Clean build artifacts"
	@echo "  make deps         - Install dependencies"
	@echo ""
	@echo "Examples:"
	@echo "  make run-custom ARGS='-symbol=ETHUSDT -start=2025-10-01 -end=2025-10-31'"
	@echo "  make run-custom ARGS='-symbol=BTCUSDT -balance=50000.0 -output=my_results'"

# Display current version
.PHONY: version
version:
	@echo "NOFX Backtest v1.0.0"