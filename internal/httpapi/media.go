package httpapi

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/storage"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/store"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/ws"
)

const (
	// flushInterval is the maximum time a frame buffer lives before being
	// flushed to object storage.
	flushInterval = 5 * time.Second
	// flushFrameCount triggers a flush when this many frames accumulate.
	flushFrameCount = 150
	// storageTimeout bounds one object storage upload and its store write.
	storageTimeout = 30 * time.Second
	// segmentContentType is the content type of flushed segment payloads.
	segmentContentType = "application/octet-stream"
	// photoContentType is the only content type a stored photo can carry:
	// still images are JPEG by protocol.
	photoContentType = "image/jpeg"
)

// jpegOrEmpty normalizes the content type a device declared for a photo frame
// and reports whether the server accepts it. An absent value means the
// canonical one, because the protocol fixes photos to JPEG; any other value is
// rejected.
func jpegOrEmpty(declared string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(declared))
	switch normalized {
	case "", photoContentType:
		return photoContentType, true
	}
	return normalized, false
}

// mediaManager accumulates recording frames per stream and flushes them to
// object storage as segments; it also stores photos pushed by devices. Frames
// arrive through the hub handlers on the device read goroutines, so handlers
// only buffer and signal.
type mediaManager struct {
	store    *store.Store
	objects  storage.ObjectStorage
	registry *ws.Registry

	mu   sync.Mutex
	accs map[string]*streamAccumulator // stream id -> accumulator
}

// newMediaManager wires a media manager to the hub handlers.
func newMediaManager(st *store.Store, objects storage.ObjectStorage, registry *ws.Registry, hub *ws.Hub) *mediaManager {
	m := &mediaManager{
		store:    st,
		objects:  objects,
		registry: registry,
		accs:     make(map[string]*streamAccumulator),
	}
	hub.SetRecordingHandler(m.handleRecording)
	hub.SetPhotoHandler(m.handlePhoto)
	return m
}

// StartStream registers the accumulator of a newly started stream.
func (m *mediaManager) StartStream(stream domain.Stream) {
	acc := &streamAccumulator{
		streamID:   stream.ID,
		deviceID:   stream.DeviceID,
		cameraEnum: stream.CameraEnum,
		fps:        stream.Metadata.FPS,
		buf:        make([][]byte, 0, flushFrameCount),
		flushCh:    make(chan struct{}, 1),
		stop:       make(chan struct{}),
		finished:   make(chan struct{}),
		manager:    m,
	}

	m.mu.Lock()
	previous := m.accs[stream.ID]
	m.accs[stream.ID] = acc
	m.mu.Unlock()

	if previous != nil {
		previous.stopAndDrain()
	}
	go acc.flushLoop()
}

