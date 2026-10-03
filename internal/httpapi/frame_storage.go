package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/store"
)

const (
	// flushInterval is the maximum time a frame buffer lives before being
	// flushed to object storage.
	flushInterval = 5 * time.Second
	// flushFrameCount triggers a flush when this many frames accumulate.
	flushFrameCount = 150
	// storageTimeout bounds the S3 upload and DB write.
	storageTimeout = 30 * time.Second
)

// frameAccumulator collects frames for one active stream and flushes them to
// object storage as segments.
type frameAccumulator struct {
	streamID   string
	cameraID   string
	buf        [][]byte
	seqStart   int
	seqEnd     int
	flushCh    chan struct{}
	stop       chan struct{} // closed once by the owner to request shutdown
	finished   chan struct{} // closed by the flusher after its final drain
	stopped    bool          // set under mu; drops frames after stopAndDrain
	stopOnce   sync.Once
	cancel     func() // unregisters the hub frame handler
	mu         sync.Mutex
	store      *store.Store
	storage    storageRef
	resolution string
	fps        int
}

// stopAndDrain asks the flusher to upload anything still buffered and exit.
// It is safe to call from any path and more than once; frames pushed after the
// call are dropped so a dead accumulator cannot grow without bound.
func (a *frameAccumulator) stopAndDrain() {
	a.mu.Lock()
	a.stopped = true
	a.mu.Unlock()
	a.stopOnce.Do(func() {
		close(a.stop)
		if a.cancel != nil {
			a.cancel()
		}
	})
}

// storageRef is the subset of storage.ObjectStorage used by the accumulator.
type storageRef interface {
	Upload(ctx context.Context, key string, data []byte, contentType string) error
}

// startFramePipeline registers a per-stream frame accumulator on the hub. Each
// active stream gets its own accumulator so concurrent streams never block one
// another. The accumulator is torn down when the stream stops; the stop handler
// signals completion via the returned channel.
func (s *server) startFramePipeline(stream *domain.Stream) *frameAccumulator {
	acc := &frameAccumulator{
		streamID:   stream.ID,
		cameraID:   stream.CameraID,
		buf:        make([][]byte, 0, flushFrameCount),
		seqStart:   -1,
		stop:       make(chan struct{}),
		finished:   make(chan struct{}),
		flushCh:    make(chan struct{}, 1),
		store:      s.store,
		storage:    s.storage,
		resolution: stream.Metadata.Resolution,
		fps:        stream.Metadata.FPS,
	}

	// The hub calls dispatchFrame on the client's read goroutine, so this
	// handler must be fast and never block. It simply pushes the frame into the
	// accumulator and triggers an async flush when full.
	acc.cancel = s.hub.OnFrame(func(cameraID, streamID string, seq int, data []byte, ts time.Time) {
		if streamID != acc.streamID {
			return
		}
		acc.push(data, seq)
	})

	// Background flusher: runs on a timer and also on explicit flush signals.
	go acc.flusher()

	return acc
}

// push adds one frame to the buffer. It is safe to call from multiple
// goroutines (the hub's frame dispatch may fan out to several handlers).
func (a *frameAccumulator) push(data []byte, seq int) {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	if a.seqStart < 0 {
		a.seqStart = seq
	}
	a.seqEnd = seq
	a.buf = append(a.buf, data)
	shouldFlush := len(a.buf) >= flushFrameCount
	a.mu.Unlock()

	if shouldFlush {
		select {
		case a.flushCh <- struct{}{}:
		default:
		}
	}
}

// flusher drains the frame buffer at least every flushInterval and on demand.
// It exits after a final drain once stop is closed.
func (a *frameAccumulator) flusher() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	defer close(a.finished)

	for {
		select {
		case <-ticker.C:
			a.flush()
		case <-a.flushCh:
			a.flush()
		case <-a.stop:
			a.flush() // final drain
			return
		}
	}
}

// flush uploads the accumulated frames as one segment, then resets the buffer.
func (a *frameAccumulator) flush() {
	a.mu.Lock()
	if len(a.buf) == 0 {
		a.mu.Unlock()
		return
	}
	data := a.buf
	seqStart := a.seqStart
	seqEnd := a.seqEnd
	a.buf = make([][]byte, 0, flushFrameCount)
	a.seqStart = -1
	a.mu.Unlock()

	payload := joinFrames(data)
	key := storageKey(a.cameraID, a.streamID, seqStart)

	ctx, cancel := context.WithTimeout(context.Background(), storageTimeout)
	defer cancel()

	if err := a.storage.Upload(ctx, key, payload, "application/octet-stream"); err != nil {
		slog.Error("upload segment", "stream_id", a.streamID, "key", key, "error", err)
		return
	}

	frames := len(data)
	duration := 0
	if a.fps > 0 {
		duration = (frames * 1000) / a.fps
	}

	seg := &domain.StreamSegment{
		StreamID:   a.streamID,
		CameraID:   a.cameraID,
		SegmentSeq: seqStart,
		StorageKey: key,
		SizeBytes:  int64(len(payload)),
		DurationMs: duration,
	}
	if err := a.store.Segment.Create(ctx, seg); err != nil {
		slog.Error("store segment", "stream_id", a.streamID, "error", err)
		return
	}

	slog.Debug("segment flushed",
		"stream_id", a.streamID,
		"frames", frames,
		"seq_range", fmt.Sprintf("%d-%d", seqStart, seqEnd),
		"size_bytes", seg.SizeBytes,
	)
}

// joinFrames concatenates raw frame data with a simple binary delimiter so the
// consumer can split them back.
func joinFrames(frames [][]byte) []byte {
	if len(frames) == 1 {
		return frames[0]
	}
	// 4-byte big-endian length prefix before each frame.
	var buf bytes.Buffer
	scratch := make([]byte, 4)
	for _, f := range frames {
		n := uint32(len(f))
		scratch[0] = byte(n >> 24)
		scratch[1] = byte(n >> 16)
		scratch[2] = byte(n >> 8)
		scratch[3] = byte(n)
		buf.Write(scratch)
		buf.Write(f)
	}
	return buf.Bytes()
}

// storageKey builds the object storage key for a segment.
func storageKey(cameraID, streamID string, seqStart int) string {
	ts := time.Now().UTC().Format("20060102T150405")
	return fmt.Sprintf("%s/%s/%s_%d.bin", cameraID, streamID, ts, seqStart)
}
