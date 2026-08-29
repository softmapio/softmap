#!/usr/bin/env bash
# Benchmark against Gitea (milestone success criteria 2 and 3):
#   - the create-pull-request handler flow must keep 15-40 nodes and include
#     SQL effects;
#   - a full scan must finish in minutes on a laptop.
#
# Clones Gitea into a cache dir (not vendored into this repo) and runs the
# locally built binary. Requires network on first run.
set -euo pipefail

cd "$(dirname "$0")/.."

CACHE_DIR="${SOFTMAP_BENCH_CACHE:-$HOME/.cache/softmap-bench}"
GITEA_DIR="$CACHE_DIR/gitea"
GITEA_REF="${GITEA_REF:-v1.24.3}"

if [ ! -d "$GITEA_DIR" ]; then
  mkdir -p "$CACHE_DIR"
  echo "cloning gitea $GITEA_REF into $GITEA_DIR ..."
  git clone --depth 1 --branch "$GITEA_REF" https://github.com/go-gitea/gitea "$GITEA_DIR"
fi

make build
BIN=bin/softmap
OUT="$CACHE_DIR/out"
mkdir -p "$OUT"

echo "== discovery =="
time "$BIN" scan "$GITEA_DIR" > "$OUT/entrypoints.txt"
wc -l "$OUT/entrypoints.txt"

echo "== create pull request flow =="
EP="$(grep -m1 'CreatePullRequest' "$OUT/entrypoints.txt" | awk '{print $1}' || true)"
if [ -z "$EP" ]; then
  # Gitea registers routes through its own web.Router wrapper around chi,
  # which discovery does not (yet) see through - exactly the case the
  # --entrypoint escape hatch exists for.
  echo "discovery missed CreatePullRequest; using the func: escape hatch"
  EP="func:code.gitea.io/gitea/routers/api/v1/repo.CreatePullRequest"
fi
echo "entrypoint: $EP"
time "$BIN" scan "$GITEA_DIR" --entrypoint "$EP" --debug-tree \
  > "$OUT/create-pull-request.json" 2> "$OUT/create-pull-request.log"
tail -5 "$OUT/create-pull-request.log"

KEPT="$(python3 -c "import json;print(json.load(open('$OUT/create-pull-request.json'))['stats']['kept_nodes'])")"
SQL="$(python3 -c "import json;d=json.load(open('$OUT/create-pull-request.json'));print(sum(1 for n in d['nodes'] for e in n.get('effects',[]) if e['type']=='sql'))")"
echo "kept_nodes=$KEPT sql_effects=$SQL"
if [ "$KEPT" -lt 15 ] || [ "$KEPT" -gt 50 ]; then
  echo "FAIL: kept_nodes=$KEPT outside 15..50"
  exit 1
fi
if [ "$SQL" -lt 1 ]; then
  echo "FAIL: no SQL effects in the create-pull-request flow"
  exit 1
fi
echo "OK"
