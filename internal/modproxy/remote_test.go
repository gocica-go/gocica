package modproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/internal/remote/core"
	"github.com/mazrean/gocica/log"
	"google.golang.org/protobuf/proto"
)

// blobClient serves a synthetic remote blob laid out the way core writes it:
// an 8-byte big-endian header length, the ActionsCache protobuf, then the
// outputs back to back.
type blobClient struct {
	blob  []byte
	reads atomic.Int64
	// gate, when non-nil, holds every read until it is closed.
	gate chan struct{}
	// started is signalled once per read, before the gate is consulted.
	started chan struct{}
}

func (c *blobClient) GetURL(context.Context) (string, error) { return "blob://test", nil }

func (c *blobClient) DownloadBlock(_ context.Context, offset, size int64, w io.Writer) error {
	c.reads.Add(1)
	if c.started != nil {
		select {
		case c.started <- struct{}{}:
		default:
		}
	}
	if c.gate != nil {
		<-c.gate
	}
	_, err := w.Write(c.blob[offset : offset+size])

	return err
}

func (c *blobClient) DownloadBlockBuffer(_ context.Context, offset, size int64, buf []byte) error {
	copy(buf, c.blob[offset:offset+size])

	return nil
}

// newBlob builds a blob holding the given objects, keyed in the index by request
// path. corrupt names an object whose stored bytes are damaged.
func newBlob(t *testing.T, objects map[string][]byte, corrupt string) *blobClient {
	t.Helper()

	cache := &v1.ActionsCache{Entries: map[string]*v1.IndexEntry{}}
	body := &bytes.Buffer{}

	for path, content := range objects {
		id := ObjectIDForContent(content)
		stored := content
		if path == corrupt {
			stored = append([]byte("damaged"), content...)
		}

		cache.Entries[path] = &v1.IndexEntry{OutputId: id, Size: int64(len(content))}
		cache.Outputs = append(cache.Outputs, &v1.ActionsOutput{
			Id:     id,
			Offset: int64(body.Len()),
			Size:   int64(len(stored)),
		})
		body.Write(stored)
	}
	cache.OutputTotalSize = int64(body.Len())

	header, err := proto.Marshal(cache)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}

	blob := make([]byte, 8, 8+len(header)+body.Len())
	binary.BigEndian.PutUint64(blob, uint64(len(header)))
	blob = append(blob, header...)
	blob = append(blob, body.Bytes()...)

	return &blobClient{blob: blob}
}

func newRemoteCache(t *testing.T, client *blobClient) *Cache {
	t.Helper()

	store, err := NewStore(StoreDir(t.TempDir()))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	downloader, err := core.NewDownloader(t.Context(), log.DefaultLogger, client)
	if err != nil {
		t.Fatalf("new downloader: %v", err)
	}

	cache, err := NewCache(t.Context(), log.DefaultLogger, store, downloader, nil, NewCompressionRegistry())
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}

	return cache
}

func TestCache_RestoresFromTheRemoteBlob(t *testing.T) {
	t.Parallel()

	objects := map[string][]byte{
		"example.com/m/@v/v1.0.0.mod": []byte("module example.com/m\n"),
		"example.com/m/@v/v1.0.0.zip": bytes.Repeat([]byte("zip"), 1000),
	}
	cache := newRemoteCache(t, newBlob(t, objects, ""))

	for path, want := range objects {
		f, size, ok := cache.Get(t.Context(), path)
		if !ok {
			t.Fatalf("%s: not restored", path)
		}

		got, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("%s: read: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: content mismatch", path)
		}
		if size != int64(len(want)) {
			t.Errorf("%s: size = %d, want %d", path, size, len(want))
		}
	}
}

func TestCache_RejectsCorruptRestoredBytes(t *testing.T) {
	t.Parallel()

	const corrupt = "example.com/m/@v/v1.0.0.zip"
	objects := map[string][]byte{
		corrupt:                       bytes.Repeat([]byte("zip"), 1000),
		"example.com/m/@v/v1.0.0.mod": []byte("module example.com/m\n"),
	}
	cache := newRemoteCache(t, newBlob(t, objects, corrupt))

	// Serving these bytes would make the go command fail the build with a
	// checksum error it cannot fall back from, so a miss is the only safe answer.
	if _, _, ok := cache.Get(t.Context(), corrupt); ok {
		t.Error("a corrupt object must not be served")
	}

	// The intact neighbour still works.
	f, _, ok := cache.Get(t.Context(), "example.com/m/@v/v1.0.0.mod")
	if !ok {
		t.Fatal("the intact object should still be restored")
	}
	_ = f.Close()
}

func TestCache_PrefetchWarmsEverything(t *testing.T) {
	t.Parallel()

	objects := map[string][]byte{}
	for i := range 20 {
		objects[string(rune('a'+i))+".example.com/m/@v/v1.0.0.zip"] = bytes.Repeat([]byte{byte(i)}, 128)
	}

	client := newBlob(t, objects, "")
	cache := newRemoteCache(t, client)

	cache.Prefetch(t.Context(), 8)

	if got := client.reads.Load(); got != int64(len(objects)) {
		t.Errorf("prefetch issued %d range reads, want %d", got, len(objects))
	}

	// Everything is local now, so serving must not touch the remote again.
	before := client.reads.Load()
	for path := range objects {
		f, _, ok := cache.Get(t.Context(), path)
		if !ok {
			t.Fatalf("%s: not warmed", path)
		}
		_ = f.Close()
	}
	if got := client.reads.Load(); got != before {
		t.Errorf("serving after prefetch issued %d extra range reads, want 0", got-before)
	}
}

func TestCache_PrefetchDisabledWithoutDownloader(t *testing.T) {
	t.Parallel()

	store, err := NewStore(StoreDir(t.TempDir()))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cache, err := NewCache(t.Context(), log.DefaultLogger, store, nil, nil, NewCompressionRegistry())
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}

	// Must not panic on a local-only cache.
	cache.Prefetch(t.Context(), 4)
}

func TestCache_RequestJoinsAnInFlightPrefetch(t *testing.T) {
	t.Parallel()

	const path = "example.com/m/@v/v1.0.0.zip"
	content := bytes.Repeat([]byte("zip"), 1000)

	client := newBlob(t, map[string][]byte{path: content}, "")
	client.gate = make(chan struct{})
	client.started = make(chan struct{}, 1)

	cache := newRemoteCache(t, client)

	prefetched := make(chan struct{})
	go func() {
		defer close(prefetched)
		cache.Prefetch(t.Context(), 4)
	}()

	// Wait until the prefetch is actually transferring this object.
	select {
	case <-client.started:
	case <-time.After(10 * time.Second):
		t.Fatal("prefetch never started a transfer")
	}

	// A request arriving now must join that transfer rather than start a second
	// one, and must answer as soon as it finishes.
	got := make(chan bool, 1)
	go func() {
		f, _, ok := cache.Get(t.Context(), path)
		if ok {
			_ = f.Close()
		}
		got <- ok
	}()

	select {
	case <-got:
		t.Fatal("the request answered before the transfer completed")
	case <-time.After(100 * time.Millisecond):
	}

	close(client.gate)

	select {
	case ok := <-got:
		if !ok {
			t.Error("the request did not get the object")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the request never answered")
	}
	<-prefetched

	if reads := client.reads.Load(); reads != 1 {
		t.Errorf("the object was transferred %d times, want 1", reads)
	}
}
