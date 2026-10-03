package storage

import (
	"context"
	"time"
)

// NoopStorage discards uploads and exposes no downloadable objects. It is used
// when object storage credentials are not configured.
type NoopStorage struct{}

var _ ObjectStorage = NoopStorage{}

// Upload succeeds without storing anything.
func (NoopStorage) Upload(ctx context.Context, key string, data []byte, contentType string) error {
	return nil
}

// GetDownloadURL returns an empty URL because no objects are ever stored.
func (NoopStorage) GetDownloadURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	return "", nil
}

// Delete succeeds without doing anything.
func (NoopStorage) Delete(ctx context.Context, key string) error {
	return nil
}
