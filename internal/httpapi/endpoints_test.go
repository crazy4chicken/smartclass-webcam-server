package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	iam "github.com/crazy4chicken/nsc-teamusers/sdk/go"
	"github.com/oklog/ulid/v2"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/auth"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/iamtest"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/redact"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/storage"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/store"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/ws"
)

// TestEndpointErrorDetails walks every route of the service and checks the JSON
// body of each error it can answer with: the caller must learn which check
// failed, and no response may carry a configured secret.
//
// It needs a scratch PostgreSQL database, so it is skipped unless
// WEBCAM_TEST_DB_URL points at one. The schema is created and migrated on the
// fly, and every row it writes belongs to the run.
func TestEndpointErrorDetails(t *testing.T) {
	m := newMatrix(t)

	// Public routes stay open and answer their documented bodies.
	m.check("health", call{method: http.MethodGet, path: "/healthz"}, http.StatusOK, `"status":"ok"`)
	m.check("readyz", call{method: http.MethodGet, path: "/readyz"}, http.StatusOK, `"status":"ready"`)

	// Authentication names the failing check, never a bare "authentication
	// failed".
	m.check("list, no header", call{method: http.MethodGet, path: "/api/devices", anonymous: true}, http.StatusUnauthorized,
		"the authorization header is missing")
	m.check("list, wrong scheme", call{method: http.MethodGet, path: "/api/devices", auth: "Basic dXNlcjpwYXNz"}, http.StatusUnauthorized,
		"uses the basic scheme")
	m.check("list, malformed token", call{method: http.MethodGet, path: "/api/devices", auth: "Bearer not.a.jwt"}, http.StatusUnauthorized,
		"access token is not a valid JWS")
	m.check("list, expired token", call{method: http.MethodGet, path: "/api/devices", token: m.expiredToken()}, http.StatusUnauthorized,
		"access token is expired")
	m.check("list, foreign audience", call{method: http.MethodGet, path: "/api/devices", token: m.mutatedToken("aud", "elsewhere")}, http.StatusUnauthorized,
		`access token audience is not "webcam"`)
	m.check("list, foreign signature", call{method: http.MethodGet, path: "/api/devices",
		token: m.stub.ForeignToken(iamtest.PublishedKid, m.stub.Claims(operator, "t1", 7))}, http.StatusUnauthorized,
		"access token signature matches no key in the issuer's JWKS document")
	m.check("list, unknown key", call{method: http.MethodGet, path: "/api/devices",
		token: m.stub.ForeignToken("key-9", m.stub.Claims(operator, "t1", 7))}, http.StatusUnauthorized,
		`access token names a signing key the issuer does not publish (kid "key-9")`)

	// Authorization names every key the ladder tried.
	m.check("list, no grants", call{method: http.MethodGet, path: "/api/devices", token: m.token(stranger)}, http.StatusForbidden,
		"permission denied: cam:read:any (no matching grant)")

	// Request validation and success paths of every management route.
	m.check("list, bad limit", call{method: http.MethodGet, path: "/api/devices?limit=0"}, http.StatusBadRequest,
		"limit must be a positive integer")
	m.check("list", call{method: http.MethodGet, path: "/api/devices"}, http.StatusOK, `"items"`)
	m.check("create, broken body", call{method: http.MethodPost, path: "/api/devices", body: "{"}, http.StatusBadRequest,
		"invalid JSON request body:")
	m.check("create, no name", call{method: http.MethodPost, path: "/api/devices", body: `{}`}, http.StatusBadRequest,
		"name is required")

	device, deviceToken := m.createDevice("matrix device")
	spare, _ := m.createDevice("matrix spare")

	m.check("get, unknown device", call{method: http.MethodGet, path: "/api/devices/" + newID() + "/"}, http.StatusNotFound,
		"device not found")
	m.check("get", call{method: http.MethodGet, path: "/api/devices/" + device + "/"}, http.StatusOK, `"id"`)
	m.check("update, empty body", call{method: http.MethodPut, path: "/api/devices/" + device + "/", body: `{}`}, http.StatusBadRequest,
		"request body must contain at least one of name, location, team_id or owner_id")
	m.check("update, blank name", call{method: http.MethodPut, path: "/api/devices/" + device + "/", body: `{"name":"  "}`}, http.StatusBadRequest,
		"name must not be empty")
	m.check("update, unknown device", call{method: http.MethodPut, path: "/api/devices/" + newID() + "/", body: `{"name":"x"}`}, http.StatusNotFound,
		"device not found")
	m.check("delete, unknown device", call{method: http.MethodDelete, path: "/api/devices/" + newID() + "/"}, http.StatusNotFound,
		"device not found")
	deviceToken = m.rotateToken(device)

	m.check("switch, no camera_enum", call{method: http.MethodPost, path: "/api/devices/" + device + "/camera/switch", body: `{}`}, http.StatusBadRequest,
		"camera_enum is required")
	m.check("switch, offline", call{method: http.MethodPost, path: "/api/devices/" + device + "/camera/switch", body: `{"camera_enum":0}`}, http.StatusConflict,
		"is offline: no live registration")
	m.check("record start, offline", call{method: http.MethodPost, path: "/api/devices/" + device + "/recording/start", body: `{"camera_enum":0}`}, http.StatusConflict,
		"is offline: no live registration")
	m.check("record stop, no active stream", call{method: http.MethodPost, path: "/api/devices/" + device + "/recording/stop", body: `{"camera_enum":0}`}, http.StatusNotFound,
		"no active stream for camera_enum 0 on device")
	m.check("photo, offline", call{method: http.MethodPost, path: "/api/devices/" + device + "/photo", body: `{"camera_enum":0}`}, http.StatusConflict,
		"is offline: no live registration")

	m.check("device streams", call{method: http.MethodGet, path: "/api/devices/" + device + "/streams"}, http.StatusOK, `"items"`)
	m.check("device photos", call{method: http.MethodGet, path: "/api/devices/" + device + "/photos"}, http.StatusOK, `"items"`)
	m.check("stream, unknown", call{method: http.MethodGet, path: "/api/streams/" + newID() + "/"}, http.StatusNotFound,
		"stream not found")
	m.check("stream segments, unknown", call{method: http.MethodGet, path: "/api/streams/" + newID() + "/segments"}, http.StatusNotFound,
		"stream not found")
	m.check("photo, unknown", call{method: http.MethodGet, path: "/api/photos/" + newID() + "/"}, http.StatusNotFound,
		"photo not found")

	// A device announces its cameras: the parameters each camera is at for the
	// connection plus everything it supports, and the device plane rejects a
	// report whose current parameters are not among them.
	m.check("register, missing support lists", call{method: http.MethodGet, path: "/ws/register", body: `{"device_id":"` + device + `","cameras":[{"camera_enum":0,"resolution":"1920x1080","fps":30,"supported_codec":["h264"]}]}`, auth: "Bearer " + deviceToken}, http.StatusBadRequest,
		"cameras[0].supported_resolutions must not be empty")
	m.check("register, current resolution unlisted", call{method: http.MethodGet, path: "/ws/register", body: `{"device_id":"` + device + `","cameras":[{"camera_enum":0,"resolution":"3840x2160","fps":30,"supported_resolutions":["1920x1080"],"supported_framerates":[30],"supported_codec":["h264"]}]}`, auth: "Bearer " + deviceToken}, http.StatusBadRequest,
		"cameras[0].resolution must be one of the supported_resolutions")
	m.check("register, current fps unlisted", call{method: http.MethodGet, path: "/ws/register", body: `{"device_id":"` + device + `","cameras":[{"camera_enum":0,"resolution":"1920x1080","fps":60,"supported_resolutions":["1920x1080"],"supported_framerates":[30,15],"supported_codec":["h264"]}]}`, auth: "Bearer " + deviceToken}, http.StatusBadRequest,
		"cameras[0].fps must be one of the supported_framerates")
	m.check("register, repeated frame rate", call{method: http.MethodGet, path: "/ws/register", body: `{"device_id":"` + device + `","cameras":[{"camera_enum":0,"resolution":"1920x1080","fps":30,"supported_resolutions":["1920x1080"],"supported_framerates":[30,30],"supported_codec":["h264"]}]}`, auth: "Bearer " + deviceToken}, http.StatusBadRequest,
		"cameras[0].supported_framerates must not contain duplicates")
	m.check("register", call{method: http.MethodGet, path: "/ws/register", body: registration(device, "1920x1080", 30), auth: "Bearer " + deviceToken}, http.StatusOK,
		`"device_websocket_id"`)

	// The device plane carries its own credential and names its own failures.
	// The registration route decodes its body before it looks at the token, so
	// every case sends a well-formed one.
	register := `{"device_id":"` + newID() + `","cameras":[]}`
	m.check("register, no header", call{method: http.MethodGet, path: "/ws/register", body: register, anonymous: true}, http.StatusUnauthorized,
		"the Authorization header is missing")
	m.check("register, wrong scheme", call{method: http.MethodGet, path: "/ws/register", body: register, auth: "Basic abc"}, http.StatusUnauthorized,
		"the Authorization header does not carry a Bearer token")
	m.check("register, not a device token", call{method: http.MethodGet, path: "/ws/register", body: register, auth: "Bearer not-a-device-token"}, http.StatusUnauthorized,
		"the Authorization header does not carry a device token")
	m.check("register, unknown token", call{method: http.MethodGet, path: "/ws/register", body: register,
		auth: "Bearer wdt_" + strings.Repeat("a", 64)}, http.StatusUnauthorized,
		"the device token is unknown or has been rotated")
	m.check("device ticket, unknown", call{method: http.MethodGet, path: "/ws/device/" + newID(), anonymous: true}, http.StatusNotFound,
		"device websocket not found")

	// Routing failures answer the same problem shape.
	m.check("unknown path", call{method: http.MethodGet, path: "/api/nowhere"}, http.StatusNotFound,
		"no route for GET /api/nowhere")
	m.check("wrong method", call{method: http.MethodPut, path: "/healthz"}, http.StatusMethodNotAllowed,
		"method PUT is not allowed on /healthz; allowed: GET")

	// A dependency failure names the operation and the cause.
	m.checkDependencyFailure()

	m.check("delete", call{method: http.MethodDelete, path: "/api/devices/" + spare + "/"}, http.StatusNoContent, "")
}

