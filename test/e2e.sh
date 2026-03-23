#!/bin/bash
set -e

# E2E test runner for MyProxy
# Uses Docker Compose to start PostgreSQL + MyProxy, then runs Go integration tests

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
COMPOSE_FILE="$PROJECT_ROOT/deployments/docker/docker-compose.yml"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[E2E]${NC} $1"; }
warn() { echo -e "${YELLOW}[E2E]${NC} $1"; }
err() { echo -e "${RED}[E2E]${NC} $1"; }

cleanup() {
    log "Cleaning up Docker containers..."
    docker compose -f "$COMPOSE_FILE" down -v --remove-orphans 2>/dev/null || true
}

# Parse args
KEEP_RUNNING=false
TEST_FILTER=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --keep) KEEP_RUNNING=true; shift ;;
        --filter) TEST_FILTER="$2"; shift 2 ;;
        --clean) cleanup; exit 0 ;;
        *) echo "Usage: $0 [--keep] [--filter TestName] [--clean]"; exit 1 ;;
    esac
done

# Trap to cleanup on exit (unless --keep)
if [ "$KEEP_RUNNING" = false ]; then
    trap cleanup EXIT
fi

log "Starting PostgreSQL and MyProxy via Docker Compose..."
docker compose -f "$COMPOSE_FILE" up -d --build --wait

log "Waiting for MyProxy to be ready..."
for i in $(seq 1 30); do
    if nc -z localhost 13306 2>/dev/null; then
        log "MyProxy is ready on port 13306"
        break
    fi
    if [ $i -eq 30 ]; then
        err "MyProxy failed to start within 30s"
        docker compose -f "$COMPOSE_FILE" logs myproxy
        exit 1
    fi
    sleep 1
done

# Run tests
log "Running e2e tests..."
export MYPROXY_DSN="root:@tcp(localhost:13306)/test?parseTime=true"

TEST_ARGS="-v -count=1 -timeout 120s"
if [ -n "$TEST_FILTER" ]; then
    TEST_ARGS="$TEST_ARGS -run $TEST_FILTER"
fi

cd "$PROJECT_ROOT"
if go test ./test/integration/ $TEST_ARGS; then
    log "All e2e tests passed!"
else
    err "Some e2e tests failed"
    docker compose -f "$COMPOSE_FILE" logs myproxy | tail -50
    exit 1
fi
