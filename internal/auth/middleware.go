// Package auth provides HTTP authentication and authorization middleware for
// the webcam server, backed by the teamusers IAM service.
//
// Middleware and WSUpgradeAuth only need the JWKS verifier and work without any
// credentials of our own. Require additionally asks teamusers for the caller's
// effective permissions, so a service token must be supplied with
// iam.WithServiceToken or iam.WithTokenSource for authorization to succeed;
// without one Require fails closed and rejects every request.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	iam "github.com/crazy4chicken/nsc-teamusers/sdk/go"
)

// tokenQueryParam carries the access token during WebSocket upgrades, where the
// browser WebSocket API cannot set request headers.
const tokenQueryParam = "token"

// Auth couples a teamusers JWKS verifier with the SDK middleware client that
// performs permission checks against the local permission cache.
type Auth struct {
	client   *iam.Client
	verifier *iam.Verifier
}

// New builds an Auth for the teamusers service at teamusersURL. audience is the
// expected JWT audience; the SDK default is used when audience is empty.
//
// opts are applied to both the verifier and the permission client; each SDK
// option targets the component it belongs to. Supply an authorization
// credential (iam.WithServiceToken or iam.WithTokenSource) to make Require
// evaluate permissions.
func New(teamusersURL, audience string, opts ...iam.Option) (*Auth, error) {
	base, err := parseBaseURL(teamusersURL)
	if err != nil {
		return nil, err
	}

	verifierOpts := append([]iam.Option{iam.WithAudience(audience)}, opts...)
	verifier := iam.NewVerifier(base, verifierOpts...)
	permissions := iam.NewPermissionsClient(base, asAnyOptions(opts)...)
	return &Auth{
		client:   iam.NewClient(verifier, permissions),
		verifier: verifier,
	}, nil
}

// NewDevAuth returns an Auth that skips all JWT verification and permission
// checks. Every request passes through with a synthetic subject. Only use for
// local development — never in production.
func NewDevAuth() *Auth {
	return &Auth{}
}

// isDev reports whether this Auth was created by NewDevAuth (no-op mode).
func (a *Auth) isDev() bool {
	return a.client == nil
}

// Close stops the verifier's background JWKS refresh workers. It is safe to
// call more than once.
func (a *Auth) Close() error {
	if a == nil || a.verifier == nil {
		return nil
	}
	return a.verifier.Close()
}

// Middleware returns middleware that verifies a Bearer token and stores the
// resulting claims in the request context, where downstream handlers read them
// with ClaimsFromContext.
//
// Requests without an Authorization header pass through unauthenticated, so the
// middleware can be mounted above public routes. Routes that require a caller
// MUST be guarded with Require, which rejects requests that carry no verified
// claims.
func (a *Auth) Middleware() func(http.Handler) http.Handler {
	if a.isDev() {
		// Dev mode: inject a synthetic dev claim and pass through.
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := iam.WithClaims(r.Context(), iam.Claims{
					Subject: "dev-user",
					Kind:    "user",
				})
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		}
	}
	verify := a.client.Middleware
	return func(next http.Handler) http.Handler {
		authenticated := verify(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if next != nil && strings.TrimSpace(r.Header.Get("Authorization")) == "" {
				next.ServeHTTP(w, r)
				return
			}
			authenticated.ServeHTTP(w, r)
		})
	}
}

// Require returns middleware that authorizes permission against the resource
// derived from each request. Requests without verified claims are rejected with
// 401 and denied authorizations with 403.
func (a *Auth) Require(permission string, resourceFn func(*http.Request) iam.Resource) func(http.Handler) http.Handler {
	if a.isDev() {
		// Dev mode: allow every request.
		return func(next http.Handler) http.Handler {
			return next
		}
	}
	return a.client.Require(permission, resourceFn)
}

// WSUpgradeAuth extracts the access token from the token query parameter and
// verifies it. WebSocket clients cannot set an Authorization header, so the
// token travels with the upgrade request instead.
func (a *Auth) WSUpgradeAuth(r *http.Request) (*iam.Claims, error) {
	if r == nil {
		return nil, errors.New("auth: nil websocket upgrade request")
	}
	if a.isDev() {
		return &iam.Claims{Subject: "dev-camera", Kind: "service"}, nil
	}
	token := strings.TrimSpace(r.URL.Query().Get(tokenQueryParam))
	if token == "" {
		return nil, fmt.Errorf("auth: missing %q query parameter", tokenQueryParam)
	}
	claims, err := a.verifier.Verify(r.Context(), token)
	if err != nil {
		return nil, fmt.Errorf("auth: verify websocket token: %w", err)
	}
	return &claims, nil
}

// ClaimsFromContext returns the verified claims stored by Middleware, or nil
// when the request carries no verified identity.
func ClaimsFromContext(ctx context.Context) *iam.Claims {
	claims, ok := iam.ClaimsFromContext(ctx)
	if !ok {
		return nil
	}
	return &claims
}

// asAnyOptions widens SDK options for NewPermissionsClient, which accepts them
// as any values.
func asAnyOptions(opts []iam.Option) []any {
	if len(opts) == 0 {
		return nil
	}
	widened := make([]any, len(opts))
	for i, opt := range opts {
		widened[i] = opt
	}
	return widened
}

// parseBaseURL validates that raw is an absolute http(s) URL and returns it
// without surrounding whitespace.
func parseBaseURL(raw string) (string, error) {
	base := strings.TrimSpace(raw)
	if base == "" {
		return "", errors.New("auth: teamusers URL is required")
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("auth: parse teamusers URL: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("auth: teamusers URL %q must be an absolute http(s) URL", raw)
	}
	return base, nil
}
