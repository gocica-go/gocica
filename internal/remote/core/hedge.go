package core

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mazrean/gocica/internal/pkg/metrics"
)

// Hedged re-requests.
//
// Measured on ubuntu-latest, a 730 MB blob arrives over ~50 range requests that
// all answer within 0.5s and deliver 850 MB in 2s -- and then one throttled
// connection trickles the last ~130 MB at 15-40 MB/s for 3-5s. Nothing about the
// request is wrong; that one TCP path is slow. So a stream that is getting a
// small fraction of what the link is known to deliver is re-requested from the
// byte it got to, on a fresh connection, and the slow one is cancelled once the
// new one answers.
//
// "Slow" is judged against the link, not against the other streams: while fifty
// of them share the link each gets a fiftieth of it, and the straggler at the
// end is faster than any of those were. What gives it away is that it is alone
// and still far below what the link just did. So the reference is the peak
// aggregate rate seen so far, divided by the number of streams in flight, and a
// stream is slow when its own recent rate is a fraction of that share.
//
// The same resume-from-offset loop doubles as bounded retry for a body that
// breaks mid-transfer, which previously failed the whole chunk.
const (
	// hedgeFloor is how old a stream must be before it can be judged. It sits
	// above the measured time to first byte (~0.5s), so a stream that is merely
	// waiting for its headers is never hedged.
	hedgeFloor = time.Second
	// hedgeSlowFraction: a stream is slow when its recent rate is below this
	// fraction of its fair share of the peak link rate.
	hedgeSlowFraction = 4
	// hedgeMinRemaining: below this a fresh request costs more than it saves.
	hedgeMinRemaining = 256 << 10
	// hedgeMinPeak keeps a link that has barely moved from judging anything.
	hedgeMinPeak = 1 << 20 // bytes per second
	// hedgeMaxPerRange bounds how often one range may be re-requested for being
	// slow, and hedgeMaxRetries how often for failing outright.
	hedgeMaxPerRange = 3
	hedgeMaxRetries  = 3
	hedgeTick        = 100 * time.Millisecond
	// rateWindow is how far back a rate looks. Shorter reacts faster and
	// flickers more.
	rateWindow = time.Second
	// rateMinSpan is the least history a rate is computed over.
	rateMinSpan     = 500 * time.Millisecond
	spareBufferSize = 64 << 10
)

var hedgeGauge = metrics.NewGauge("remote_download_hedge")

var errSpareAbandoned = errors.New("spare stream abandoned")

// rateMeter computes a rate over a sliding window from a monotonic byte count.
type rateMeter struct {
	samples []rateSample
	peak    float64
}

type rateSample struct {
	at    time.Time
	bytes int64
}

// observe records the count and returns the rate over the window. ok is false
// until the window holds enough history.
func (m *rateMeter) observe(now time.Time, bytes int64) (rate float64, ok bool) {
	m.samples = append(m.samples, rateSample{at: now, bytes: bytes})
	for len(m.samples) > 1 && now.Sub(m.samples[1].at) >= rateWindow {
		m.samples = m.samples[1:]
	}

	oldest := m.samples[0]
	span := now.Sub(oldest.at)
	if span < rateMinSpan {
		return 0, false
	}

	rate = float64(bytes-oldest.bytes) / span.Seconds()
	m.peak = max(m.peak, rate)

	return rate, true
}

// transferStats is what one Downloader knows about its link.
type transferStats struct {
	totalBytes atomic.Int64
	active     atomic.Int64

	linkLocker sync.Mutex
	link       rateMeter

	ranges      atomic.Int64
	hedges      atomic.Int64
	hedgedBytes atomic.Int64
	retries     atomic.Int64

	slowLocker  sync.Mutex
	slowestRate float64
	slowestSize int64
	slowestTime time.Duration
}

// linkRate samples the link and returns its current and peak rate.
func (s *transferStats) linkRate(now time.Time) (rate, peak float64, ok bool) {
	s.linkLocker.Lock()
	defer s.linkLocker.Unlock()

	rate, ok = s.link.observe(now, s.totalBytes.Load())

	return rate, s.link.peak, ok
}

func (s *transferStats) peakRate() float64 {
	s.linkLocker.Lock()
	defer s.linkLocker.Unlock()

	return s.link.peak
}

