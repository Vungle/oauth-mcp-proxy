#!/bin/bash
# generate-claude-knowledge.sh — Master script to generate all computed knowledge artifacts.
# Run from repo root: .claude/scripts/generate-claude-knowledge.sh
# Called by CI on every merge to main.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPTS_DIR="$REPO_ROOT/.claude/scripts"
OUTDIR="$REPO_ROOT/.claude/knowledge/computed"

cd "$REPO_ROOT"
mkdir -p "$OUTDIR"

echo "=== Generating Claude knowledge artifacts for $(basename "$REPO_ROOT") ==="
echo "    Repo root: $REPO_ROOT"
echo "    Output:    $OUTDIR"
echo ""

FAILED=0

run_step() {
  local name="$1"
  local outfile="$2"
  shift 2
  echo -n "  [$name] ... "
  local start_time
  start_time=$(date +%s)
  if "$@" > "$OUTDIR/$outfile" 2>"$OUTDIR/${outfile%.json}.err"; then
    local end_time
    end_time=$(date +%s)
    local bytes
    bytes=$(wc -c < "$OUTDIR/$outfile" | tr -d ' ')
    local tokens=$(( bytes / 4 ))
    echo "OK (${bytes} bytes, ~${tokens} tokens, $((end_time - start_time))s)"
    rm -f "$OUTDIR/${outfile%.json}.err"
  else
    local end_time
    end_time=$(date +%s)
    echo "FAILED ($((end_time - start_time))s) — see ${outfile%.json}.err"
    FAILED=$((FAILED + 1))
  fi
}

###############################################################################
# P0: Must Have
###############################################################################
echo "--- P0: Must Have ---"

# Interface->Implementor Map
run_step "interface-map" "interface-map.json" \
  go run -C "$SCRIPTS_DIR" ./extract-interfaces.go

# AST Type Index
run_step "type-index" "type-index.json" \
  go run -C "$SCRIPTS_DIR" ./extract-types.go

# Module Dependency Graph (simple — no custom tool needed)
run_step "go-mod-graph" "go-mod-graph.txt" \
  go mod graph

# Internal Package Dependency DAG
run_step "internal-deps" "internal-deps.json" \
  go run -C "$SCRIPTS_DIR" ./internal-deps.go

# Exported API Surface
run_step "api-surface" "api-surface.json" \
  go run -C "$SCRIPTS_DIR" ./extract-api-surface.go

###############################################################################
# P1: Should Have
###############################################################################
echo ""
echo "--- P1: Should Have ---"

# Git Intelligence
run_step "git-intelligence" "git-intelligence.json" \
  bash "$SCRIPTS_DIR/generate-git-intelligence.sh" 180

# Concurrency Map
run_step "concurrency-map" "concurrency-map.json" \
  go run -C "$SCRIPTS_DIR" ./extract-concurrency.go

# Call Graph (VTA — builds SSA, may take 1-2 minutes)
run_step "call-graph" "call-graph.json" \
  go run -C "$SCRIPTS_DIR" ./extract-call-graph.go

###############################################################################
# Summary: Compact index for quick Claude consumption
###############################################################################
echo ""
echo "--- Summary Index ---"

run_step "summary" "summary.json" \
  python3 "$SCRIPTS_DIR/generate-summary.py" "$OUTDIR"

###############################################################################
# Meta: Token budget and freshness
###############################################################################
echo ""
echo "--- Generating meta.yml ---"

{
  echo "# Claude knowledge layer metadata"
  echo "generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "source_commit: ${GITHUB_SHA:-$(git rev-parse HEAD)}"
  echo "stale_if_older_than: 7d"
  echo "artifacts:"
  for f in "$OUTDIR"/*.json "$OUTDIR"/*.txt; do
    [ -f "$f" ] || continue
    fname=$(basename "$f")
    bytes=$(wc -c < "$f" | tr -d ' ')
    tokens=$(( bytes / 4 ))
    echo "  $fname: { bytes: $bytes, tokens: ~$tokens }"
  done
  total_bytes=0
  for f in "$OUTDIR"/*.json "$OUTDIR"/*.txt; do
    [ -f "$f" ] || continue
    b=$(wc -c < "$f" | tr -d ' ')
    total_bytes=$((total_bytes + b))
  done
  echo "total_bytes: $total_bytes"
  echo "total_tokens: ~$((total_bytes / 4))"
} > "$OUTDIR/meta.yml"

# Clean up error files if empty
find "$OUTDIR" -name "*.err" -empty -delete 2>/dev/null || true

echo ""
echo "=== Done: $(ls "$OUTDIR" | grep -c -E '\.(json|txt|yml)$') artifacts generated ==="

if [ "$FAILED" -gt 0 ]; then
  echo "WARNING: $FAILED artifact(s) failed to generate. Check .err files."
  exit 1
fi
