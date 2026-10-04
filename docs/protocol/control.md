---
title: Control Channel
outline: 2
---

# Control Channel

The `control` channel carries every non-media message between the server and a device. It uses
**text WebSocket frames in both directions**, and it is the only channel the server writes to: the
server never sends binary frames. This page assumes the connection was already established as
described in [WebSocket Transport](/protocol/transport); media frames and their server-side
pipeline are documented in [Media Channels](/protocol/media).

## Message envelope

Every text frame is a JSON object with the same envelope used by binary media headers:

| Field | JSON type | Required | Allowed values | Meaning |
| --- | --- | --- | --- | --- |
| `channel` | string | Yes | `control` on this channel. The full envelope vocabulary is `control`, `recording` and `photo` (`internal/ws/message.go`); only `control` carries text frames in either direction. | Selects the logical channel. |
| `type` | string | Yes | One of the eleven values in the catalogs below: server to device `switch_camera`, `start_recording`, `stop_recording`, `take_photo`, `ping`; device to server `ack`, `pong`, `status`, `error`; media `frame` and `photo` (binary channels, documented in [Media Channels](/protocol/media)). Anything else is ignored with a `debug` log, not rejected. | Message type. |
| `id` | string | No | A 26-character ULID string (uppercase Crockford base32, e.g. `01J8ZKQ3B5N7P9R1T3V5X7Z9B1`), or absent. Present on the four operator-triggered commands, absent on `ping`. On device→server messages the server logs it verbatim without validating it. | Command identifier. |
| `payload` | object | No | A JSON object; keys and value types are type-specific (tables below). Unknown keys are accepted and ignored, and on device→server messages the payload's keys are never validated — the value itself must still be a JSON object, otherwise the frame is discarded as malformed. | Type-specific fields; absent when the message has none. |

The server dispatches inbound text frames **by `type` only**; it does not check `channel` on text
frames (binary frames are dispatched by channel instead). Send `channel: "control"` on every text
frame anyway: future protocol revisions may tighten this, and peers in other implementations
expect it.

## Server to device commands

The server sends five message types. Four are operator-triggered commands; `ping` is the
application-level keepalive. A command is emitted exactly once per trigger — there is no retry,
no re-delivery and no queue that survives a reconnection.

| Type | Trigger | `id` | Payload keys |
| --- | --- | --- | --- |
| `switch_camera` | `POST /api/devices/{device_id}/camera/switch` | ULID (26 characters) | `camera_enum` |
| `start_recording` | `POST /api/devices/{device_id}/recording/start` | ULID (26 characters) | `camera_enum`, `stream_id` |
| `stop_recording` | `POST /api/devices/{device_id}/recording/stop` | ULID (26 characters) | `camera_enum`, `stream_id` |
| `take_photo` | `POST /api/devices/{device_id}/photo` | ULID (26 characters) | `camera_enum`, `request_id` |
| `ping` | keepalive timer, every 30 seconds | none (absent) | `ts` |

### `switch_camera`

Makes one camera the device's active camera.

| Field | JSON type | Required | Allowed values | Default | Meaning |
| --- | --- | --- | --- | --- | --- |
| `camera_enum` | number | Yes | An integer in `0..n-1` of the camera list the device last announced, e.g. `0` or `1`. The server validates it against the device's current registration before sending, so it always refers to an announced camera; an unregistered value never reaches the device (the HTTP call answers `400`). | — | Camera that must become active. |

```json
{
  "channel": "control",
  "type": "switch_camera",
  "id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B1",
  "payload": {"camera_enum": 1}
}
```

- **Ack expectation.** An `ack` echoing `id`, with `payload.ok: true` once the camera is active, or
  `payload.ok: false` and `payload.error` when the device cannot switch. The server does not wait
  for it.
- **Device obligations.** Apply the switch to whatever "active camera" means locally (the camera
  used for the next capture/stream) and answer with an `ack`. There is no server-side
  active-camera state: the server records nothing about the switch beyond the HTTP response, so
  the ack is the only confirmation an operator can get.
- **Server state.** None. No record is created or updated.

### `start_recording`

Tells the device to start pushing video frames for one camera into one server-created stream.

