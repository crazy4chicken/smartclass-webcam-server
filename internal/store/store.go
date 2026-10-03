// Package store provides PostgreSQL-backed persistence for cameras, streams
// and their segments.
package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned by store methods when the requested row does not
// exist. Callers can test for it with errors.Is.
var ErrNotFound = errors.New("store: not found")

const (
	defaultListLimit = 100
	maxListLimit     = 1000
)

// Store groups the persistence stores used by the server.
type Store struct {
	Camera  *CameraStore
	Stream  *StreamStore
	Segment *SegmentStore
}

// New returns a Store whose sub-stores all share pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{
		Camera:  NewCameraStore(pool),
		Stream:  NewStreamStore(pool),
		Segment: NewSegmentStore(pool),
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
