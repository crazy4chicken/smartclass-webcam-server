// Package storage provides object storage backends for recorded stream segments.
package storage

import (
	"context"
	"time"
)

// ObjectStorage stores stream segment payloads and grants temporary download access.
type ObjectStorage interface {
	// Upload stores data under key with the given content type.
	Upload(ctx context.Context, key string, data []byte, contentType string) error
	// GetDownloadURL returns a pre-signed URL granting read access to key until expiry.
	GetDownloadURL(ctx context.Context, key string, expiry time.Duration) (string, error)
	// Delete removes the object stored under key.
	Delete(ctx context.Context, key string) error
}
