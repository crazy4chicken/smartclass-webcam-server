package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	iam "github.com/crazy4chicken/nsc-teamusers/sdk/go"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/iamtest"
)

// newTestAuth returns an Auth whose permission client reads snapshots from a
// stub teamusers service, with the supplied v2 grants registered for u1.
func newTestAuth(t *testing.T, grants ...string) *Auth {
	t.Helper()
	stub := iamtest.New(t, "webcam")
	stub.SetGrants("u1", 7, grants...)

	a, err := New(stub.URL(), "webcam", nil, iam.WithServiceToken("svc-token"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// TestGrantCollectionScope pins the collection ladder against the v2 snapshot
// semantics: a :team request needs the caller's team as the resource, a grant
// carrying a team_id only applies to that team, and the own rung carries the
// caller's subject so ownership conditions evaluate.
func TestGrantCollectionScope(t *testing.T) {
	claims := iam.Claims{Subject: "u1", Team: "t1", Kind: "user", PermVer: 7}
	cases := []struct {
		name   string
		grants []string
		want   string
		wantOK bool
	}{
		{
			name:   "platform any grant",
			grants: []string{`{"key":"cam:read:any"}`},
			want:   ScopeAny,
			wantOK: true,
		},
		{
			name:   "team-scoped team grant",
			grants: []string{`{"key":"cam:read:team","team_id":"t1"}`},
			want:   ScopeTeam,
			wantOK: true,
		},
		{
			name:   "team grant scoped to another team",
			grants: []string{`{"key":"cam:read:team","team_id":"t2"}`},
			wantOK: false,
		},
		{
			name:   "team-bound any grant stays off the any rung",
			grants: []string{`{"key":"cam:read:any","team_id":"t1"}`},
			wantOK: false,
		},
		{
			name:   "ownership-conditional own grant",
			grants: []string{`{"key":"cam:read:own","condition":"resource.owner_id == subject.id"}`},
			want:   ScopeOwn,
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestAuth(t, tc.grants...)
			grant, _, ok := a.grantCollection(context.Background(), claims, "read")
			if ok != tc.wantOK {
				t.Fatalf("grantCollection allowed = %v, want %v", ok, tc.wantOK)
			}
			if ok && grant.Scope != tc.want {
				t.Fatalf("scope = %q, want %q", grant.Scope, tc.want)
			}
		})
	}
}

// okHandler answers 200 so a passing ladder stays observable.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// TestRequireForbiddenDetail pins the body an endpoint answers when the ladder
// denies: the problem detail names every key that was tried and the cause each
// check reported.
func TestRequireForbiddenDetail(t *testing.T) {
	claims := iam.Claims{Subject: "u1", Team: "t1", Kind: "user", PermVer: 7}

	cases := []struct {
		name string
		gate func(*Auth) http.Handler
		want string
	}{
		{
			name: "collection",
			gate: func(a *Auth) http.Handler {
				return a.RequireCollection("read")(okHandler)
			},
			want: "permission denied: cam:read:any (no matching grant); cam:read:team (no matching grant); cam:read:own (no matching grant)",
		},
		{
			name: "device",
			gate: func(a *Auth) http.Handler {
				load := func(*http.Request) (*domain.Device, error) {
					return &domain.Device{ID: "d1", TeamID: "t1", OwnerID: "u1"}, nil
				}
				return a.RequireDevice("control", "device", load)(okHandler)
			},
			want: "permission denied: cam:control:any (no matching grant); cam:control:team (no matching grant); cam:control:own (no matching grant)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestAuth(t)
			request := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
			request = request.WithContext(iam.WithClaims(request.Context(), claims))

			recorder := httptest.NewRecorder()
			tc.gate(a).ServeHTTP(recorder, request)

			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
			}
			var problem struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decode problem body %q: %v", recorder.Body.String(), err)
			}
			if problem.Detail != tc.want {
				t.Fatalf("detail = %q, want %q", problem.Detail, tc.want)
			}
		})
	}
}

