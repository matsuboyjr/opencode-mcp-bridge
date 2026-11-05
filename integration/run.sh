#!/bin/sh
# Added: unique project ownership, diagnostic logs, scoped cleanup on all exits.
set -eu
cd "$(dirname "$0")/.."
project="bridge-compat-$(date +%s)-$$"
logs=".run/$project"
mkdir -p "$logs"
# Keep Docker's writable client/buildx state and registry auth isolated too.
export DOCKER_CONFIG="$PWD/$logs/docker"
mkdir -p "$DOCKER_CONFIG"
compose() { docker compose -p "$project" -f integration/compose.yaml "$@"; }
cleanup() {
  result=$?
  trap - EXIT INT TERM
  compose logs --no-color > "$logs/containers.log" 2>&1 || true
  compose ps -a > "$logs/containers.txt" 2>&1 || true
  compose down --volumes --remove-orphans > "$logs/cleanup.log" 2>&1 || result=1
  printf 'Diagnostics: %s\n' "$logs"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
compose config > "$logs/compose.yaml"
compose build > "$logs/build.log" 2>&1
compose up -d llm opencode-v1 opencode-v2 bridge-v1 bridge-v2
compose images > "$logs/images.txt"
compose run --rm test > "$logs/tests.log" 2>&1
cat "$logs/tests.log"
