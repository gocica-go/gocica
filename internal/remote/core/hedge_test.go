package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	myio "github.com/mazrean/gocica/internal/pkg/io"
	"github.com/mazrean/gocica/log"
)

// shape is how one request behaves: a throttle (chunk bytes per interval; zero
// chunk is as fast as possible), a stall after some bytes, or a failure after
// some bytes.
type shape struct {
	chunk      int
	interval   time.Duration
	stallAfter int64
	failAfter  int64
}

var fast = shape{}

// shapedClient serves a blob with a behaviour per request ordinal (1-based),
// which is enough to replay the doc's straggler: a contended phase where every
// stream is slow, then one lone stream far below what the link just did.
type shapedClient struct {
	blob          []byte
	shapes        map[int64]shape
	fallbackShape shape
	release       chan struct{}

	calls    atomic.Int64
	locker   sync.Mutex
	requests []shapedRequest
}

type shapedRequest struct {
	offset, size int64
	cancelled    bool
}

func newShapedClient(blob []byte) *shapedClient {
	return &shapedClient{blob: blob, shapes: map[int64]shape{}, release: make(chan struct{})}
}

func (c *shapedClient) GetURL(context.Context) (string, error) { return "blob://shaped", nil }

func (c *shapedClient) DownloadBlockBuffer(_ context.Context, offset, size int64, buf []byte) error {
	copy(buf, c.blob[offset:min(offset+size, int64(len(c.blob)))])

	return nil
}

func (c *shapedClient) DownloadBlock(ctx context.Context, offset, size int64, w io.Writer) error {
	n := c.calls.Add(1)
	c.locker.Lock()
	idx := len(c.requests)
	c.requests = append(c.requests, shapedRequest{offset: offset, size: size})
	c.locker.Unlock()

	sh, ok := c.shapes[n]
	if !ok {
		sh = c.fallbackShape
	}
	data := c.blob[offset:min(offset+size, int64(len(c.blob)))]

	cancelled := func() error {
		c.locker.Lock()
		c.requests[idx].cancelled = true
		c.locker.Unlock()

		return ctx.Err()
	}
	write := func(p []byte) error {
		chunk := sh.chunk
		if chunk <= 0 {
			chunk = 64 << 10
		}
		for len(p) > 0 {
			step := min(len(p), chunk)
			if _, err := w.Write(p[:step]); err != nil {
				return err
			}
			p = p[step:]
			if sh.interval > 0 {
				select {
				case <-time.After(sh.interval):
				case <-ctx.Done():
					return cancelled()
				}
			} else if ctx.Err() != nil {
				return cancelled()
			}
		}

		return nil
	}

	switch {
	case sh.failAfter > 0:
		if err := write(data[:min(sh.failAfter, int64(len(data)))]); err != nil {
			return err
		}

		return errors.New("connection reset")
	case sh.stallAfter > 0:
		head := min(sh.stallAfter, int64(len(data)))
		if err := write(data[:head]); err != nil {
			return err
		}
		select {
		case <-c.release:
			return write(data[head:])
		case <-ctx.Done():
			return cancelled()
		}
	default:
		return write(data)
	}
}

func (c *shapedClient) snapshot() []shapedRequest {
	c.locker.Lock()
	defer c.locker.Unlock()

	return append([]shapedRequest(nil), c.requests...)
}

func testBlob(size int) []byte {
	blob := make([]byte, size)
	for i := range blob {
		blob[i] = byte(i * 7)
	}

	return blob
}

func newHedgeDownloader(client DownloadClient) *Downloader {
	return &Downloader{logger: log.DefaultLogger, client: client}
}

const (
	peakStreams    = 4
	peakStreamSize = 8 << 20
)

