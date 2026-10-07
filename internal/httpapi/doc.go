// Package httpapi exposes the webcam server's HTTP API over a chi router built
// by NewRouter.
//
// # Routes
//
// Public endpoints:
//
//	GET /healthz                          liveness probe
//	GET /readyz                           readiness probe
//	GET /ws/register                      register a device and issue a WebSocket ticket
//	GET /ws/device/{device_websocket_id}  upgrade the device WebSocket
//
// Management endpoints under /api require a teamusers Bearer token carrying a
// cam:read, cam:manage or cam:control permission scoped to own, team or any
// (401 without verified claims, 403 when denied):
//
//	GET    /api/devices                          list devices
//	POST   /api/devices                          register a device and issue its token
//	GET    /api/devices/{device_id}/             fetch one device with its live cameras
//	PUT    /api/devices/{device_id}/             update device fields
//	DELETE /api/devices/{device_id}/             delete a device
//	POST   /api/devices/{device_id}/token        rotate the device token
//	POST   /api/devices/{device_id}/camera/switch      switch the active camera
//	POST   /api/devices/{device_id}/recording/start    start a recording stream
//	POST   /api/devices/{device_id}/recording/stop     stop the active stream
//	POST   /api/devices/{device_id}/photo              capture a still image
//	GET    /api/devices/{device_id}/streams      list a device's streams
//	GET    /api/devices/{device_id}/photos       list a device's photos
//	GET    /api/streams/{stream_id}/             fetch a stream with its segments
//	GET    /api/streams/{stream_id}/segments     list a stream's segments
//	GET    /api/photos/{photo_id}/               fetch a photo
//
// The device plane authenticates with its own long-lived token on GET
// /ws/register; the returned device_websocket_id is a single-use ticket for GET
// /ws/device/{device_websocket_id}, which then carries the control, recording
// and photo channels of the WebSocket protocol.
//
// # Responses
//
// Collection endpoints return {"items": [...]}. Single-resource endpoints
// return the resource object itself. Errors use RFC 9457 problem details
// (application/problem+json) with type "about:blank"; a 401 on an /api route is
// instead the teamusers SDK decision body {"allow": false, "reason": ...}, whose
// reason names the exact cause.
//
// DocOperations lists every route together with its request and response
// shapes. cmd/genspec walks the router and reflects the table into
// docs/public/openapi.yaml, failing when a route and its metadata drift apart.
package httpapi

import (
	"strings"
	"time"

	apidocs "github.com/crazy4chicken/nsc-teamusers/apidocs/go"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/domain"
	"github.com/crazy4chicken/smartclass-webcam-server/internal/ws"
)

// DocPermissionDeriver returns the cam:<action>:any and cam:<action>:team keys
// that guard method and path for the generated reference. The own-scoped key
// depends on the caller and the device, so it cannot be derived statically and
// is documented in the guide only. Health and device-plane routes are public
// and therefore carry no permission line.
func DocPermissionDeriver(method, path string) (anyKey, teamKey string) {
	var action string
	switch path {
	case "/api/devices":
		if method == "POST" {
			action = "manage"
		} else {
			action = "read"
		}
	case "/api/devices/{device_id}/":
		if method == "PUT" || method == "DELETE" {
			action = "manage"
		} else {
			action = "read"
		}
	case "/api/devices/{device_id}/token":
		action = "manage"
	case "/api/devices/{device_id}/camera/switch",
		"/api/devices/{device_id}/recording/start",
		"/api/devices/{device_id}/recording/stop",
		"/api/devices/{device_id}/photo":
		action = "control"
	case "/api/devices/{device_id}/streams",
		"/api/devices/{device_id}/photos",
		"/api/streams/{stream_id}/",
		"/api/streams/{stream_id}/segments",
		"/api/photos/{photo_id}/":
		action = "read"
	default:
		return "", ""
	}
	return "cam:" + action + ":any", "cam:" + action + ":team"
}

