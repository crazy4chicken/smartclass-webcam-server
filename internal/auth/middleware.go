// Package auth provides HTTP authentication and authorization middleware for
// the webcam server, backed by the teamusers IAM service.
//
// Middleware only needs the JWKS verifier and works without any credentials of
// our own. RequireCollection and RequireDevice additionally ask teamusers for
// the caller's effective permissions, so a service token must be supplied with
// iam.WithServiceToken or iam.WithTokenSource for authorization to succeed;
// without one they fail closed and reject every request.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	iam "github.com/crazy4chicken/nsc-teamusers/sdk/go"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/redact"
)

// Auth couples a teamusers JWKS verifier with the SDK middleware client that
// performs permission checks against the local permission cache.
type Auth struct {
	client   *iam.Client
	verifier *iam.Verifier
	// audience is the expected token audience, named when a token carries a
	// different one.
	audience string
	// sanitize strips configured secrets from error text before it reaches a
	// response body. It may be nil.
	sanitize func(string) string
}

// New builds an Auth for the teamusers service at teamusersURL. audience is the
// expected JWT audience; the SDK default is used when audience is empty.
//
// opts are applied to both the verifier and the permission client; each SDK
// option targets the component it belongs to. Supply an authorization
// credential (iam.WithServiceToken or iam.WithTokenSource) to make the
// authorization gates evaluate permissions. sanitize, which may be nil, is
// applied to every error cause the middleware copies into a response body.
func New(teamusersURL, audience string, sanitize func(string) string, opts ...iam.Option) (*Auth, error) {
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
		audience: strings.TrimSpace(audience),
		sanitize: sanitize,
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
// with ClaimsFromContext. A token that fails verification answers 401 with the
// cause the JWKS verifier reported, so the caller learns whether the token is
// expired, was issued for another audience, is signed by an unknown key, and so
// on.
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
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if strings.TrimSpace(header) == "" {
				next.ServeHTTP(w, r)
				return
			}
			token, reason := bearerToken(header)
			if reason != "" {
				writeUnauthorized(w, "invalid_request", reason)
				return
			}
			claims, err := a.verifier.Verify(r.Context(), token)
			if err != nil {
				writeUnauthorized(w, "invalid_token", a.tokenFailureReason(err))
				return
			}
			next.ServeHTTP(w, r.WithContext(iam.WithClaims(r.Context(), claims)))
		})
	}
}

// expectedIssuer is the token issuer the verifier accepts; the SDK default is
// not overridden anywhere in this service.
const expectedIssuer = "teamusers"

// expectedAudience returns the audience the verifier accepts, falling back to
// the SDK default.
func (a *Auth) expectedAudience() string {
	if a.audience != "" {
		return a.audience
	}
	return expectedIssuer
}

// bearerToken extracts the access token from an Authorization header value and
// reports why the header cannot be used.
func bearerToken(header string) (string, string) {
	scheme, rest, found := strings.Cut(strings.TrimLeft(header, " \t"), " ")
	if !found {
		return "", `the authorization header must read "Bearer <access token>"`
	}
	if !strings.EqualFold(scheme, "Bearer") {
		return "", fmt.Sprintf("the authorization header uses the %s scheme; only Bearer is accepted", strings.ToLower(scheme))
	}
	token := strings.TrimSpace(rest)
	switch {
	case token == "":
		return "", "the authorization header carries an empty bearer token"
	case strings.ContainsAny(token, " \t"):
		return "", "the authorization header carries more than the bearer token"
	}
	return token, ""
}

// tokenFailureReason explains a failed access-token verification in terms the
// caller can act on. The cases below name the ones the verifier reports and
// everything else is passed through as reported, so an unrecognised cause still
// reaches the caller instead of collapsing into a bare "authentication failed".
func (a *Auth) tokenFailureReason(err error) string {
	text := redact.Trim(redact.Text(a.sanitize, err.Error()), 240)
	switch {
	case strings.Contains(text, `"exp" not satisfied`):
		return "access token is expired"
	case strings.Contains(text, `"nbf" not satisfied`):
		return "access token is not valid yet (nbf claim)"
	case strings.Contains(text, `"iss" not satisfied`):
		return fmt.Sprintf("access token issuer is not %q", expectedIssuer)
	case text == "invalid access token audience":
		return fmt.Sprintf("access token audience is not %q", a.expectedAudience())
	case text == "invalid access token kind":
		return `access token kind must be "user" or "service"`
	case text == "invalid access token perm_ver":
		return "access token perm_ver claim must be a non-negative integer"
	case strings.Contains(text, "could not verify message using any of the signatures or keys"):
		return "access token signature matches no key in the issuer's JWKS document"
	case strings.Contains(text, "failed to find key with key ID"):
		if kid := unknownKeyID(text); kid != "" {
			return fmt.Sprintf("access token names a signing key the issuer does not publish (kid %q)", kid)
		}
		return "access token names a signing key the issuer does not publish"
	case strings.Contains(text, "failed to parse jws"), strings.Contains(text, "failed to parse JOSE"):
		return "access token is not a valid JWS: " + text
	case strings.Contains(text, "fetch JWKS"),
		strings.Contains(text, "refresh JWKS"),
		strings.Contains(text, "JWKS cache is unavailable"),
		strings.Contains(text, "configure JWKS cache"):
		return "the issuer's JWKS document is unavailable: " + text
	}
	return text
}

// unknownKeyID returns the kid named by the verifier's unknown-key error.
func unknownKeyID(text string) string {
	const marker = `key ID "`
	start := strings.Index(text, marker)
	if start < 0 {
		return ""
	}
	rest := text[start+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
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
