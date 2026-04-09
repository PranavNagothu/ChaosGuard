# ChaosGuard Makefile
# Usage: make <target>

.PHONY: all build test run docker-up docker-down tidy lint clean bench

BINARY_SERVER=bin/chaosguard-server
BINARY_AGENT=bin/chaosguard-agent
GO=go

all: tidy build test

## ─── Build ────────────────────────────────────────────────────────────────────

build: build-server build-agent

build-server:
	@echo "→ Building control plane server..."
	@mkdir -p bin
	$(GO) build -ldflags="-s -w" -o $(BINARY_SERVER) ./cmd/server

build-agent:
	@echo "→ Building proxy agent..."
	@mkdir -p bin
	$(GO) build -ldflags="-s -w" -o $(BINARY_AGENT) ./cmd/agent

## ─── Run (local, needs postgres+redis running) ────────────────────────────────

run-server:
	@echo "→ Starting control plane on :8080..."
	$(GO) run ./cmd/server

run-agent:
	@echo "→ Starting proxy agent on :8181 → :9090..."
	$(GO) run ./cmd/agent

## ─── Test ────────────────────────────────────────────────────────────────────

test:
	@echo "→ Running unit tests..."
	$(GO) test -v -race ./...

bench:
	@echo "→ Running benchmarks..."
	$(GO) test -bench=. -benchmem -count=3 ./internal/policy/...

cover:
	@echo "→ Generating coverage report..."
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "→ Coverage report: coverage.html"

## ─── Docker ──────────────────────────────────────────────────────────────────

docker-up:
	@echo "→ Starting full ChaosGuard stack..."
	docker compose up -d --build
	@echo "✅ Stack running:"
	@echo "   Control Plane: http://localhost:8080"
	@echo "   Proxy Agent:   http://localhost:8181 (→ echo-service)"
	@echo "   Prometheus:    http://localhost:9091"
	@echo ""
	@echo "→ Try: make apply-example"

docker-down:
	@echo "→ Stopping ChaosGuard stack..."
	docker compose down -v

docker-logs:
	docker compose logs -f

## ─── Demo ────────────────────────────────────────────────────────────────────

apply-example:
	@echo "→ Applying latency chaos policy..."
	curl -s -X POST http://localhost:8080/api/v1/policies \
	  -H "Content-Type: application/json" \
	  -d @examples/latency-policy.json | python3 -m json.tool

kill-switch:
	@echo "→ Firing kill switch for echo-service..."
	curl -s -X POST http://localhost:8080/api/v1/services/echo-service/kill | python3 -m json.tool

list-policies:
	@echo "→ Listing policies for echo-service..."
	curl -s "http://localhost:8080/api/v1/policies?service_id=echo-service" | python3 -m json.tool

health:
	@echo "→ Server health:"
	@curl -s http://localhost:8080/healthz | python3 -m json.tool

## ─── Dev Tools ───────────────────────────────────────────────────────────────

tidy:
	@echo "→ Tidying Go modules..."
	$(GO) mod tidy

lint:
	@which golangci-lint || (echo "Install: curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b \$$(go env GOPATH)/bin" && exit 1)
	golangci-lint run ./...

clean:
	@echo "→ Cleaning build artifacts..."
	rm -rf bin/ coverage.out coverage.html