| Field | JSON type | Required | Allowed values | Default | Meaning |
| --- | --- | --- | --- | --- | --- |
| `camera_enum` | number | Yes | Integer in `0..n-1` of the current registration, validated by the server before sending. | — | Camera to record. |
| `stream_id` | string | Yes | Opaque server-issued ULID string: 26 uppercase Crockford base32 characters (`0-9A-HJKMNP-TV-Z`), e.g. `01J8ZKQ3B5N7P9R1T3V5X7Z9B2`. Always non-empty and unique per stream; it matches the `id` of the stream row created by the triggering endpoint. | — | Stream that the device must push `recording.frame` messages into. |

```json
{
  "channel": "control",
  "type": "start_recording",
  "id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B1",
  "payload": {
    "camera_enum": 0,
    "stream_id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B2"
  }
}
```

- **Ack expectation.** An `ack` echoing `id`, `ok: true` when the encoder is running, `ok: false`
  and `error` when it cannot start. The server does not wait for it.
- **Device obligations.** Start encoding `camera_enum` and send `recording.frame` binary frames
  whose `stream_id` and `camera_enum` are exactly these values ([Media
  Channels](/protocol/media)). Keep sending until a `stop_recording` for the same `stream_id`
  arrives or the connection closes. Frames may be sent before the ack — the server registers the
  stream's frame accumulator **before** it queues the command, so nothing is lost in that race.
- **Server state.** The triggering endpoint has already inserted the stream row with `status:
  "active"` and registered its frame accumulator. An `ok: false` ack does **not** change that: the
  stream stays `active` (and its accumulator stays registered) until an operator stops it, a
  subsequent command cannot be delivered, or the device disconnects. There is no device-initiated
  way to end a stream.

### `stop_recording`

Tells the device to stop pushing frames for one stream.

| Field | JSON type | Required | Allowed values | Default | Meaning |
| --- | --- | --- | --- | --- | --- |
| `camera_enum` | number | Yes | Integer; conventionally `0..n-1` of the current registration. The endpoint resolves it to the camera's active stream, and no registration check is performed on this route — a value with no active stream answers `404` on the HTTP call. | — | Camera whose stream must stop. |
| `stream_id` | string | Yes | Opaque server-issued ULID string (26 characters); the `id` of the camera's active stream at the time of the call. | — | Stream the device must stop feeding. |

```json
{
  "channel": "control",
  "type": "stop_recording",
  "id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B1",
  "payload": {
    "camera_enum": 0,
    "stream_id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B2"
  }
}
```

- **Ack expectation.** An `ack` echoing `id`, `ok: true` once the encoder stopped, `ok: false` and
  `error` otherwise.
- **Device obligations.** Stop encoding and stop sending frames for `stream_id`; anything sent
  after the stop is dropped by the server. Flush or drop locally buffered frames as the device
  sees fit — the server only keeps what already arrived.
- **Server state.** The endpoint marks the stream `completed` immediately after queueing the
  command (it does not wait for the ack), flushing any buffered frames into a final segment first.
  A later `ok: false` ack does not revert that.

### `take_photo`

Asks for one still image.

| Field | JSON type | Required | Allowed values | Default | Meaning |
| --- | --- | --- | --- | --- | --- |
| `camera_enum` | number | Yes | Integer in `0..n-1` of the current registration, validated by the server before sending. | — | Camera to capture with. |
| `request_id` | string | Yes | Opaque server-issued ULID string: 26 uppercase Crockford base32 characters, e.g. `01J8ZKQ3B5N7P9R1T3V5X7Z9B3`. Minted fresh per `POST /api/devices/{device_id}/photo` call. | — | Correlation id the device must copy into the resulting `photo.photo` message. |

```json
{
  "channel": "control",
  "type": "take_photo",
  "id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B1",
  "payload": {
    "camera_enum": 0,
    "request_id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B3"
  }
}
```

- **Ack expectation.** An `ack` echoing `id`: `ok: true` once the capture/upload was accepted,
  `ok: false` and `error` when the camera cannot capture.
- **Device obligations.** Capture one still from `camera_enum`, then upload it as a `photo.photo`
  binary frame carrying the same `camera_enum` and `request_id` (plus a real `content_type`, e.g.
  `image/jpeg`). The photo is stored under a server-generated id; `request_id` is the only link
  back to this command.
- **Server state.** None at command time. The photo record appears only when the device uploads and
  the server finishes storing it (see [Media Channels](/protocol/media)).

### `ping`

Application-level keepalive. The server sends it every 30 seconds, immediately after a
WebSocket protocol-level Ping control frame, so the device keeps both its socket and its read
deadline alive.

