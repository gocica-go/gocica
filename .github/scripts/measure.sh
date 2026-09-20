#!/usr/bin/env bash
# Measures one repetition of one scenario and writes newline-delimited JSON for
# tools/bench.
#
# Phases are timed separately, but the number that decides anything is the job's
# wall time, which the report job reads from the GitHub API. A GOPROXY daemon
# moves work outside `go build` (it has to start before the go command and flush
# after it), and so does actions/setup-go, whose post step tars and uploads the
# module cache. Timing only the build makes both look free.
#
# Two entry points:
#
#   measure.sh start   launches the gocica daemon and returns at once. It runs
#                      before actions/setup-go, so the daemon's warm-up overlaps
#                      the toolchain download instead of sitting in the Measure
#                      step. Needs no go command.
#   measure.sh         waits for the daemon (when one was started), then runs
#                      the go commands and flushes.
set -euo pipefail

MODE="${1:-measure}"

: "${SCENARIO:?SCENARIO is required}"
: "${REP:?REP is required}"

USE_GOCICA="${USE_GOCICA:-0}"
METRICS_DIR="${GITHUB_WORKSPACE:-$PWD}/metrics"
mkdir -p "$METRICS_DIR"
METRICS="$METRICS_DIR/$SCENARIO-$REP.jsonl"

# The start step, when there is one, owns the file: its rows have to survive
# into the Measure step. GOCICA_PROXY_STARTED is the marker it leaves in
# $GITHUB_ENV; probing for the daemon's state file would race its start-up.
if [ "$MODE" = start ] || [ "${GOCICA_PROXY_STARTED:-0}" != 1 ]; then
  : > "$METRICS"
fi

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

# The toolchain's default locations, worked out without the go command: in
# start mode it is not installed yet. Same rule as cmd/go/internal/cfg.
go_mod_cache() {
  if [ -n "${GOMODCACHE:-}" ]; then
    echo "$GOMODCACHE"
  else
    local gopath="${GOPATH:-$HOME/go}"
    echo "${gopath%%:*}/pkg/mod"
  fi
}
go_build_cache() {
  echo "${GOCACHE:-$HOME/.cache/go-build}"
}

# setup-go deliberately restores its cache in its own job; everyone else starts
# from an empty one so the runner image cannot skew the result.
#
# Not `go clean -cache -modcache`: that needs a toolchain. Module files are
# written 0444 inside 0555 directories, which is what go clean chmods away
# first, so the same happens here.
clean_caches() {
  local dir
  for dir in "$(go_mod_cache)" "$(go_build_cache)"; do
    chmod -R u+w "$dir" 2>/dev/null || true
    rm -rf "$dir"
  done
}

if [ "$USE_GOCICA" = "1" ]; then
  : "${GOCICA_BIN:?GOCICA_BIN is required when USE_GOCICA=1}"
  GOCICA_DIR="${GOCICA_DIR:-${RUNNER_TEMP:-/tmp}/gocica}"
  export GOCICA_DIR
  PROXY_STATE="$GOCICA_DIR/mod/proxy.json"

  launch_proxy() {
    mkdir -p "$GOCICA_DIR"
    # stdio fully redirected, so the step does not wait on the daemon and the
    # daemon outlives the step.
    # shellcheck disable=SC2046 # the dev flags are meant to split.
    nohup "$GOCICA_BIN" serve --dir="$GOCICA_DIR" $(dev_flags proxy) > "$METRICS_DIR/proxy-$SCENARIO-$REP.log" 2>&1 &
  }

  # Waits for readiness, not just liveness: the daemon restores modules into
  # GOMODCACHE in extracted form, and starting the go command before that
  # finishes would have it extracting into the same directories.
  wait_proxy() {
    for _ in $(seq 1 3000); do
      if [ -f "$PROXY_STATE" ]; then
        local url health
        url=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['url'])" "$PROXY_STATE")
        health=$(curl -fsS "$url/-/healthz" 2>/dev/null || true)
        case "$health" in
        *'"ready":true'*)
          echo "GOPROXY_URL=$url" >> "${GITHUB_ENV:-/dev/null}"
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
fi

if [ "$MODE" = start ]; then
  if [ "$USE_GOCICA" != "1" ]; then
    echo "measure.sh start: nothing to start without USE_GOCICA=1" >&2
    exit 1
  fi

  phase clean clean_caches
  phase proxy_start launch_proxy
  {
    echo "GOCICA_PROXY_STARTED=1"
    echo "GOCICA_DIR=$GOCICA_DIR"
  } >> "${GITHUB_ENV:-/dev/null}"

  exit 0
fi

: "${BUILD_CMD:?BUILD_CMD is required}"

# What the runner looked like, for reading the disk numbers later. findmnt is
# called once per path: one that does not exist would otherwise empty the whole
# listing.
{
  echo "## sysctl"; sysctl vm.dirty_background_ratio vm.dirty_ratio vm.dirty_expire_centisecs 2>/dev/null
  echo "## mounts"
  for p in "$(go env GOMODCACHE)" "$(go env GOCACHE)" "${GOCICA_DIR:-${RUNNER_TEMP:-/tmp}}" / /mnt; do
    findmnt -no SOURCE,FSTYPE,OPTIONS,TARGET "$p" 2>/dev/null || echo "(none) $p"
  done
  echo "## lsblk"; lsblk -o NAME,SIZE,TYPE,MOUNTPOINTS,ROTA 2>/dev/null
  echo "## mem"; free -m
} > "$METRICS_DIR/runner-$SCENARIO-$REP.txt" 2>&1 || true

if [ "$USE_GOCICA" = "1" ]; then
  # No start step in this job: do its work here, inside the measurement.
  if [ "${GOCICA_PROXY_STARTED:-0}" != 1 ]; then
    phase clean clean_caches
    phase proxy_start launch_proxy
  fi
  # What is left of the warm-up once setup-go has run. Zero is the goal.
  phase proxy_ready wait_proxy

  # "|" rather than ",": it makes the go command fall back on any error, so a
  # daemon that dies mid-run cannot fail the build.
  export GOPROXY="$GOPROXY_URL|https://proxy.golang.org,direct"
else
  case "$SCENARIO" in
  setupgo*) ;;
  *) phase clean clean_caches ;;
  esac
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