// operator is the subject whose token carries the permission version the stub
// registers grants under, and stranger is a subject without grants.
const (
	operator = "operator"
	stranger = "stranger"
)

// matrix is one running service under test.
type matrix struct {
	t        *testing.T
	router   http.Handler
	stub     *iamtest.Stub
	sanitize func(string) string
	secret   string
	dsn      string
	base     string // listener serving the router, created on first use
}

// newMatrix builds the service under test: a real schema in a scratch
// PostgreSQL database, the teamusers stub, and the router over a no-op object
// storage. The test is skipped unless WEBCAM_TEST_DB_URL points at the scratch
// database.
func newMatrix(t *testing.T) *matrix {
	t.Helper()
	dsn := os.Getenv("WEBCAM_TEST_DB_URL")
	if dsn == "" {
		t.Skip("set WEBCAM_TEST_DB_URL to a scratch PostgreSQL database to run the endpoint matrix")
	}

	ctx := context.Background()
	pool, err := store.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := store.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	secret := dbPassword(dsn)
	sanitize := redact.New(secret)
	stub := iamtest.New(t, "webcam")
	stub.SetGrants(operator, 7,
		`{"key":"cam:read:any"}`,
		`{"key":"cam:manage:any"}`,
		`{"key":"cam:control:any"}`)
	stub.SetGrants(stranger, 7)

	authn, err := auth.New(stub.URL(), "webcam", sanitize, iam.WithServiceToken("svc-token"))
	if err != nil {
		t.Fatalf("create auth: %v", err)
	}
	t.Cleanup(func() { _ = authn.Close() })

	return &matrix{
		t:        t,
		router:   NewRouter(authn, store.New(pool), ws.NewHub(), ws.NewRegistry(time.Minute), storage.NoopStorage{}, sanitize),
		stub:     stub,
		sanitize: sanitize,
		secret:   secret,
		dsn:      dsn,
	}
}