// docDeviceDetail mirrors deviceDetail with the embedded device flattened, so
// the reflected schema matches the JSON body the handler writes.
type docDeviceDetail struct {
	ID        string                `json:"id"`
	Name      string                `json:"name"`
	Location  string                `json:"location,omitempty"`
	TeamID    string                `json:"team_id,omitempty"`
	OwnerID   string                `json:"owner_id,omitempty"`
	LastSeen  *time.Time            `json:"last_seen,omitempty"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
	Online    bool                  `json:"online"`
	Cameras   []ws.CameraCapability `json:"cameras"`
}

// docDeviceToken mirrors the creation and rotation envelope, which carries the
// device together with its plaintext token.
type docDeviceToken struct {
	Device domain.Device `json:"device"`
	Token  string        `json:"token"`
}

// docDeviceCreateRequest mirrors the device creation body.
type docDeviceCreateRequest struct {
	Name     string  `json:"name"`
	Location string  `json:"location,omitempty"`
	TeamID   *string `json:"team_id,omitempty"`
	OwnerID  *string `json:"owner_id,omitempty"`
}

// docDeviceUpdateRequest mirrors the partial device update body.
type docDeviceUpdateRequest struct {
	Name     *string `json:"name,omitempty"`
	Location *string `json:"location,omitempty"`
	TeamID   *string `json:"team_id,omitempty"`
	OwnerID  *string `json:"owner_id,omitempty"`
}

// docCameraCommandRequest mirrors the body shared by the stop-recording and
// take-photo routes, which act on the camera the device is already using.
type docCameraCommandRequest struct {
	CameraEnum *int `json:"camera_enum"`
}

// docSwitchRequest mirrors the switch-camera body: the camera to select and,
// optionally, the parameters to select it at. An omitted parameter is left
// unchanged, so a switch to another camera leaves that camera at the
// parameters it reported.
type docSwitchRequest struct {
	CameraEnum *int    `json:"camera_enum"`
	Resolution *string `json:"resolution,omitempty"`
	FPS        *int    `json:"fps,omitempty"`
}

// docRecordingStartRequest mirrors the recording-start body: the camera to
// record and, optionally, the codec to record it with. An omitted codec lets
// the device pick its preferred one, the first entry of supported_codec.
type docRecordingStartRequest struct {
	CameraEnum *int    `json:"camera_enum"`
	Codec      *string `json:"codec,omitempty"`
}

// docSwitchAccepted mirrors the 202 body of the camera switch command.
type docSwitchAccepted struct {
	CommandID  string `json:"command_id"`
	CameraEnum int    `json:"camera_enum"`
}

// docPhotoAccepted mirrors the 202 body of the photo command.
type docPhotoAccepted struct {
	CommandID  string `json:"command_id"`
	CameraEnum int    `json:"camera_enum"`
	RequestID  string `json:"request_id"`
}

// docRegisterRequest mirrors the JSON body of the device registration call.
type docRegisterRequest struct {
	DeviceID string                `json:"device_id"`
	Cameras  []ws.CameraCapability `json:"cameras"`
}

// docRegisterResponse mirrors the ticket returned by device registration.
type docRegisterResponse struct {
	DeviceWebsocketID string    `json:"device_websocket_id"`
	ExpiresAt         time.Time `json:"expires_at"`
	WebsocketPath     string    `json:"websocket_path"`
}

// docSegmentWithURL mirrors segmentWithURL with the embedded segment flattened,
// so the reflected schema matches the JSON body the handler writes.
type docSegmentWithURL struct {
	ID          string    `json:"id"`
	StreamID    string    `json:"stream_id"`
	DeviceID    string    `json:"device_id"`
	CameraEnum  int       `json:"camera_enum"`
	SegmentSeq  int       `json:"segment_seq"`
	SizeBytes   int64     `json:"size_bytes"`
	DurationMS  int       `json:"duration_ms,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	DownloadURL string    `json:"download_url,omitempty"`
}

// docStreamDetail mirrors streamDetail with the embedded stream flattened, so
// the reflected schema matches the JSON body the handler writes.
type docStreamDetail struct {
	ID         string                `json:"id"`
	DeviceID   string                `json:"device_id"`
	CameraEnum int                   `json:"camera_enum"`
	Status     domain.StreamStatus   `json:"status"`
	StartedAt  time.Time             `json:"started_at"`
	EndedAt    *time.Time            `json:"ended_at,omitempty"`
	Metadata   domain.StreamMetadata `json:"metadata"`
	Segments   []docSegmentWithURL   `json:"segments"`
}

