#!/usr/bin/env bash
# Measures one repetition of one scenario and writes newline-delimited JSON for
# tools/bench.
#
# Phases are timed separately, but the number that decides anything is the job's
# wall time, which the report job reads from the GitHub API. A GOPROXY daemon
# moves work outside `go build` (it has to start before the go command and flush
# after it), and so does actions/setup-go, whose post step tars and uploads the
# module cache. Timing only the build makes both look free.
set -euo pipefail

: "${SCENARIO:?SCENARIO is required}"
: "${REP:?REP is required}"
: "${BUILD_CMD:?BUILD_CMD is required}"

USE_GOCICA="${USE_GOCICA:-0}"
METRICS_DIR="${GITHUB_WORKSPACE:-$PWD}/metrics"
mkdir -p "$METRICS_DIR"
METRICS="$METRICS_DIR/$SCENARIO-$REP.jsonl"
: > "$METRICS"

record() {
  printf '{"scenario":"%s","rep":%s,"phase":"%s","ns":%s,"status":%s}\n' \
    "$SCENARIO" "$REP" "$1" "$2" "$3" >> "$METRICS"
}

phase() {
  local name="$1"
  shift

  local start end status=0
  start=$(date +%s%N)
  "$@" || status=$?
  end=$(date +%s%N)

  record "$name" "$((end - start))" "$status"

  return "$status"
}

if [ "$SCENARIO" != "setupgo" ]; then
  # setup-go deliberately restored its cache in this job; everyone else starts
  # from an empty one so the runner image cannot skew the result.
  phase clean go clean -cache -modcache
fi

if [ "$USE_GOCICA" = "1" ]; then
  : "${GOCICA_BIN:?GOCICA_BIN is required when USE_GOCICA=1}"
  GOCICA_DIR="${GOCICA_DIR:-${RUNNER_TEMP:-/tmp}/gocica}"
  mkdir -p "$GOCICA_DIR"
  export GOCICA_DIR

  start_proxy() {
    nohup "$GOCICA_BIN" serve --dir="$GOCICA_DIR" > "$METRICS_DIR/proxy-$SCENARIO-$REP.log" 2>&1 &

    local state="$GOCICA_DIR/mod/proxy.json"
    for _ in $(seq 1 150); do
      if [ -f "$state" ]; then
        local url
        url=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['url'])" "$state")
        if curl -fsS "$url/-/healthz" > /dev/null 2>&1; then
          echo "GOPROXY_URL=$url" >> "$GITHUB_ENV"
          export GOPROXY_URL="$url"

          return 0
        fi
      fi
      sleep 0.1
    done

    echo "module proxy did not come up" >&2

    return 1
  }

  phase proxy_start start_proxy

  # "|" rather than ",": it makes the go command fall back on any error, so a
  # daemon that dies mid-run cannot fail the build.
  export GOPROXY="$GOPROXY_URL|https://proxy.golang.org,direct"
  export GOCACHEPROG="$GOCICA_BIN --dir=$GOCICA_DIR"
fi

phase mod_download go mod download
# shellcheck disable=SC2086 # BUILD_CMD is a command line on purpose.
phase build $BUILD_CMD

if [ "$USE_GOCICA" = "1" ]; then
  phase flush "$GOCICA_BIN" proxy-stop --dir="$GOCICA_DIR"
fi
