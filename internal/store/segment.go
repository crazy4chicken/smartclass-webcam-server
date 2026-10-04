package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
)

// segmentColumns is selected into a domain.StreamSegment; it must stay in sync
// with scanSegment. duration_ms is coalesced because
// domain.StreamSegment.DurationMS is a plain int.
const segmentColumns = `id, stream_id, device_id, camera_enum, segment_seq, storage_key, size_bytes, COALESCE(duration_ms, 0) AS duration_ms, created_at`

// SegmentStore provides persistence for uploaded stream segments.
type SegmentStore struct {
	pool *pgxpool.Pool
}

// NewSegmentStore returns a SegmentStore backed by pool.
func NewSegmentStore(pool *pgxpool.Pool) *SegmentStore {
	return &SegmentStore{pool: pool}
}

// Create inserts seg, assigning a ULID and a creation timestamp when they are
// zero, and returns the stored segment.
func (s *SegmentStore) Create(ctx context.Context, seg *domain.StreamSegment) (*domain.StreamSegment, error) {
	if seg == nil {
		return nil, errors.New("store: nil segment")
	}
	if seg.ID == "" {
		seg.ID = ulid.Make().String()
	}
	if seg.CreatedAt.IsZero() {
		seg.CreatedAt = time.Now().UTC()
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO stream_segments (id, stream_id, device_id, camera_enum, segment_seq, storage_key, size_bytes, duration_ms, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		seg.ID, seg.StreamID, seg.DeviceID, seg.CameraEnum, seg.SegmentSeq, seg.StorageKey, seg.SizeBytes, seg.DurationMS, seg.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create segment %s: %w", seg.ID, err)
	}
	return seg, nil
}

// ListByStream returns the segments of a stream in playback order. A limit of
// zero or less applies a default limit.
func (s *SegmentStore) ListByStream(ctx context.Context, streamID string, limit int) ([]domain.StreamSegment, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+segmentColumns+` FROM stream_segments WHERE stream_id = $1 ORDER BY segment_seq ASC LIMIT $2`,
		streamID, normalizeLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list segments for stream %s: %w", streamID, err)
	}
	defer rows.Close()

	segments := make([]domain.StreamSegment, 0)
	for rows.Next() {
		seg, err := scanSegment(rows)
		if err != nil {
			return nil, fmt.Errorf("list segments for stream %s: %w", streamID, err)
		}
		segments = append(segments, *seg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list segments for stream %s: %w", streamID, err)
	}
	return segments, nil
}

// scanSegment reads one row selected with segmentColumns.
func scanSegment(row pgx.Row) (*domain.StreamSegment, error) {
	var seg domain.StreamSegment
	if err := row.Scan(&seg.ID, &seg.StreamID, &seg.DeviceID, &seg.CameraEnum, &seg.SegmentSeq,
		&seg.StorageKey, &seg.SizeBytes, &seg.DurationMS, &seg.CreatedAt); err != nil {
		return nil, err
	}
	return &seg, nil
}
