package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	iam "github.com/crazy4chicken/nsc-teamusers/sdk/go"
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
			grant, ok := a.grantCollection(context.Background(), claims, "read")
			if ok != tc.wantOK {
				t.Fatalf("grantCollection allowed = %v, want %v", ok, tc.wantOK)
			}
			if ok && grant.Scope != tc.want {
				t.Fatalf("scope = %q, want %q", grant.Scope, tc.want)
			}
		})
	}
}