// recordRange remembers the slowest completed range, for the summary.
func (s *transferStats) recordRange(size int64, elapsed time.Duration) {
	if elapsed <= 0 {
		return
	}
	rate := float64(size) / elapsed.Seconds()

	s.slowLocker.Lock()
	defer s.slowLocker.Unlock()

	if s.slowestTime == 0 || rate < s.slowestRate {
		s.slowestRate, s.slowestSize, s.slowestTime = rate, size, elapsed
	}
}

// Summary is one line on what the link did, for the daemon log: without it a
// run with no hedges cannot be told from a run where the detector missed.
func (s *transferStats) summary() string {
	s.slowLocker.Lock()
	defer s.slowLocker.Unlock()

	return fmt.Sprintf("%d ranges, %d MB, peak link rate %.0f MB/s, slowest range %d KB in %s (%.1f MB/s), hedged %d (%d MB re-requested), retried %d",
		s.ranges.Load(), s.totalBytes.Load()>>20, s.peakRate()/(1<<20),
		s.slowestSize>>10, s.slowestTime.Round(time.Millisecond), s.slowestRate/(1<<20),
		s.hedges.Load(), s.hedgedBytes.Load()>>20, s.retries.Load())
}

// countingWriter counts the bytes the sink accepted, which is where a
// replacement stream has to resume from, and remembers the sink's own error so
// a failing disk is never mistaken for a failing connection.
type countingWriter struct {
	w     io.Writer
	total *atomic.Int64
	n     atomic.Int64
	err   error
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	c.total.Add(int64(n))
	if err != nil {
		c.err = err
	}

	return n, err
}

func (c *countingWriter) count() int64 {
	return c.n.Load()
}

// downloadRange streams [offset, offset+size) of the blob into w.
func (d *Downloader) downloadRange(ctx context.Context, offset, size int64, w io.Writer) error {
	if size <= 0 {
		return nil
	}

	d.stats.ranges.Add(1)
	d.stats.active.Add(1)
	defer d.stats.active.Add(-1)
	started := time.Now()
	sink := &countingWriter{w: w, total: &d.stats.totalBytes}

	var (
		spare   *spareStream
		hedges  int
		retries int
	)
	for sink.count() < size {
		if err := ctx.Err(); err != nil {
			spare.close()

			return err
		}

		from := sink.count()
		streamCtx, cancelStream := context.WithCancel(ctx)
		watch := d.watchStream(ctx, streamCtx, cancelStream, offset, size, sink, hedges < hedgeMaxPerRange)

		var err error
		if spare != nil {
			err = spare.drainInto(streamCtx, sink, offset+from)
			spare = nil
		} else {
			err = d.client.DownloadBlock(streamCtx, offset+from, size-from, sink)
		}
		cancelStream()
		spare = watch.stop()

		if sink.err != nil {
			spare.close()

			return fmt.Errorf("write block: %w", sink.err)
		}
		if err == nil && sink.count() == size {
			break
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			spare.close()

			return ctxErr
		}

		if spare != nil && spare.answered() {
			// The watchdog cancelled this stream because the spare answered.
			hedges++
			d.stats.hedges.Add(1)
			d.stats.hedgedBytes.Add(size - sink.count())
			hedgeGauge.Set(float64(size-sink.count()), "bytes")
			d.logger.Debugf("hedged range at offset %d: %d of %d bytes re-requested after %s", offset, size-sink.count(), size, time.Since(started))

			continue
		}
		// A spare that never answered is no use to a stream that failed.
		spare.close()
		spare = nil

		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		retries++
		d.stats.retries.Add(1)
		if retries > hedgeMaxRetries {
			return fmt.Errorf("download block: %w", err)
		}
		d.logger.Debugf("download range at offset %d failed after %d of %d bytes: %v. resuming.", offset, sink.count(), size, err)
	}
	spare.close()

	d.stats.recordRange(size, time.Since(started))

	return nil
}

// spareStream is a replacement request, started at the byte the running stream
// had reached, that is only adopted once its first byte has arrived.
type spareStream struct {
	offset int64 // absolute blob offset of its first byte
	cancel context.CancelFunc
	pr     *io.PipeReader
	br     *bufio.Reader
	// peeked closes once the first byte, or an error, has arrived; nothing else
	// touches br before then.
	peeked  chan struct{}
	peekErr error
}