// TestMiddlewares verifies the authentication middleware and the gate behind
// it: every rejected request names the failing check, both in the decision body
// and in the Bearer challenge.
func TestMiddlewares(t *testing.T) {
	subject := "u1"
	permVer := int64(7)

	cases := []struct {
		name     string
		header   func(*iamtest.Stub) string
		wantCode string
		want     string
	}{
		{
			name: "no header",
			header: func(*iamtest.Stub) string {
				return ""
			},
			wantCode: "",
			want:     "", // passes through unauthenticated; the gate rejects below
		},
		{
			name: "wrong scheme",
			header: func(*iamtest.Stub) string {
				return "Basic dXNlcjpwYXNz"
			},
			wantCode: "invalid_request",
			want:     "uses the basic scheme; only Bearer is accepted",
		},
		{
			name: "scheme without token",
			header: func(*iamtest.Stub) string {
				return "Bearer"
			},
			wantCode: "invalid_request",
			want:     `the authorization header must read "Bearer <access token>"`,
		},
		{
			name: "empty bearer token",
			header: func(*iamtest.Stub) string {
				return "Bearer    "
			},
			wantCode: "invalid_request",
			want:     "the authorization header carries an empty bearer token",
		},
		{
			name: "trailing content",
			header: func(*iamtest.Stub) string {
				return "Bearer abc def"
			},
			wantCode: "invalid_request",
			want:     "the authorization header carries more than the bearer token",
		},
		{
			name: "malformed token",
			header: func(*iamtest.Stub) string {
				return "Bearer not.a.jwt"
			},
			wantCode: "invalid_token",
			want:     "access token is not a valid JWS",
		},
		{
			name: "expired token",
			header: func(stub *iamtest.Stub) string {
				claims := stub.Claims(subject, "t1", permVer)
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
				return "Bearer " + stub.Token(iamtest.PublishedKid, claims)
			},
			wantCode: "invalid_token",
			want:     "access token is expired",
		},
		{
			name: "foreign audience",
			header: func(stub *iamtest.Stub) string {
				claims := stub.Claims(subject, "t1", permVer)
				claims["aud"] = "elsewhere"
				return "Bearer " + stub.Token(iamtest.PublishedKid, claims)
			},
			wantCode: "invalid_token",
			want:     `access token audience is not "webcam"`,
		},
		{
			name: "foreign issuer",
			header: func(stub *iamtest.Stub) string {
				claims := stub.Claims(subject, "t1", permVer)
				claims["iss"] = "somebody"
				return "Bearer " + stub.Token(iamtest.PublishedKid, claims)
			},
			wantCode: "invalid_token",
			want:     `access token issuer is not "teamusers"`,
		},
		{
			name: "unknown kind",
			header: func(stub *iamtest.Stub) string {
				claims := stub.Claims(subject, "t1", permVer)
				claims["kind"] = "robot"
				return "Bearer " + stub.Token(iamtest.PublishedKid, claims)
			},
			wantCode: "invalid_token",
			want:     `access token kind must be "user" or "service"`,
		},
		{
			name: "foreign signature",
			header: func(stub *iamtest.Stub) string {
				return "Bearer " + stub.ForeignToken(iamtest.PublishedKid, stub.Claims(subject, "t1", permVer))
			},
			wantCode: "invalid_token",
			want:     "access token signature matches no key in the issuer's JWKS document",
		},
		{
			name: "unknown signing key",
			header: func(stub *iamtest.Stub) string {
				return "Bearer " + stub.ForeignToken("key-9", stub.Claims(subject, "t1", permVer))
			},
			wantCode: "invalid_token",
			want:     `access token names a signing key the issuer does not publish (kid "key-9")`,
		},
		{
			name: "valid token",
			header: func(stub *iamtest.Stub) string {
				return "Bearer " + stub.Token(iamtest.PublishedKid, stub.Claims(subject, "t1", permVer))
			},
			wantCode: "",
			want:     "", // verified below
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := iamtest.New(t, "webcam")
			stub.SetGrants(subject, permVer, `{"key":"cam:read:any"}`)
			a, err := New(stub.URL(), "webcam", nil, iam.WithServiceToken("svc-token"))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer func() { _ = a.Close() }()

			var reached bool
			var seen *iam.Claims
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				seen = ClaimsFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			})
			handler := a.Middleware()(a.RequireCollection("read")(next))

			request := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
			if header := tc.header(stub); header != "" {
				request.Header.Set("Authorization", header)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if tc.wantCode == "" && tc.name != "valid token" {
				// No header: the middleware lets the request through and the
				// gate rejects it with the missing-header reason.
				assertDecision(t, recorder, http.StatusUnauthorized, "invalid_request",
					"the authorization header is missing")
				return
			}
			if tc.name == "valid token" {
				if recorder.Code != http.StatusOK || !reached || seen == nil || seen.Subject != subject {
					t.Fatalf("valid token: status=%d reached=%v claims=%+v, want 200 with subject %s",
						recorder.Code, reached, seen, subject)
				}
				return
			}
			assertDecision(t, recorder, http.StatusUnauthorized, tc.wantCode, tc.want)
		})
	}
}

// assertDecision checks the 401 decision body and the Bearer challenge that
// carries the same reason.
func assertDecision(t *testing.T, recorder *httptest.ResponseRecorder, status int, code, reason string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d (body %q)", recorder.Code, status, recorder.Body.String())
	}
	var decision struct {
		Allow  bool   `json:"allow"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &decision); err != nil {
		t.Fatalf("decode decision body %q: %v", recorder.Body.String(), err)
	}
	if decision.Allow {
		t.Fatalf("decision allowed a rejected request: %q", recorder.Body.String())
	}
	if !strings.Contains(decision.Reason, reason) {
		t.Fatalf("reason = %q, want it to contain %q", decision.Reason, reason)
	}
	challenge := recorder.Header().Get("WWW-Authenticate")
	if !strings.Contains(challenge, `Bearer realm="teamusers"`) {
		t.Fatalf("WWW-Authenticate = %q, want a Bearer challenge", challenge)
	}
	if code != "" && !strings.Contains(challenge, `error="`+code+`"`) {
		t.Fatalf("WWW-Authenticate = %q, want error=%q", challenge, code)
	}
	if !strings.Contains(challenge, `error_description="`) {
		t.Fatalf("WWW-Authenticate = %q, want an error_description", challenge)
	}
}

// TestTokenFailureReasonRedacts verifies that a cause the classifier does not
// recognise still reaches the caller, with configured secrets removed.
func TestTokenFailureReasonRedacts(t *testing.T) {
	a := &Auth{sanitize: func(text string) string {
		return strings.ReplaceAll(text, "s3cr3t-value", "[redacted]")
	}}
	reason := a.tokenFailureReason(errors.New("verify access token: unexpected s3cr3t-value in JWKS document"))
	if strings.Contains(reason, "s3cr3t-value") {
		t.Fatalf("reason leaked a secret: %q", reason)
	}
	if !strings.Contains(reason, "[redacted]") {
		t.Fatalf("reason = %q, want the redacted cause", reason)
	}
}
