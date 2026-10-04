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

// photoColumns is selected into a domain.Photo; it must stay in sync with
// scanPhoto. request_id is coalesced because domain.Photo.RequestID is a plain
// string.
const photoColumns = `id, device_id, camera_enum, storage_key, content_type, size_bytes, COALESCE(request_id, '') AS request_id, taken_at, created_at`

// PhotoStore provides persistence for captured photos.
type PhotoStore struct {
	pool *pgxpool.Pool
}

// NewPhotoStore returns a PhotoStore backed by pool.
func NewPhotoStore(pool *pgxpool.Pool) *PhotoStore {
	return &PhotoStore{pool: pool}
}

// Create inserts p, assigning a ULID and timestamps when they are zero, and
// returns the stored photo. The content type defaults to
// application/octet-stream.
func (s *PhotoStore) Create(ctx context.Context, p *domain.Photo) (*domain.Photo, error) {
	if p == nil {
		return nil, errors.New("store: nil photo")
	}
	if p.ID == "" {
		p.ID = ulid.Make().String()
	}
	now := time.Now().UTC()
	if p.TakenAt.IsZero() {
		p.TakenAt = now
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.ContentType == "" {
		p.ContentType = "application/octet-stream"
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO photos (id, device_id, camera_enum, storage_key, content_type, size_bytes, request_id, taken_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		p.ID, p.DeviceID, p.CameraEnum, p.StorageKey, p.ContentType, p.SizeBytes, p.RequestID, p.TakenAt, p.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create photo %s: %w", p.ID, err)
	}
	return p, nil
}

// Get returns the photo with the given id. It returns ErrNotFound when no such
// photo exists.
func (s *PhotoStore) Get(ctx context.Context, id string) (*domain.Photo, error) {
	p, err := scanPhoto(s.pool.QueryRow(ctx,
		`SELECT `+photoColumns+` FROM photos WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("photo %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get photo %s: %w", id, err)
	}
	return p, nil
}

// ListByDevice returns the photos of deviceID, newest first. A limit of zero or
// less applies a default limit.
func (s *PhotoStore) ListByDevice(ctx context.Context, deviceID string, limit int) ([]domain.Photo, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+photoColumns+` FROM photos WHERE device_id = $1 ORDER BY taken_at DESC, id DESC LIMIT $2`,
		deviceID, normalizeLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list photos for device %s: %w", deviceID, err)
	}
	defer rows.Close()

	photos := make([]domain.Photo, 0)
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, fmt.Errorf("list photos for device %s: %w", deviceID, err)
		}
		photos = append(photos, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list photos for device %s: %w", deviceID, err)
	}
	return photos, nil
}

// scanPhoto reads one row selected with photoColumns.
func scanPhoto(row pgx.Row) (*domain.Photo, error) {
	var p domain.Photo
	if err := row.Scan(&p.ID, &p.DeviceID, &p.CameraEnum, &p.StorageKey, &p.ContentType,
		&p.SizeBytes, &p.RequestID, &p.TakenAt, &p.CreatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}