// StopStream stops the stream's accumulator and flushes the frames still
// buffered. It waits for the final flush unless ctx expires first.
func (m *mediaManager) StopStream(ctx context.Context, stream domain.Stream) error {
	m.mu.Lock()
	acc := m.accs[stream.ID]
	delete(m.accs, stream.ID)
	m.mu.Unlock()

	if acc == nil {
		return nil
	}

	acc.stopAndDrain()
	select {
	case <-acc.finished:
		return acc.flushError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StopDevice drains and finalizes every recording of a device that went
// offline. Streams are marked failed: they were not stopped by a command.
func (m *mediaManager) StopDevice(ctx context.Context, deviceID string) {
	m.mu.Lock()
	accs := make([]*streamAccumulator, 0, len(m.accs))
	for id, acc := range m.accs {
		if acc.deviceID == deviceID {
			accs = append(accs, acc)
			delete(m.accs, id)
		}
	}
	m.mu.Unlock()

	for _, acc := range accs {
		acc.stopAndDrain()
	}
	for _, acc := range accs {
		select {
		case <-acc.finished:
		case <-ctx.Done():
		}
	}

	finishCtx, cancel := context.WithTimeout(context.Background(), storageTimeout)
	defer cancel()
	for _, acc := range accs {
		if err := acc.flushError(); err != nil {
			slog.Error("final flush of disconnected device failed",
				"device_id", deviceID, "stream_id", acc.streamID, "error", err)
		}
		if _, err := m.store.Streams.Finish(finishCtx, acc.streamID, domain.StreamStatusFailed); err != nil {
			slog.Error("finish stream of disconnected device",
				"device_id", deviceID, "stream_id", acc.streamID, "error", err)
		}
	}
}

// handleRecording buffers a recording frame for its stream. Frames of unknown
// cameras or streams are dropped.
func (m *mediaManager) handleRecording(deviceID, streamID string, cameraEnum int, seq int64, ts time.Time, data []byte) {
	if !m.cameraLive(deviceID, cameraEnum) {
		slog.Debug("discarding frame for unknown camera",
			"device_id", deviceID, "camera_enum", cameraEnum, "stream_id", streamID)
		return
	}

	m.mu.Lock()
	acc := m.accs[streamID]
	m.mu.Unlock()
	if acc == nil || acc.deviceID != deviceID || acc.cameraEnum != cameraEnum {
		slog.Debug("discarding frame for unknown stream",
			"device_id", deviceID, "stream_id", streamID, "camera_enum", cameraEnum)
		return
	}
	acc.push(data, seq, ts)
}

// handlePhoto stores a still image pushed by a device. The row is written only
// after the upload succeeded. Still images are JPEG by protocol, so the
// declared content type is either the canonical one or absent, and anything
// else is discarded.
func (m *mediaManager) handlePhoto(deviceID string, cameraEnum int, requestID, contentType string, ts time.Time, data []byte) {
	if !m.cameraLive(deviceID, cameraEnum) {
		slog.Debug("discarding photo for unknown camera", "device_id", deviceID, "camera_enum", cameraEnum)
		return
	}
	contentType, ok := jpegOrEmpty(contentType)
	if !ok {
		slog.Warn("discarding photo with an unsupported content type",
			"device_id", deviceID, "camera_enum", cameraEnum, "content_type", contentType)
		return
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	photoID := ulid.Make().String()
	key := fmt.Sprintf("%s/photos/%s", deviceID, photoID)

	ctx, cancel := context.WithTimeout(context.Background(), storageTimeout)
	defer cancel()

	if err := m.objects.Upload(ctx, key, data, contentType); err != nil {
		slog.Error("upload photo", "device_id", deviceID, "photo_id", photoID, "error", err)
		return
	}

	photo := &domain.Photo{
		ID:          photoID,
		DeviceID:    deviceID,
		CameraEnum:  cameraEnum,
		StorageKey:  key,
		ContentType: contentType,
		SizeBytes:   int64(len(data)),
		RequestID:   requestID,
		TakenAt:     ts,
	}
	if _, err := m.store.Photos.Create(ctx, photo); err != nil {
		slog.Error("store photo", "device_id", deviceID, "photo_id", photoID, "error", err)
		return
	}
	slog.Debug("photo stored", "device_id", deviceID, "photo_id", photoID, "size_bytes", photo.SizeBytes)
}

// cameraLive reports whether cameraEnum belongs to the device's current
// registration.
func (m *mediaManager) cameraLive(deviceID string, cameraEnum int) bool {
	reg, ok := m.registry.Current(deviceID)
	if !ok {
		return false
	}
	_, ok = reg.Camera(cameraEnum)
	return ok
}

// streamAccumulator collects frames for one active stream and flushes them to
// object storage as segments.
type streamAccumulator struct {
	streamID   string
	deviceID   string
	cameraEnum int
	fps        int
	manager    *mediaManager

	buf      [][]byte
	seqStart int64
	seqEnd   int64
	frames   int
	firstTS  time.Time
	lastTS   time.Time
	flushErr error

	flushCh  chan struct{}
	stop     chan struct{}
	finished chan struct{}
	stopped  bool
	stopOnce sync.Once
	mu       sync.Mutex
}

// push adds one frame to the buffer. Triggers an asynchronous flush when the
// buffer is full.
func (a *streamAccumulator) push(data []byte, seq int64, ts time.Time) {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	if a.frames == 0 {
		a.seqStart = seq
		a.firstTS = ts
	}
	a.seqEnd = seq
	a.lastTS = ts
	a.frames++
	a.buf = append(a.buf, data)
	full := len(a.buf) >= flushFrameCount
	a.mu.Unlock()

	if full {
		select {
		case a.flushCh <- struct{}{}:
		default:
		}
	}
}

// stopAndDrain asks the flush loop to upload anything still buffered and exit.
// It is safe to call more than once; frames pushed afterwards are dropped.
func (a *streamAccumulator) stopAndDrain() {
	a.mu.Lock()
	a.stopped = true
	a.mu.Unlock()

	a.stopOnce.Do(func() {
		close(a.stop)
	})
}

// flushLoop drains the buffer on a timer, on demand and once more on stop.
func (a *streamAccumulator) flushLoop() {
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
			a.flush()
			return
		}
	}
}

// flush uploads the buffered frames as one segment and resets the buffer.
// Concurrent flushes split the buffer between them.
func (a *streamAccumulator) flush() {
	a.mu.Lock()
	if len(a.buf) == 0 {
		a.mu.Unlock()
		return
	}
	data := a.buf
	seqStart := a.seqStart
	seqEnd := a.seqEnd
	frames := a.frames
	firstTS := a.firstTS
	lastTS := a.lastTS
	a.buf = make([][]byte, 0, flushFrameCount)
	a.seqStart = 0
	a.seqEnd = 0
	a.frames = 0
	a.firstTS = time.Time{}
	a.lastTS = time.Time{}
	a.mu.Unlock()

	payload := joinFrames(data)
	key := segmentKey(a.deviceID, a.streamID, seqStart)

	ctx, cancel := context.WithTimeout(context.Background(), storageTimeout)
	defer cancel()

	if err := a.manager.objects.Upload(ctx, key, payload, segmentContentType); err != nil {
		a.setFlushError(fmt.Errorf("upload segment %s: %w", key, err))
		slog.Error("upload segment", "device_id", a.deviceID, "stream_id", a.streamID, "key", key, "error", err)
		return
	}

	seg := &domain.StreamSegment{
		StreamID:   a.streamID,
		DeviceID:   a.deviceID,
		CameraEnum: a.cameraEnum,
		SegmentSeq: int(seqStart),
		StorageKey: key,
		SizeBytes:  int64(len(payload)),
		DurationMS: segmentDuration(frames, a.fps, firstTS, lastTS),
	}
	created, err := a.manager.store.Segments.Create(ctx, seg)
	if err != nil {
		a.setFlushError(fmt.Errorf("store segment %s: %w", key, err))
		slog.Error("store segment", "device_id", a.deviceID, "stream_id", a.streamID, "error", err)
		return
	}

	a.setFlushError(nil)
	slog.Debug("segment flushed",
		"device_id", a.deviceID,
		"stream_id", a.streamID,
		"segment_id", created.ID,
		"frames", frames,
		"seq_range", fmt.Sprintf("%d-%d", seqStart, seqEnd),
		"size_bytes", created.SizeBytes,
	)
}

// flushError returns the error of the most recent failed flush, if any.
func (a *streamAccumulator) flushError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.flushErr
}