| Field | JSON type | Required | Allowed values | Default | Meaning |
| --- | --- | --- | --- | --- | --- |
| `ts` | string | Yes | RFC3339Nano timestamp from the server clock in UTC, e.g. `2026-10-04T10:00:30Z` or `2026-10-04T10:00:30.512384921Z`. Always present on a server-sent `ping`. | — | Send time; informational. |

`ping` is the only server message **without** an `id` and without a payload object beyond `ts`:

```json
{
  "channel": "control",
  "type": "ping",
  "payload": {"ts": "2026-10-04T10:00:30Z"}
}
```

- **Ack expectation.** None. The device answers with `pong`, not with `ack`.
- **Device obligations.** Reply with a `pong` promptly. Any inbound frame refreshes the server's
  60-second read deadline, so a slow or missing `pong` only costs the device its connection once
  no other traffic arrives for a full minute. The protocol-level Ping is answered by the device's
  WebSocket library automatically; the application `pong` is what the server's own keepalive
  loop is paired with.
- **Server state.** None.

## Command identifiers

- Every operator-triggered command carries an `id` in the envelope: a **ULID**, 26 uppercase
  Crockford base32 characters (for example `01J8ZKQ3B5N7P9R1T3V5X7Z9B1`), a 48-bit millisecond
  timestamp followed by 80 random bits. The server's ULIDs come from a process-wide monotonic
  source, so successive ids increase even within one millisecond and sort lexicographically by
  creation time.
- The server keeps **no registry of pending commands**. It never matches an `ack` against
  anything: it only logs the `id` it finds in the ack.
- Echo the received `id` exactly (same string, same case) in the `ack` envelope. An ack with an
  unknown, empty or duplicated `id` is accepted and logged like any other; it triggers no error,
  no retry and no state change.
- `ping` has no `id`; do not invent one when answering it.
- Command ids, `stream_id` and `request_id` are all ULIDs minted by the server. Treat them as
  opaque strings; the device never generates them.

## Device to server messages

