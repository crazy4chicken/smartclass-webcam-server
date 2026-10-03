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

// cameraColumns is selected into a domain.Camera; it must stay in sync with
// scanCamera. location is coalesced because domain.Camera.Location is a plain
// string.
const cameraColumns = `id, name, COALESCE(location, '') AS location, status, config, last_seen, created_at, updated_at`

// CameraStore provides persistence for cameras.
type CameraStore struct {
	pool *pgxpool.Pool
}

// NewCameraStore returns a CameraStore backed by pool.
func NewCameraStore(pool *pgxpool.Pool) *CameraStore {
	return &CameraStore{pool: pool}
}

// List returns every camera, newest first.
func (s *CameraStore) List(ctx context.Context) ([]domain.Camera, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+cameraColumns+` FROM cameras ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list cameras: %w", err)
	}
	defer rows.Close()

	cameras := make([]domain.Camera, 0)
	for rows.Next() {
		cam, err := scanCamera(rows)
		if err != nil {
			return nil, fmt.Errorf("list cameras: %w", err)
		}
		cameras = append(cameras, *cam)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list cameras: %w", err)
	}
	return cameras, nil
}

// Get returns the camera with the given id. It returns ErrNotFound when no
// such camera exists.
func (s *CameraStore) Get(ctx context.Context, id string) (*domain.Camera, error) {
	cam, err := scanCamera(s.pool.QueryRow(ctx,
		`SELECT `+cameraColumns+` FROM cameras WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("camera %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get camera %s: %w", id, err)
	}
	return cam, nil
}

// Create inserts a new camera with a generated ULID and returns the stored row.
// New cameras start out offline.
func (s *CameraStore) Create(ctx context.Context, name, location string, cfg domain.CameraConfig) (*domain.Camera, error) {
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("encode camera config: %w", err)
	}

	cam := &domain.Camera{
		ID:       ulid.Make().String(),
		Name:     name,
		Location: location,
		Status:   domain.CameraStatusOffline,
		Config:   cfg,
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO cameras (id, name, location, status, config)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at, updated_at`,
		cam.ID, cam.Name, cam.Location, string(cam.Status), cfgJSON,
	).Scan(&cam.CreatedAt, &cam.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create camera: %w", err)
	}
	return cam, nil
}

// Update stores the mutable fields of cam and refreshes cam.UpdatedAt. It
// returns ErrNotFound when the camera no longer exists. last_seen is owned by
// UpdateStatus and is left untouched.
func (s *CameraStore) Update(ctx context.Context, cam *domain.Camera) error {
	if cam == nil {
		return errors.New("store: nil camera")
	}
	cfgJSON, err := json.Marshal(cam.Config)
	if err != nil {
		return fmt.Errorf("encode camera config: %w", err)
	}

	err = s.pool.QueryRow(ctx, `
		UPDATE cameras
		SET name = $2, location = $3, status = $4, config = $5, updated_at = now()
		WHERE id = $1
		RETURNING updated_at`,
		cam.ID, cam.Name, cam.Location, string(cam.Status), cfgJSON,
	).Scan(&cam.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("camera %s: %w", cam.ID, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("update camera %s: %w", cam.ID, err)
	}
	return nil
}

// Delete removes the camera with the given id together with its streams and
// segments (ON DELETE CASCADE). It returns ErrNotFound when no row matched.
func (s *CameraStore) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM cameras WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete camera %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("camera %s: %w", id, ErrNotFound)
	}
	return nil
}

// UpdateStatus sets the camera status and refreshes last_seen and updated_at in
// a single statement. It returns ErrNotFound when the camera does not exist.
func (s *CameraStore) UpdateStatus(ctx context.Context, id string, status domain.CameraStatus) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE cameras
		SET status = $2, last_seen = now(), updated_at = now()
		WHERE id = $1`,
		id, string(status))
	if err != nil {
		return fmt.Errorf("update camera %s status: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("camera %s: %w", id, ErrNotFound)
	}
	return nil
}

// UpdateConfig replaces the camera configuration and refreshes updated_at. It
// returns ErrNotFound when the camera does not exist.
func (s *CameraStore) UpdateConfig(ctx context.Context, id string, cfg domain.CameraConfig) error {
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode camera config: %w", err)
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE cameras
		SET config = $2, updated_at = now()
		WHERE id = $1`,
		id, cfgJSON)
	if err != nil {
		return fmt.Errorf("update camera %s config: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("camera %s: %w", id, ErrNotFound)
	}
	return nil
}

// scanCamera reads one row selected with cameraColumns.
func scanCamera(row pgx.Row) (*domain.Camera, error) {
	var (
		cam    domain.Camera
		status string
		config []byte
	)
	if err := row.Scan(&cam.ID, &cam.Name, &cam.Location, &status, &config,
		&cam.LastSeen, &cam.CreatedAt, &cam.UpdatedAt); err != nil {
		return nil, err
	}
	cam.Status = domain.CameraStatus(status)
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cam.Config); err != nil {
			return nil, fmt.Errorf("decode camera config: %w", err)
		}
	}
	return &cam, nil
}
