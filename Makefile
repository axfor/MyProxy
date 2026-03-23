.PHONY: all build build-arm clean test test-unit test-integration test-pg-unsupported bench tpcc run docker-build docker-push help

BINARY_NAME=aproxy
VERSION?=dev
COMMIT=$(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS=-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

GOEXPERIMENT=greenteagc
export GOEXPERIMENT

all: build

help:
	@echo "Available targets:"
	@echo "  build              - Build the binary"
	@echo "  build-arm          - Build the Linux ARM64 binary"
	@echo "  clean              - Remove build artifacts"
	@echo "  test               - Run all tests (unit + integration)"
	@echo "  test-unit          - Run unit tests"
	@echo "  test-integration   - Run integration tests (including CDC/binlog tests)"
	@echo "  test-pg-unsupported- Run tests for PG-unsupported MySQL features (most will skip)"
	@echo "  bench              - Run benchmarks"
	@echo "  tpcc               - Run industry-standard TPC-C benchmark (go-tpc)"
	@echo "  run                - Build and run the proxy"
	@echo "  docker-build       - Build Docker image"
	@echo "  docker-push        - Push Docker image"

build:
	@echo "Building $(BINARY_NAME) with GOEXPERIMENT=$(GOEXPERIMENT)..."
	GOEXPERIMENT=$(GOEXPERIMENT) go build $(LDFLAGS) -o bin/$(BINARY_NAME) ./cmd/aproxy

build-linux:
	@echo "Building $(BINARY_NAME) for Linux with GOEXPERIMENT=$(GOEXPERIMENT)..."
	GOOS=linux GOARCH=amd64 GOEXPERIMENT=$(GOEXPERIMENT) go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-amd64 ./cmd/aproxy

build-arm:
	@echo "Building $(BINARY_NAME) for Linux ARM64 with GOEXPERIMENT=$(GOEXPERIMENT)..."
	GOOS=linux GOARCH=arm64 GOEXPERIMENT=$(GOEXPERIMENT) go build $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-arm64 ./cmd/aproxy

clean:
	@echo "Cleaning..."
	rm -rf bin/
	go clean

test: test-unit test-integration

test-unit:
	@echo "Running unit tests with GOEXPERIMENT=$(GOEXPERIMENT)..."
	GOEXPERIMENT=$(GOEXPERIMENT) go test -v -race -coverprofile=coverage.out ./pkg/... ./internal/...

test-integration: build
	@echo "Running integration tests (including CDC) with GOEXPERIMENT=$(GOEXPERIMENT)..."
	@echo "Starting aproxy service..."
	@lsof -ti:3306 | xargs kill -9 2>/dev/null || true
	@lsof -ti:9090 | xargs kill -9 2>/dev/null || true
	@./bin/$(BINARY_NAME) -config configs/config.yaml > /tmp/aproxy-test.log 2>&1 & echo $$! > /tmp/aproxy-test.pid
	@echo "Waiting for aproxy to be ready..."
	@sleep 3
	@echo "Running tests..."
	@GOEXPERIMENT=$(GOEXPERIMENT) go test -v -timeout 5m ./test/integration/... || (kill `cat /tmp/aproxy-test.pid` 2>/dev/null; rm -f /tmp/aproxy-test.pid; exit 1)
	@echo "Stopping aproxy service..."
	@kill `cat /tmp/aproxy-test.pid` 2>/dev/null || true
	@rm -f /tmp/aproxy-test.pid

test-pg-unsupported: build
	@echo "Running PostgreSQL-unsupported MySQL feature tests with GOEXPERIMENT=$(GOEXPERIMENT)..."
	@echo "NOTE: Most tests will be skipped (t.Skip) as these features are not supported by PostgreSQL"
	@echo "Starting aproxy service..."
	@lsof -ti:3306 | xargs kill -9 2>/dev/null || true
	@lsof -ti:9090 | xargs kill -9 2>/dev/null || true
	@./bin/$(BINARY_NAME) -config configs/config.yaml > /tmp/aproxy-test.log 2>&1 & echo $$! > /tmp/aproxy-test.pid
	@echo "Waiting for aproxy to be ready..."
	@sleep 3
	@echo "Running tests..."
	@GOEXPERIMENT=$(GOEXPERIMENT) go test -v -timeout 5m ./test/pg-unsupported/... || (kill `cat /tmp/aproxy-test.pid` 2>/dev/null; rm -f /tmp/aproxy-test.pid; exit 1)
	@echo "Stopping aproxy service..."
	@kill `cat /tmp/aproxy-test.pid` 2>/dev/null || true
	@rm -f /tmp/aproxy-test.pid

bench:
	@echo "Running benchmarks with GOEXPERIMENT=$(GOEXPERIMENT)..."
	GOEXPERIMENT=$(GOEXPERIMENT) go test -bench=. -benchmem ./pkg/... ./internal/...

tpcc: build
	@echo "===================================="
	@echo "  TPC-C Benchmark (go-tpc)"
	@echo "===================================="
	@echo ""
	@echo "Checking prerequisites..."
	@if [ ! -f ${HOME}/.go-tpc/bin/go-tpc ] && ! command -v go-tpc > /dev/null 2>&1; then \
		echo "go-tpc not found, installing..."; \
		curl --proto '=https' --tlsv1.2 -sSf https://raw.githubusercontent.com/pingcap/go-tpc/master/install.sh | sh; \
	fi
	@if [ -f ${HOME}/.go-tpc/bin/go-tpc ]; then \
		GO_TPC="${HOME}/.go-tpc/bin/go-tpc"; \
	else \
		GO_TPC="go-tpc"; \
	fi; \
	echo "✓ go-tpc found: $$GO_TPC"; \
	if ! pgrep -x "$(BINARY_NAME)" > /dev/null; then \
		echo "Starting aproxy..."; \
		lsof -ti:3306 | xargs kill -9 2>/dev/null || true; \
		lsof -ti:9090 | xargs kill -9 2>/dev/null || true; \
		./bin/$(BINARY_NAME) -config configs/config.yaml > /tmp/aproxy-tpcc.log 2>&1 & \
		echo $$! > /tmp/aproxy-tpcc.pid; \
		echo "Waiting for aproxy to be ready..."; \
		sleep 3; \
	else \
		echo "✓ aproxy is already running"; \
	fi; \
	echo ""; \
	echo "Step 1: Prepare TPC-C data (1 warehouses)..."; \
	$$GO_TPC tpcc -H 127.0.0.1 -P 3306 -D test -U root --warehouses 1 prepare -T 1 || true; \
	echo ""; \
	echo "Step 2: Run TPC-C benchmark (600 seconds)..."; \
	$$GO_TPC tpcc -H 127.0.0.1 -P 3306 -D test -U root --warehouses 1 --time 600s run -T 10 | tee /tmp/aproxy-tpcc-results.txt; \
	echo ""; \
	echo "Step 3: Check consistency..."; \
	$$GO_TPC tpcc -H 127.0.0.1 -P 3306 -D test -U root --warehouses 1 check || true; \
	echo ""; \
	echo "===================================="; \
	echo "  Benchmark Complete!"; \
	echo "===================================="; \
	echo ""; \
	if [ -f /tmp/aproxy-tpcc.pid ]; then \
		echo "Stopping aproxy..."; \
		kill `cat /tmp/aproxy-tpcc.pid` 2>/dev/null || true; \
		rm -f /tmp/aproxy-tpcc.pid; \
	fi; \
	echo "Results:"; \
	echo "  - TPC-C Results: /tmp/aproxy-tpcc-results.txt"; \
	echo "  - aproxy logs:   /tmp/aproxy-tpcc.log"; \
	echo ""; \
	if [ -f /tmp/aproxy-tpcc-results.txt ]; then \
		echo "Performance Summary:"; \
		grep -E "(tpmC|Finished)" /tmp/aproxy-tpcc-results.txt | tail -5; \
	fi

run: build
	@echo "Running $(BINARY_NAME)..."
	./bin/$(BINARY_NAME) -config configs/config.yaml

docker-build:
	@echo "Building Docker image..."
	docker build -t aproxy:$(VERSION) -f deployments/docker/Dockerfile .

docker-push:
	@echo "Pushing Docker image..."
	docker push aproxy:$(VERSION)

install-deps:
	@echo "Installing dependencies..."
	go mod download
	go mod verify

lint:
	@echo "Running linters..."
	golangci-lint run ./...

fmt:
	@echo "Formatting code..."
	go fmt ./...
	goimports -w .

mod-tidy:
	@echo "Tidying go.mod..."
	go mod tidy

coverage:
	@echo "Generating coverage report..."
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

.DEFAULT_GOAL := help
