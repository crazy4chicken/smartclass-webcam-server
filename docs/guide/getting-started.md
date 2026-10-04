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
- A running teamusers deployment with the `cam:<action>:any|team|own` keys (or
  the `cam:<action>:*` wildcards) registered, and an access token that carries
  them (see [Permissions and access control](/guide/permissions))

## Configuration

A local run needs `WEBCAM_DB_URL`, and `WEBCAM_TEAMUSERS_URL` unless
`WEBCAM_DEV=true` waives authentication. `WEBCAM_LISTEN_ADDR` defaults to
`:8080`; object storage is optional (`WEBCAM_FILEHOUSE_URL` or
`WEBCAM_S3_ACCESS_KEY` — without either, payloads are discarded); teamusers
credentials (`WEBCAM_TEAMUSERS_CLIENT_ID`/`WEBCAM_TEAMUSERS_CLIENT_SECRET`,
otherwise `WEBCAM_TEAMUSERS_SVC_TOKEN`) are only needed once authentication is
enabled, and filehouse storage additionally requires the client credentials.

The [deployment guide](/guide/deploy#configuration) has the full environment
table with every default and required/conditional rule.

## Database

Create an empty database; the schema is embedded in the binary and applied on
startup, so no migration tool is needed:

```sh
createdb webcam
export WEBCAM_DB_URL='postgres://<user>:<password>@127.0.0.1:5432/webcam?sslmode=disable'
```

A local server usually runs with `sslmode=disable`; deployed environments use
`sslmode=require` (see the [deployment guide](/guide/deploy#first-time-setup)).

## Object storage

Storage is [nsc-filehouse](https://github.com/crazy4chicken/nsc-filehouse) when
`WEBCAM_FILEHOUSE_URL` is set, S3 when `WEBCAM_S3_ACCESS_KEY` is set, and
otherwise a no-op backend that records sessions but discards payloads. Clients
never hold storage credentials — they receive short-lived presigned download
URLs. See the [deployment guide](/guide/deploy#prerequisites) for setup and
[Service Integration](/guide/service-integration) for consuming recorded media.

## Run

```sh
export WEBCAM_TEAMUSERS_URL='http://127.0.0.1:8081'
export WEBCAM_TEAMUSERS_SVC_TOKEN='<teamusers service token>'
go run ./cmd/server
```

Check that the process is alive:

```sh
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

## Register a device

Log in to teamusers to obtain an access token, then send it as a bearer token on
every `/api/*` request. A device is created once; the response carries the
device and its long-lived device token, and the token is shown only this one
time:

```sh
export WEBCAM_URL='http://127.0.0.1:8080'
export WEBCAM_TOKEN='<teamusers access token>'

curl -sS -X POST "$WEBCAM_URL/api/devices" \
  -H "Authorization: Bearer $WEBCAM_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"name":"Classroom 101","location":"Building A, room 101"}'
```

```json
{
  "device": {
    "id": "01J8ZK9WQ7X3YV0M4N5P6Q7R8S",
    "name": "Classroom 101",
    "location": "Building A, room 101",
    "created_at": "2026-10-04T09:58:00Z",
    "updated_at": "2026-10-04T09:58:00Z"
  },
  "token": "wdt_<base64url of 32 random bytes>"
}
```

Configure the device agent with the device id and the token. The token is
permanent until rotated: `POST /api/devices/{device_id}/token` returns a new one
and invalidates the old, closes the live connection, and drops any pending
registration ticket.

## First requests

Once the device is online, operators trigger commands through one endpoint per
command. Each call is delivered to the device over its WebSocket; the HTTP
response reports the server-side result, while the device's own outcome arrives
in the command's `ack`:

```sh
curl -sS -X POST "$WEBCAM_URL/api/devices/<device-id>/recording/start" \
  -H "Authorization: Bearer $WEBCAM_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"camera_enum":0}'

curl -sS -X POST "$WEBCAM_URL/api/devices/<device-id>/camera/switch" \
  -H "Authorization: Bearer $WEBCAM_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"camera_enum":1}'

curl -sS -X POST "$WEBCAM_URL/api/devices/<device-id>/photo" \
  -H "Authorization: Bearer $WEBCAM_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"camera_enum":0}'

curl -sS -X POST "$WEBCAM_URL/api/devices/<device-id>/recording/stop" \
  -H "Authorization: Bearer $WEBCAM_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"camera_enum":0}'
```

`recording/start` creates an `active` stream session, snapshots the camera's
parameters into the session metadata, and sends `start_recording` to the device;
`recording/stop` sends `stop_recording` and finalizes the session as
`completed`. `GET /api/devices/{device_id}/streams` lists the sessions, and
`GET /api/streams/{stream_id}/` returns one together with its segments — each
segment in that response carries a presigned `download_url` valid for 15
minutes. `GET /api/streams/{stream_id}/segments` lists the same segments
**without** download URLs. The [Service Integration](/guide/service-integration)
guide walks through retrieving recordings and photos.

Every `/api` route requires a Bearer token carrying `cam:read`, `cam:manage`,
or `cam:control` at the right scope (see
[Permissions and access control](/guide/permissions)); the
[API overview](/api/overview) documents the routes and the RFC 9457
`application/problem+json` error shape.

## Device flow

The device side of the contract has its own reference section:
[Device protocol](/protocol/). In short:

1. The device calls `GET /ws/register` with its device token and announces the
   full list of cameras it drives.
2. The response carries a single-use `device_websocket_id` ticket that expires
   after `WEBCAM_WS_TICKET_TTL` (default 60 seconds).
3. The device attaches with `GET /ws/device/{device_websocket_id}`; the ticket
   binds to exactly that connection.
4. The server sends control commands and the device answers with `ack`s and
   pushes media frames for recordings and photos.

[Device protocol](/protocol/) covers the registration body, the ticket
lifecycle, the binary framing, and every control command in full.
