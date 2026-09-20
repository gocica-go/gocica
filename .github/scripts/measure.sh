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

# The wall-clock start lets a phase be lined up against the daemon's log and
# metrics, which carry their own timestamps.
record() {
  printf '{"scenario":"%s","rep":%s,"phase":"%s","ns":%s,"status":%s,"start_ns":%s}\n' \
    "$SCENARIO" "$REP" "$1" "$2" "$3" "$4" >> "$METRICS"
}

phase() {
  local name="$1"
  shift

  local start end status=0
  start=$(date +%s%N)
  "$@" || status=$?
  end=$(date +%s%N)

  record "$name" "$((end - start))" "$status" "$start"

  return "$status"
}

# GOCICA_PROFILE=true records the dev build's metrics and profiles next to the
# timings. It is off while measuring wall time: the 100ms sampler and the
# profilers are not free.
PROFILE="${GOCICA_PROFILE:-false}"
dev_flags() {
  local name="$1"
  if [ "$PROFILE" != "true" ]; then
    return 0
  fi
  printf -- '--dev.metrics=%s/%s-metrics-%s-%s.csv --dev.cpu-prof=%s/%s-cpu-%s-%s.pprof --dev.fg-prof=%s/%s-fg-%s-%s.pprof' \
    "$METRICS_DIR" "$name" "$SCENARIO" "$REP" \
    "$METRICS_DIR" "$name" "$SCENARIO" "$REP" \
    "$METRICS_DIR" "$name" "$SCENARIO" "$REP"
}

# What the runner looked like, for reading the disk numbers later.
{
  echo "## sysctl"; sysctl vm.dirty_background_ratio vm.dirty_ratio vm.dirty_expire_centisecs 2>/dev/null
  echo "## mounts"; findmnt -no SOURCE,FSTYPE,OPTIONS,TARGET "$(go env GOMODCACHE)" "$(go env GOCACHE)" "${GOCICA_DIR:-${RUNNER_TEMP:-/tmp}}" / /mnt 2>/dev/null
  echo "## lsblk"; lsblk -o NAME,SIZE,TYPE,MOUNTPOINTS,ROTA 2>/dev/null
  echo "## mem"; free -m
} > "$METRICS_DIR/runner-$SCENARIO-$REP.txt" 2>&1 || true

case "$SCENARIO" in
setupgo*) ;;
*)
  # setup-go deliberately restored its cache in this job; everyone else starts
  # from an empty one so the runner image cannot skew the result.
  #
  # Tolerated on failure: the module cache is written read-only, so a clean can
  # fail on a runner where something else already touched it. The phase records
  # its status either way.
  phase clean go clean -cache -modcache || true
  ;;
esac

if [ "$USE_GOCICA" = "1" ]; then
  : "${GOCICA_BIN:?GOCICA_BIN is required when USE_GOCICA=1}"
  GOCICA_DIR="${GOCICA_DIR:-${RUNNER_TEMP:-/tmp}/gocica}"
  mkdir -p "$GOCICA_DIR"
  export GOCICA_DIR

  start_proxy() {
    # shellcheck disable=SC2046 # the dev flags are meant to split.
    nohup "$GOCICA_BIN" serve --dir="$GOCICA_DIR" $(dev_flags proxy) > "$METRICS_DIR/proxy-$SCENARIO-$REP.log" 2>&1 &

    local state="$GOCICA_DIR/mod/proxy.json"
    # Waits for readiness, not just liveness: the daemon restores modules into
    # GOMODCACHE in extracted form, and starting the go command before that
    # finishes would have it extracting into the same directories.
    for _ in $(seq 1 3000); do
      if [ -f "$state" ]; then
        local url health
        url=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['url'])" "$state")
        health=$(curl -fsS "$url/-/healthz" 2>/dev/null || true)
        case "$health" in
        *'"ready":true'*)
          echo "GOPROXY_URL=$url" >> "$GITHUB_ENV"
          export GOPROXY_URL="$url"

          return 0
          ;;
        esac
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
fi

if [ -n "${GOFLAGS_EXTRA:-}" ]; then
  export GOFLAGS="${GOFLAGS:-} $GOFLAGS_EXTRA"
  echo "GOFLAGS=$GOFLAGS"
fi

# BUILD_ONLY skips it. `go mod download` fetches every module's zip whether or
# not the module is already extracted (cmd/go/internal/modcmd/download.go), so a
# workflow that only builds never pays for the zips at all -- which is where the
# extracted-module cache actually shows.
#
# -x lists every fetch the go command makes ("# get https://..."), so a warm run
# that should need none can be checked for what it still asked for.
mod_download() {
  go mod download -x 2> "$METRICS_DIR/mod-download-$SCENARIO-$REP.log"
}
if [ "${BUILD_ONLY:-0}" != "1" ]; then
  phase mod_download mod_download
fi

if [ "$USE_GOCICA" = "1" ]; then
  # Only the build runs under GOCACHEPROG. `go mod download` also touches the
  # build cache, and since one run publishes a single cache entry per key, the
  # first gocica process to upload claims it -- which would leave the build's own
  # output unpublished and make the next warm run look worse than it is.
  export GOCACHEPROG="$GOCICA_BIN --dir=$GOCICA_DIR $(dev_flags cacheprog)"
fi

# The cacheprog logs to the build's stderr, so keep a copy next to the timings.
build() {
  # shellcheck disable=SC2086 # BUILD_CMD is a command line on purpose.
  $BUILD_CMD 2> >(tee "$METRICS_DIR/build-$SCENARIO-$REP.log" >&2)
}
phase build build

if [ "$USE_GOCICA" = "1" ]; then
  phase flush "$GOCICA_BIN" proxy-stop --dir="$GOCICA_DIR"
fi
