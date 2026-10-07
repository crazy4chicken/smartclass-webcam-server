package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	iam "github.com/crazy4chicken/nsc-teamusers/sdk/go"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/redact"
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
// context for collection filtering; a denial answers 403 with every key that
// was tried and the cause each check reported.
func (a *Auth) RequireCollection(action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := iam.ClaimsFromContext(r.Context())
			if !ok {
				writeUnauthorized(w, "invalid_request", `the authorization header is missing; send "Authorization: Bearer <access token>"`)
				return
			}
			if a.isDev() {
				next.ServeHTTP(w, r.WithContext(withGrant(r.Context(), AccessGrant{Scope: ScopeAny, Claims: claims})))
				return
			}

			grant, reason, allowed := a.grantCollection(r.Context(), claims, action)
			if !allowed {
				writeForbidden(w, r, reason)
				return
			}
			next.ServeHTTP(w, r.WithContext(withGrant(r.Context(), grant)))
		})
	}
}

// RequireDevice returns middleware that authorizes action against the device
// returned by load. target names the resource the loader looked up ("device",
// "stream" or "photo"): a missing one becomes 404 with that name and a load
// failure 500 with the cause. A denied permission answers 403 with every key
// that was tried and the cause each check reported. The matching scope and the
// loaded device are stored in the request context.
func (a *Auth) RequireDevice(action, target string, load func(*http.Request) (*domain.Device, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := iam.ClaimsFromContext(r.Context())
			if !ok {
				writeUnauthorized(w, "invalid_request", `the authorization header is missing; send "Authorization: Bearer <access token>"`)
				return
			}

			device, err := load(r)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					writeProblem(w, r, http.StatusNotFound, target+" not found")
					return
				}
				slog.Error("load "+target+" for authorization", "method", r.Method, "path", r.URL.Path, "error", err)
				writeProblem(w, r, http.StatusInternalServerError,
					redact.Trim("loading the "+target+" failed: "+redact.Text(a.sanitize, err.Error()), 300))
				return
			}
			if device == nil {
				writeProblem(w, r, http.StatusNotFound, target+" not found")
				return
			}

			if a.isDev() {
				next.ServeHTTP(w, r.WithContext(withGrant(r.Context(), AccessGrant{Scope: ScopeAny, Claims: claims, Device: device})))
				return
			}

			grant, reason, allowed := a.grantDevice(r.Context(), claims, action, device)
			if !allowed {
				writeForbidden(w, r, reason)
				return
			}
			next.ServeHTTP(w, r.WithContext(withGrant(r.Context(), grant)))
		})
	}
}

// rung is one step of a permission ladder: the scope to request and the
// resource identity that scope selects.
type rung struct {
	scope    string
	resource iam.Resource
}

// grant walks the rungs in order and returns the first scope the caller may
// use. When every rung denies, it returns the cause each check reported as
// "cam:<action>:<scope> (<reason>)" entries joined with "; ", so the endpoint
// can name the keys that failed and why.
func (a *Auth) grant(ctx context.Context, claims iam.Claims, action string, rungs []rung) (string, string, bool) {
	reasons := make([]string, 0, len(rungs))
	for _, candidate := range rungs {
		key := permissionKey(action, candidate.scope)
		allowed, reason := a.client.Allow(ctx, claims, key, candidate.resource)
		if allowed {
			return candidate.scope, "", true
		}
		reasons = append(reasons, fmt.Sprintf("%s (%s)", key, reason))
	}
	return "", strings.Join(reasons, "; "), false
}

// grantCollection evaluates the ladder of collection permission keys and
// returns the first matching scope. A collection has no single device, so each
// rung is evaluated against the slice its scope selects: no resource identity
// for any, the caller's team for team, and the caller's subject for own. The
// SDK requires a non-empty resource team before a :team key or a team-scoped
// grant can match, and ABAC conditions then see the same identity the resolved
// scope filters the collection by.
func (a *Auth) grantCollection(ctx context.Context, claims iam.Claims, action string) (AccessGrant, string, bool) {
	rungs := []rung{{scope: ScopeAny}}
	if claims.Team != "" {
		rungs = append(rungs, rung{scope: ScopeTeam, resource: iam.Resource{TeamID: claims.Team}})
	}
	if claims.Subject != "" {
		rungs = append(rungs, rung{scope: ScopeOwn, resource: iam.Resource{OwnerID: claims.Subject}})
	}
	scope, reason, allowed := a.grant(ctx, claims, action, rungs)
	if !allowed {
		return AccessGrant{}, reason, false
	}
	return AccessGrant{Scope: scope, Claims: claims}, "", true
}

// grantDevice evaluates the ladder of device permission keys and returns the
// first matching scope. The team and own keys are only tried when the device's
// team or owner matches the caller.
func (a *Auth) grantDevice(ctx context.Context, claims iam.Claims, action string, device *domain.Device) (AccessGrant, string, bool) {
	resource := iam.Resource{OwnerID: device.OwnerID, TeamID: device.TeamID}
	rungs := []rung{{scope: ScopeAny, resource: resource}}
	if device.TeamID != "" && claims.Team == device.TeamID {
		rungs = append(rungs, rung{scope: ScopeTeam, resource: resource})
	}
	if device.OwnerID != "" && claims.Subject == device.OwnerID {
		rungs = append(rungs, rung{scope: ScopeOwn, resource: resource})
	}
	scope, reason, allowed := a.grant(ctx, claims, action, rungs)
	if !allowed {
		return AccessGrant{}, reason, false
	}
	return AccessGrant{Scope: scope, Claims: claims, Device: device}, "", true
}

// permissionKey builds the cam:<action>:<scope> permission key.
func permissionKey(action, scope string) string {
	return camResource + ":" + action + ":" + scope
}

// writeUnauthorized rejects a request that carries no usable identity, naming
// the cause in both the teamusers decision body and the Bearer challenge.
func writeUnauthorized(w http.ResponseWriter, code, reason string) {
	w.Header().Set("WWW-Authenticate", authorizationChallenge(code, reason))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(decisionResponse{Allow: false, Reason: reason})
}

// authorizationChallenge builds the Bearer challenge carrying the same cause as
// the decision body, per RFC 6750.
func authorizationChallenge(code, reason string) string {
	return `Bearer realm="teamusers", error="` + code + `", error_description="` + quoteHeaderValue(reason) + `"`
}

// quoteHeaderValue escapes a value for a quoted-string header parameter.
func quoteHeaderValue(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", " ", "\n", " ").Replace(value)
}

// decisionResponse mirrors the body the teamusers middleware writes for
// authentication decisions.
type decisionResponse struct {
	Allow  bool   `json:"allow"`
	Reason string `json:"reason"`
}

// writeForbidden rejects a request whose claims do not grant the permission.
// reason names every key the ladder tried and the cause each check reported.
func writeForbidden(w http.ResponseWriter, r *http.Request, reason string) {
	detail := "permission denied"
	if reason != "" {
		detail += ": " + reason
	}
	writeProblem(w, r, http.StatusForbidden, detail)
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
