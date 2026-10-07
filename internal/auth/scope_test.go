package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	iam "github.com/crazy4chicken/nsc-teamusers/sdk/go"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
)

// newTestAuth returns an Auth whose permission client reads the supplied v2
// snapshot from a stub teamusers endpoint.
func newTestAuth(t *testing.T, snapshot string) *Auth {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(snapshot))
	}))
	t.Cleanup(server.Close)

	a, err := New(server.URL, "webcam", iam.WithServiceToken("svc-token"))
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
		grants string
		want   string
		wantOK bool
	}{
		{
			name:   "platform any grant",
			grants: `[{"key":"cam:read:any"}]`,
			want:   ScopeAny,
			wantOK: true,
		},
		{
			name:   "team-scoped team grant",
			grants: `[{"key":"cam:read:team","team_id":"t1"}]`,
			want:   ScopeTeam,
			wantOK: true,
		},
		{
			name:   "team grant scoped to another team",
			grants: `[{"key":"cam:read:team","team_id":"t2"}]`,
			wantOK: false,
		},
		{
			name:   "team-bound any grant stays off the any rung",
			grants: `[{"key":"cam:read:any","team_id":"t1"}]`,
			wantOK: false,
		},
		{
			name:   "ownership-conditional own grant",
			grants: `[{"key":"cam:read:own","condition":"resource.owner_id == subject.id"}]`,
			want:   ScopeOwn,
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestAuth(t, `{"version":2,"user_id":"u1","perm_ver":7,"grants":`+tc.grants+`}`)
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
	empty := `{"version":2,"user_id":"u1","perm_ver":7,"grants":[]}`

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
				return a.RequireDevice("control", load)(okHandler)
			},
			want: "permission denied: cam:control:any (no matching grant); cam:control:team (no matching grant); cam:control:own (no matching grant)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestAuth(t, empty)
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
