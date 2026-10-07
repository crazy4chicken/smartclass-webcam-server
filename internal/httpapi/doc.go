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
// (application/problem+json) with type "about:blank"; the missing-claims 401
// emitted by the teamusers middleware uses the SDK's own
// {"allow": false, "reason": ...} shape instead.
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

// docCameraCommandRequest mirrors the body shared by the four command routes.
type docCameraCommandRequest struct {
	CameraEnum *int `json:"camera_enum"`
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

var (
	docInvalidBody       = docError(400, "invalid JSON request body", "Invalid Request")
	docBodyTooLarge      = docError(413, "request body too large", "Request Entity Too Large")
	docBadLimit          = docError(400, "limit must be a positive integer", "Invalid Request")
	docUnauthorized      = docError(401, "authentication failed", "Unauthorized")
	docDeviceAuthFailed  = docError(401, "device authentication failed", "Unauthorized")
	docDeviceNotFound    = docError(404, "device not found", "Not Found")
	docStreamNotFound    = docError(404, "stream not found", "Not Found")
	docPhotoNotFound     = docError(404, "photo not found", "Not Found")
	docTicketNotFound    = docError(404, "device websocket ticket not found", "Not Found")
	docTicketReplay      = docError(409, "device websocket ticket is already attached to a live session", "Conflict")
	docDeviceOffline     = docError(409, "device is offline", "Conflict")
	docStreamActive      = docError(409, "a stream is already active for this camera", "Conflict")
	docCommandFailed     = docError(502, "device connection is unavailable", "Bad Gateway")
	docInternalFailure   = docError(500, "internal server error", "Internal Server Error")
	docCameraEnumUnknown = docError(400, "camera_enum is not part of the device registration", "Invalid Request")
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
				"camera_enum":     0,
				"resolution":      "1920x1080",
				"fps":             30,
				"supported_codec": []any{"h264", "mjpeg"},
				"attrs":           map[string]any{"label": "front"},
			},
			map[string]any{
				"camera_enum":     1,
				"resolution":      "1280x720",
				"fps":             15,
				"supported_codec": []any{"mjpeg"},
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
				"camera_enum":     0,
				"resolution":      "1920x1080",
				"fps":             30,
				"supported_codec": []any{"h264", "mjpeg"},
				"attrs":           map[string]any{"label": "front"},
			},
			map[string]any{
				"camera_enum":     1,
				"resolution":      "1280x720",
				"fps":             15,
				"supported_codec": []any{"mjpeg"},
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
		Description:     "Use from a device agent to announce its cameras and obtain a single-use WebSocket ticket. The device token travels in the Authorization header because this call carries a JSON body; an unknown device and a wrong token answer the same 401 so the endpoint never leaks which devices exist. Camera parameters live only in the registration: camera_enum must be exactly 0..n-1 in the order given, resolution must not be empty, fps must be positive and supported_codec must be non-empty with every element one of " + strings.Join(ws.SupportedCodecNames(), ", ") + ". Registering again invalidates the device's previous unused ticket, and the returned device_websocket_id expires after the configured ticket TTL when it is never redeemed.",
		Request:         docRegisterRequest{},
		RequestExample:  registerRequestExample,
		Response:        docRegisterResponse{},
		ResponseExample: registerResponseExample,
		Errors: []apidocs.ErrorDoc{
			docInvalidBody,
			docError(400, "cameras must not be empty", "Invalid Request"),
			docError(400, "camera_enum values must be 0..n-1 in order without gaps or duplicates", "Invalid Request"),
			docBodyTooLarge,
			docDeviceAuthFailed,
			docInternalFailure,
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
			docInternalFailure,
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
		Errors:          []apidocs.ErrorDoc{docBadLimit, docUnauthorized, docForbidden("read"), docInternalFailure},
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
			docError(400, "name is required", "Invalid Request"),
			docError(400, "team_id and owner_id cannot be set outside your scope", "Invalid Request"),
			docBodyTooLarge,
			docUnauthorized,
			docForbidden("manage"),
			docInternalFailure,
		},
	},
	{
		Method:          "GET",
		Path:            "/api/devices/{device_id}/",
		Tag:             "Devices",
		Summary:         "Get a device",
		Description:     "Use to fetch one device's stored fields together with its live state: online reports whether a device WebSocket is attached right now, and cameras lists the camera parameters of the current registration (empty while the device is offline).",
		Security:        "bearerAuth",
		Response:        docDeviceDetail{},
		ResponseExample: deviceDetailExample,
		Errors:          []apidocs.ErrorDoc{docUnauthorized, docForbidden("read"), docDeviceNotFound, docInternalFailure},
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
			docError(400, "request body must contain at least one of name, location, team_id or owner_id", "Invalid Request"),
			docError(400, "name must not be empty", "Invalid Request"),
			docBodyTooLarge,
			docUnauthorized,
			docForbidden("manage"),
			docDeviceNotFound,
			docInternalFailure,
		},
	},
	{
		Method:      "DELETE",
		Path:        "/api/devices/{device_id}/",
		Tag:         "Devices",
		Summary:     "Delete a device",
		Description: "Use to remove a device together with every stream, segment and photo recorded for it. A live connection is closed and any pending ticket is invalidated. The response has no body; deleting an unknown device answers 404 and changes nothing.",
		Security:    "bearerAuth",
		Errors:      []apidocs.ErrorDoc{docUnauthorized, docForbidden("manage"), docDeviceNotFound, docInternalFailure},
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
		Errors:          []apidocs.ErrorDoc{docUnauthorized, docForbidden("manage"), docDeviceNotFound, docInternalFailure},
	},

	{
		Method:          "POST",
		Path:            "/api/devices/{device_id}/camera/switch",
		Tag:             "Devices",
		Summary:         "Switch the active camera",
		Description:     "Use to make one of the device's cameras the active one for subsequent operations. The device must be online (409 otherwise) and the camera enum must belong to its current registration (400 otherwise). The command is queued to the device and answers 202 Accepted with the command id; the device reports the outcome asynchronously over the control channel.",
		Security:        "bearerAuth",
		Request:         docCameraCommandRequest{},
		RequestExample:  map[string]any{"camera_enum": 1},
		Response:        docSwitchAccepted{},
		ResponseExample: switchAcceptedExample,
		Errors: []apidocs.ErrorDoc{
			docInvalidBody,
			docError(400, "camera_enum is required", "Invalid Request"),
			docCameraEnumUnknown,
			docBodyTooLarge,
			docUnauthorized,
			docForbidden("control"),
			docDeviceNotFound,
			docDeviceOffline,
			docCommandFailed,
			docInternalFailure,
		},
	},
	{
		Method:          "POST",
		Path:            "/api/devices/{device_id}/recording/start",
		Tag:             "Streams",
		Summary:         "Start a recording stream",
		Description:     "Use to begin recording one camera of a device. The camera enum is validated against the device's current registration (400 when unknown, 409 when the device has no live session) and a stream must not already be active for that camera (409 otherwise). The server creates the stream row first — snapshotting the camera's resolution, fps and codec list into its metadata — then tells the device to start pushing frames and stores each uploaded chunk as a segment in object storage. The request answers 201 Created with the new stream and its URL in the Location header; a stream whose start command cannot be delivered is marked failed.",
		Security:        "bearerAuth",
		Request:         docCameraCommandRequest{},
		RequestExample:  map[string]any{"camera_enum": 0},
		Response:        domain.Stream{},
		ResponseExample: streamExample,
		Errors: []apidocs.ErrorDoc{
			docInvalidBody,
			docError(400, "camera_enum is required", "Invalid Request"),
			docCameraEnumUnknown,
			docBodyTooLarge,
			docUnauthorized,
			docForbidden("control"),
			docDeviceNotFound,
			docDeviceOffline,
			docStreamActive,
			docCommandFailed,
			docInternalFailure,
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
			docError(400, "camera_enum is required", "Invalid Request"),
			docBodyTooLarge,
			docUnauthorized,
			docForbidden("control"),
			docDeviceNotFound,
			docStreamNotFound,
			docDeviceOffline,
			docCommandFailed,
			docInternalFailure,
		},
	},
	{
		Method:          "POST",
		Path:            "/api/devices/{device_id}/photo",
		Tag:             "Photos",
		Summary:         "Capture a photo",
		Description:     "Use to ask a camera for a single still image. The device must be online (409 otherwise) and the camera enum must belong to its current registration (400 otherwise). The command is queued to the device and answers 202 Accepted with the command id and request id; the device later uploads a photo binary frame carrying the same request_id, and the uploaded photo becomes visible under the device's photos.",
		Security:        "bearerAuth",
		Request:         docCameraCommandRequest{},
		RequestExample:  map[string]any{"camera_enum": 0},
		Response:        docPhotoAccepted{},
		ResponseExample: photoAcceptedExample,
		Errors: []apidocs.ErrorDoc{
			docInvalidBody,
			docError(400, "camera_enum is required", "Invalid Request"),
			docCameraEnumUnknown,
			docBodyTooLarge,
			docUnauthorized,
			docForbidden("control"),
			docDeviceNotFound,
			docDeviceOffline,
			docCommandFailed,
			docInternalFailure,
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
		Errors:          []apidocs.ErrorDoc{docBadLimit, docUnauthorized, docForbidden("read"), docDeviceNotFound, docInternalFailure},
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
		Errors:          []apidocs.ErrorDoc{docBadLimit, docUnauthorized, docForbidden("read"), docDeviceNotFound, docInternalFailure},
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
		Errors:          []apidocs.ErrorDoc{docBadLimit, docUnauthorized, docForbidden("read"), docStreamNotFound, docInternalFailure},
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
		Errors:          []apidocs.ErrorDoc{docBadLimit, docUnauthorized, docForbidden("read"), docStreamNotFound, docInternalFailure},
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
		Errors:          []apidocs.ErrorDoc{docUnauthorized, docForbidden("read"), docPhotoNotFound, docInternalFailure},
	},
}