// listen serves the router on a real listener, which the device plane needs to
// dial the WebSocket route.
func (m *matrix) listen() string {
	m.t.Helper()
	if m.base != "" {
		return m.base
	}
	server := httptest.NewServer(m.router)
	m.t.Cleanup(server.Close)
	m.base = server.URL
	return m.base
}

// call describes one request against the router. Unless anonymous or an
// explicit auth header is set, the operator's token is attached.
type call struct {
	method    string
	path      string
	body      string
	anonymous bool
	token     string
	auth      string
}

// check issues one call and asserts its status, the cause in its body, and that
// the body leaks no configured secret. Every call is logged, so a failing run
// prints the whole matrix.
func (m *matrix) check(name string, c call, status int, contains string) *httptest.ResponseRecorder {
	m.t.Helper()
	var reader io.Reader
	if c.body != "" {
		reader = strings.NewReader(c.body)
	}
	r := httptest.NewRequest(c.method, c.path, reader)
	if c.body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	switch {
	case c.auth != "":
		r.Header.Set("Authorization", c.auth)
	case c.token != "":
		r.Header.Set("Authorization", "Bearer "+c.token)
	case !c.anonymous:
		r.Header.Set("Authorization", "Bearer "+m.token(operator))
	}

	recorder := httptest.NewRecorder()
	m.router.ServeHTTP(recorder, r)
	body := recorder.Body.String()
	m.t.Logf("%-30s %-6s %-44s -> %d %s", name, c.method, c.path, recorder.Code, truncate(body, 140))

	if recorder.Code != status {
		m.t.Fatalf("status = %d, want %d (body %q)", recorder.Code, status, body)
	}
	if contains != "" && !strings.Contains(body, contains) && !strings.Contains(causeOf(m.t, body), contains) {
		m.t.Fatalf("body %q does not name the cause %q", body, contains)
	}
	if status >= 400 {
		if cause := causeOf(m.t, body); strings.TrimSpace(cause) == "" {
			m.t.Fatalf("status %d carries no cause: %q", status, body)
		}
		wantType := "application/problem+json"
		if strings.Contains(body, `"reason"`) {
			// The authentication decision body mirrors teamusers.
			wantType = "application/json"
		}
		if got := recorder.Header().Get("Content-Type"); got != wantType {
			m.t.Fatalf("status %d answered %q, want %q", status, got, wantType)
		}
	}
	if m.secret != "" && strings.Contains(body, m.secret) {
		m.t.Fatalf("body %q leaked the configured secret", body)
	}
	return recorder
}

