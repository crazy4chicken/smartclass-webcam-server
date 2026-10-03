package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
)

// streamColumns is selected into a domain.Stream; it must stay in sync with
// scanStream.
const streamColumns = `id, camera_id, status, started_at, ended_at, metadata`

// StreamStore provides persistence for recording streams.
type StreamStore struct {
	pool *pgxpool.Pool
}

// NewStreamStore returns a StreamStore backed by pool.
func NewStreamStore(pool *pgxpool.Pool) *StreamStore {
	return &StreamStore{pool: pool}
}

// Create starts a new stream for cameraID with a generated ULID and returns the
// stored row. New streams start out active.
func (s *StreamStore) Create(ctx context.Context, cameraID string, metadata domain.StreamMetadata) (*domain.Stream, error) {
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("encode stream metadata: %w", err)
	}

	st := &domain.Stream{
		ID:       ulid.Make().String(),
		CameraID: cameraID,
		Status:   domain.StreamStatusActive,
		Metadata: metadata,
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO streams (id, camera_id, status, metadata)
		VALUES ($1, $2, $3, $4)
		RETURNING started_at`,
		st.ID, st.CameraID, string(st.Status), metaJSON,
	).Scan(&st.StartedAt)
	if err != nil {
		return nil, fmt.Errorf("create stream: %w", err)
	}
	return st, nil
}

// Get returns the stream with the given id. It returns ErrNotFound when no such
// stream exists.
func (s *StreamStore) Get(ctx context.Context, id string) (*domain.Stream, error) {
	st, err := scanStream(s.pool.QueryRow(ctx,
		`SELECT `+streamColumns+` FROM streams WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("stream %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get stream %s: %w", id, err)
	}
	return st, nil
}

// ListByCamera returns the streams of cameraID, newest first. A limit of zero
// or less applies a default limit.
func (s *StreamStore) ListByCamera(ctx context.Context, cameraID string, limit int) ([]domain.Stream, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+streamColumns+` FROM streams WHERE camera_id = $1 ORDER BY started_at DESC, id DESC LIMIT $2`,
		cameraID, normalizeLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list streams for camera %s: %w", cameraID, err)
	}
	defer rows.Close()

	streams := make([]domain.Stream, 0)
	for rows.Next() {
		st, err := scanStream(rows)
		if err != nil {
			return nil, fmt.Errorf("list streams for camera %s: %w", cameraID, err)
		}
		streams = append(streams, *st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list streams for camera %s: %w", cameraID, err)
	}
	return streams, nil
}

// UpdateStatus sets the stream status. Marking a stream completed or failed
// records ended_at; marking it active again clears it. It returns ErrNotFound
// when the stream does not exist.
func (s *StreamStore) UpdateStatus(ctx context.Context, id string, status domain.StreamStatus) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE streams
		SET status = $2,
		    ended_at = CASE WHEN $2 = 'active' THEN NULL ELSE COALESCE(ended_at, now()) END
		WHERE id = $1`,
		id, string(status))
	if err != nil {
		return fmt.Errorf("update stream %s status: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("stream %s: %w", id, ErrNotFound)
	}
	return nil
}

// scanStream reads one row selected with streamColumns.
func scanStream(row pgx.Row) (*domain.Stream, error) {
	var (
		st       domain.Stream
		status   string
		metadata []byte
	)
	if err := row.Scan(&st.ID, &st.CameraID, &status, &st.StartedAt, &st.EndedAt, &metadata); err != nil {
		return nil, err
	}
	st.Status = domain.StreamStatus(status)
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &st.Metadata); err != nil {
			return nil, fmt.Errorf("decode stream metadata: %w", err)
		}
	}
	return &st, nil
}