// docPhotoDetail mirrors photoDetail with the embedded photo flattened, so the
// reflected schema matches the JSON body the handler writes.
type docPhotoDetail struct {
	ID          string    `json:"id"`
	DeviceID    string    `json:"device_id"`
	CameraEnum  int       `json:"camera_enum"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	RequestID   string    `json:"request_id,omitempty"`
	TakenAt     time.Time `json:"taken_at"`
	CreatedAt   time.Time `json:"created_at"`
	DownloadURL string    `json:"download_url,omitempty"`
}

// docError builds one documented problem response. The code is rendered as the
// detail of the response example, matching the problem body handlers emit.
func docError(status int, code, title string) apidocs.ErrorDoc {
	return apidocs.ErrorDoc{Status: status, Code: code, Title: title}
}

// docForbidden builds the 403 problem an operation documents. The endpoint
// answers "permission denied" followed by every key its ladder tried and the
// cause the check reported; the :any key is always tried first, so the example
// names it.
func docForbidden(action string) apidocs.ErrorDoc {
	return docError(403, "permission denied: cam:"+action+":any (no matching grant)", "Forbidden")
}

// docFailure builds the 500 problem an operation documents. Every server-side
// failure names the operation that failed and the sanitized cause behind it, so
// the example documents the prefix and leaves the cause open.
func docFailure(operation string) apidocs.ErrorDoc {
	return docError(500, operation+" failed: <cause>", "Internal Server Error")
}

// docCommandFailure builds the 502 problem a command route documents: the
// command it could not deliver and the classified websocket cause behind it.
func docCommandFailure(command string) apidocs.ErrorDoc {
	return docError(502, "sending "+command+" failed: <cause>", "Bad Gateway")
}

// docLoadFailure builds the 500 problem RequireDevice writes when it cannot
// load the resource whose access it authorizes.
func docLoadFailure(target string) apidocs.ErrorDoc {
	return docFailure("loading the " + target)
}

var (
	docInvalidBody   = docError(400, "invalid JSON request body: <cause>", "Invalid Request")
	docBodyNotSingle = docError(400, "request body must contain a single JSON object", "Invalid Request")
	docBodyTooLarge  = docError(413, "request body too large", "Request Entity Too Large")
	docBadLimit      = docError(400, "limit must be a positive integer", "Invalid Request")
)

// The 401 of an /api route is the teamusers decision body
// {"allow": false, "reason": "..."}, not a problem document: reason names the
// exact cause, one of "the authorization header is missing; send
// \"Authorization: Bearer <access token>\"", "the authorization header must
// read \"Bearer <access token>\"", "access token is expired", "access token
// audience is not \"webcam\"", "access token issuer is not \"teamusers\"",
// "access token signature matches no key in the issuer's JWKS document",
// "access token names a signing key the issuer does not publish (kid \"...\")",
// "access token is not a valid JWS: ..." and "the issuer's JWKS document is
// unavailable: ...". The example below names the most common cause.
var docUnauthorized = docError(401, "access token is expired", "Unauthorized")

// The device plane answers 401 with the reason the credential was rejected. An
// unknown device, a wrong token and a rotated token share the last reason, so
// the endpoint never leaks whether a device exists.
var (
	docDeviceAuthMissing  = docError(401, "the Authorization header is missing", "Unauthorized")
	docDeviceAuthScheme   = docError(401, "the Authorization header does not carry a Bearer token", "Unauthorized")
	docDeviceAuthToken    = docError(401, "the Authorization header does not carry a device token", "Unauthorized")
	docDeviceAuthRejected = docError(401, "the device token is unknown or has been rotated", "Unauthorized")
)

var (
	docDeviceNotFound = docError(404, "device not found", "Not Found")
	docStreamNotFound = docError(404, "stream not found", "Not Found")
	docPhotoNotFound  = docError(404, "photo not found", "Not Found")
	docTicketNotFound = docError(404, "device websocket not found", "Not Found")
)

// The read handlers repeat the middleware's 404 with the id from the path, so a
// resource deleted between authorization and the read still names itself.
var (
	docStreamNotFoundByID = docError(404, `stream "<stream_id>" not found`, "Not Found")
	docPhotoNotFoundByID  = docError(404, `photo "<photo_id>" not found`, "Not Found")
	docNoActiveStream     = docError(404, `no active stream for camera_enum <camera_enum> on device "<device_id>"`, "Not Found")
)

var (
	docTicketReplay = docError(409, "device websocket ticket already attached", "Conflict")
	docDeviceExists = docError(409, "a device with this id already exists", "Conflict")
	docStreamActive = docError(409, `camera_enum <camera_enum> is already streaming on device "<device_id>"`, "Conflict")
)

// The two offline checks behind the command routes: the device never
// registered, or it registered but its websocket is not attached.
var (
	docDeviceOfflineNoRegistration = docError(409, `device "<device_id>" is offline: no live registration`, "Conflict")
	docDeviceOfflineNoWebsocket    = docError(409, `device "<device_id>" is offline: no live websocket`, "Conflict")
)

// docPanic documents the response the recoverer writes for a handler panic.
var docPanic = docError(500, "panic: <cause>", "Internal Server Error")

var (
	docCameraEnumUnknown  = docError(400, `camera_enum <camera_enum> is not registered for device "<device_id>"`, "Invalid Request")
	docCamerasEmpty       = docError(400, "cameras must not be empty", "Invalid Request")
	docCameraEnumOrder    = docError(400, `cameras[<index>].camera_enum must be <index>`, "Invalid Request")
	docCameraResolution   = docError(400, `cameras[<index>].resolution must not be empty`, "Invalid Request")
	docCameraFPS          = docError(400, `cameras[<index>].fps must be positive`, "Invalid Request")
	docCameraCodecEmpty   = docError(400, `cameras[<index>].supported_codec must not be empty`, "Invalid Request")
	docCameraCodecRepeat  = docError(400, `cameras[<index>].supported_codec must not contain duplicates`, "Invalid Request")
	docCameraCodecUnknown = docError(400, `cameras[<index>].supported_codec[<codec>] must be one of `+strings.Join(ws.SupportedCodecNames(), ", "), "Invalid Request")

	// The supported lists bound what a later switch may select, so the
	// parameters a camera is at must appear in them.
	docCameraResolutionsEmpty       = docError(400, `cameras[<index>].supported_resolutions must not be empty`, "Invalid Request")
	docCameraResolutionEntryEmpty   = docError(400, `cameras[<index>].supported_resolutions[<resolution>] must not be empty`, "Invalid Request")
	docCameraResolutionsRepeat      = docError(400, `cameras[<index>].supported_resolutions must not contain duplicates`, "Invalid Request")
	docCameraResolutionSupported    = docError(400, `cameras[<index>].resolution must be one of the supported_resolutions`, "Invalid Request")
	docCameraFrameratesEmpty        = docError(400, `cameras[<index>].supported_framerates must not be empty`, "Invalid Request")
	docCameraFramerateEntryPositive = docError(400, `cameras[<index>].supported_framerates[<fps>] must be positive`, "Invalid Request")
	docCameraFrameratesRepeat       = docError(400, `cameras[<index>].supported_framerates must not contain duplicates`, "Invalid Request")
	docCameraFPSSupported           = docError(400, `cameras[<index>].fps must be one of the supported_framerates`, "Invalid Request")
)

// The optional switch parameters and the recording codec must each be a value
// the camera declared during registration.
var (
	docSwitchResolutionUnsupported = docError(400, `resolution "<resolution>" is not supported by camera_enum <camera_enum> on device "<device_id>"`, "Invalid Request")
	docSwitchFPSUnsupported        = docError(400, `fps <fps> is not supported by camera_enum <camera_enum> on device "<device_id>"`, "Invalid Request")
	docRecordingCodecUnsupported   = docError(400, `codec "<codec>" is not supported by camera_enum <camera_enum> on device "<device_id>"`, "Invalid Request")
)

var (
	deviceExample = map[string]any{
		"id":         "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"name":       "Room 101 device",
		"location":   "Building A / Room 101",
		"team_id":    "01J8Z2TEAM5A7C9E1G3J5L7N9P1",
		"owner_id":   "01J8Z1USER3Y5W7A9C1E3G5J7L9",
		"last_seen":  "2026-09-30T08:15:04Z",
		"created_at": "2026-09-01T09:00:00Z",
		"updated_at": "2026-09-30T08:15:04Z",
	}
	updatedDeviceExample = map[string]any{
		"id":         "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"name":       "Room 101 device (moved)",
		"location":   "Building A / Room 102",
		"team_id":    "01J8Z2TEAM5A7C9E1G3J5L7N9P1",
		"owner_id":   "01J8Z1USER3Y5W7A9C1E3G5J7L9",
		"last_seen":  "2026-09-30T08:15:04Z",
		"created_at": "2026-09-01T09:00:00Z",
		"updated_at": "2026-09-30T08:16:40Z",
	}
	deviceDetailExample = map[string]any{
		"id":         "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"name":       "Room 101 device",
		"location":   "Building A / Room 101",
		"team_id":    "01J8Z2TEAM5A7C9E1G3J5L7N9P1",
		"owner_id":   "01J8Z1USER3Y5W7A9C1E3G5J7L9",
		"last_seen":  "2026-09-30T08:15:04Z",
		"created_at": "2026-09-01T09:00:00Z",
		"updated_at": "2026-09-30T08:15:04Z",
		"online":     true,
		"cameras": []any{
			map[string]any{
				"camera_enum":           0,
				"resolution":            "1920x1080",
				"fps":                   30,
				"supported_resolutions": []any{"1920x1080", "1280x720"},
				"supported_framerates":  []any{30, 15},
				"supported_codec":       []any{"h264", "mjpeg"},
				"attrs":                 map[string]any{"label": "front"},
			},
			map[string]any{
				"camera_enum":           1,
				"resolution":            "1280x720",
				"fps":                   15,
				"supported_resolutions": []any{"1280x720"},
				"supported_framerates":  []any{15},
				"supported_codec":       []any{"mjpeg"},
			},
		},
	}
	deviceTokenExample = map[string]any{
		"device": deviceExample,
		"token":  "wdt_9Qm3vT7pX1cR5zB8nK2sD4fG6hJ0lM3aP7uW1yE5iO9qS2tV4xZ6bN8cF0gH2jL4",
	}
	streamExample = map[string]any{
		"id":          "01J8Z5STRM4C8N2P6R0T4V8X2Z6",
		"device_id":   "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"camera_enum": 0,
		"status":      "active",
		"started_at":  "2026-09-30T08:00:00Z",
		"metadata": map[string]any{
			"resolution": "1920x1080",
			"fps":        30,
			"codec":      "h264",
			"codecs":     []any{"h264", "mjpeg"},
		},
	}
	completedStreamExample = map[string]any{
		"id":          "01J8Z7STRM6E0R4T8V2X6Z0B4D8",
		"device_id":   "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"camera_enum": 1,
		"status":      "completed",
		"started_at":  "2026-09-30T07:15:00Z",
		"ended_at":    "2026-09-30T07:45:00Z",
		"metadata": map[string]any{
			"resolution": "1280x720",
			"fps":        15,
			"codec":      "mjpeg",
			"codecs":     []any{"mjpeg"},
		},
	}
	segmentExample = map[string]any{
		"id":          "01J8Z6SEGM5D9Q3S7U1W5Y9A3C7",
		"stream_id":   "01J8Z5STRM4C8N2P6R0T4V8X2Z6",
		"device_id":   "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"camera_enum": 0,
		"segment_seq": 12,
		"size_bytes":  1048576,
		"duration_ms": 6000,
		"created_at":  "2026-09-30T08:01:12Z",
	}
	segmentWithURLExample = map[string]any{
		"id":           "01J8Z6SEGM5D9Q3S7U1W5Y9A3C7",
		"stream_id":    "01J8Z5STRM4C8N2P6R0T4V8X2Z6",
		"device_id":    "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"camera_enum":  0,
		"segment_seq":  12,
		"size_bytes":   1048576,
		"duration_ms":  6000,
		"created_at":   "2026-09-30T08:01:12Z",
		"download_url": "https://filehouse.example.edu/objects/webcam-segments/01J8Z4W3K5M7Q9R1T3V5X7Z9B1/streams/01J8Z5STRM4C8N2P6R0T4V8X2Z6/20260930T080112_12.bin?X-Amz-Expires=900",
	}
	streamDetailExample = map[string]any{
		"id":          "01J8Z5STRM4C8N2P6R0T4V8X2Z6",
		"device_id":   "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"camera_enum": 0,
		"status":      "active",
		"started_at":  "2026-09-30T08:00:00Z",
		"metadata": map[string]any{
			"resolution": "1920x1080",
			"fps":        30,
			"codec":      "h264",
			"codecs":     []any{"h264", "mjpeg"},
		},
		"segments": []any{segmentWithURLExample},
	}
	photoExample = map[string]any{
		"id":           "01J8Z8PHOT7F1S5U9W3Y7A1C5E9",
		"device_id":    "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"camera_enum":  0,
		"content_type": "image/jpeg",
		"size_bytes":   245760,
		"request_id":   "01J8Z9REQ8G2T6V0X4Z8B2D6F0H4",
		"taken_at":     "2026-09-30T08:20:00Z",
		"created_at":   "2026-09-30T08:20:01Z",
	}
	photoDetailExample = map[string]any{
		"id":           "01J8Z8PHOT7F1S5U9W3Y7A1C5E9",
		"device_id":    "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"camera_enum":  0,
		"content_type": "image/jpeg",
		"size_bytes":   245760,
		"request_id":   "01J8Z9REQ8G2T6V0X4Z8B2D6F0H4",
		"taken_at":     "2026-09-30T08:20:00Z",
		"created_at":   "2026-09-30T08:20:01Z",
		"download_url": "https://filehouse.example.edu/objects/webcam-segments/01J8Z4W3K5M7Q9R1T3V5X7Z9B1/photos/01J8Z8PHOT7F1S5U9W3Y7A1C5E9?X-Amz-Expires=900",
	}
	registerRequestExample = map[string]any{
		"device_id": "01J8Z4W3K5M7Q9R1T3V5X7Z9B1",
		"cameras": []any{
			map[string]any{
				"camera_enum":           0,
				"resolution":            "1920x1080",
				"fps":                   30,
				"supported_resolutions": []any{"1920x1080", "1280x720"},
				"supported_framerates":  []any{30, 15},
				"supported_codec":       []any{"h264", "mjpeg"},
				"attrs":                 map[string]any{"label": "front"},
			},
			map[string]any{
				"camera_enum":           1,
				"resolution":            "1280x720",
				"fps":                   15,
				"supported_resolutions": []any{"1280x720"},
				"supported_framerates":  []any{15},
				"supported_codec":       []any{"mjpeg"},
			},
		},
	}
	registerResponseExample = map[string]any{
		"device_websocket_id": "9f2c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d",
		"expires_at":          "2026-10-04T10:01:00Z",
		"websocket_path":      "/ws/device/9f2c1d4e5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d",
	}
	switchAcceptedExample = map[string]any{
		"command_id":  "01J8ZA2CMD9H3V7Y1B5D9F3J7L1",
		"camera_enum": 1,
	}
	photoAcceptedExample = map[string]any{
		"command_id":  "01J8ZB4CMD1K5X9A3E7G1J5N9R3",
		"camera_enum": 0,
		"request_id":  "01J8Z9REQ8G2T6V0X4Z8B2D6F0H4",
	}
)

// DocOperations is the route contract used by the OpenAPI generator. Every
// route served by NewRouter must appear here exactly once, with its chi
// pattern, so cmd/genspec fails instead of publishing a stale reference.
var DocOperations = []apidocs.Operation{
	{
		Method:          "GET",
		Path:            "/healthz",
		Tag:             "Health",
		Summary:         "Check liveness",
		Description:     "Use this unauthenticated probe to determine whether the HTTP process is alive. It returns ok without touching the database, so orchestrators and load balancers can call it frequently.",
		Response:        statusResponse{},
		ResponseExample: map[string]string{"status": "ok"},
		Errors:          []apidocs.ErrorDoc{},
	},
	{
		Method:          "GET",
		Path:            "/readyz",
		Tag:             "Health",
		Summary:         "Check readiness",
		Description:     "Use this unauthenticated probe before routing traffic to the process. It reports ready as soon as the HTTP server serves requests; it does not check the database connection, so a database outage does not change this response.",
		Response:        statusResponse{},
		ResponseExample: map[string]string{"status": "ready"},
		Errors:          []apidocs.ErrorDoc{},
	},

	{
		Method:          "GET",
		Path:            "/ws/register",
		Tag:             "WebSocket",
		Summary:         "Register a device",
		Description:     "Use from a device agent to announce its cameras and obtain a single-use WebSocket ticket. The device token travels in the Authorization header because this call carries a JSON body; an unknown device and a wrong token answer the same 401 so the endpoint never leaks which devices exist. Camera parameters live only in the registration: camera_enum must be exactly 0..n-1 in the order given; resolution and fps are the parameters that camera is at for the current connection and must each appear in supported_resolutions and supported_framerates, two non-empty duplicate-free lists whose entries the server trims and whose frame rates must be positive; supported_codec must be non-empty with every element one of " + strings.Join(ws.SupportedCodecNames(), ", ") + ". A device captures from exactly one camera at exactly one resolution and frame rate at a time, and only a switch_camera command selects another camera or another pair; a device never changes them on its own. Registering again invalidates the device's previous unused ticket, and the returned device_websocket_id expires after the configured ticket TTL when it is never redeemed.",
		Request:         docRegisterRequest{},
		RequestExample:  registerRequestExample,
		Response:        docRegisterResponse{},
		ResponseExample: registerResponseExample,
		Errors: []apidocs.ErrorDoc{
			docInvalidBody,
			docBodyNotSingle,
			docBodyTooLarge,
			docCamerasEmpty,
			docCameraEnumOrder,
			docCameraResolution,
			docCameraResolutionsEmpty,
			docCameraFPS,
			docCameraFrameratesEmpty,
			docCameraCodecEmpty,
			docCameraResolutionEntryEmpty,
			docCameraResolutionsRepeat,
			docCameraResolutionSupported,
			docCameraFramerateEntryPositive,
			docCameraFrameratesRepeat,
			docCameraFPSSupported,
			docCameraCodecUnknown,
			docCameraCodecRepeat,
			docDeviceAuthMissing,
			docDeviceAuthScheme,
			docDeviceAuthToken,
			docDeviceAuthRejected,
			docFailure("load device for registration"),
			docFailure("issue device websocket ticket"),
		},
	},
	{
		Method:      "GET",
		Path:        "/ws/device/{device_websocket_id}",
		Tag:         "WebSocket",
		Summary:     "Open the device WebSocket",
		Description: "Use from a device to redeem its registration ticket and open the realtime channel. The ticket is the credential: no teamusers token is sent here. It is strictly single use: after one successful attach it is bound to that connection and becomes invalid the moment the connection closes, so a reconnect always requires a fresh registration. Attaching again with the same ticket while its session is still live answers 409; registering afresh replaces the live session and the server closes the old connection, finalizing its in-flight recordings. Control commands and acknowledgements are text JSON frames, media are binary frames carrying a length-prefixed JSON header followed by the raw bytes. A successful handshake answers 101 Switching Protocols, so the response below records the no-body default rather than a JSON document.",
		Errors: []apidocs.ErrorDoc{
			docTicketNotFound,
			docTicketReplay,
			docPanic,
		},
	},

	{
		Method:          "GET",
		Path:            "/api/devices",
		Tag:             "Devices",
		Summary:         "List devices",
		Description:     "Use to enumerate the devices visible to the caller. The result is filtered by the broadest matching scope: cam:read:any returns every device, cam:read:team only those of the caller's team and cam:read:own only those owned by the caller. Pass limit to cap the number returned; the default is 100 and the maximum is 1000.",
		Security:        "bearerAuth",
		Response:        listResponse[domain.Device]{},
		ResponseExample: map[string]any{"items": []any{deviceExample}},
		Errors:          []apidocs.ErrorDoc{docBadLimit, docUnauthorized, docForbidden("read"), docFailure("list devices")},
	},
	{
		Method:      "POST",
		Path:        "/api/devices",
		Tag:         "Devices",
		Summary:     "Register a device",
		Description: "Use to add a device to the fleet and issue its permanent device token, which is returned once in plaintext and can only be replaced by rotating it. Ownership is taken from the caller unless explicitly overridden: cam:manage:own callers always get owner_id set to their own subject, cam:manage:team callers may only set their own team, and cam:manage:any callers may set either field freely. The request answers 201 Created with the device and its token, and the device URL in the Location header.",
		Security:    "bearerAuth",
		Request:     docDeviceCreateRequest{},
		RequestExample: map[string]any{
			"name":     "Room 101 device",
			"location": "Building A / Room 101",
		},
		Response:        docDeviceToken{},
		ResponseExample: deviceTokenExample,
		Errors: []apidocs.ErrorDoc{
			docInvalidBody,
			docBodyNotSingle,
			docBodyTooLarge,
			docError(400, "name is required", "Invalid Request"),
			docUnauthorized,
			docForbidden("manage"),
			docDeviceExists,
			docFailure("create device"),
		},
	},
	{
		Method:          "GET",
		Path:            "/api/devices/{device_id}/",
		Tag:             "Devices",
		Summary:         "Get a device",
		Description:     "Use to fetch one device's stored fields together with its live state: online reports whether a device WebSocket is attached right now, and cameras lists the capabilities of the current registration - the resolution and frame rate each camera is at together with the supported_resolutions, supported_framerates and supported_codec lists a switch or a recording may select (empty while the device is offline).",
		Security:        "bearerAuth",
		Response:        docDeviceDetail{},
		ResponseExample: deviceDetailExample,
		Errors:          []apidocs.ErrorDoc{docUnauthorized, docForbidden("read"), docDeviceNotFound, docLoadFailure("device")},
	},
	{
		Method:      "PUT",
		Path:        "/api/devices/{device_id}/",
		Tag:         "Devices",
		Summary:     "Update a device",
		Description: "Use to change a device's fixed fields. The body is a partial update: present fields overwrite the stored values and absent fields keep theirs, and at least one field is required. Ownership fields may only be changed by callers whose scope allows it. Camera parameters are not stored and cannot be changed here.",
		Security:    "bearerAuth",
		Request:     docDeviceUpdateRequest{},
		RequestExample: map[string]any{
			"name":     "Room 101 device (moved)",
			"location": "Building A / Room 102",
		},
		Response:        domain.Device{},
		ResponseExample: updatedDeviceExample,
		Errors: []apidocs.ErrorDoc{
			docInvalidBody,
			docBodyNotSingle,
			docBodyTooLarge,
			docError(400, "request body must contain at least one of name, location, team_id or owner_id", "Invalid Request"),
			docError(400, "name must not be empty", "Invalid Request"),
			docUnauthorized,
			docForbidden("manage"),
			docDeviceNotFound,
			docLoadFailure("device"),
			docFailure("update device"),
		},
	},
	{
		Method:      "DELETE",
		Path:        "/api/devices/{device_id}/",
		Tag:         "Devices",
		Summary:     "Delete a device",
		Description: "Use to remove a device together with every stream, segment and photo recorded for it. A live connection is closed and any pending ticket is invalidated. The response has no body; deleting an unknown device answers 404 and changes nothing.",
		Security:    "bearerAuth",
		Errors:      []apidocs.ErrorDoc{docUnauthorized, docForbidden("manage"), docDeviceNotFound, docLoadFailure("device"), docFailure("delete device")},
	},
	{
		Method:          "POST",
		Path:            "/api/devices/{device_id}/token",
		Tag:             "Devices",
		Summary:         "Rotate the device token",
		Description:     "Use to replace a device's token. The old token stops working immediately, pending registration tickets are invalidated and the device's live WebSocket connection is closed, so the device must re-register with the new token; the new plaintext token is returned once in this response. The request has no body.",
		Security:        "bearerAuth",
		Response:        docDeviceToken{},
		ResponseExample: deviceTokenExample,
		Errors:          []apidocs.ErrorDoc{docUnauthorized, docForbidden("manage"), docDeviceNotFound, docLoadFailure("device"), docFailure("rotate device token")},
	},

	{
		Method:          "POST",
		Path:            "/api/devices/{device_id}/camera/switch",
		Tag:             "Devices",
		Summary:         "Switch the active camera",
		Description:     "Use to make one of the device's cameras the active one, optionally at new parameters. A device captures from exactly one camera at exactly one resolution and frame rate at a time, and only this command selects another camera or another pair; a device never changes them on its own. The device must be online (409 otherwise) and the camera enum must belong to its current registration (400 otherwise). resolution and fps are optional and must each be a value the camera declared during registration (400 otherwise); when one is absent it stays unchanged, so a switch to another camera leaves that camera at the parameters it reported. The command is queued to the device and answers 202 Accepted with the command id; the device reports the outcome asynchronously over the control channel.",
		Security:        "bearerAuth",
		Request:         docSwitchRequest{},
		RequestExample:  map[string]any{"camera_enum": 1, "resolution": "1280x720", "fps": 15},
		Response:        docSwitchAccepted{},
		ResponseExample: switchAcceptedExample,
		Errors: []apidocs.ErrorDoc{
			docInvalidBody,
			docBodyNotSingle,
			docBodyTooLarge,
			docError(400, "camera_enum is required", "Invalid Request"),
			docCameraEnumUnknown,
			docSwitchResolutionUnsupported,
			docSwitchFPSUnsupported,
			docUnauthorized,
			docForbidden("control"),
			docDeviceNotFound,
			docDeviceOfflineNoRegistration,
			docDeviceOfflineNoWebsocket,
			docCommandFailure(ws.CommandSwitchCamera),
			docLoadFailure("device"),
		},
	},
	{
		Method:          "POST",
		Path:            "/api/devices/{device_id}/recording/start",
		Tag:             "Streams",
		Summary:         "Start a recording stream",
		Description:     "Use to begin recording one camera of a device. The camera enum is validated against the device's current registration (400 when unknown, 409 when the device has no live session) and a stream must not already be active for that camera (409 otherwise). The recording runs on the camera's current resolution and frame rate; only the codec can be chosen, and only here - never on a camera switch or a photo. The optional codec must be a value the camera declared during registration (400 otherwise); when it is absent the device records with its preferred codec, the first entry of the camera's supported_codec list. The server creates the stream row first - snapshotting the camera's resolution and fps, its whole codec list into metadata.codecs and the requested codec into metadata.codec (omitted when none was named) - then tells the device to start pushing frames and stores each uploaded chunk as a segment in object storage. The request answers 201 Created with the new stream and its URL in the Location header; a stream whose start command cannot be delivered is marked failed.",
		Security:        "bearerAuth",
		Request:         docRecordingStartRequest{},
		RequestExample:  map[string]any{"camera_enum": 0, "codec": "h264"},
		Response:        domain.Stream{},
		ResponseExample: streamExample,
		Errors: []apidocs.ErrorDoc{
			docInvalidBody,
			docBodyNotSingle,
			docBodyTooLarge,
			docError(400, "camera_enum is required", "Invalid Request"),
			docCameraEnumUnknown,
			docRecordingCodecUnsupported,
			docUnauthorized,
			docForbidden("control"),
			docDeviceNotFound,
			docDeviceOfflineNoRegistration,
			docDeviceOfflineNoWebsocket,
			docStreamActive,
			docCommandFailure(ws.CommandStartRecording),
			docLoadFailure("device"),
			docFailure("create stream"),
		},
	},
	{
		Method:          "POST",
		Path:            "/api/devices/{device_id}/recording/stop",
		Tag:             "Streams",
		Summary:         "Stop the active stream",
		Description:     "Use to end a camera's active recording. The device is told to stop and buffered frames are flushed to object storage before the response is sent, so every segment of the finished recording is visible once this call returns. The request answers 200 with the stream marked completed; a camera that has no active stream answers 404.",
		Security:        "bearerAuth",
		Request:         docCameraCommandRequest{},
		RequestExample:  map[string]any{"camera_enum": 0},
		Response:        domain.Stream{},
		ResponseExample: completedStreamExample,
		Errors: []apidocs.ErrorDoc{
			docInvalidBody,
			docBodyNotSingle,
			docBodyTooLarge,
			docError(400, "camera_enum is required", "Invalid Request"),
			docUnauthorized,
			docForbidden("control"),
			docDeviceNotFound,
			docNoActiveStream,
			docStreamNotFoundByID,
			docDeviceOfflineNoWebsocket,
			docCommandFailure(ws.CommandStopRecording),
			docLoadFailure("device"),
			docFailure("find active stream"),
			docFailure("finish stream"),
		},
	},
	{
		Method:          "POST",
		Path:            "/api/devices/{device_id}/photo",
		Tag:             "Photos",
		Summary:         "Capture a photo",
		Description:     "Use to ask a camera for a single still image. The device must be online (409 otherwise) and the camera enum must belong to its current registration (400 otherwise). Still images are always JPEG: the upload either omits content_type or sets it to image/jpeg, any other value is discarded by the server so no photo record appears, and every stored photo reports content_type image/jpeg. The command is queued to the device and answers 202 Accepted with the command id and request id; the device later uploads a photo binary frame carrying the same request_id, and the uploaded photo becomes visible under the device's photos.",
		Security:        "bearerAuth",
		Request:         docCameraCommandRequest{},
		RequestExample:  map[string]any{"camera_enum": 0},
		Response:        docPhotoAccepted{},
		ResponseExample: photoAcceptedExample,
		Errors: []apidocs.ErrorDoc{
			docInvalidBody,
			docBodyNotSingle,
			docBodyTooLarge,
			docError(400, "camera_enum is required", "Invalid Request"),
			docCameraEnumUnknown,
			docUnauthorized,
			docForbidden("control"),
			docDeviceNotFound,
			docDeviceOfflineNoRegistration,
			docDeviceOfflineNoWebsocket,
			docCommandFailure(ws.CommandTakePhoto),
			docLoadFailure("device"),
		},
	},

	{
		Method:          "GET",
		Path:            "/api/devices/{device_id}/streams",
		Tag:             "Streams",
		Summary:         "List a device's streams",
		Description:     "Use to browse the recording history of one device, newest first. Pass limit to cap the number of streams returned; the default is 100 and the maximum is 1000.",
		Security:        "bearerAuth",
		Response:        listResponse[domain.Stream]{},
		ResponseExample: map[string]any{"items": []any{streamExample, completedStreamExample}},
		Errors:          []apidocs.ErrorDoc{docBadLimit, docUnauthorized, docForbidden("read"), docDeviceNotFound, docLoadFailure("device"), docFailure("list device streams")},
	},
	{
		Method:          "GET",
		Path:            "/api/devices/{device_id}/photos",
		Tag:             "Photos",
		Summary:         "List a device's photos",
		Description:     "Use to browse the photos captured by one device, newest first. Pass limit to cap the number returned; the default is 100 and the maximum is 1000. Photos carry metadata only; use GET /api/photos/{photo_id}/ when a download URL is needed.",
		Security:        "bearerAuth",
		Response:        listResponse[domain.Photo]{},
		ResponseExample: map[string]any{"items": []any{photoExample}},
		Errors:          []apidocs.ErrorDoc{docBadLimit, docUnauthorized, docForbidden("read"), docDeviceNotFound, docLoadFailure("device"), docFailure("list device photos")},
	},
	{
		Method:          "GET",
		Path:            "/api/streams/{stream_id}/",
		Tag:             "Streams",
		Summary:         "Get a stream",
		Description:     "Use to fetch one stream with its segments and a pre-signed download URL for each segment, valid for 15 minutes. Pass limit to cap the number of segments returned; the default is 100 and the maximum is 1000.",
		Security:        "bearerAuth",
		Response:        docStreamDetail{},
		ResponseExample: streamDetailExample,
		Errors:          []apidocs.ErrorDoc{docBadLimit, docUnauthorized, docForbidden("read"), docStreamNotFound, docStreamNotFoundByID, docLoadFailure("stream"), docFailure("load stream"), docFailure("list stream segments")},
	},
	{
		Method:          "GET",
		Path:            "/api/streams/{stream_id}/segments",
		Tag:             "Streams",
		Summary:         "List stream segments",
		Description:     "Use to enumerate the stored chunks of one stream in recording order. Pass limit to cap the number returned; the default is 100 and the maximum is 1000. Segments carry storage keys only; use GET /api/streams/{stream_id}/ when download URLs are needed.",
		Security:        "bearerAuth",
		Response:        listResponse[domain.StreamSegment]{},
		ResponseExample: map[string]any{"items": []any{segmentExample}},
		Errors:          []apidocs.ErrorDoc{docBadLimit, docUnauthorized, docForbidden("read"), docStreamNotFound, docStreamNotFoundByID, docLoadFailure("stream"), docFailure("load stream"), docFailure("list stream segments")},
	},
	{
		Method:          "GET",
		Path:            "/api/photos/{photo_id}/",
		Tag:             "Photos",
		Summary:         "Get a photo",
		Description:     "Use to fetch one photo's metadata together with a pre-signed download URL, valid for 15 minutes.",
		Security:        "bearerAuth",
		Response:        docPhotoDetail{},
		ResponseExample: photoDetailExample,
		Errors:          []apidocs.ErrorDoc{docUnauthorized, docForbidden("read"), docPhotoNotFound, docPhotoNotFoundByID, docLoadFailure("photo"), docFailure("load photo")},
	},
}
