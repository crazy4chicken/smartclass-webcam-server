---
title: Getting started
outline: 2
---

# Getting started

`smartclass-webcam-server` registers classroom camera devices, drives them over
WebSocket connections, and records the streams they produce. Management calls
are authorized against a [teamusers](https://github.com/crazy4chicken/nsc-teamusers)
instance and all state lives in PostgreSQL.

## Prerequisites

- Go 1.26 or newer
- PostgreSQL 16 with a database the service may create tables in
- An object store: [nsc-filehouse](https://github.com/crazy4chicken/nsc-filehouse)
  (preferred) or any S3-compatible endpoint such as MinIO (optional, see below)
- A running teamusers deployment and an access token that carries the
  `webcam:cameras:any` permission

## Configuration

The service reads its configuration from environment variables:

| Variable | Default | Required | Purpose |
| --- | --- | --- | --- |
| `WEBCAM_LISTEN_ADDR` | `:8080` | No | HTTP listen address. |
| `WEBCAM_DB_URL` | — | Yes | PostgreSQL connection string. |
| `WEBCAM_TEAMUSERS_URL` | — | Yes | Base URL of the teamusers service. |
| `WEBCAM_TEAMUSERS_AUD` | `teamusers` | No | Expected JWT audience. |
| `WEBCAM_TEAMUSERS_CLIENT_ID` | — | No | Service account client id; with the secret below it lets the service refresh its own credentials. |
| `WEBCAM_TEAMUSERS_CLIENT_SECRET` | — | No | Service account client secret. Preferred over a static token. |
| `WEBCAM_TEAMUSERS_SVC_TOKEN` | — | No | Static teamusers service token used for permission checks; expires after ten minutes. Without a credential every authorization check fails closed. |
| `WEBCAM_FILEHOUSE_URL` | — | No | Base URL of an nsc-filehouse instance. Takes precedence over S3 when set. |
| `WEBCAM_FILEHOUSE_BUCKET` | `webcam-segments` | No | filehouse bucket for stream segments; must already exist. |
| `WEBCAM_S3_ENDPOINT` | `localhost:9000` | No | S3-compatible endpoint (`host:port`). |
| `WEBCAM_S3_ACCESS_KEY` | — | No | Object storage access key. |
| `WEBCAM_S3_SECRET_KEY` | — | No | Object storage secret key. |
| `WEBCAM_S3_BUCKET` | `webcam-streams` | No | Bucket for stream segments. Created on startup when missing. |
| `WEBCAM_S3_USE_SSL` | `false` | No | Use TLS for the object storage connection. |

`WEBCAM_DB_URL` and `WEBCAM_TEAMUSERS_URL` are mandatory; startup fails without
them.

## Database

Create an empty database and point `WEBCAM_DB_URL` at it. The schema is
embedded in the binary and applied on startup, so no external migration tool is
needed:

```sh
createdb webcam
export WEBCAM_DB_URL='postgres://<user>:<password>@127.0.0.1:5432/webcam?sslmode=disable'
```

## Object storage

The storage backend is chosen in this order: nsc-filehouse when
`WEBCAM_FILEHOUSE_URL` is set, then S3 when `WEBCAM_S3_ACCESS_KEY` is set, and
otherwise a no-op backend where stream sessions and segments are still tracked
in PostgreSQL but no payload is stored and `download_url` comes back empty.

The filehouse bucket must already exist; the S3 bucket is created on startup
when it is missing. Both backends hand out short-lived presigned download URLs,
so clients never hold storage credentials.

See the [deployment guide](/deploy) for the full operator checklist.

## Run

```sh
export WEBCAM_TEAMUSERS_URL='http://127.0.0.1:8081'
export WEBCAM_TEAMUSERS_SVC_TOKEN='<teamusers service token>'
go run ./cmd/server
```

Check that the process is alive and the database is reachable:

```sh
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

## First requests

Log in to teamusers to obtain an access token, then send it as a bearer token on
every `/api/*` request:

```sh
export WEBCAM_URL='http://127.0.0.1:8080'
export WEBCAM_TOKEN='<teamusers access token>'

curl -sS -X POST "$WEBCAM_URL/api/cameras" \
  -H "Authorization: Bearer $WEBCAM_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"name":"Classroom 101 front","location":"Building A, room 101","config":{"resolution":"1920x1080","fps":30,"codec":"h264","bitrate":4000}}'
```

The response contains the camera `id`. Once the camera device connects over
WebSocket, start and stop recordings through the REST API:

```sh
curl -sS -X POST "$WEBCAM_URL/api/cameras/<camera-id>/stream/start" \
  -H "Authorization: Bearer $WEBCAM_TOKEN"

curl -sS -X POST "$WEBCAM_URL/api/cameras/<camera-id>/stream/stop" \
  -H "Authorization: Bearer $WEBCAM_TOKEN"

curl -sS "$WEBCAM_URL/api/cameras/<camera-id>/streams?limit=10" \
  -H "Authorization: Bearer $WEBCAM_TOKEN"
```

`GET /api/streams/{id}` returns the session together with its segments and a
presigned download URL per segment. The management surface is
`GET|POST /api/cameras`, `GET|PUT|DELETE /api/cameras/{id}`,
`POST /api/cameras/{id}/configure`, `POST /api/cameras/{id}/stream/start`,
`POST /api/cameras/{id}/stream/stop`, `GET /api/cameras/{id}/streams`,
`GET /api/streams/{id}`, and `GET /api/streams/{id}/segments`; every `/api`
route requires a Bearer token carrying the `webcam:cameras:any` permission and
returns RFC 9457 `application/problem+json` bodies on error.

## Camera client protocol

A camera device holds one WebSocket connection to the server. It authenticates
with a teamusers access token in the `token` query parameter, because the
JavaScript `WebSocket` API cannot set an `Authorization` header:

```js
const cameraId = '01J8ZK9WQ7X3YV0M4N5P6Q7R8S'
const token = '<teamusers access token>'
const socket = new WebSocket(
  `ws://127.0.0.1:8080/ws/camera/${cameraId}?token=${encodeURIComponent(token)}`
)
```

Connecting replaces any live connection already registered for the same camera,
so a restarting device does not need to wait for a timeout. The server answers
with a standard WebSocket upgrade; a missing or invalid token is rejected with
`401` and an unknown camera ID with `404` before the upgrade happens.

### Message envelope

Every message is a JSON object with a `type` field and an optional `payload`
object:

```json
{"type": "frame", "payload": {"stream_id": "01J8ZKG5TQ2M7B9C4D6E8F0H2J", "seq": 42, "data": "AAAB...", "ts": "2026-10-03T10:15:00Z"}}
```

### Messages sent by the server

| Type | Payload | Meaning |
| --- | --- | --- |
| `start_stream` | `stream_id`, `config` | Start capturing and sending frames for the given stream; `config` is the camera's current configuration. |
| `stop_stream` | `stream_id` | Stop the given stream. |
| `configure` | configuration fields | Apply a new configuration; the payload is the configuration object itself (`resolution`, `fps`, `codec`, `bitrate`). |
| `ping` | — | Application-level keepalive. Answer with `pong`. |

```json
{"type": "start_stream", "payload": {"stream_id": "01J8ZKG5TQ2M7B9C4D6E8F0H2J", "config": {"resolution": "1920x1080", "fps": 30, "codec": "h264", "bitrate": 4000}}}
```

### Messages sent by the camera

| Type | Payload | Meaning |
| --- | --- | --- |
| `stream_started` | `stream_id` | Acknowledges `start_stream` once capture is running. |
| `stream_stopped` | `stream_id` | Acknowledges `stop_stream`. |
| `frame` | `stream_id`, `seq`, `data`, `ts` | One video frame: `seq` is a monotonically increasing frame number, `data` is the base64-encoded frame payload, and `ts` is the capture time in RFC 3339 format. |
| `status` | free-form | Periodic camera status report; the server logs it. |
| `error` | free-form | A capture error reported by the camera; the server logs it. |
| `pong` | — | Answers `ping`. |

The server also sends protocol-level WebSocket pings. A camera that stops
answering its keepalives is disconnected.

### Recording flow

1. An operator registers the camera and calls
   `POST /api/cameras/{id}/stream/start`; the server creates an `active` stream
   session and sends `start_stream` with the new stream ID.
2. The camera captures frames and sends `frame` messages carrying a
   base64-encoded payload, a sequence number, and the capture timestamp.
3. An operator calls `POST /api/cameras/{id}/stream/stop`; the server sends
   `stop_stream` and marks the session `completed`.
4. The server buffers the incoming frames and flushes them to object storage as
   segments - every 150 frames, every five seconds, and once more when the
   stream stops. Segment objects are keyed as
   `{camera_id}/{stream_id}/{timestamp}_{first_seq}.bin`; the recorded segments
   appear under `GET /api/cameras/{id}/streams` and `GET /api/streams/{id}`.

Commands are only deliverable while the camera is connected: REST calls that
must reach an offline camera fail with `409`, and a camera that disappears
mid-command makes the call fail with `502`.
