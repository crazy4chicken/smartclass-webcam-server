# API Overview

The webcam server exposes one HTTP API for two callers: operators and management
tools use the REST management plane, while camera devices hold a WebSocket
connection open to stream frames.

The generated reference is produced from the same route table the server
serves, so it can never drift from the running code.

## Base URL

All paths in this reference are relative to the server's listen address, set by
`WEBCAM_LISTEN_ADDR` (default `:8080`). Replace the host with wherever the
service is reachable in your deployment:

```text
http://webcam-server.example.com:8080/api/cameras
```

See the [deployment guide](/deploy) for how the service is exposed in the
Nekostick fleet.

## Authentication

Two authentication classes share the service, and they differ in how the
teamusers access token travels.

### Management API

Every route under `/api` requires a teamusers-issued bearer token:

```http
GET /api/cameras HTTP/1.1
Authorization: Bearer <teamusers access token>
```

The token must carry the `webcam:cameras:any` permission; a request without
verified claims is rejected with `401` and a request whose token lacks the
permission with `403`.

### Camera WebSocket

Camera devices cannot set request headers on a WebSocket handshake, so the
access token travels in the query string instead:

```text
GET /ws/camera/{id}?token=<teamusers access token>
```

The token is verified before the connection is upgraded; a missing or invalid
token answers `401` without upgrading.

::: warning Development mode
Setting `WEBCAM_DEV=true` disables both checks: every request is treated as an
authenticated developer identity. Never enable it on a reachable host.
:::

## Errors

Failures that reach a handler are reported as RFC 9457 problem details with the
content type `application/problem+json`:

```json
{
  "type": "about:blank",
  "title": "Not Found",
  "status": 404,
  "detail": "camera \"01J8Z4W3K5M7Q9R1T3V5X7Z9B1\" not found",
  "instance": "/api/cameras/01J8Z4W3K5M7Q9R1T3V5X7Z9B1"
}
```

Switch on `status` and read `detail` for the human-readable cause; `type` is
always `about:blank` because the service defines no error catalogue of its own.
Each operation page lists the statuses it can return.

The `401` and `403` responses produced by the teamusers middleware are the
exception: they carry the IAM SDK's own `{"allow": false, "reason": "..."}`
body instead of problem details.

## Collections

List endpoints wrap their results in an `items` envelope, so a response is
always a JSON object and never a bare array:

```json
{
  "items": [
    { "id": "01J8Z4W3K5M7Q9R1T3V5X7Z9B1", "name": "Room 101 front", "status": "online" }
  ]
}
```

An empty collection returns `{"items": []}`.

The three stream and segment listings accept an optional `limit` query
parameter:

```http
GET /api/streams/{id}/segments?limit=200
```

- The default is `100` when `limit` is omitted.
- The maximum is `1000`; larger values are clamped, not rejected.
- Any non-positive or non-numeric value is rejected with `400`.
- Results are not paginated: a truncated list carries no cursor, and recordings
  are returned from the newest stream or the lowest segment number.

## Reference

One page per resource domain, generated from the OpenAPI document:

- [Cameras](/api/reference/cameras) - the camera registry, its status, and
  configuration delivery.
- [Streams](/api/reference/streams) - recording sessions, their segments, and
  segment download URLs.
- [WebSocket](/api/reference/websocket) - the camera device channel.
- [Health](/api/reference/health) - liveness and readiness probes.

The machine-readable document these pages are built from is available for
download: [openapi.yaml](/openapi.yaml).