// checkDependencyFailure proves a 500 names the failing operation and its cause
// rather than a bare "internal server error", using a pool that is already
// closed.
func (m *matrix) checkDependencyFailure() {
	m.t.Helper()
	broken, err := store.NewPool(context.Background(), m.dsn)
	if err != nil {
		m.t.Fatalf("open broken pool: %v", err)
	}
	broken.Close()

	authn, err := auth.New(m.stub.URL(), "webcam", m.sanitize, iam.WithServiceToken("svc-token"))
	if err != nil {
		m.t.Fatalf("create auth: %v", err)
	}
	defer func() { _ = authn.Close() }()

	router := NewRouter(authn, store.New(broken), ws.NewHub(), ws.NewRegistry(time.Minute), storage.NoopStorage{}, m.sanitize)
	r := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	r.Header.Set("Authorization", "Bearer "+m.token(operator))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, r)

	body := recorder.Body.String()
	m.t.Logf("%-30s %-6s %-44s -> %d %s", "list, closed pool", http.MethodGet, "/api/devices", recorder.Code, truncate(body, 140))
	if recorder.Code != http.StatusInternalServerError {
		m.t.Fatalf("status = %d, want %d (body %q)", recorder.Code, http.StatusInternalServerError, body)
	}
	detail := causeOf(m.t, body)
	const prefix = "list devices failed: "
	if !strings.HasPrefix(detail, prefix) || strings.TrimSpace(strings.TrimPrefix(detail, prefix)) == "" {
		m.t.Fatalf("detail = %q, want the failing operation and its cause", detail)
	}
}

