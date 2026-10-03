package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// sha256Header carries the content digest filehouse uses for deduplication and
// integrity checking on upload.
const sha256Header = "X-Filehouse-SHA256"

// TokenProvider yields a bearer token for filehouse API calls. Implementations
// are expected to refresh expired credentials themselves.
type TokenProvider interface {
	Token(ctx context.Context) (string, error)
}

// FilehouseStorage stores stream segments in an nsc-filehouse instance. The
// service authenticates with a teamusers bearer token and authorizes every call
// against the filehouse permission catalog.
type FilehouseStorage struct {
	baseURL    string
	bucket     string
	tokens     TokenProvider
	httpClient *http.Client
}

// NewFilehouseStorage builds a storage backend for the filehouse instance at
// baseURL. bucket must already exist; the service does not create buckets.
func NewFilehouseStorage(baseURL, bucket string, tokens TokenProvider, httpClient *http.Client) (*FilehouseStorage, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("storage: filehouse base URL is required")
	}
	if strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("storage: filehouse bucket is required")
	}
	if tokens == nil {
		return nil, fmt.Errorf("storage: filehouse requires a token provider")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &FilehouseStorage{
		baseURL:    strings.TrimRight(baseURL, "/"),
		bucket:     bucket,
		tokens:     tokens,
		httpClient: httpClient,
	}, nil
}

var _ ObjectStorage = (*FilehouseStorage)(nil)

// Upload stores data at key. The key may contain slashes; each segment is
// escaped so the path structure survives.
func (s *FilehouseStorage) Upload(ctx context.Context, key string, data []byte, contentType string) error {
	digest := sha256.Sum256(data)

	req, err := s.newRequest(ctx, http.MethodPut, s.objectPath(key), bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set(sha256Header, hex.EncodeToString(digest[:]))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.ContentLength = int64(len(data))

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("storage: filehouse upload %s: %w", key, err)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("storage: filehouse upload %s: status %d: %s", key, resp.StatusCode, readSnippet(resp.Body))
	}
	return nil
}

// GetDownloadURL asks filehouse for a presigned GET link valid for expiry.
func (s *FilehouseStorage) GetDownloadURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	ttl := int(expiry / time.Second)
	if ttl <= 0 {
		ttl = 900
	}

	body, err := json.Marshal(map[string]any{
		"bucket":      s.bucket,
		"key":         key,
		"method":      http.MethodGet,
		"ttl_seconds": ttl,
	})
	if err != nil {
		return "", fmt.Errorf("storage: encode presign request: %w", err)
	}

	req, err := s.newRequest(ctx, http.MethodPost, "/api/v1/presign", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("storage: filehouse presign %s: %w", key, err)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("storage: filehouse presign %s: status %d: %s", key, resp.StatusCode, readSnippet(resp.Body))
	}

	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("storage: decode presign response: %w", err)
	}
	if out.URL == "" {
		return "", fmt.Errorf("storage: filehouse presign %s: empty url", key)
	}
	if strings.HasPrefix(out.URL, "/") {
		out.URL = s.baseURL + out.URL
	}
	return out.URL, nil
}

// Delete removes the object at key.
func (s *FilehouseStorage) Delete(ctx context.Context, key string) error {
	req, err := s.newRequest(ctx, http.MethodDelete, s.objectPath(key), nil)
	if err != nil {
		return err
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("storage: filehouse delete %s: %w", key, err)
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("storage: filehouse delete %s: status %d: %s", key, resp.StatusCode, readSnippet(resp.Body))
	}
	return nil
}

// newRequest builds a request with the filehouse base URL joined to path and a
// fresh bearer token attached.
func (s *FilehouseStorage) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	token, err := s.tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage: filehouse token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("storage: build filehouse request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// objectPath renders the object endpoint for key, escaping each segment while
// preserving the slash separators that form the object's key namespace.
func (s *FilehouseStorage) objectPath(key string) string {
	segments := strings.Split(strings.Trim(key, "/"), "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return "/api/v1/buckets/" + url.PathEscape(s.bucket) + "/objects/" + strings.Join(segments, "/")
}

// drainAndClose consumes and closes a response body so the connection can be
// reused.
func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<16))
	_ = body.Close()
}

// readSnippet returns a short, safely truncated error body for diagnostics.
func readSnippet(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, 512))
	if err != nil {
		return "<unreadable>"
	}
	return strings.TrimSpace(string(raw))
}
