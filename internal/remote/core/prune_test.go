package core

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
)

func TestRetainedBase(t *testing.T) {
	t.Parallel()

	entriesFor := func(ids ...string) map[string]*v1.IndexEntry {
		entries := make(map[string]*v1.IndexEntry, len(ids))
		for _, id := range ids {
			entries["action-"+id] = &v1.IndexEntry{OutputId: id}
		}

		return entries
	}

	tests := []struct {
		name        string
		entries     map[string]*v1.IndexEntry
		outputs     []*v1.ActionsOutput
		wantRuns    []baseRun
		wantOutputs []*v1.ActionsOutput
		wantSize    int64
	}{
		{
			name:    "everything referenced stays one run",
			entries: entriesFor("a", "b", "c"),
			outputs: []*v1.ActionsOutput{
				{Id: "a", Offset: 0, Size: 10},
				{Id: "b", Offset: 10, Size: 20},
				{Id: "c", Offset: 30, Size: 30},
			},
			wantRuns: []baseRun{{offset: 0, size: 60}},
			wantOutputs: []*v1.ActionsOutput{
				{Id: "a", Offset: 0, Size: 10},
				{Id: "b", Offset: 10, Size: 20},
				{Id: "c", Offset: 30, Size: 30},
			},
			wantSize: 60,
		},
		{
			// The orphan in the middle splits the copy in two and shifts everything
			// after it down.
			name:    "an orphan splits the run and compacts the offsets",
			entries: entriesFor("a", "c"),
			outputs: []*v1.ActionsOutput{
				{Id: "a", Offset: 0, Size: 10},
				{Id: "b", Offset: 10, Size: 20},
				{Id: "c", Offset: 30, Size: 30},
			},
			wantRuns: []baseRun{{offset: 0, size: 10}, {offset: 30, size: 30}},
			wantOutputs: []*v1.ActionsOutput{
				{Id: "a", Offset: 0, Size: 10},
				{Id: "c", Offset: 10, Size: 30},
			},
			wantSize: 40,
		},
		{
			name:    "adjacent survivors coalesce",
			entries: entriesFor("a", "b", "d"),
			outputs: []*v1.ActionsOutput{
				{Id: "a", Offset: 0, Size: 10},
				{Id: "b", Offset: 10, Size: 10},
				{Id: "c", Offset: 20, Size: 10},
				{Id: "d", Offset: 30, Size: 10},
			},
			wantRuns: []baseRun{{offset: 0, size: 20}, {offset: 30, size: 10}},
			wantOutputs: []*v1.ActionsOutput{
				{Id: "a", Offset: 0, Size: 10},
				{Id: "b", Offset: 10, Size: 10},
				{Id: "d", Offset: 20, Size: 10},
			},
			wantSize: 30,
		},
		{
			// Zero-size outputs occupy no bytes, so they must not start or extend a
			// run, but they still belong in the index.
			name:    "zero-size outputs are kept without a run",
			entries: entriesFor("a", "empty", "b"),
			outputs: []*v1.ActionsOutput{
				{Id: "a", Offset: 0, Size: 10},
				{Id: "empty", Offset: 10, Size: 0},
				{Id: "b", Offset: 10, Size: 10},
			},
			wantRuns: []baseRun{{offset: 0, size: 20}},
			wantOutputs: []*v1.ActionsOutput{
				{Id: "a", Offset: 0, Size: 10},
				{Id: "empty", Offset: 10, Size: 0},
				{Id: "b", Offset: 10, Size: 10},
			},
			wantSize: 20,
		},
		{
			name:     "nothing referenced",
			entries:  map[string]*v1.IndexEntry{},
			outputs:  []*v1.ActionsOutput{{Id: "a", Offset: 0, Size: 10}},
			wantSize: 0,
		},
		{
			name:    "unsorted input is handled",
			entries: entriesFor("a", "b"),
			outputs: []*v1.ActionsOutput{
				{Id: "b", Offset: 10, Size: 10},
				{Id: "a", Offset: 0, Size: 10},
			},
			wantRuns: []baseRun{{offset: 0, size: 20}},
			wantOutputs: []*v1.ActionsOutput{
				{Id: "a", Offset: 0, Size: 10},
				{Id: "b", Offset: 10, Size: 10},
			},
			wantSize: 20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runs, outputs, size := retainedBase(tt.entries, tt.outputs)

			if diff := cmp.Diff(tt.wantRuns, runs, cmp.AllowUnexported(baseRun{})); diff != "" {
				t.Errorf("runs mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantOutputs, outputs, cmpopts.IgnoreUnexported(v1.ActionsOutput{})); diff != "" {
				t.Errorf("outputs mismatch (-want +got):\n%s", diff)
			}
			if size != tt.wantSize {
				t.Errorf("size = %d, want %d", size, tt.wantSize)
			}
		})
	}
}

func TestRetainedBaseDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	outputs := []*v1.ActionsOutput{
		{Id: "b", Offset: 10, Size: 10},
		{Id: "a", Offset: 0, Size: 10},
	}
	entries := map[string]*v1.IndexEntry{"x": {OutputId: "a"}}

	retainedBase(entries, outputs)

	if outputs[0].Id != "b" || outputs[0].Offset != 10 {
		t.Errorf("input outputs were mutated: %+v", outputs[0])
	}
}
