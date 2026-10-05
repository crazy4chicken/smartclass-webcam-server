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
const streamColumns = `id, device_id, camera_enum, status, started_at, ended_at, metadata`

// StreamStore provides persistence for recording streams.
type StreamStore struct {
	pool *pgxpool.Pool
}

// NewStreamStore returns a StreamStore backed by pool.
func NewStreamStore(pool *pgxpool.Pool) *StreamStore {
	return &StreamStore{pool: pool}
}

// Create starts a new stream for one camera of deviceID with a generated ULID
// and returns the stored row. New streams start out active; it returns
// ErrConflict when the device already has an active stream for that camera.
func (s *StreamStore) Create(ctx context.Context, deviceID string, cameraEnum int, md domain.StreamMetadata) (*domain.Stream, error) {
	metaJSON, err := json.Marshal(md)
	if err != nil {
		return nil, fmt.Errorf("encode stream metadata: %w", err)
	}

	st := &domain.Stream{
		ID:         ulid.Make().String(),
		DeviceID:   deviceID,
		CameraEnum: cameraEnum,
		Status:     domain.StreamStatusActive,
		Metadata:   md,
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO streams (id, device_id, camera_enum, status, metadata)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING started_at`,
		st.ID, st.DeviceID, st.CameraEnum, string(st.Status), metaJSON,
	).Scan(&st.StartedAt)
	if isUniqueViolation(err) {
		return nil, fmt.Errorf("stream for device %s camera %d: %w", deviceID, cameraEnum, ErrConflict)
	}
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

// ListByDevice returns the streams of deviceID, newest first. A limit of zero
// or less applies a default limit.
func (s *StreamStore) ListByDevice(ctx context.Context, deviceID string, limit int) ([]domain.Stream, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+streamColumns+` FROM streams WHERE device_id = $1 ORDER BY started_at DESC, id DESC LIMIT $2`,
		deviceID, normalizeLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list streams for device %s: %w", deviceID, err)
	}
	defer rows.Close()

	streams := make([]domain.Stream, 0)
	for rows.Next() {
		st, err := scanStream(rows)
		if err != nil {
			return nil, fmt.Errorf("list streams for device %s: %w", deviceID, err)
		}
		streams = append(streams, *st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list streams for device %s: %w", deviceID, err)
	}
	return streams, nil
}

// Active returns the active stream of one camera of deviceID. It returns
// ErrNotFound when no such stream exists.
func (s *StreamStore) Active(ctx context.Context, deviceID string, cameraEnum int) (*domain.Stream, error) {
	st, err := scanStream(s.pool.QueryRow(ctx,
		`SELECT `+streamColumns+` FROM streams
		 WHERE device_id = $1 AND camera_enum = $2 AND status = 'active'
		 ORDER BY started_at DESC LIMIT 1`,
		deviceID, cameraEnum))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("active stream for device %s camera %d: %w", deviceID, cameraEnum, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("active stream for device %s camera %d: %w", deviceID, cameraEnum, err)
	}
	return st, nil
}

// Finish marks the stream completed or failed and records ended_at. It returns
// ErrNotFound when the stream does not exist.
func (s *StreamStore) Finish(ctx context.Context, id string, status domain.StreamStatus) (*domain.Stream, error) {
	st, err := scanStream(s.pool.QueryRow(ctx, `
		UPDATE streams
		SET status = $2, ended_at = COALESCE(ended_at, now())
		WHERE id = $1
		RETURNING `+streamColumns,
		id, string(status)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("stream %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("finish stream %s: %w", id, err)
	}
	return st, nil
}

// FinalizeOrphans marks every stream still active from an earlier server
// process as failed and records ended_at. A stream's accumulator lives in
// process memory only, so after a restart any active row belongs to a dead
// connection; left alone it would keep blocking new recordings for its
// camera. Call it at startup, before device connections are accepted.
func (s *StreamStore) FinalizeOrphans(ctx context.Context) ([]domain.Stream, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE streams
		SET status = $1, ended_at = COALESCE(ended_at, now())
		WHERE status = $2
		RETURNING `+streamColumns,
		string(domain.StreamStatusFailed), string(domain.StreamStatusActive))
	if err != nil {
		return nil, fmt.Errorf("finalize orphaned streams: %w", err)
	}
	defer rows.Close()

	streams := make([]domain.Stream, 0)
	for rows.Next() {
		st, err := scanStream(rows)
		if err != nil {
			return nil, fmt.Errorf("finalize orphaned streams: %w", err)
		}
		streams = append(streams, *st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("finalize orphaned streams: %w", err)
	}
	return streams, nil
}

// scanStream reads one row selected with streamColumns.
func scanStream(row pgx.Row) (*domain.Stream, error) {
	var (
		st       domain.Stream
		status   string
		metadata []byte
	)
	if err := row.Scan(&st.ID, &st.DeviceID, &st.CameraEnum, &status, &st.StartedAt, &st.EndedAt, &metadata); err != nil {
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
