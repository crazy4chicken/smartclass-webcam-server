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
  without valid claims answers `401`; a token that does not reach the target
  answers `403`.
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
  "detail": "device \"01J8Z4W3K5M7Q9R1T3V5X7Z9B1\" not found",
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

The device plane answers its own failures with the same body shape; its statuses
and the WebSocket close behavior are in the
[device protocol reference](/protocol/#device-plane-http-statuses).

Authentication failures are the exception: a `401` carries the teamusers
decision body `{"allow": false, "reason": "..."}` instead of problem details,
while a `403` from the scope ladder is a problem detail whose `detail` is
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