func (d *Downloader) startSpare(ctx context.Context, offset, count int64) *spareStream {
	streamCtx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	s := &spareStream{
		offset: offset,
		cancel: cancel,
		pr:     pr,
		br:     bufio.NewReaderSize(pr, spareBufferSize),
		peeked: make(chan struct{}),
	}

	go func() {
		err := d.client.DownloadBlock(streamCtx, offset, count, pw)
		if err == nil {
			// An early EOF is caught by the byte count downstream.
			err = io.EOF
		}
		pw.CloseWithError(err)
	}()
	go func() {
		defer close(s.peeked)
		_, s.peekErr = s.br.Peek(1)
	}()

	return s
}

// answered reports whether the first byte has arrived.
func (s *spareStream) answered() bool {
	select {
	case <-s.peeked:
		return s.peekErr == nil
	default:
		return false
	}
}

// drainInto writes the spare's bytes into sink, skipping what the stream it
// replaces had already delivered by the time it was cancelled. from is the
// absolute blob offset to resume at.
func (s *spareStream) drainInto(ctx context.Context, sink io.Writer, from int64) error {
	defer s.close()
	stop := context.AfterFunc(ctx, s.cancel)
	defer stop()

	// Never read br while the peek still owns it.
	<-s.peeked
	if s.peekErr != nil {
		return fmt.Errorf("spare stream: %w", s.peekErr)
	}
	if from < s.offset {
		return fmt.Errorf("spare stream starts at %d, after the resume point %d", s.offset, from)
	}
	if _, err := io.CopyN(io.Discard, s.br, from-s.offset); err != nil {
		return fmt.Errorf("skip delivered bytes: %w", err)
	}
	if _, err := io.Copy(sink, s.br); err != nil {
		return fmt.Errorf("copy spare stream: %w", err)
	}

	return nil
}

// close is nil-safe: it releases the request and its goroutines whether or not
// the spare was ever read.
func (s *spareStream) close() {
	if s == nil {
		return
	}
	s.cancel()
	_ = s.pr.CloseWithError(errSpareAbandoned)
}

// streamWatch is the watchdog over one running stream.
type streamWatch struct {
	stopCh chan struct{}
	done   chan struct{}
	spare  chan *spareStream
}

// watchStream judges the running stream against the link and, when it has
// fallen far enough behind, starts a spare for the remainder. The running
// stream is cancelled only once the spare has answered, so a spare that fails
// costs nothing.
func (d *Downloader) watchStream(
	ctx, streamCtx context.Context,
	cancelStream context.CancelFunc,
	offset, size int64,
	sink *countingWriter,
	allowHedge bool,
) *streamWatch {
	w := &streamWatch{
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
		spare:  make(chan *spareStream, 1),
	}

	go func() {
		defer close(w.done)
		if !allowHedge {
			return
		}

		started := time.Now()
		var own rateMeter
		ticker := time.NewTicker(hedgeTick)
		defer ticker.Stop()

		for {
			select {
			case <-w.stopCh:
				return
			case <-streamCtx.Done():
				return
			case <-ticker.C:
			}

			now := time.Now()
			count := sink.count()
			ownRate, ownOK := own.observe(now, count)
			_, peak, linkOK := d.stats.linkRate(now)

			remaining := size - count
			if remaining < hedgeMinRemaining || now.Sub(started) < hedgeFloor {
				continue
			}
			if !ownOK || !linkOK || peak < hedgeMinPeak {
				continue
			}
			share := peak / float64(max(d.stats.active.Load(), 1))
			if ownRate >= share/hedgeSlowFraction {
				continue
			}

			d.logger.Debugf("range at offset %d is slow: %.1f MB/s with %d bytes left, link peaked at %.0f MB/s over %d streams. requesting a spare.",
				offset, ownRate/(1<<20), remaining, peak/(1<<20), d.stats.active.Load())
			spare := d.startSpare(ctx, offset+count, remaining)
			select {
			case <-spare.peeked:
				if spare.peekErr != nil {
					d.logger.Debugf("spare request for offset %d failed: %v", offset+count, spare.peekErr)
					spare.close()

					return
				}
				w.spare <- spare
				cancelStream()
			case <-w.stopCh:
				w.spare <- spare
			case <-streamCtx.Done():
				w.spare <- spare
			}

			return
		}
	}()

	return w
}

// stop ends the watch and hands back the spare it started, if any. The caller
// adopts it when the stream was cancelled for it, and closes it otherwise.
func (w *streamWatch) stop() *spareStream {
	close(w.stopCh)
	<-w.done

	select {
	case s := <-w.spare:
		return s
	default:
		return nil
	}
}
