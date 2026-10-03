package storage

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// bucketInitTimeout bounds the bucket check and creation performed on startup.
const bucketInitTimeout = 30 * time.Second

// S3Storage stores objects in an S3-compatible bucket through the MinIO client.
type S3Storage struct {
	client *minio.Client
	bucket string
}

var _ ObjectStorage = (*S3Storage)(nil)

// NewS3Storage connects to an S3-compatible endpoint and ensures bucket exists,
// creating it when it does not.
func NewS3Storage(endpoint, accessKey, secretKey, bucket string, useSSL bool) (*S3Storage, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create s3 client for %q: %w", endpoint, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), bucketInitTimeout)
	defer cancel()

	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("check bucket %q: %w", bucket, err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("create bucket %q: %w", bucket, err)
		}
	}

	return &S3Storage{client: client, bucket: bucket}, nil
}

// Upload stores data under key with the given content type.
func (s *S3Storage) Upload(ctx context.Context, key string, data []byte, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("upload %q: %w", key, err)
	}
	return nil
}

// GetDownloadURL returns a pre-signed GET URL for key that expires after expiry.
func (s *S3Storage) GetDownloadURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	target, err := s.client.PresignedGetObject(ctx, s.bucket, key, expiry, url.Values{})
	if err != nil {
		return "", fmt.Errorf("presign %q: %w", key, err)
	}
	return target.String(), nil
}

// Delete removes the object stored under key.
func (s *S3Storage) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete %q: %w", key, err)
	}
	return nil
}
