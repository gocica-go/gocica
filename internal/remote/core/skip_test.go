package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"github.com/mazrean/gocica/log"
	"google.golang.org/protobuf/proto"
)

// byteBlobClient serves a real blob so the offsets the chunker computes are
// checked against the bytes, not against an expectation written by hand.
type byteBlobClient struct {
	blob  []byte
	reads atomic.Int64
}

func (c *byteBlobClient) GetURL(context.Context) (string, error) { return "blob://test", nil }

// A range past the end is answered with what exists, as the storage does.
func (c *byteBlobClient) slice(offset, size int64) []byte {
	return c.blob[min(offset, int64(len(c.blob))):min(offset+size, int64(len(c.blob)))]
}

func (c *byteBlobClient) DownloadBlock(_ context.Context, offset, size int64, w io.Writer) error {
	c.reads.Add(1)
	_, err := w.Write(c.slice(offset, size))

	return err
}

func (c *byteBlobClient) DownloadBlockBuffer(_ context.Context, offset, size int64, buf []byte) error {
	copy(buf, c.slice(offset, size))

	return nil
}

func newByteBlob(t *testing.T, contents []string) *byteBlobClient {
	t.Helper()

	cache := &v1.ActionsCache{Entries: map[string]*v1.IndexEntry{}}
	body := &bytes.Buffer{}
	for i, content := range contents {
		cache.Outputs = append(cache.Outputs, &v1.ActionsOutput{
			Id:     string(rune('a' + i)),
			Offset: int64(body.Len()),
			Size:   int64(len(content)),
		})
		body.WriteString(content)
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

	return &byteBlobClient{blob: blob}
}

type collectingWriter struct {
	buf bytes.Buffer
}

func (w *collectingWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *collectingWriter) Close() error                { return nil }

func TestDownloadAllOutputBlocks_SkipsWhatIsAlreadyLocal(t *testing.T) {
	t.Parallel()

	contents := []string{"aaaa", "bbbbbb", "cc", "dddddddd", "e"}
	ids := []string{"a", "b", "c", "d", "e"}

	tests := []struct {
		name    string
		skipped []string
	}{
		{name: "nothing skipped", skipped: nil},
		// An object in the middle splits the read in two; get the offsets wrong
		// and every object after it is silently filled with the wrong bytes.
		{name: "one in the middle", skipped: []string{"c"}},
		{name: "the first", skipped: []string{"a"}},
		{name: "the last", skipped: []string{"e"}},
		{name: "two adjacent", skipped: []string{"b", "c"}},
		{name: "two apart", skipped: []string{"b", "d"}},
		{name: "everything", skipped: ids},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := newByteBlob(t, contents)
			downloader, err := NewDownloader(t.Context(), log.DefaultLogger, client)
			// The header read is a read too; only the outputs are under test.
			client.reads.Store(0)
			if err != nil {
				t.Fatalf("new downloader: %v", err)
			}

			var locker sync.Mutex
			got := map[string]*collectingWriter{}
			err = downloader.DownloadAllOutputBlocks(t.Context(), func(_ context.Context, objectID string) (io.WriteCloser, error) {
				locker.Lock()
				defer locker.Unlock()

				w := &collectingWriter{}
				got[objectID] = w

				return w, nil
			}, func(objectID string) bool {
				return slices.Contains(tt.skipped, objectID)
			})
			if err != nil {
				t.Fatalf("download all output blocks: %v", err)
			}

			for i, id := range ids {
				w, ok := got[id]
				if slices.Contains(tt.skipped, id) {
					if ok {
						t.Errorf("%s was transferred even though it was already local", id)
					}

					continue
				}
				if !ok {
					t.Errorf("%s was not transferred", id)

					continue
				}
				if w.buf.String() != contents[i] {
					t.Errorf("%s = %q, want %q", id, w.buf.String(), contents[i])
				}
			}

			if len(tt.skipped) == len(ids) && client.reads.Load() != 0 {
				t.Errorf("issued %d reads with nothing to fetch, want 0", client.reads.Load())
			}
		})
	}
}
