package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
)

// segmentColumns is selected into a domain.StreamSegment; it must stay in sync
// with scanSegment. duration_ms is coalesced because
// domain.StreamSegment.DurationMs is a plain int.
const segmentColumns = `id, stream_id, camera_id, segment_seq, storage_key, size_bytes, COALESCE(duration_ms, 0) AS duration_ms, created_at`

// SegmentStore provides persistence for uploaded stream segments.
type SegmentStore struct {
	pool *pgxpool.Pool
}

// NewSegmentStore returns a SegmentStore backed by pool.
func NewSegmentStore(pool *pgxpool.Pool) *SegmentStore {
	return &SegmentStore{pool: pool}
}

// Create inserts seg, assigning a ULID when seg.ID is empty, and fills in
// seg.CreatedAt with the database timestamp.
func (s *SegmentStore) Create(ctx context.Context, seg *domain.StreamSegment) error {
	if seg == nil {
		return errors.New("store: nil segment")
	}
	if seg.ID == "" {
		seg.ID = ulid.Make().String()
	}

	err := s.pool.QueryRow(ctx, `
		INSERT INTO stream_segments (id, stream_id, camera_id, segment_seq, storage_key, size_bytes, duration_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at`,
		seg.ID, seg.StreamID, seg.CameraID, seg.SegmentSeq, seg.StorageKey, seg.SizeBytes, seg.DurationMs,
	).Scan(&seg.CreatedAt)
	if err != nil {
		return fmt.Errorf("create segment %s: %w", seg.ID, err)
	}
	return nil
}

// Get returns the segment with the given id. It returns ErrNotFound when no
// such segment exists.
func (s *SegmentStore) Get(ctx context.Context, id string) (*domain.StreamSegment, error) {
	seg, err := scanSegment(s.pool.QueryRow(ctx,
		`SELECT `+segmentColumns+` FROM stream_segments WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("segment %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get segment %s: %w", id, err)
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
	if err := row.Scan(&seg.ID, &seg.StreamID, &seg.CameraID, &seg.SegmentSeq,
		&seg.StorageKey, &seg.SizeBytes, &seg.DurationMs, &seg.CreatedAt); err != nil {
		return nil, err
	}
	return &seg, nil
}
