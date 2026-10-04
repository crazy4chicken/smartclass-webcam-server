---
title: Device registration
outline: 2
---

# Device registration

`GET /ws/register` is how a device agent starts a session. It presents its long-lived device token
and the full list of cameras it drives, and receives a single-use WebSocket ticket. The ticket is
the only credential used on the WebSocket itself.

Registration is an HTTP `GET` that carries a JSON body. The camera list is structured data that
stays out of the URL query string and out of access logs, while the call still behaves like the
read it is: the server parses the body exactly as it would on a `POST`. Every proxy in front of the
server must forward GET bodies unchanged (the [deployment guide](/guide/deploy) calls this out).

- Token provisioning and rotation: `POST /api/devices` and
  `POST /api/devices/{device_id}/token` in the [Devices reference](/api/reference/devices) and the
  [deployment guide](/guide/deploy).
- Next step: [WebSocket transport](/protocol/transport) redeems the ticket returned here.

## Request

```http
GET /ws/register HTTP/1.1
Authorization: Bearer wdt_VDhYaHHNEKsCD5z1wuEGY35UhmwSaGdmUbkUjPeKJOY
Content-Type: application/json

{
  "device_id": "01J8ZK9WQ7X3YV0M4N5P6Q7R8S",
  "cameras": [
    {
      "camera_enum": 0,
      "resolution": "1920x1080",
      "fps": 30,
      "supported_codec": ["h264", "h265", "mjpeg"],
      "attrs": {"label": "front"}
    },
    {
      "camera_enum": 1,
      "resolution": "1280x720",
      "fps": 15,
      "supported_codec": ["mjpeg"]
    }
  ]
}
```

### Request headers

| Header | Required | Allowed values | Notes |
| --- | --- | --- | --- |
| `Authorization` | Yes | `Bearer` or `bearer` + one or more spaces/tabs + a device token: the literal `wdt_` followed by 43 base64url characters (`A-Z`, `a-z`, `0-9`, `-`, `_`; 32 random bytes, no padding). Nothing else is accepted. | Grammar: the header is split on whitespace and must yield exactly two fields, the first equal to `bearer` (case-insensitive) and the second starting with the literal `wdt_` (case-sensitive). Anything else is treated as no token at all. |
| `Content-Type` | No | Any string; the value the service documents and sends is `application/json`. | Not validated by the server; the body is decoded as JSON regardless of this header. Send `application/json` so proxies and logs see the correct type. |
| `Content-Length` / `Transfer-Encoding` | Yes | Standard HTTP framing; the body is a single JSON object of at most 1 MiB (1048576 bytes). | Normal HTTP framing rules apply. |

The body limit is 1 MiB (1048576 bytes), enforced while decoding. A larger body answers `413 Request
Entity Too Large`; the device token is never inspected in that case, because the body is decoded
first.

### Request body

```text
{
  "device_id": <string>,
  "cameras": [ <camera>, ... ]
}
```

