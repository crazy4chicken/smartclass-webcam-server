// Package store provides PostgreSQL-backed persistence for devices, streams,
// segments and photos.
package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned by store methods when the requested row does not
// exist. Callers can test for it with errors.Is.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned by store methods when a write violates a uniqueness
// constraint. Callers can test for it with errors.Is.
var ErrConflict = errors.New("store: conflict")

const (
	defaultListLimit = 100
	maxListLimit     = 1000
)

// Store groups the persistence stores used by the server.
type Store struct {
	Devices  *DeviceStore
	Streams  *StreamStore
	Segments *SegmentStore
	Photos   *PhotoStore
}

// New returns a Store whose sub-stores all share pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{
		Devices:  NewDeviceStore(pool),
		Streams:  NewStreamStore(pool),
		Segments: NewSegmentStore(pool),
		Photos:   NewPhotoStore(pool),
	}
}

// normalizeLimit turns a caller supplied limit into a usable LIMIT value.
func normalizeLimit(limit int) int {
	if limit <= 0 {
		return defaultListLimit
	}
	if limit > maxListLimit {
		return maxListLimit
	}
	return limit
}

// isUniqueViolation reports whether err is a PostgreSQL unique constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