Four `type` values are understood on the `control` channel. Everything else is ignored (see
[Unknown types and channels](#unknown-types-and-channels)).

| Type | Purpose | Server handling |
| --- | --- | --- |
| `ack` | Result of a server command. | Debug log with `id` and payload. Nothing persisted. |
| `pong` | Keepalive answer to `ping`. | Silently accepted; the read deadline was already refreshed. |
| `status` | Device-initiated state report. | Info log with the payload. Nothing persisted. |
| `error` | Device-reported problem. | Info log with `id` and payload. Nothing persisted. |

### `ack`

| Field | Level | JSON type | Required | Allowed values | Default | Meaning |
| --- | --- | --- | --- | --- | --- | --- |
| `id` | envelope | string | Yes (convention) | Any string; conventionally the exact command `id` echoed back. The server does not validate it — an unknown, empty or duplicated value is still accepted and logged. | — | The `id` of the command being acknowledged. |
| `payload.ok` | payload | boolean | Yes (convention) | JSON `true` or `false`. Not validated: a missing `ok` or a non-boolean value is still accepted and logged. | — | `true` when the command succeeded, `false` when it failed. |
| `payload.error` | payload | string | When `ok` is `false` | Any string; free-form, not persisted. | absent when `ok` is `true` | Human-readable failure reason. |
| further payload keys | payload | any | No | Any JSON value (string, number, boolean, null, array or object); accepted and logged verbatim. | — | Not interpreted. |

Success (complete document):

```json
{
  "channel": "control",
  "type": "ack",
  "id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B1",
  "payload": {"ok": true}
}
```

Failure with a reason (complete document):

```json
{
  "channel": "control",
  "type": "ack",
  "id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B1",
  "payload": {"ok": false, "error": "camera 0 is busy"}
}
```

Server behavior, exactly as implemented: the frame is decoded, and the ack is logged at **debug**
level (`device acknowledged command`) together with its `id` and the whole payload. The server
does not validate the payload — an `ack` without `ok`, or with `ok` as a string, is still accepted
and logged. Nothing is persisted, nothing is retried, and no HTTP request is still waiting: the
operator's call already returned `202`/`201`/`200` as soon as the command was queued. An `ok:
false` ack therefore never changes the HTTP result and never rolls back server state created by
the trigger (for example the stream row of `recording/start`).

### `pong`

| Field | JSON type | Required | Allowed values | Default | Meaning |
| --- | --- | --- | --- | --- | --- |
| `payload.ts` | string | No | Any string; conventionally the RFC3339Nano `ts` echoed from the `ping`. Never parsed or validated. | absent | Echo of the `ping` timestamp; not interpreted. |
| `id` | — | No | Any string; unused. | — | Not used; omit it. |

```json
{
  "channel": "control",
  "type": "pong",
  "payload": {"ts": "2026-10-04T10:00:30Z"}
}
```

Any inbound frame — `pong`, `ack`, `status`, media or a WebSocket pong — refreshes the server's
read deadline. `pong` itself produces no log entry and no state change.

### `status`

A device-initiated report. The server treats the payload as opaque: any JSON object is accepted,
logged at **info** level (`device status`) and otherwise ignored.

| Field | JSON type | Required | Allowed values | Default | Meaning |
| --- | --- | --- | --- | --- | --- |
| `payload` | object | No | Any JSON object; free-form keys and values of any JSON type, not validated, not interpreted, not stored. | absent | Free-form report. |
| `id` | string | No | Any string; unused for status. | — | Unused; omit it. |

```json
{
  "channel": "control",
  "type": "status",
  "payload": {
    "ts": "2026-10-04T10:00:31Z",
    "active_camera": 1,
    "recording": false
  }
}
```

The payload keys above are an illustration only — the server imposes no schema on a `status`
payload and stores nothing from it.

### `error`

A device-reported problem.

| Field | JSON type | Required | Allowed values | Default | Meaning |
| --- | --- | --- | --- | --- | --- |
| `id` | string | No | Any string; conventionally the command id, empty otherwise. Accepted verbatim. | — | The command id when the error belongs to a command. |
| `payload` | object | No | Any JSON object; free-form. A `message` string key is the conventional choice. | absent | Free-form error report. |

```json
{
  "channel": "control",
  "type": "error",
  "id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B1",
  "payload": {"message": "camera 1 is unavailable"}
}
```

The server logs it at **info** level (`device reported an error`) with the `id` and the payload,
and nothing else happens. For failures of a specific command prefer `ack` with `ok: false`; `error`
is for problems that do not belong to a pending command.

## Command lifecycle

Every operator-triggered command follows the same path. The HTTP response is written as soon as the
command has been queued for the connection — it does not mean the device acted, or even that it
received the command.

```text
 Operator                                     Server                              Device
    |                                            |                                   |
    | POST /api/devices/{id}/recording/start     |                                   |
    |------------------------------------------->|                                   |
    |                                            | 1. validate body, live session,   |
    |                                            |    registered camera              |
    |                                            | 2. create side state if any       |
    |                                            |    (stream row, accumulator)      |
    |                                            | 3. queue command (id = new ULID)  |
    |      201 Created {stream, Location}        |---------------------------------->| text frame
    |<-------------------------------------------|                                   |
    |                                            |                                   | 4. act
    |                                            |<----------------------------------| ack {id, ok}
    |                                            | 5. debug log only                 |
    |                                            |<----------------------------------| media frames (binary)
```

Detail per step:

1. The endpoint first validates the request, then the live session and the camera (see the
   [failure table](#failure-semantics)).
2. Commands that need server-side state create it before queueing: `recording/start` inserts the
   stream row and registers its frame accumulator; the other commands create nothing.
3. The command is placed on the connection's outbound queue, which holds **16** messages. A full
   queue (or a connection that is closing) fails the send and produces `502`.
4. The device applies the command and answers with an `ack`. Steps 3 and 4 are decoupled: the
   server does not block on the socket write beyond the queue push, and it never waits for the ack.
5. The ack is logged at debug level. Command failures surface only in that log and in any state
   the failure prevented the device from producing — never in the HTTP response.

## Operator HTTP triggers

All four endpoints live under `/api`, require a teamusers bearer token with
`cam:control:<scope>`, and share the same request body. See [Permissions and access
control](/guide/permissions) for the scope ladder and [API Overview](/api/overview) for the
authentication model; the generated schemas are in the [API reference](/api/reference/devices).

| Endpoint | Queues command | Success | Response body |
| --- | --- | --- | --- |
| `POST /api/devices/{device_id}/camera/switch` | `switch_camera` | `202 Accepted` | `{command_id, camera_enum}` |
| `POST /api/devices/{device_id}/recording/start` | `start_recording` | `201 Created` | stream resource, `Location: /api/streams/{stream_id}` |
| `POST /api/devices/{device_id}/recording/stop` | `stop_recording` | `200 OK` | completed stream resource |
| `POST /api/devices/{device_id}/photo` | `take_photo` | `202 Accepted` | `{command_id, request_id, camera_enum}` |

### Request body

All four endpoints take the same body:

| Field | JSON type | Required | Allowed values | Default | Meaning |
| --- | --- | --- | --- | --- | --- |
| `camera_enum` | number | Yes | JSON integer in `0..n-1` of the device's current registration, e.g. `0` or `1`. A fractional number or a string is a decode error (`400`); a missing key answers `camera_enum is required`; a value not in the registration answers `400` on switch, start and photo (`recording/stop` instead resolves the camera's active stream and answers `404` when there is none). | — | Target camera. |

```json
{"camera_enum": 0}
```

Body rules, as enforced by the shared decode helper:

- The body must be exactly one JSON object; a second value or trailing content answers `400` with
  `request body must contain a single JSON object`.
- The body is limited to 1 MiB; a larger body answers `413` with `request body too large`.
- Unknown fields inside the object are ignored.
- Malformed JSON, or `camera_enum` missing entirely, answers `400`: malformed bodies produce
  `invalid JSON request body: <decoder error>`, a body without the key produces exactly
  `camera_enum is required`.

### Success shapes

`camera/switch` — `202 Accepted`:

```json
{
  "command_id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B1",
  "camera_enum": 1
}
```

`photo` — `202 Accepted`. `request_id` is the value the device must echo in the uploaded
`photo.photo`:

```json
{
  "command_id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B1",
  "request_id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B3",
  "camera_enum": 0
}
```

Fields of both `202` bodies:

| Field | JSON type | Endpoints | Allowed values | Meaning |
| --- | --- | --- | --- | --- |
| `command_id` | string | switch, photo | ULID: 26 uppercase Crockford base32 characters, freshly minted per call. | It is the `id` of the envelope the device receives. |
| `camera_enum` | number | switch, photo | Integer echo of the requested camera (`0..n-1`). | Echo of the requested camera. |
| `request_id` | string | photo | ULID: 26 uppercase Crockford base32 characters, freshly minted per call. | The device must echo it in the uploaded `photo.photo`. |

`recording/start` — `201 Created` with the stream resource and a `Location` header pointing at
`/api/streams/{stream_id}`:

```json
{
  "id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B2",
  "device_id": "01J8ZK9WQ7X3YV0M4N5P6Q7R8S",
  "camera_enum": 0,
  "status": "active",
  "started_at": "2026-10-04T10:01:00Z",
  "metadata": {
    "resolution": "1920x1080",
    "fps": 30,
    "codecs": ["h264", "h265", "mjpeg"]
  }
}
```

`recording/stop` — `200 OK` with the same resource after it was finished:

```json
{
  "id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B2",
  "device_id": "01J8ZK9WQ7X3YV0M4N5P6Q7R8S",
  "camera_enum": 0,
  "status": "completed",
  "started_at": "2026-10-04T10:01:00Z",
  "ended_at": "2026-10-04T10:06:30Z",
  "metadata": {
    "resolution": "1920x1080",
    "fps": 30,
    "codecs": ["h264", "h265", "mjpeg"]
  }
}
```

Stream resource fields:

| Field | JSON type | Present | Allowed values | Meaning |
| --- | --- | --- | --- | --- |
| `id` | string | Always | ULID, 26 characters; unique per stream. | Stream id; the `stream_id` sent to the device. |
| `device_id` | string | Always | ULID, 26 characters. | Owning device. |
| `camera_enum` | number | Always | Integer `0..n-1` of the registration that started the stream. | Camera of the stream. |
| `status` | string | Always | Exactly one of `active`, `completed`, `failed` (`internal/domain/domain.go`). | Stream lifecycle state; set to `active` on creation. |
| `started_at` | string | Always | RFC3339 timestamp from the database clock, e.g. `2026-10-04T10:01:00Z`. | Creation time. |
| `ended_at` | string | When finished | RFC3339 timestamp; written once and kept on later finishes. | Finish time. |
| `metadata` | object | Always | `resolution`: free-form string as announced (`WIDTHxHEIGHT` convention); `fps`: integer > 0 as announced; `codecs`: array of the announced values, order preserved, each one of `h264`, `h265`, `mjpeg`, `mpeg4`, `vp8`, `vp9`, `av1`; `codec`: schema field that is never set by any code path. | Camera snapshot taken at start. `metadata.codecs` is the announced list, not a negotiated codec — see [Codec values](/protocol/registration#codec-values). |

Only the recording endpoints touch the database: `recording/start` inserts the stream row and
`recording/stop` finishes it. `camera/switch` just asks the device to switch, and `photo` mints a
`request_id` — neither writes a row.

### Failure semantics

| Condition | Status | `detail` in the problem body | State left behind |
| --- | --- | --- | --- |
| Body not a single JSON object, or `camera_enum` not an integer | `400` | decoder message, or `camera_enum is required` when the key is absent | None |
| Body larger than 1 MiB | `413` | `request body too large` | None |
| `camera_enum` not in the current registration (switch, start, photo) | `400` | `camera_enum 2 is not registered for device "01J8ZK9WQ7X3YV0M4N5P6Q7R8S"` | None |
| Device has no live session (switch, start, photo), or the command send finds it gone | `409` | `device "01J8ZK9WQ7X3YV0M4N5P6Q7R8S" is offline` | None |
| `recording/start` while that camera already has an `active` stream | `409` | `camera_enum 0 is already streaming on device "01J8ZK9WQ7X3YV0M4N5P6Q7R8S"` | No new row; the existing stream is untouched |
| `recording/stop` with no `active` stream for that camera | `404` | `no active stream for camera_enum 0 on device "01J8ZK9WQ7X3YV0M4N5P6Q7R8S"` | None |
| Command cannot be queued: outbound buffer full or connection closing | `502` | `device connection is unavailable` | `start`: the new stream is drained and immediately finished as `failed`. The other three: nothing changed — in particular a stop that fails this way leaves its stream `active` |
| Caller token missing, invalid or lacking `cam:control:<scope>` | `401` / `403` | management-plane body (see [API Overview](/api/overview)) | None |
| Store failure during the operation | `500` | `internal server error` | Depends on where the failure happened; the request is abandoned |

Notes on the ordering and the side effects:

- The checks run in a fixed order: body, then live session (except for stop), then camera
  registration, then stream state. A missing `camera_enum` therefore answers `400` even when the
  device is offline, and a stop for a device that has never registered answers `404` (not `409`)
  because it only looks for an active stream.
- `recording/stop` is the exception to the live-session rule: it resolves the active stream first
  and only fails with `409`/`502` when the command itself cannot be delivered. That leaves the
  stream row `active` with its accumulator still running.
- A failed send on `recording/start` is the only failure that leaves a persisted trace: the row
  exists with `status: "failed"` and `ended_at` set, which is what an operator sees afterwards.
- Failures reported by the device (`ack` with `ok: false`) are **not** HTTP failures. The HTTP
  response was already written when the command was queued, so it is never changed retroactively.

The problem body is RFC 9457, `Content-Type: application/problem+json`:

```json
{
  "type": "about:blank",
  "title": "Conflict",
  "status": 409,
  "detail": "device \"01J8ZK9WQ7X3YV0M4N5P6Q7R8S\" is offline",
  "instance": "/api/devices/01J8ZK9WQ7X3YV0M4N5P6Q7R8S/camera/switch"
}
```

## Unknown types and channels

The server's behavior for anything it does not recognize:

| Inbound frame | Server behavior |
| --- | --- |
| Text frame whose `type` is not `ack`, `pong`, `status` or `error` | Ignored, debug log (`ignoring unknown control message`). The connection stays open. |
| Text frame that is not valid JSON, or whose `payload` is not a JSON object | Discarded, debug log (`discarding malformed control message`). The connection stays open. |
| Text frame on a `channel` other than `control` | The `channel` is not checked on text frames, so it is handled exactly like a `control` text frame. |
| Binary frame on a channel other than `recording` or `photo` | Ignored, debug log (`ignoring media frame on unknown channel`). |
| Binary frame whose type does not match its channel (e.g. `photo` on `recording`) | Ignored, debug log. |

Because unknown messages are ignored rather than rejected, a device can safely:

- ignore any server `type` it does not implement (log it and keep the connection);
- ignore unknown payload keys on the commands it does implement;
- answer an unknown *command* that carries an `id` with `ack` + `ok: false` and a short `error`
  (for example `unsupported command`), so an operator at least gets a logged reason — this is a
  recommendation, not server-enforced behavior;
- send new device→server types: older servers ignore them with a debug log, so a rollout can add
  message types without breaking existing servers.

Never close the connection because of an unrecognized message; only transport-level violations
(oversized frames, malformed binary framing, keepalive timeouts) end it.