// token mints a valid token for subject with the stub's permission version.
func (m *matrix) token(subject string) string {
	return m.stub.Token(iamtest.PublishedKid, m.stub.Claims(subject, "t1", 7))
}

// mutatedToken mints a valid token with one claim replaced.
func (m *matrix) mutatedToken(claim string, value any) string {
	claims := m.stub.Claims(operator, "t1", 7)
	claims[claim] = value
	return m.stub.Token(iamtest.PublishedKid, claims)
}

// expiredToken mints a token whose exp already passed.
func (m *matrix) expiredToken() string {
	claims := m.stub.Claims(operator, "t1", 7)
	claims["exp"] = time.Now().Add(-time.Hour).Unix()
	return m.stub.Token(iamtest.PublishedKid, claims)
}

// createDevice registers a device through the API and returns its id together
// with the plaintext device token the creation response carries.
func (m *matrix) createDevice(name string) (string, string) {
	m.t.Helper()
	recorder := m.check("create "+name, call{method: http.MethodPost, path: "/api/devices",
		body: `{"name":"` + name + `"}`, token: m.token(operator)}, http.StatusCreated, `"token"`)

	var envelope struct {
		Device struct {
			ID string `json:"id"`
		} `json:"device"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		m.t.Fatalf("decode created device %q: %v", recorder.Body.String(), err)
	}
	if envelope.Device.ID == "" || envelope.Token == "" {
		m.t.Fatalf("created device carries no id or token: %q", recorder.Body.String())
	}
	return envelope.Device.ID, envelope.Token
}

// rotateToken rotates a device token through the API and returns the new
// plaintext token, which is the only one the device plane accepts afterwards.
func (m *matrix) rotateToken(deviceID string) string {
	m.t.Helper()
	recorder := m.check("rotate token", call{method: http.MethodPost, path: "/api/devices/" + deviceID + "/token"},
		http.StatusOK, `"token"`)
	var envelope struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || envelope.Token == "" {
		m.t.Fatalf("rotate response carries no token: %q", recorder.Body.String())
	}
	return envelope.Token
}

// registration builds a camera registration body whose current parameters are
// one of the supported ones.
func registration(deviceID, resolution string, fps int) string {
	return fmt.Sprintf(
		`{"device_id":%q,"cameras":[{"camera_enum":0,"resolution":%q,"fps":%d,`+
			`"supported_resolutions":["1920x1080","1280x720"],"supported_framerates":[30,15],`+
			`"supported_codec":["h264","mjpeg"],"attrs":{"label":"front"}}]}`,
		deviceID, resolution, fps)
}

// causeOf returns the human-readable cause of a problem or decision body.
func causeOf(t *testing.T, body string) string {
	t.Helper()
	var envelope struct {
		Detail string `json:"detail"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	if envelope.Detail != "" {
		return envelope.Detail
	}
	return envelope.Reason
}

// dbPassword extracts the password of the test database URL, so the matrix can
// prove no response echoes it.
func dbPassword(dsn string) string {
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.User == nil {
		return ""
	}
	password, _ := parsed.User.Password()
	return password
}

// newID returns a fresh identifier that no row holds.
func newID() string { return ulid.Make().String() }

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