// setFlushError records the outcome of the most recent flush.
func (a *streamAccumulator) setFlushError(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.flushErr = err
}

// joinFrames concatenates frames with a 4-byte big-endian length prefix before
// each one so consumers can split them again.
func joinFrames(frames [][]byte) []byte {
	size := 0
	for _, frame := range frames {
		size += 4 + len(frame)
	}

	out := make([]byte, 0, size)
	var prefix [4]byte
	for _, frame := range frames {
		binary.BigEndian.PutUint32(prefix[:], uint32(len(frame)))
		out = append(out, prefix[:]...)
		out = append(out, frame...)
	}
	return out
}

// segmentDuration estimates a segment's duration in milliseconds: frames and
// session fps when the fps is known, otherwise the timestamp delta between the
// first and the last frame.
func segmentDuration(frames, fps int, first, last time.Time) int {
	if fps > 0 {
		return frames * 1000 / fps
	}
	if !first.IsZero() && last.After(first) {
		return int(last.Sub(first).Milliseconds())
	}
	return 0
}

// segmentKey builds the object storage key of a segment.
func segmentKey(deviceID, streamID string, seqStart int64) string {
	ts := time.Now().UTC().Format("20060102T150405")
	return fmt.Sprintf("%s/streams/%s/%s_%d.bin", deviceID, streamID, ts, seqStart)
}
