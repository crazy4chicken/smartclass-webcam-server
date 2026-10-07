package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	iam "github.com/crazy4chicken/nsc-teamusers/sdk/go"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/store"
)

// Scopes of the cam:<action>:<scope> permission keys.
const (
	ScopeOwn  = "own"
	ScopeTeam = "team"
	ScopeAny  = "any"
)

// camResource is the resource segment of the camera permission keys.
const camResource = "cam"

// AccessGrant describes the permission that authorized a request. Scope is the
// broadest scope that matched; Device is set on device routes.
type AccessGrant struct {
	Scope  string
	Claims iam.Claims
	Device *domain.Device
}

// grantKey is the context key under which the granted access is stored.
type grantKey struct{}

// GrantFromContext returns the access grant stored by RequireCollection or
// RequireDevice. It reports false when the request carries no grant.
func GrantFromContext(ctx context.Context) (AccessGrant, bool) {
	grant, ok := ctx.Value(grantKey{}).(AccessGrant)
	return grant, ok
}

// withGrant stores grant in ctx for the downstream handlers.
func withGrant(ctx context.Context, grant AccessGrant) context.Context {
	return context.WithValue(ctx, grantKey{}, grant)
}

// RequireCollection returns middleware that authorizes action on the device
// collection. The permission ladder mirrors the teamusers admin resolver: the
// literal "any" scope is tried first, then "team" and "own" when the caller
// carries a team or subject. The matching scope is stored in the request
// context for collection filtering.
func (a *Auth) RequireCollection(action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := iam.ClaimsFromContext(r.Context())
			if !ok {
				writeUnauthorized(w, r)
				return
			}
			if a.isDev() {
				next.ServeHTTP(w, r.WithContext(withGrant(r.Context(), AccessGrant{Scope: ScopeAny, Claims: claims})))
				return
			}

			grant, allowed := a.grantCollection(r.Context(), claims, action)
			if !allowed {
				writeForbidden(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(withGrant(r.Context(), grant)))
		})
	}
}

// RequireDevice returns middleware that authorizes action against the device
// returned by load. A missing device becomes 404, a load failure 500, and a
// denied permission 403. The matching scope and the loaded device are stored in
// the request context.
func (a *Auth) RequireDevice(action string, load func(*http.Request) (*domain.Device, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := iam.ClaimsFromContext(r.Context())
			if !ok {
				writeUnauthorized(w, r)
				return
			}

			device, err := load(r)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					writeProblem(w, r, http.StatusNotFound, "resource not found")
					return
				}
				slog.Error("load device for authorization", "method", r.Method, "path", r.URL.Path, "error", err)
				writeProblem(w, r, http.StatusInternalServerError, "internal server error")
				return
			}
			if device == nil {
				writeProblem(w, r, http.StatusNotFound, "device not found")
				return
			}

			if a.isDev() {
				next.ServeHTTP(w, r.WithContext(withGrant(r.Context(), AccessGrant{Scope: ScopeAny, Claims: claims, Device: device})))
				return
			}

			grant, allowed := a.grantDevice(r.Context(), claims, action, device)
			if !allowed {
				writeForbidden(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(withGrant(r.Context(), grant)))
		})
	}
}

// grantCollection evaluates the ladder of collection permission keys and
// returns the first matching scope. A collection has no single device, so each
// rung is evaluated against the slice its scope selects: no resource identity
// for any, the caller's team for team, and the caller's subject for own. The
// SDK requires a non-empty resource team before a :team key or a team-scoped
// grant can match, and ABAC conditions then see the same identity the resolved
// scope filters the collection by.
func (a *Auth) grantCollection(ctx context.Context, claims iam.Claims, action string) (AccessGrant, bool) {
	if a.allows(ctx, claims, action, ScopeAny, iam.Resource{}) {
		return AccessGrant{Scope: ScopeAny, Claims: claims}, true
	}
	if claims.Team != "" && a.allows(ctx, claims, action, ScopeTeam, iam.Resource{TeamID: claims.Team}) {
		return AccessGrant{Scope: ScopeTeam, Claims: claims}, true
	}
	if claims.Subject != "" && a.allows(ctx, claims, action, ScopeOwn, iam.Resource{OwnerID: claims.Subject}) {
		return AccessGrant{Scope: ScopeOwn, Claims: claims}, true
	}
	return AccessGrant{}, false
}

// grantDevice evaluates the ladder of device permission keys and returns the
// first matching scope. The team and own keys are only tried when the device's
// team or owner matches the caller.
func (a *Auth) grantDevice(ctx context.Context, claims iam.Claims, action string, device *domain.Device) (AccessGrant, bool) {
	resource := iam.Resource{OwnerID: device.OwnerID, TeamID: device.TeamID}
	if a.allows(ctx, claims, action, ScopeAny, resource) {
		return AccessGrant{Scope: ScopeAny, Claims: claims, Device: device}, true
	}
	if device.TeamID != "" && claims.Team == device.TeamID && a.allows(ctx, claims, action, ScopeTeam, resource) {
		return AccessGrant{Scope: ScopeTeam, Claims: claims, Device: device}, true
	}
	if device.OwnerID != "" && claims.Subject == device.OwnerID && a.allows(ctx, claims, action, ScopeOwn, resource) {
		return AccessGrant{Scope: ScopeOwn, Claims: claims, Device: device}, true
	}
	return AccessGrant{}, false
}

// allows reports whether claims grant cam:<action>:<scope> for resource.
func (a *Auth) allows(ctx context.Context, claims iam.Claims, action, scope string, resource iam.Resource) bool {
	allowed, _ := a.client.Allow(ctx, claims, permissionKey(action, scope), resource)
	return allowed
}

// permissionKey builds the cam:<action>:<scope> permission key.
func permissionKey(action, scope string) string {
	return camResource + ":" + action + ":" + scope
}

// writeUnauthorized rejects a request that carries no verified identity. It
// writes the same decision body the teamusers middleware emits for
// unauthenticated requests.
func writeUnauthorized(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="teamusers"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(decisionResponse{Allow: false, Reason: "authentication is required"})
}

// decisionResponse mirrors the body the teamusers middleware writes for
// authentication decisions.
type decisionResponse struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason"`
}

// writeForbidden rejects a request whose claims do not grant the permission.
func writeForbidden(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusForbidden, "permission denied")
}

// problemResponse mirrors the RFC 9457 problem body written by the httpapi
// package.
type problemResponse struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// writeProblem writes an RFC 9457 problem+json response, matching the body
// httpapi writes for the same statuses.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, detail string) {
	problem := problemResponse{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	}
	if r != nil && r.URL != nil {
		problem.Instance = r.URL.Path
	}

	body, err := json.Marshal(problem)
	if err != nil {
		slog.Error("encode problem response", "error", err)
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