// establishPeak replays the contended phase: several streams in flight, each
// throttled alike, so the link's peak is on record before the test's own
// stream starts. Requests 1..peakStreams are those streams.
func establishPeak(t *testing.T, d *Downloader, client *shapedClient) {
	t.Helper()

	// 64 KiB every 10ms per stream: ~6 MB/s each, ~25 MB/s aggregate, for
	// about 1.3s -- long enough for the link meter to see it.
	for i := range int64(peakStreams) {
		client.shapes[i+1] = shape{chunk: 64 << 10, interval: 10 * time.Millisecond}
	}

	var wg sync.WaitGroup
	errs := make([]error, peakStreams)
	for i := range peakStreams {
		wg.Go(func() {
			offset := int64(i * peakStreamSize)
			errs[i] = d.downloadRange(t.Context(), offset, peakStreamSize, io.Discard)
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("peak phase: %v", err)
		}
	}
	if client.calls.Load() != peakStreams {
		t.Fatalf("peak phase hedged: %d requests", client.calls.Load())
	}
	if peak := d.stats.peakRate(); peak < hedgeMinPeak {
		t.Fatalf("peak rate %.0f B/s after the contended phase, want at least %d", peak, hedgeMinPeak)
	}
}

// The doc's shape: after the contended phase, one lone stream at a fraction
// of the link's peak. Its spare, on a fresh connection, is fast.
func TestDownloadRange_HedgesTheStraggler(t *testing.T) {
	t.Parallel()

	blob := testBlob(peakStreams*peakStreamSize + 2<<20)
	client := newShapedClient(blob)
	d := newHedgeDownloader(client)
	establishPeak(t, d, client)

	// Request 5 is the straggler: 64 KiB every 100ms, ~0.6 MB/s.
	client.shapes[peakStreams+1] = shape{chunk: 64 << 10, interval: 100 * time.Millisecond}
	offset := int64(peakStreams * peakStreamSize)
	size := int64(len(blob)) - offset

	var out bytes.Buffer
	started := time.Now()
	if err := d.downloadRange(t.Context(), offset, size, &out); err != nil {
		t.Fatalf("downloadRange: %v", err)
	}
	if !bytes.Equal(out.Bytes(), blob[offset:]) {
		t.Fatalf("bytes differ: got %d bytes, want %d", out.Len(), size)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("took %s; unhedged the straggler needs ~3.2s", elapsed)
	}

	reqs := client.snapshot()[peakStreams:]
	if len(reqs) != 2 {
		t.Fatalf("got %d requests for the straggler, want 2 (original + spare): %+v", len(reqs), reqs)
	}
	if !reqs[0].cancelled {
		t.Errorf("the straggler was not cancelled")
	}
	if reqs[1].offset <= offset || reqs[1].offset+reqs[1].size != offset+size {
		t.Errorf("spare requested [%d, %d), want the remainder of [%d, %d)", reqs[1].offset, reqs[1].offset+reqs[1].size, offset, offset+size)
	}
	if got := d.stats.hedges.Load(); got != 1 {
		t.Errorf("hedges = %d, want 1", got)
	}
}

func TestDownloadRange_SpliceLandsInsideAJoinedWriter(t *testing.T) {
	t.Parallel()

	// Two outputs in one range; the straggler is cancelled somewhere inside the
	// first, and the splice has to cross into the second without duplicating
	// or dropping a byte.
	blob := testBlob(peakStreams*peakStreamSize + 3<<20)
	client := newShapedClient(blob)
	d := newHedgeDownloader(client)
	establishPeak(t, d, client)

	client.shapes[peakStreams+1] = shape{chunk: 64 << 10, interval: 100 * time.Millisecond}
	offset := int64(peakStreams * peakStreamSize)
	first, second := newBufferCloser(), newBufferCloser()
	jw := myio.NewJoinedWriter(
		myio.WriterWithSize{Writer: first, Size: 1 << 20},
		myio.WriterWithSize{Writer: second, Size: 2 << 20},
	)

	if err := d.downloadRange(t.Context(), offset, 3<<20, jw); err != nil {
		t.Fatalf("downloadRange: %v", err)
	}
	if !bytes.Equal(first.Bytes(), blob[offset:offset+1<<20]) {
		t.Errorf("first output differs")
	}
	if !bytes.Equal(second.Bytes(), blob[offset+1<<20:]) {
		t.Errorf("second output differs")
	}
	if got := d.stats.hedges.Load(); got != 1 {
		t.Errorf("hedges = %d, want 1", got)
	}
}

func TestDownloadRange_ResumesAfterABrokenBody(t *testing.T) {
	t.Parallel()

	blob := testBlob(1 << 20)
	client := newShapedClient(blob)
	client.shapes[1] = shape{failAfter: 100 << 10}
	d := newHedgeDownloader(client)

	var out bytes.Buffer
	if err := d.downloadRange(t.Context(), 0, int64(len(blob)), &out); err != nil {
		t.Fatalf("downloadRange: %v", err)
	}
	if !bytes.Equal(out.Bytes(), blob) {
		t.Fatalf("bytes differ")
	}
	reqs := client.snapshot()
	if len(reqs) != 2 || reqs[1].offset != 100<<10 {
		t.Fatalf("requests = %+v, want a resume from the failure point", reqs)
	}
	if d.stats.retries.Load() != 1 || d.stats.hedges.Load() != 0 {
		t.Errorf("retries = %d, hedges = %d", d.stats.retries.Load(), d.stats.hedges.Load())
	}
}

func TestDownloadRange_GivesUpAfterRepeatedFailures(t *testing.T) {
	t.Parallel()

	blob := testBlob(1 << 20)
	client := newShapedClient(blob)
	client.fallbackShape = shape{failAfter: 1 << 10}
	d := newHedgeDownloader(client)

	err := d.downloadRange(t.Context(), 0, int64(len(blob)), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := client.calls.Load(); got != hedgeMaxRetries+1 {
		t.Errorf("requests = %d, want %d", got, hedgeMaxRetries+1)
	}
}

func TestDownloadRange_DoesNotRetryASinkError(t *testing.T) {
	t.Parallel()

	blob := testBlob(64 << 10)
	client := newShapedClient(blob)
	d := newHedgeDownloader(client)

	sinkErr := errors.New("disk full")
	err := d.downloadRange(t.Context(), 0, int64(len(blob)), &errorWriter{err: sinkErr})
	if !errors.Is(err, sinkErr) {
		t.Fatalf("err = %v, want the sink's", err)
	}
	if got := client.calls.Load(); got != 1 {
		t.Errorf("requests = %d, want 1: a failing sink must not be re-requested", got)
	}
}

func TestDownloadRange_ParentCancelWins(t *testing.T) {
	t.Parallel()

	blob := testBlob(1 << 20)
	client := newShapedClient(blob)
	client.shapes[1] = shape{stallAfter: 10 << 10}
	d := newHedgeDownloader(client)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := d.downloadRange(ctx, 0, int64(len(blob)), &bytes.Buffer{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := client.calls.Load(); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestDownloadRange_NoHedgeWhenLittleRemains(t *testing.T) {
	t.Parallel()

	// Stalled with less than hedgeMinRemaining left: not worth a request.
	blob := testBlob(peakStreams*peakStreamSize + 512<<10)
	client := newShapedClient(blob)
	d := newHedgeDownloader(client)
	establishPeak(t, d, client)

	client.shapes[peakStreams+1] = shape{stallAfter: 400 << 10}
	offset := int64(peakStreams * peakStreamSize)

	done := make(chan error, 1)
	go func() {
		done <- d.downloadRange(t.Context(), offset, 512<<10, &bytes.Buffer{})
	}()

	// Long enough for the watchdog to have hedged if it were going to.
	time.Sleep(hedgeFloor + rateWindow + 3*hedgeTick)
	if got := client.calls.Load(); got != peakStreams+1 {
		t.Fatalf("requests = %d, want %d", got, peakStreams+1)
	}
	close(client.release)
	if err := <-done; err != nil {
		t.Fatalf("downloadRange: %v", err)
	}
}

func TestDownloadRange_HedgeCap(t *testing.T) {
	t.Parallel()

	// Every request after the contended phase is a straggler; the cap bounds
	// how many spares are tried before the range is left to finish on its own.
	// 3 MiB at ~0.6 MB/s outlasts three hedge floors.
	blob := testBlob(peakStreams*peakStreamSize + 3<<20)
	client := newShapedClient(blob)
	d := newHedgeDownloader(client)
	establishPeak(t, d, client)

	client.fallbackShape = shape{chunk: 64 << 10, interval: 100 * time.Millisecond}
	offset := int64(peakStreams * peakStreamSize)

	var out bytes.Buffer
	if err := d.downloadRange(t.Context(), offset, 3<<20, &out); err != nil {
		t.Fatalf("downloadRange: %v", err)
	}
	if !bytes.Equal(out.Bytes(), blob[offset:]) {
		t.Fatalf("bytes differ")
	}
	if got := client.calls.Load() - peakStreams; got != hedgeMaxPerRange+1 {
		t.Fatalf("requests = %d, want the original plus %d spares", got, hedgeMaxPerRange)
	}
}

func TestRateMeter(t *testing.T) {
	t.Parallel()

	var m rateMeter
	base := time.Now()
	if _, ok := m.observe(base, 0); ok {
		t.Fatal("a single sample must not yield a rate")
	}
	if _, ok := m.observe(base.Add(100*time.Millisecond), 1<<20); ok {
		t.Fatal("less than rateMinSpan of history must not yield a rate")
	}
	rate, ok := m.observe(base.Add(rateMinSpan), 5<<20)
	if !ok {
		t.Fatal("expected a rate")
	}
	want := float64(5<<20) / rateMinSpan.Seconds()
	if rate != want {
		t.Errorf("rate = %.0f, want %.0f", rate, want)
	}
	// A quiet window later, the rate drops but the peak stays.
	rate, _ = m.observe(base.Add(rateMinSpan+2*rateWindow), 5<<20)
	if rate != 0 || m.peak != want {
		t.Errorf("after a quiet window rate = %.0f, peak = %.0f, want 0 and %.0f", rate, m.peak, want)
	}
}

type bufferCloser struct {
	bytes.Buffer
}

func newBufferCloser() *bufferCloser { return &bufferCloser{} }

func (b *bufferCloser) Close() error { return nil }

type errorWriter struct{ err error }

func (w *errorWriter) Write([]byte) (int, error) { return 0, w.err }
