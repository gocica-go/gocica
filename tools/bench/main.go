// Command bench aggregates the measurements produced by .github/workflows/perf.yaml.
//
// It reads the per-phase JSON lines each scenario job uploads, plus the job list
// from the GitHub API, and prints a markdown table of medians.
//
// The headline number is the job wall time, not the build command. A GOPROXY
// daemon moves work outside `go build` -- it has to start and prewarm before the
// go command runs, and it flushes to the remote cache after -- and so does
// actions/setup-go, which tars and uploads the module cache in its own post step.
// Timing only the build makes both of those look free.
package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// measurement is one line of a scenario's metrics file.
type measurement struct {
	Scenario string `json:"scenario"`
	Rep      int    `json:"rep"`
	Phase    string `json:"phase"`
	Nanos    int64  `json:"ns"`
	Status   int    `json:"status"`
}

// job is the subset of the GitHub jobs API this needs.
type job struct {
	Name        string    `json:"name"`
	Conclusion  string    `json:"conclusion"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

type jobList struct {
	Jobs []job `json:"jobs"`
}

func main() {
	metricsDir := flag.String("metrics", "", "directory holding the downloaded metrics artifacts")
	jobsFile := flag.String("jobs", "", "JSON from `gh api repos/{owner}/{repo}/actions/runs/{id}/jobs` (optional)")
	baseline := flag.String("baseline", "setupgo", "scenario to compare the others against")
	flag.Parse()

	if *metricsDir == "" {
		fmt.Fprintln(os.Stderr, "bench: -metrics is required")
		os.Exit(2)
	}

	measurements, err := readMeasurements(*metricsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: read measurements: %v\n", err)
		os.Exit(1)
	}
	if len(measurements) == 0 {
		fmt.Fprintf(os.Stderr, "bench: no measurements under %s\n", *metricsDir)
		os.Exit(1)
	}

	out := &strings.Builder{}
	writePhaseTable(out, measurements)

	if *jobsFile != "" {
		jobs, err := readJobs(*jobsFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bench: read jobs: %v\n", err)
			os.Exit(1)
		}
		writeWallTable(out, jobs, *baseline)
	}

	fmt.Print(out.String())
}

func readMeasurements(dir string) ([]measurement, error) {
	var out []measurement

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		defer f.Close()

		dec := json.NewDecoder(f)
		for {
			var m measurement
			if err := dec.Decode(&m); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}

				return fmt.Errorf("decode %s: %w", path, err)
			}
			out = append(out, m)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}

	return out, nil
}

func readJobs(path string) ([]job, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var list jobList
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}

	return list.Jobs, nil
}

type key struct {
	scenario string
	phase    string
	cold     bool
}

func writePhaseTable(out *strings.Builder, measurements []measurement) {
	grouped := map[key][]float64{}
	for _, m := range measurements {
		k := key{scenario: m.Scenario, phase: m.Phase, cold: m.Rep == 0}
		grouped[k] = append(grouped[k], float64(m.Nanos)/float64(time.Second))
	}

	keys := slices.SortedFunc(mapKeys(grouped), func(a, b key) int {
		return cmp.Or(
			strings.Compare(a.scenario, b.scenario),
			compareBool(a.cold, b.cold),
			strings.Compare(a.phase, b.phase),
		)
	})

	fmt.Fprintln(out, "## Phase timings")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "| scenario | state | phase | n | median (s) | min (s) | max (s) |")
	fmt.Fprintln(out, "|---|---|---|--:|--:|--:|--:|")
	for _, k := range keys {
		v := grouped[k]
		slices.Sort(v)
		state := "warm"
		if k.cold {
			state = "cold"
		}
		fmt.Fprintf(out, "| %s | %s | %s | %d | %.2f | %.2f | %.2f |\n",
			k.scenario, state, k.phase, len(v), median(v), v[0], v[len(v)-1])
	}
	fmt.Fprintln(out)
}

type wallKey struct {
	scenario string
	cold     bool
}

func writeWallTable(out *strings.Builder, jobs []job, baseline string) {
	grouped := map[wallKey][]float64{}
	for _, j := range jobs {
		scenario, rep, ok := scenarioOf(j.Name)
		if !ok || j.CompletedAt.IsZero() {
			continue
		}
		// Cold and warm are different workloads. Pooling them would let the single
		// cold run dominate the median and hide every warm-path change.
		k := wallKey{scenario: scenario, cold: rep == 0}
		grouped[k] = append(grouped[k], j.CompletedAt.Sub(j.StartedAt).Seconds())
	}

	if len(grouped) == 0 {
		return
	}

	baseMedian := func(cold bool) float64 {
		v, ok := grouped[wallKey{scenario: baseline, cold: cold}]
		if !ok {
			return math.NaN()
		}
		slices.Sort(v)

		return median(v)
	}

	keys := slices.SortedFunc(mapKeys(grouped), func(a, b wallKey) int {
		return cmp.Or(
			strings.Compare(a.scenario, b.scenario),
			compareBool(a.cold, b.cold),
		)
	})

	fmt.Fprintln(out, "## Job wall time (the headline number)")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "| scenario | state | n | median (s) | vs "+baseline+" |")
	fmt.Fprintln(out, "|---|---|--:|--:|--:|")
	for _, k := range keys {
		v := grouped[k]
		slices.Sort(v)
		m := median(v)
		state := "warm"
		if k.cold {
			state = "cold"
		}
		delta := "-"
		if base := baseMedian(k.cold); !math.IsNaN(base) && base != 0 {
			delta = fmt.Sprintf("%+.1f%%", (m-base)/base*100)
		}
		fmt.Fprintf(out, "| %s | %s | %d | %.2f | %s |\n", k.scenario, state, len(v), m, delta)
	}
	fmt.Fprintln(out)
}

// scenarioOf pulls the scenario and repetition out of a matrix job name such as
// "measure (gocica, 3)".
func scenarioOf(name string) (string, int, bool) {
	open := strings.IndexByte(name, '(')
	closing := strings.LastIndexByte(name, ')')
	if open < 0 || closing < open {
		return "", 0, false
	}

	scenario, repText, ok := strings.Cut(name[open+1:closing], ",")
	if !ok {
		return "", 0, false
	}

	rep, err := strconv.Atoi(strings.TrimSpace(repText))
	if err != nil {
		return "", 0, false
	}

	return strings.TrimSpace(scenario), rep, true
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n == 0 {
		return math.NaN()
	}
	if n%2 == 1 {
		return sorted[n/2]
	}

	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	default:
		return 1
	}
}

func mapKeys[K comparable, V any](m map[K]V) func(func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
