package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// tokenRefreshMargin is how long before expiry a cached service token is
// considered stale and refreshed.
const tokenRefreshMargin = 60 * time.Second

// ClientCredentialsTokenSource obtains teamusers service access tokens with the
// OAuth2 client-credentials flow and refreshes them before they expire. It
// satisfies the SDK's WithTokenSource contract: the returned function is called
// whenever the SDK needs a credential for a permission lookup.
type ClientCredentialsTokenSource struct {
	baseURL      string
	clientID     string
	clientSecret string
	httpClient   *http.Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// NewClientCredentialsTokenSource builds a token source for the teamusers
// service at baseURL. The service account is identified by clientID and its
// one-time clientSecret.
func NewClientCredentialsTokenSource(baseURL, clientID, clientSecret string, httpClient *http.Client) *ClientCredentialsTokenSource {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &ClientCredentialsTokenSource{
		baseURL:      strings.TrimRight(baseURL, "/"),
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   httpClient,
	}
}

// Token returns a valid service access token, fetching a new one when the
// cached token is missing or about to expire.
func (s *ClientCredentialsTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != "" && time.Now().Before(s.expiresAt.Add(-tokenRefreshMargin)) {
		return s.token, nil
	}

	token, expiresIn, err := s.fetch(ctx)
	if err != nil {
		return "", err
	}
	s.token = token
	s.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	return token, nil
}

// TokenFunc adapts Token to the func() (string, error) shape expected by
// iam.WithTokenSource.
func (s *ClientCredentialsTokenSource) TokenFunc() func() (string, error) {
	return func() (string, error) {
		return s.Token(context.Background())
	}
}

// credsRequest is the body of POST /auth/client-credentials.
type credsRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// credsResponse is the success body of POST /auth/client-credentials.
type credsResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// fetch performs one client-credentials exchange.
func (s *ClientCredentialsTokenSource) fetch(ctx context.Context) (string, int, error) {
	body, err := json.Marshal(credsRequest{ClientID: s.clientID, ClientSecret: s.clientSecret})
	if err != nil {
		return "", 0, fmt.Errorf("auth: encode client credentials: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/auth/client-credentials", bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("auth: build client credentials request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("auth: client credentials request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("auth: client credentials rejected: status %d", resp.StatusCode)
	}

	var out credsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, fmt.Errorf("auth: decode client credentials response: %w", err)
	}
	if out.AccessToken == "" {
		return "", 0, fmt.Errorf("auth: client credentials response carried no access token")
	}
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 600
	}
	return out.AccessToken, out.ExpiresIn, nil
}
