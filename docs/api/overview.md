# API Overview

The webcam server exposes one HTTP API for two callers: operators and management
tools use the REST management plane, while device agents register over HTTP and
then hold a WebSocket connection open to receive commands and stream media.

The generated reference is produced from the same route table the server
serves, so it can never drift from the running code.

## Base URL

All paths in this reference are relative to the server's listen address, set by
`WEBCAM_LISTEN_ADDR` (default `:8080`). Replace the host with wherever the
service is reachable in your deployment:

```text
http://webcam-server.example.com:8080/api/devices
```

See the [deployment guide](/guide/deploy) for how the service is exposed in the
Nekostick fleet.

## Authentication

Two credential classes share the service; a caller never mixes them.

- **Management plane** (`/api/...`): a teamusers-issued bearer token whose
  verified claims carry a `cam:<action>:<scope>` permission - `cam:read`,
  `cam:manage` or `cam:control`, scoped to `own`, `team` or `any`. A request
  without valid claims answers `401` with the teamusers decision body
  `{"allow": false, "reason": "<cause>"}` and a challenge header
  `WWW-Authenticate: Bearer realm="teamusers", error="<code>",
  error_description="<cause>"` carrying the same cause, where the cause names
  the failing check (see [Errors](#errors)); a token that does not reach the
  target answers `403`.
- **Device plane** (`/ws/register`, `/ws/device/{device_websocket_id}`): the
  device's long-lived `wdt_...` device token, exchanged for a single-use
  WebSocket ticket that authorizes the connection.

The scope ladder, the key grammar and the full route-to-permission matrix are
in [Permissions and access control](/guide/permissions). The device credential
flow - registration body, ticket lifecycle and every control command - is the
[device protocol reference](/protocol/); the endpoint list is in the
[WebSocket reference](/api/reference/websocket).

::: warning Development mode
Setting `WEBCAM_DEV=true` disables the management-plane check; see the
[configuration reference](/guide/deploy#configuration). Never enable it on a
reachable host.
:::

## Errors

Failures that reach a handler are reported as RFC 9457 problem details with the
content type `application/problem+json`:

```json
{
  "type": "about:blank",
  "title": "Not Found",
  "status": 404,
  "detail": "device not found",
  "instance": "/api/devices/01J8Z4W3K5M7Q9R1T3V5X7Z9B1"
}
```

Switch on `status` and read `detail` for the human-readable cause; `type` is
always `about:blank` because the service defines no error catalogue of its own.
Each operation page lists the statuses it can return.

| Field | JSON type | Present | Allowed values | Meaning |
| --- | --- | --- | --- | --- |
| `type` | string | always | Always `about:blank`; the service defines no error catalogue. | Problem type URI. |
| `title` | string | always | The HTTP status text of `status`, for example `Bad Request`, `Unauthorized`, `Not Found`, `Conflict`, `Request Entity Too Large`, `Internal Server Error`. | Human-readable status name. |
| `status` | integer | always | The HTTP status code of the response, repeated in the body. | Status code. |
| `detail` | string | always on this service | Free-form string; the fixed messages are listed on the operation page that returns them. | Human-readable cause. |
| `instance` | string | when the request URL is available | The request path, e.g. `/api/devices/01J8ZK9WQ7X3YV0M4N5P6Q7R8S/`. | Path of the failing request. |

### Routing and server-side failures

Unknown paths and unsupported methods answer the same problem shape as every
other error:

| Status | `detail` |
| --- | --- |
| `404` | `no route for <METHOD> <path>` - no route matches the request. |
| `405` | `method <METHOD> is not allowed on <path>; allowed: GET, PUT` - the path is served for other methods, which are also repeated in the `Allow` header. |

Server-side failures name the operation that failed:

- `500` - `<operation> failed: <cause>`, for example `list devices failed: ...`,
  `create device failed: ...` or `issue device websocket ticket failed: ...`.
  The authorization middleware writes `loading the <device|stream|photo>
  failed: <cause>` when it cannot load the resource whose access it authorizes,
  and a recovered handler panic is `panic: <cause>`.
- `502` - `sending <command> failed: <cause>` on the device command routes,
  where `<command>` is `switch_camera`, `start_recording`, `stop_recording` or
  `take_photo` and the cause is either the classified transport failure
  (`the device websocket is closed`, `the device websocket send buffer is
  full`) or the underlying hub error.

The cause in a `500` or `502` body is stripped of the configured credentials
(database password, teamusers service token and client secret, storage
credentials) and truncated at 300 bytes with a trailing `...`.

### Recurring detail values

| Status | `detail` |
| --- | --- |
| `400` | `invalid JSON request body: <cause>` (sanitized and truncated), `request body must contain a single JSON object`, `camera_enum is required`, `camera_enum <camera_enum> is not registered for device "<device_id>"`, `resolution "<resolution>" is not supported by camera_enum <camera_enum> on device "<device_id>"` (camera switch), `fps <fps> is not supported by camera_enum <camera_enum> on device "<device_id>"` (camera switch), `codec "<codec>" is not supported by camera_enum <camera_enum> on device "<device_id>"` (recording start), `limit must be a positive integer`. |
| `404` | `device not found`, `stream not found` and `photo not found` from the middleware that loads the resource being authorized; `stream "<stream_id>" not found`, `photo "<photo_id>" not found` and `no active stream for camera_enum <camera_enum> on device "<device_id>"` from the handlers. |
| `409` | `device "<device_id>" is offline: no live registration`, `device "<device_id>" is offline: no live websocket`, `camera_enum <camera_enum> is already streaming on device "<device_id>"`, `a device with this id already exists`, `device websocket ticket already attached`. |

The device plane answers its own failures with the same body shape; its statuses
and the WebSocket close behavior are in the
[device protocol reference](/protocol/#device-plane-http-statuses).

Authentication failures are the exception: a `401` carries the teamusers
decision body `{"allow": false, "reason": "<cause>"}` instead of problem details,
plus a `WWW-Authenticate` Bearer challenge that repeats the same cause as
`error` (`invalid_request` for an unusable header, `invalid_token` for a failed
verification) and `error_description`. The cause names the failing check:

- a missing, malformed or wrong-scheme `Authorization` header:
  `the authorization header is missing; send "Authorization: Bearer <access token>"`,
  `the authorization header must read "Bearer <access token>"`,
  `the authorization header uses the <scheme> scheme; only Bearer is accepted`,
  `the authorization header carries an empty bearer token`,
  `the authorization header carries more than the bearer token`;
- a token the verifier rejected: `access token is expired`,
  `access token is not valid yet (nbf claim)`,
  `access token audience is not "<audience>"` (the configured
  `WEBCAM_TEAMUSERS_AUD`, default `teamusers`),
  `access token issuer is not "teamusers"`,
  `access token signature matches no key in the issuer's JWKS document`,
  `access token names a signing key the issuer does not publish (kid "...")`,
  `access token is not a valid JWS: ...`,
  `the issuer's JWKS document is unavailable: ...`.

Any other verifier failure is echoed as reported, sanitized and truncated. A
`403` from the scope ladder is a problem detail whose `detail` is
`permission denied` followed by every key the ladder tried and the cause each
check reported, e.g.
`permission denied: cam:read:any (no matching grant); cam:read:own (no matching grant)`.

## Collections

List endpoints wrap their results in an `items` envelope, so a response is
always a JSON object and never a bare array:

```json
{
  "items": [
    { "id": "01J8Z4W3K5M7Q9R1T3V5X7Z9B1", "name": "Room 101", "location": "Building A, room 101" }
  ]
}
```

An empty collection returns `{"items": []}`. `GET /api/devices` returns only the
devices the caller's resolved scope reaches: everything for `any`, the caller's
team for `team`, and the caller's own devices for `own`.

The list endpoints accept an optional `limit` query parameter:

```http
GET /api/streams/{stream_id}/segments?limit=200
```

- The default is `100` when `limit` is omitted.
- The maximum is `1000`; larger values are clamped, not rejected.
- Any non-positive or non-numeric value is rejected with `400` (`limit must be a positive integer`).
- Results are not paginated: a truncated list carries no cursor, and recordings
  are returned from the newest stream or the lowest segment number.

The consumer-side read workflow - listing streams, segments and photos and
downloading their bytes - is walked through in the
[service integration guide](/guide/service-integration).

## Reference

One page per resource domain, generated from the OpenAPI document, plus the consumer-side guide:

- [Devices](/api/reference/devices) - the device registry, its tokens, and its
  live status.
- [Streams](/api/reference/streams) - recording sessions, their segments, and
  segment download URLs.
- [Photos](/api/reference/photos) - still images captured from a device camera.
- [WebSocket](/api/reference/websocket) - device registration and the device
  channel.
- [Health](/api/reference/health) - liveness and readiness probes.
- [Service integration](/guide/service-integration) - authenticating as another
  backend service and retrieving recordings and photos, including direct
  object-storage access.

The machine-readable document these pages are built from is available for
download: [openapi.yaml](/openapi.yaml).
