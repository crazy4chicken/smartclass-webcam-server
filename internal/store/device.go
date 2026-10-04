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

// deviceColumns is selected into a domain.Device; it must stay in sync with
// scanDevice. The nullable columns are coalesced because their domain fields
// are plain strings.
const deviceColumns = `id, name, COALESCE(location, '') AS location, COALESCE(team_id, '') AS team_id, COALESCE(owner_id, '') AS owner_id, last_seen, created_at, updated_at, token_hash`

// DeviceFilter narrows DeviceStore.List by ownership scope. Scope "team"
// filters by TeamID and "own" by Subject; any other scope matches every
// device.
type DeviceFilter struct {
	Scope   string
	Subject string
	TeamID  string
	Limit   int
}

// DeviceUpdate carries the mutable fields of a device; nil fields are left
// unchanged.
type DeviceUpdate struct {
	Name     *string
	Location *string
	TeamID   *string
	OwnerID  *string
}

// DeviceStore provides persistence for registered devices.
type DeviceStore struct {
	pool *pgxpool.Pool
}

// NewDeviceStore returns a DeviceStore backed by pool.
func NewDeviceStore(pool *pgxpool.Pool) *DeviceStore {
	return &DeviceStore{pool: pool}
}

// Create inserts d, assigning a ULID and timestamps when they are zero, and
// returns the stored device.
func (s *DeviceStore) Create(ctx context.Context, d *domain.Device) (*domain.Device, error) {
	if d == nil {
		return nil, errors.New("store: nil device")
	}
	if d.ID == "" {
		d.ID = ulid.Make().String()
	}
	now := time.Now().UTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = now
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO devices (id, name, location, team_id, owner_id, last_seen, token_hash, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		d.ID, d.Name, d.Location, d.TeamID, d.OwnerID, d.LastSeen, d.TokenHash, d.CreatedAt, d.UpdatedAt)
	if isUniqueViolation(err) {
		return nil, fmt.Errorf("device %s: %w", d.ID, ErrConflict)
	}
	if err != nil {
		return nil, fmt.Errorf("create device %s: %w", d.ID, err)
	}
	return d, nil
}

// Get returns the device with the given id. It returns ErrNotFound when no
// such device exists.
func (s *DeviceStore) Get(ctx context.Context, id string) (*domain.Device, error) {
	d, err := scanDevice(s.pool.QueryRow(ctx,
		`SELECT `+deviceColumns+` FROM devices WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("device %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get device %s: %w", id, err)
	}
	return d, nil
}

// List returns the devices matching f, newest first. A limit of zero or less
// applies a default limit.
func (s *DeviceStore) List(ctx context.Context, f DeviceFilter) ([]domain.Device, error) {
	query := `SELECT ` + deviceColumns + ` FROM devices`
	args := make([]any, 0, 2)
	switch f.Scope {
	case "team":
		query += ` WHERE team_id = $1`
		args = append(args, f.TeamID)
	case "own":
		query += ` WHERE owner_id = $1`
		args = append(args, f.Subject)
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args)+1)
	args = append(args, normalizeLimit(f.Limit))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()

	devices := make([]domain.Device, 0)
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("list devices: %w", err)
		}
		devices = append(devices, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	return devices, nil
}

// Update patches the provided fields of the device and refreshes updated_at.
// It returns ErrNotFound when the device does not exist.
func (s *DeviceStore) Update(ctx context.Context, id string, upd DeviceUpdate) (*domain.Device, error) {
	d, err := scanDevice(s.pool.QueryRow(ctx, `
		UPDATE devices
		SET name       = COALESCE($2, name),
		    location   = COALESCE($3, location),
		    team_id    = COALESCE($4, team_id),
		    owner_id   = COALESCE($5, owner_id),
		    updated_at = now()
		WHERE id = $1
		RETURNING `+deviceColumns,
		id, upd.Name, upd.Location, upd.TeamID, upd.OwnerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("device %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("update device %s: %w", id, err)
	}
	return d, nil
}

// Delete removes the device with the given id together with its streams,
// segments and photos (ON DELETE CASCADE). It returns ErrNotFound when no row
// matched.
func (s *DeviceStore) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM devices WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete device %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("device %s: %w", id, ErrNotFound)
	}
	return nil
}

// SetTokenHash replaces the device token hash and refreshes updated_at. It
// returns ErrNotFound when the device does not exist.
func (s *DeviceStore) SetTokenHash(ctx context.Context, id string, hash []byte) (*domain.Device, error) {
	d, err := scanDevice(s.pool.QueryRow(ctx, `
		UPDATE devices
		SET token_hash = $2, updated_at = now()
		WHERE id = $1
		RETURNING `+deviceColumns,
		id, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("device %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("set device %s token hash: %w", id, err)
	}
	return d, nil
}

// Touch records seenAt as the device last_seen time. It returns ErrNotFound
// when the device does not exist.
func (s *DeviceStore) Touch(ctx context.Context, id string, seenAt time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE devices
		SET last_seen = $2, updated_at = now()
		WHERE id = $1`,
		id, seenAt)
	if err != nil {
		return fmt.Errorf("touch device %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("device %s: %w", id, ErrNotFound)
	}
	return nil
}

// scanDevice reads one row selected with deviceColumns.
func scanDevice(row pgx.Row) (*domain.Device, error) {
	var d domain.Device
	if err := row.Scan(&d.ID, &d.Name, &d.Location, &d.TeamID, &d.OwnerID,
		&d.LastSeen, &d.CreatedAt, &d.UpdatedAt, &d.TokenHash); err != nil {
		return nil, err
	}
	return &d, nil
}
