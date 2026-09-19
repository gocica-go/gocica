package modproxy

import (
	"testing"
	"time"

	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func newLocalCache(t *testing.T) (*Cache, *Store) {
	t.Helper()

	store, err := NewStore(StoreDir(t.TempDir()))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cache, err := NewCache(t.Context(), log.DefaultLogger, store, nil, nil, NewCompressionRegistry())
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}

	return cache, store
}

func TestCache_SnapshotDropsStaleEntries(t *testing.T) {
	t.Parallel()

	cache, _ := newLocalCache(t)

	cache.entries = map[string]*v1.IndexEntry{
		"fresh": {OutputId: "a", LastUsedAt: timestamppb.Now()},
		"edge":  {OutputId: "b", LastUsedAt: timestamppb.New(time.Now().Add(-entryRetention + time.Hour))},
		// Nothing has asked for this in over a week. Carrying it forward is how
		// the blob grows without bound.
		"stale": {OutputId: "c", LastUsedAt: timestamppb.New(time.Now().Add(-entryRetention - time.Hour))},
	}

	got := cache.snapshot()

	if _, ok := got["fresh"]; !ok {
		t.Error("a recently used entry was dropped")
	}
	if _, ok := got["edge"]; !ok {
		t.Error("an entry just inside the retention window was dropped")
	}
	if _, ok := got["stale"]; ok {
		t.Error("an entry past the retention window was kept")
	}
}

func TestCache_ForgetRemovesAnEntry(t *testing.T) {
	t.Parallel()

	cache, _ := newLocalCache(t)
	cache.entries = map[string]*v1.IndexEntry{"gone": {OutputId: "a", LastUsedAt: timestamppb.Now()}}

	cache.forget("gone")

	if _, ok := cache.entries["gone"]; ok {
		t.Error("forget left the entry in place")
	}
}

func TestCache_StoreIndexesAndMarksZipsIncompressible(t *testing.T) {
	t.Parallel()

	cache, store := newLocalCache(t)

	const path = "example.com/m/@v/v1.0.0.zip"
	content := []byte("a module zip, already compressed")

	w, err := store.NewWriter()
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := w.Size(); got != int64(len(content)) {
		t.Errorf("writer size = %d, want %d", got, len(content))
	}

	objectID, err := cache.Store(t.Context(), path, w, false)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if want := ObjectIDForContent(content); objectID != want {
		t.Errorf("objectID = %q, want %q", objectID, want)
	}

	entry, ok := cache.entries[path]
	if !ok {
		t.Fatal("the object was not indexed")
	}
	if entry.OutputId != objectID {
		t.Errorf("entry points at %q, want %q", entry.OutputId, objectID)
	}
	if cache.Stored() != 1 {
		t.Errorf("Stored() = %d, want 1", cache.Stored())
	}

	// Compressing an already-compressed zip costs CPU and buffers the whole
	// archive in memory for nothing.
	policy := cache.compressions.Policy()
	if policy(objectID, 10<<20) {
		t.Error("a zip must not be marked for compression")
	}
	if !policy("some-other-object", 10<<20) {
		t.Error("everything else should still follow the size threshold")
	}
}