| Field | JSON type | Required | Allowed values | Meaning |
| --- | --- | --- | --- | --- |
| `device_id` | string | Yes | The id of an existing device: a 26-character ULID as issued by `POST /api/devices`, e.g. `01J8ZK9WQ7X3YV0M4N5P6Q7R8S`. The server looks the device up by this exact string (no case folding); an unknown id answers `401`. | Identifies the device whose token is presented. The token's SHA-256 hash must equal the stored hash of that device; an unknown id and a wrong token are indistinguishable (`401`). |
| `cameras` | array of camera objects | Yes | At least one JSON object (see [`cameras[]`](#cameras)); the array length `n` fixes the camera range `0..n-1`. | The full camera set the device drives. The order defines the camera numbering. |

#### `cameras[]`

| Field | JSON type | Required | Default | Allowed values | Meaning |
| --- | --- | --- | --- | --- | --- |
| `camera_enum` | integer | Yes | `0` when omitted | Integer in `0..n-1` only, where `n` is the length of `cameras`; it must equal the element's index. A fractional number (e.g. `0.5`) or a string is a decode error (`400`). | Must equal the element's index in the array: `0` for the first camera, `1` for the second, and so on. A gap, a duplicate or a reorder is rejected with `cameras[i].camera_enum must be i`. The value identifies the camera in every later command and media frame. |
| `resolution` | string | Yes | `""` when omitted | Free-form non-empty string (leading/trailing whitespace is ignored by the check; only a whitespace-only value is rejected). Canonical convention: `WIDTHxHEIGHT` in pixels, e.g. `1920x1080` or `1280x720`. The server never parses it. | The announced value is kept in the live registration and snapshotted into the stream's `metadata.resolution`. |
| `fps` | integer | Yes | `0` when omitted | Integer greater than `0` (`1`, `2`, `3`, ...). `0`, a negative value or a missing key is rejected with `400`; a JSON fraction such as `29.97` fails decoding with `400` rather than being rounded. | Frames per second the device expects to deliver. Snapshotted into a stream's `metadata.fps` and used to estimate segment durations. |
| `supported_codec` | array of strings | Yes | `null` when omitted | At least one element, each exactly one of `h264`, `h265`, `mjpeg`, `mpeg4`, `vp8`, `vp9`, `av1` — FFmpeg-style names in exact lowercase, so `H264` and the alias `hevc` are rejected; duplicates are rejected. See [Codec values](#codec-values). | The codecs the device can produce for this camera. Snapshotted into the stream's `metadata.codecs`; the server never negotiates a codec. |
| `attrs` | object | No | `{}` when omitted | Any JSON object; free-form keys and values of any JSON type, stored verbatim (never validated, never persisted to the database). | Device-specific data (for example a label). Lives in the registration and is visible through `GET /api/devices/{device_id}/` while the device is online. |

Decoder behavior, verified against the implementation:

- Unknown fields are ignored, at every level; there is no strict-field mode.
- Duplicate keys keep the last value.
- `null` decodes like the field's zero value: `"cameras": null` becomes an empty list (`400
  cameras must not be empty`), a `null` string becomes `""`, a `null` integer becomes `0`.
- A body of exactly `null` decodes successfully into the zero request; it then fails the ordinary
  checks.
- Arrays and objects must be well formed and types must match exactly: `"fps": "30"` is a decode
  error, not a coercion.

### Codec values

`supported_codec` is a closed vocabulary: the server accepts exactly the seven values below and
rejects everything else. The names follow FFmpeg-style codec identifiers and are exact lowercase
only; there is no alias handling, so `hevc` is not accepted as a synonym for `h265`. It performs
**no negotiation and no normalisation**, and it never intersects the announced list with a
server-side list.

| Allowed value | Meaning | Typical producer |
| --- | --- | --- |
| `h264` | H.264/AVC video; typically one encoded access unit per `recording.frame`. | Cameras encoding video with hardware or software H.264. |
| `h265` | H.265/HEVC video; typically one encoded access unit per `recording.frame`. | Cameras encoding video with hardware or software H.265/HEVC. |
| `mjpeg` | Motion JPEG video; one JPEG picture per `recording.frame`. | Cameras that emit JPEG frames continuously. |
| `mpeg4` | MPEG-4 Part 2 video; typically one encoded access unit per `recording.frame`. | Cameras or encoder libraries producing MPEG-4 Part 2 elementary streams. |
| `vp8` | Google VP8 video; typically one encoded access unit per `recording.frame`. | Cameras or software encoders producing VP8. |
| `vp9` | Google VP9 video; typically one encoded access unit per `recording.frame`. | Cameras or software encoders producing VP9. |
| `av1` | AOMedia AV1 video; typically one encoded access unit per `recording.frame`. | Cameras or software encoders producing AV1. |
| `image/jpeg` | Not a `supported_codec` value. It is the canonical `content_type` of a `photo.photo` body on the `photo` channel. | Still-image capture. |

The values are listed in the server's canonical order (`ws.SupportedCodecNames()`,
`internal/ws/codec.go`); rejection messages join the names with `, ` in exactly this order.

Enforcement semantics, exactly as implemented (`internal/httpapi/ws_handler.go`,
`validateCameraCapabilities`):

- **The array must exist and be non-empty.** `[]` and `null` are rejected with `400
  cameras[i].supported_codec must not be empty`.
- **Every element must match exactly.** Each element must be one of the seven literals `h264`,
  `h265`, `mjpeg`, `mpeg4`, `vp8`, `vp9`, `av1`: comparison is case-sensitive, with no trimming,
  no case folding and no synonym handling. `""`, `"H264"`, `"MJPEG"`, `"h264 "` and `"hevc"` are
  all rejected with `400
  cameras[i].supported_codec[j] must be one of h264, h265, mjpeg, mpeg4, vp8, vp9, av1`, where
  `i` is the camera index and `j` the element index.
- **Duplicates are rejected.** A value that appears twice inside the array answers `400
  cameras[i].supported_codec must not contain duplicates`.
- **Checks run in this order per camera:** missing or empty array, then per-element membership
  (the first offending element wins), then duplicates.
- **List order carries no server semantics.** It is a device-side preference declaration; the
  server never reads the first, last or any particular element.

What the server still does **not** do:

- **No negotiation and no recording of a negotiated codec.** `recording/start` copies the
  announced list into the stream's `metadata.codecs` (`internal/httpapi/commands.go`);
  `metadata.codec` — a schema field — is never set by any code path
  (`internal/domain/domain.go`). `metadata.codecs` is a snapshot of the validated announcement,
  not a codec the server picked.
- **Segment and photo bytes stay opaque.** They are stored byte for byte and never parsed for
  codec information (`internal/httpapi/media.go`); the segment object is uploaded as
  `application/octet-stream`. Nothing checks that the payload bytes match the announced value.

**Where the announced values surface:** in the live device detail (`cameras` of
`GET /api/devices/{device_id}/` while the device is online) and in the `metadata.codecs` array
of the stream resource returned by `recording/start` and the read endpoints. Consumers of
`metadata.codecs` can rely on every element being exactly one of the seven lowercase values.

**Extending the vocabulary requires a server change.** A device cannot introduce a value outside
this list (for example `prores`): the accepted set is fixed in the server
(`internal/ws/codec.go`), and adding a value means changing the server, not just deploying a
device.

Rejected example — the request passes every other camera rule but announces a case variant and an
unsupported value:

```json
{
  "device_id": "01J8ZK9WQ7X3YV0M4N5P6Q7R8S",
  "cameras": [
    {
      "camera_enum": 0,
      "resolution": "1920x1080",
      "fps": 30,
      "supported_codec": ["H264", "prores"]
    }
  ]
}
```

`400 Bad Request`, content type `application/problem+json` — the first offending element is
reported:

```json
{
  "type": "about:blank",
  "title": "Bad Request",
  "status": 400,
  "detail": "cameras[0].supported_codec[0] must be one of h264, h265, mjpeg, mpeg4, vp8, vp9, av1",
  "instance": "/ws/register"
}
```

A duplicate such as `["h264", "h264"]` answers
`cameras[0].supported_codec must not contain duplicates` with the same status and body shape.

### Validation order

The handler applies its checks in this order, so the first failure wins:

1. **Body decoding** - malformed JSON, a non-object body, more than one JSON value, or a body over
   1 MiB (`400` / `413`). This happens before authentication.
2. **Token extraction** - a missing or non-`wdt_` bearer token answers `401`.
3. **Device lookup** - an unknown `device_id` answers the same `401`; a database error answers
   `500`.
4. **Token comparison** - the SHA-256 hash of the presented token is compared in constant time
   with the stored hash; a mismatch answers the same `401`.
5. **Camera rules** - the `cameras` array is validated per element (`400`), in the order
   `camera_enum`, `resolution`, `fps`, then `supported_codec` (non-empty, then per-element
   membership, then duplicates); the first failure wins.
6. **Ticket minting** - a failure here answers `500`.

Because authentication precedes camera validation, an invalid token hides camera errors; because
decoding precedes authentication, a malformed body with an invalid token still answers `400`.

### Failure table

`i` is the index of the failing camera in the request array. Every problem body carries
`"type": "about:blank"`, the `title` matching the status text, and
`"instance": "/ws/register"`; the table lists the `detail` value.

| Condition | Status | `detail` |
| --- | --- | --- |
| Body is not valid JSON, or not a single JSON value | `400` | `invalid JSON request body: <decoder message>` |
| Body holds more than one JSON value | `400` | `request body must contain a single JSON object` |
| Body larger than 1 MiB | `413` | `request body too large` |
| Missing, malformed or non-`wdt_` `Authorization` header | `401` | `device authentication failed` |
| Unknown `device_id`, or token hash mismatch | `401` | `device authentication failed` |
| `cameras` empty or `null` | `400` | `cameras must not be empty` |
| `cameras[i].camera_enum` is not `i` (gap, duplicate, reorder) | `400` | `cameras[i].camera_enum must be i` |
| `cameras[i].resolution` empty or whitespace-only | `400` | `cameras[i].resolution must not be empty` |
| `cameras[i].fps` missing, zero or negative | `400` | `cameras[i].fps must be positive` |
| `cameras[i].supported_codec` missing or empty | `400` | `cameras[i].supported_codec must not be empty` |
| `cameras[i].supported_codec[j]` is not one of the seven accepted values | `400` | `cameras[i].supported_codec[j] must be one of h264, h265, mjpeg, mpeg4, vp8, vp9, av1` |
| `cameras[i].supported_codec` contains a duplicate value | `400` | `cameras[i].supported_codec must not contain duplicates` |
| Database failure while loading the device or minting the ticket | `500` | `internal server error` |

The `401` response also carries `WWW-Authenticate: Bearer realm="device"`. All other listed
responses are `application/problem+json` with no extra headers; the `413` body is the standard
problem shape shown in the examples below.

## Response

A successful registration answers `200 OK` with content type `application/json; charset=utf-8`:

```json
{
  "device_websocket_id": "0b30557a9fc4e90e33587da2c7ec11365b80a5caef14395e83a8cdf2173c6186",
  "expires_at": "2026-10-04T10:01:00Z",
  "websocket_path": "/ws/device/0b30557a9fc4e90e33587da2c7ec11365b80a5caef14395e83a8cdf2173c6186"
}
```

| Field | JSON type | Allowed values | Meaning |
| --- | --- | --- | --- |
| `device_websocket_id` | string | Exactly 64 lowercase hex characters (`0-9`, `a-f`): 32 random bytes hex-encoded. Regenerated on every successful call; the value is never reused. | The ticket. Single use (see below). |
| `expires_at` | string | RFC 3339 / RFC3339Nano timestamp in UTC, normally with sub-second precision (Go `time.Time` JSON encoding), e.g. `2026-10-04T10:01:00Z` or `2026-10-04T10:01:00.512384921Z`. | Expiry of the pending ticket: the issue time plus the ticket TTL. Parse it with a full RFC 3339 parser. |
| `websocket_path` | string | Fixed shape `/ws/device/` + the `device_websocket_id` value, e.g. `/ws/device/0b30557a…`. | Ready-to-use path for the upgrade. Served relative to the same origin and base path as this request. |

All three fields are always present on a `200` response.

Notes on side effects of a successful call:

- The ticket becomes the device's **pending** ticket, replacing any previous unused ticket of the
  same device.
- The device's `last_seen` is refreshed best-effort. A failure to record it is logged and does not
  fail registration.
- A live WebSocket session of the same device is **not** touched yet; it is only replaced when the
  new ticket attaches (see [Transport](/protocol/transport)).

### Ticket lifecycle

| # | Rule |
| --- | --- |
| 1 | **Creation.** Every successful registration mints a fresh ticket: 32 random bytes hex-encoded. It is indexed by ticket id and as the device's pending ticket. |
| 2 | **TTL.** An unused ticket is valid until `expires_at` (default 60 seconds, `WEBCAM_WS_TICKET_TTL`). Expiry is evaluated lazily on lookup, so an expired ticket simply stops existing and answers `404`. |
| 3 | **Single use.** The first successful attach binds the ticket to that connection forever. The attach is the single point of truth: a second connection using the same ticket is closed right after the upgrade with code `1008`. |
| 4 | **Replay while live.** Redeeming the ticket that *is* the device's live session answers `409 Conflict` before the upgrade, because the connection already holds it. |
| 5 | **Re-registration.** Registering again silently invalidates the device's previous **unused** ticket. A device that restarts before attaching therefore cleans up after itself. |
| 6 | **Replacement.** A newly attached ticket replaces a live session: the server closes the old connection without a close handshake. Until the new connection attaches, the old session keeps running. |
| 7 | **Death with the connection.** Attached tickets do not expire by time; they die the moment their connection closes. After that the ticket answers `404`, even with the original `expires_at` still in the future. |
| 8 | **Administrative invalidation.** Rotating the device token or deleting the device drops the pending ticket and closes the live connection. |
| 9 | **No resume.** There is no way to re-attach a ticket or resume a session; a reconnect always starts with a fresh `GET /ws/register`. |

## Worked examples

### curl

```sh
curl -sS -X GET "$WEBCAM_URL/ws/register" \
  -H "Authorization: Bearer $WEBCAM_DEVICE_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{
    "device_id": "01J8ZK9WQ7X3YV0M4N5P6Q7R8S",
    "cameras": [
      {
        "camera_enum": 0,
        "resolution": "1920x1080",
        "fps": 30,
        "supported_codec": ["h264", "h265", "mjpeg"],
        "attrs": {"label": "front"}
      },
      {
        "camera_enum": 1,
        "resolution": "1280x720",
        "fps": 15,
        "supported_codec": ["mjpeg"]
      }
    ]
  }'
```

### Valid response

```json
{
  "device_websocket_id": "0b30557a9fc4e90e33587da2c7ec11365b80a5caef14395e83a8cdf2173c6186",
  "expires_at": "2026-10-04T10:01:00Z",
  "websocket_path": "/ws/device/0b30557a9fc4e90e33587da2c7ec11365b80a5caef14395e83a8cdf2173c6186"
}
```

### Camera gap

Only one camera is registered, but it is numbered `1`. Indices must run `0..n-1`, so the second
element would have to exist or this element would have to be `0`:

```json
{
  "device_id": "01J8ZK9WQ7X3YV0M4N5P6Q7R8S",
  "cameras": [
    {
      "camera_enum": 1,
      "resolution": "1920x1080",
      "fps": 30,
      "supported_codec": ["h264"]
    }
  ]
}
```

`400 Bad Request`, content type `application/problem+json`:

```json
{
  "type": "about:blank",
  "title": "Bad Request",
  "status": 400,
  "detail": "cameras[0].camera_enum must be 0",
  "instance": "/ws/register"
}
```

### Duplicate camera number

Two cameras both claim `0`; the second must be `1`:

```json
{
  "device_id": "01J8ZK9WQ7X3YV0M4N5P6Q7R8S",
  "cameras": [
    {
      "camera_enum": 0,
      "resolution": "1920x1080",
      "fps": 30,
      "supported_codec": ["h264"]
    },
    {
      "camera_enum": 0,
      "resolution": "1280x720",
      "fps": 15,
      "supported_codec": ["mjpeg"]
    }
  ]
}
```

`400 Bad Request`:

```json
{
  "type": "about:blank",
  "title": "Bad Request",
  "status": 400,
  "detail": "cameras[1].camera_enum must be 1",
  "instance": "/ws/register"
}
```

### Empty codec list

```json
{
  "device_id": "01J8ZK9WQ7X3YV0M4N5P6Q7R8S",
  "cameras": [
    {
      "camera_enum": 0,
      "resolution": "1920x1080",
      "fps": 30,
      "supported_codec": []
    }
  ]
}
```

`400 Bad Request`:

```json
{
  "type": "about:blank",
  "title": "Bad Request",
  "status": 400,
  "detail": "cameras[0].supported_codec must not be empty",
  "instance": "/ws/register"
}
```

### Empty camera list

```json
{
  "device_id": "01J8ZK9WQ7X3YV0M4N5P6Q7R8S",
  "cameras": []
}
```

`400 Bad Request`:

```json
{
  "type": "about:blank",
  "title": "Bad Request",
  "status": 400,
  "detail": "cameras must not be empty",
  "instance": "/ws/register"
}
```

### Other camera rule failures

Each row changes the first camera of the valid request; the status is always `400` and the body is a
problem document with the listed `detail` (`cameras[i]` uses the failing element's index):

| Change | `detail` |
| --- | --- |
| `resolution` set to `"  "` (only whitespace) | `cameras[0].resolution must not be empty` |
| `resolution` omitted | `cameras[0].resolution must not be empty` |
| `fps` set to `0` | `cameras[0].fps must be positive` |
| `fps` set to `-5` | `cameras[0].fps must be positive` |
| `supported_codec` set to `[]` or `null` | `cameras[0].supported_codec must not be empty` |
| `supported_codec` set to `["H264"]` or `["prores"]` | `cameras[0].supported_codec[0] must be one of h264, h265, mjpeg, mpeg4, vp8, vp9, av1` |
| `supported_codec` set to `["h264", "h264"]` | `cameras[0].supported_codec must not contain duplicates` |
| `camera_enum` omitted on the first camera | Accepted as `0`; the remaining rules still apply |

### Missing or malformed token

No `Authorization` header, a header that is not `Bearer`, or a token that does not start with
`wdt_` all answer the same way:

```http
HTTP/1.1 401 Unauthorized
Content-Type: application/problem+json
WWW-Authenticate: Bearer realm="device"
```

```json
{
  "type": "about:blank",
  "title": "Unauthorized",
  "status": 401,
  "detail": "device authentication failed",
  "instance": "/ws/register"
}
```

### Wrong token or unknown device

A token that has the right shape but does not match the stored hash, and a `device_id` that does
not exist, produce the *identical* `401` body above. The endpoint never reveals whether a device
exists.

### Oversized body

A body larger than 1 MiB (1048576 bytes) answers `413` while decoding; the request is rejected
before the token is checked. The body itself is not echoed:

```http
HTTP/1.1 413 Request Entity Too Large
Content-Type: application/problem+json
```

```json
{
  "type": "about:blank",
  "title": "Request Entity Too Large",
  "status": 413,
  "detail": "request body too large",
  "instance": "/ws/register"
}
```

### Malformed JSON

A truncated or syntactically invalid body answers `400` and quotes the decoder error. Body, shown
verbatim:

```text
{
  "device_id": "01J8ZK9WQ7X3YV0M4N5P6Q7R8S",
  "cameras": [
```

```json
{
  "type": "about:blank",
  "title": "Bad Request",
  "status": 400,
  "detail": "invalid JSON request body: unexpected EOF",
  "instance": "/ws/register"
}
```

### Wrong field type

`device_id` sent as a number instead of a string:

```json
{
  "device_id": 123,
  "cameras": []
}
```

```json
{
  "type": "about:blank",
  "title": "Bad Request",
  "status": 400,
  "detail": "invalid JSON request body: json: cannot unmarshal number into Go struct field deviceRegisterRequest.device_id of type string",
  "instance": "/ws/register"
}
```

The same applies to a nested field: `"fps": "30"` answers with
`invalid JSON request body: json: cannot unmarshal string into Go struct field deviceRegisterRequest.cameras.0.fps of type int`.

### Two JSON documents

A body carrying more than one JSON value is rejected, even if the first one is valid. Body, shown
verbatim:

```text
{"device_id": "01J8ZK9WQ7X3YV0M4N5P6Q7R8S", "cameras": []}{"device_id": "01J8ZK9WQ7X3YV0M4N5P6Q7R8S", "cameras": []}
```

```json
{
  "type": "about:blank",
  "title": "Bad Request",
  "status": 400,
  "detail": "request body must contain a single JSON object",
  "instance": "/ws/register"
}
```

## What to do next

1. Save the ticket and attach at `websocket_path`: [WebSocket transport](/protocol/transport).
2. Use the announced `camera_enum` values in commands and media frames:
   [Control channel](/protocol/control) and [Media channels](/protocol/media).
3. When the connection closes, start over here: every ticket is single-use and dies with its
   connection.
