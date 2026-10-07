---
title: Protocol overview
outline: 2
---

# Protocol overview

`smartclass-webcam-server` speaks one device protocol. A classroom device agent authenticates
over HTTP with its long-lived **device token**, receives a single-use WebSocket **ticket**, and then
holds one WebSocket connection that multiplexes a text **control** channel and two binary **media**
channels. Every recording and photo the device produces travels over that connection; the server
buffers the media and writes it to object storage.

This section is the normative reference for that contract:

- [Device registration](/protocol/registration) - the HTTP call that announces the cameras and
  returns the ticket.
- [WebSocket transport](/protocol/transport) - the upgrade, the ticket attach rules, the text and
  binary framing, and the transport limits.
- [Control channel](/protocol/control) - every command the server sends and every control message a
  device sends back.
- [Media channels](/protocol/media) - the recording and photo frames, their acceptance rules, and
  the storage pipeline.

The pages state the server's actual behavior, including edge cases, as implemented. Where a
behavior is not part of the intended contract but is observable, the page says so and marks client
advice as recommended.

## Planes and actors

Two HTTP planes meet in this service. They use different credentials and different error bodies,
and a device never mixes them.

| Plane | Prefix | Credential | Caller |
| --- | --- | --- | --- |
| Device plane | `/ws/register`, `/ws/device/{device_websocket_id}` | Device token (`wdt_...`), then the ticket | The device agent |
| Management plane | `/api/...` | teamusers Bearer token with a `cam:<action>:<scope>` permission | Operators and management tools |

The management plane is documented in the [API overview](/api/overview) and
[Permissions and access control](/guide/permissions); this section covers only the device plane.

| Actor | Responsibility |
| --- | --- |
| Device agent | Drives one or more cameras. Registers, holds the WebSocket, executes commands, pushes media frames, acknowledges commands. |
| Server | Authenticates the device, issues tickets, tracks the live session, buffers media to object storage, exposes the management API. |
| Operator | Uses the management plane to create devices, rotate tokens, trigger commands, and download recordings and photos. |

Camera parameters (`resolution`, `fps`, `supported_resolutions`, `supported_framerates`,
`supported_codec`, `attrs`) are ephemeral: the device announces them on every registration and the
server keeps them only in the live registration, never in the device record. `resolution` and
`fps` are the parameters a camera is at for the current connection — a device captures from exactly
one camera at exactly one resolution and frame rate at a time, and only a `switch_camera` command
selects another camera or another pair; a device never changes them on its own.

## Endpoint map

| Purpose | Method and path | Credential | Success | Reference |
| --- | --- | --- | --- | --- |
| Register a device, mint a ticket | `GET /ws/register` | Device token | `200` ticket | [Registration](/protocol/registration) |
| Attach the WebSocket | `GET /ws/device/{device_websocket_id}` | Ticket in the path | `101 Switching Protocols` | [Transport](/protocol/transport) |
| Control channel | text frames on the open connection | Ticket | - | [Control](/protocol/control) |
| Recording channel | binary frames on the open connection | Ticket | - | [Media](/protocol/media) |
| Photo channel | binary frames on the open connection | Ticket | - | [Media](/protocol/media) |

The table is the device-plane surface. The four command triggers under
`/api/devices/{device_id}/...` are documented for device implementers in
[Control channel](/protocol/control); every management-plane route - commands, read-back and
device management - has its schema in the generated [API reference](/api/overview) and its
permission mapping in the [endpoint matrix](/guide/permissions#endpoint-matrix). Reading
recordings and photos back is walked through in the
[service integration guide](/guide/service-integration).

## Connection lifecycle

A registration (ticket) moves through the states below. `pending` and `attached` are the two live
states; `replaced`, `expired` and `closed` are terminal for that ticket. Time flows downwards.

```text
            GET /ws/register                      another registration
                    |                              for the same device
                    v                                     |
             +--------------+  ---------------------------+--> +--------------+
             |   pending    |                                  |   expired    |
             +--------------+                                  +--------------+
                    |
                    | GET /ws/device/{ticket}  (attach wins)
                    v
             +--------------+   a newer ticket attaches   +--------------+
             |   attached   | --------------------------> |   replaced   |
             +--------------+                             +--------------+
                    |                                             |
                    | connection closes                           | old handler exits
                    v                                             v
             +--------------+                             +--------------+
             |    closed    | <-------------------------- |   closed     |
             +--------------+        (terminal)           +--------------+
                    |
                    | GET /ws/register again
                    +----------------------> pending
```

A pending ticket that is never redeemed expires after its TTL; an attached one becomes replaced
when a newer connection takes over and closes when its own connection ends. `expired`, `replaced`
and `closed` are terminal: the ticket is gone and attaching it answers `404` (or `409` while the
device's live session still holds that same ticket).

| From | Trigger | To | Server-side effect |
| --- | --- | --- | --- |
| - | `GET /ws/register` succeeds | pending | Ticket indexed by id and by device. The device's previous **unused** ticket is silently dropped. |
| pending | `GET /ws/device/{ticket}` attaches | attached | Bound to exactly one connection and made the device's live session. A previous live connection is closed. |
| pending | TTL elapses before attach | expired | Dropped lazily on the next lookup; it then answers `404`. |
| pending | Another registration for the same device | expired | The previous unused ticket is dropped from the registry immediately. |
| attached | A newer ticket of the same device attaches | replaced | The server closes the old TCP connection without a close handshake; its recordings are left alone, the new session owns the device. |
| attached | Connection closes for any reason | closed | Ticket dropped (`404` afterwards), device offline, its active streams drained and finalized as `failed`. |
| attached | Device token rotation or device deletion | closed | Pending ticket dropped and the live connection closed. |
| replaced | The old connection's handler exits | closed | No further effect; the newer session stays live. |

Only `pending` and `attached` tickets are known to the registry:

- A `pending` ticket is valid until `expires_at`; after that it is unknown and answers `404`.
- An `attached` ticket is the device's live session. It ignores `expires_at`, answers `409` when
  redeemed while it is still live, and becomes unknown (answering `404`) the moment its connection
  closes.
- A `replaced` ticket is dropped when its old connection's handler exits. In the short window
  before that it can still pass the pre-upgrade checks; redeeming it then succeeds at the HTTP
  level and is closed right after the upgrade with code `1008` (see
  [Transport](/protocol/transport)).

## Channels and message types

One WebSocket connection carries three logical channels. The `channel` field of the JSON envelope
selects one; the frame kind is fixed per channel.

| Channel | Frame | Direction | Message types | Reference |
| --- | --- | --- | --- | --- |
| `control` | Text, JSON | Both | Server to device: `switch_camera`, `start_recording`, `stop_recording`, `take_photo`, `ping`. Device to server: `ack`, `pong`, `status`, `error`. | [Control channel](/protocol/control) |
| `recording` | Binary | Device to server | `frame` | [Media channels](/protocol/media) |
| `photo` | Binary | Device to server | `photo` | [Media channels](/protocol/media) |

The envelope fields, the payload of each message and the acknowledgement rules are specified in
the [Control channel](/protocol/control); the binary framing - length prefix, decode rules and
transport limits - in the [WebSocket transport](/protocol/transport) page.

The server never sends binary frames: every server-to-device message is a text frame carrying the
JSON envelope. A binary frame the server receives is dispatched on the channel named in its header;
anything else is ignored (see [Transport](/protocol/transport)).

Every field that appears in an envelope or payload, its allowed values and whether the server
enforces them are collected in [Enumerated values](#enumerated-values).

## Limits and timings

These are the canonical numbers for the protocol; other pages link here instead of restating them.
Everything is a compile-time constant except `WEBCAM_WS_TICKET_TTL`, which sets the ticket
lifetime.

| Limit | Value | Setting |
| --- | --- | --- |
| Ticket lifetime (`expires_at` - issue time) | 60 seconds | `WEBCAM_WS_TICKET_TTL` (Go duration syntax, default `60s`) |
| WebSocket upgrade handshake timeout | 10 seconds | - |
| WebSocket upgrade read/write buffer per direction | 4096 bytes | - |
| Server keepalive interval (protocol ping, then application `ping`) | every 30 seconds | - |
| Read deadline (silence tolerated before the server drops the connection) | 60 seconds, refreshed by any inbound traffic | - |
| Write deadline (per outbound write) | 10 seconds | - |
| Maximum inbound message, i.e. one whole binary media frame | 16 MiB (16777216 bytes) | - |
| Binary header length prefix (`N`) | 1 to 65536 bytes inclusive | - |
| Registration request body | 1 MiB (1048576 bytes), larger answers `413` | - |
| Outbound command queue per device | 16 messages, overflow answers `502` on the trigger call | - |
| Media flush: timer, frame count, and on stop | every 5 seconds, or 150 buffered frames, or when the stream stops | - |
| Disconnect drain budget (final flush of a device's streams) | 30 seconds | - |
| Single object-storage upload / store write timeout | 30 seconds | - |
| Presigned segment and photo download URL lifetime | 15 minutes | - |
| Device token format | `wdt_` + 43 base64url characters (32 random bytes, no padding); only its SHA-256 hash is stored | - |
| Ticket format | 32 random bytes as 64 lowercase hex characters | - |
| Stream, command, request and photo id format | ULID, 26 characters | - |
| Content type of a segment object | `application/octet-stream` | - |
| Content type of a stored photo | `image/jpeg` — an absent photo `content_type` means the canonical one | - |

## Enumerated values

This hub collects every enumerated field of the device protocol. The enforcement column uses three
levels:

- **enforced** - the server validates, rejects, normalises or stores the value, or otherwise acts
  on it. A violating value never reaches the wire unchanged.
- **canonical-only** - the server accepts any value but never acts on it; the listed values are the
  documented convention a device should follow.
- **free-form** - any value of that JSON type is accepted and stored or echoed verbatim; there is
  no server-side validation.

| Field | Allowed values | Enforcement | Where documented |
| --- | --- | --- | --- |
| `channel` (envelope) | `control`, `recording`, `photo` | enforced on binary dispatch; ignored on text frames, which are handled as `control` whatever the value | [Transport](/protocol/transport#text-frames-the-json-envelope), [Control](/protocol/control#message-envelope) |
| message `type` | Server to device: `switch_camera`, `start_recording`, `stop_recording`, `take_photo`, `ping`. Device to server: `ack`, `pong`, `status`, `error` (text) and `frame`, `photo` (binary). | enforced dispatch: recognised values are handled, unknown values are ignored with a `debug` log and never rejected | [Transport catalog](/protocol/transport#channels-and-message-types), [Control](/protocol/control#server-to-device-commands), [Media](/protocol/media) |
| `camera_enum` (registration) | Integer `0..n-1` only, must equal the element's index in `cameras` | enforced: gaps, duplicates and reorders answer `400 cameras[i].camera_enum must be i` | [Registration](/protocol/registration) |
| `camera_enum` (commands) | Integer in `0..n-1` of the device's current registration | enforced for `switch_camera`, `start_recording` and `take_photo` (`400` when unregistered); `stop_recording` resolves the camera's active stream instead (`404` when none) | [Control](/protocol/control#operator-http-triggers) |
| `camera_enum` (media payloads) | JSON number; non-integers truncated toward zero; must match the current registration | enforced: truncation and registration match; mismatches drop the frame or photo | [Media](/protocol/media#frame-payload) |
| `resolution` | Free-form non-empty string (whitespace-only rejected); `WIDTHxHEIGHT` convention such as `1920x1080`, `1280x720`. The value a camera is at must be one of its `supported_resolutions` (compared after trimming) | enforced: non-empty and membership in the trimmed `supported_resolutions` (`400 cameras[i].resolution must be one of the supported_resolutions`); a switch may only select a listed value, otherwise `400 resolution "<resolution>" is not supported by camera_enum <camera_enum> on device "<device_id>"` | [Registration](/protocol/registration#cameras), [Control](/protocol/control#switch-camera) |
| `fps` | Integer greater than `0` (`1`, `2`, `3`, ...); fractions are decode errors. The value a camera is at must be one of its `supported_framerates` | enforced: positivity and membership in `supported_framerates` (`400 cameras[i].fps must be one of the supported_framerates`); a switch may only select a listed value, otherwise `400 fps <fps> is not supported by camera_enum <camera_enum> on device "<device_id>"` | [Registration](/protocol/registration#cameras), [Control](/protocol/control#switch-camera) |
| `supported_resolutions` | Non-empty array of strings; entries are trimmed, empty ones are rejected, duplicates (after trimming) are rejected, and the camera's current `resolution` must appear in the list | enforced: non-empty, per-entry emptiness, duplicates, membership of `resolution` | [Supported parameters](/protocol/registration#supported-parameters) |
| `supported_framerates` | Non-empty array of integers, every entry `> 0`, no duplicates; the camera's current `fps` must appear in the list | enforced: non-empty, per-entry positivity, duplicates, membership of `fps` | [Supported parameters](/protocol/registration#supported-parameters) |
| `supported_codec` | Non-empty array of strings; every element exactly one of `h264`, `h265`, `mjpeg`, `mpeg4`, `vp8`, `vp9`, `av1` (FFmpeg-style lowercase names, case-sensitive: `H264` and the alias `hevc` are rejected), no duplicates. The first entry is the preferred codec of the camera | enforced closed set: an unknown value answers `400 cameras[i].supported_codec[j] must be one of h264, h265, mjpeg, mpeg4, vp8, vp9, av1`, a duplicate answers `400 cameras[i].supported_codec must not contain duplicates`; a `recording/start` codec outside the announced list answers `400 codec "<codec>" is not supported by camera_enum <camera_enum> on device "<device_id>"`; no normalisation, no negotiation; extending the vocabulary requires a server change | [Codec values](/protocol/registration#codec-values) |
| `attrs` | Any JSON object | free-form: kept in the live registration, never validated, never persisted | [Registration](/protocol/registration#request) |
| `content_type` (photo) | `image/jpeg` (trimmed, case-insensitive) or absent; empty or non-string counts as absent | enforced JPEG-only: absent means the canonical `image/jpeg`, any other value discards the photo with no record | [Media](/protocol/media#photo-payload) |
| `seq` | Any JSON number; non-integers truncated toward zero; absent or non-number counts as `0` | enforced truncation and default | [Media](/protocol/media#frame-payload) |
| `ts` (server `ping`) | RFC3339Nano UTC timestamp from the server clock, e.g. `2026-10-04T10:00:30Z` | enforced: the server formats it; always present on a `ping` | [Control](/protocol/control#ping) |
| `ts` (media payloads) | RFC3339Nano string; malformed, non-string or missing is treated as absent (`frame`) or replaced by the server's receive time (`photo`) | enforced fallback | [Media](/protocol/media#frame-payload) |
| `payload.ok` (`ack`) | JSON boolean `true` or `false` | canonical-only, not enforced: a device-to-server value the server logs but never validates (a missing or non-boolean `ok` is still accepted) | [Control](/protocol/control#ack) |
| `payload.error` (`ack`, `error`) | Any string; free-form | free-form: logged, never persisted or validated | [Control](/protocol/control#ack) |
| `id` (envelope) | 26-character ULID string (uppercase Crockford base32) for commands; absent on server-to-device `ping` | canonical-only for commands (server-minted); on device-to-server messages the value is logged verbatim without validation | [Control](/protocol/control#command-identifiers) |
| `stream_id` | 26-character server-issued ULID; non-empty string on frames, and the accumulator match is exact | enforced: `start_recording`/`stop_recording` supply it; frames without a matching non-empty `stream_id` are dropped | [Control](/protocol/control#start-recording), [Media](/protocol/media#frame-payload) |
| `request_id` | 26-character server-issued ULID (from `take_photo`); any string accepted on upload and never matched | canonical-only: correlation only, no lookup or validation | [Control](/protocol/control#take-photo), [Media](/protocol/media#photo-payload) |
| device token | `wdt_` + 43 base64url characters (32 random bytes, no padding) | enforced bearer shape and `wdt_` prefix; the value itself is compared by SHA-256 hash | [Registration](/protocol/registration#request-headers) |
| ticket (`device_websocket_id`) | 64 lowercase hex characters (32 random bytes); lookup accepts any path-segment string | enforced lookup: an unknown or expired ticket answers `404` | [Registration](/protocol/registration#response), [Transport](/protocol/transport#path-parameter) |
| stream `status` | `active`, `completed`, `failed` | enforced: the server sets only these values, and never re-activates a finished stream | [Media](/protocol/media#stream-status-transitions) |
| `limit` (read endpoints) | Integer `>= 1`; omitted → `100`; values above `1000` are clamped to `1000` | enforced: non-integer or `<= 0` answers `400 limit must be a positive integer` | [API overview](/api/overview#collections) |
| `download_url` | Absolute presigned URL string; valid for **15 minutes**; absent when presigning fails | canonical-only: minted per response by the storage backend, omitted on failure | [Service integration](/guide/service-integration) |

## Error model

### Management-plane HTTP errors

Failures that reach an `/api` handler are RFC 9457 problem details (`application/problem+json`,
`type` always `about:blank`); field table, example and auth exceptions: [API overview](/api/overview#errors).
Two shapes come from the router itself rather than a handler: an unknown path answers `404` with
detail `no route for <METHOD> <path>`, and a known path called with the wrong method answers `405`
with detail `method <METHOD> is not allowed on <path>; allowed: <methods>`, repeating the allowed
methods in the `Allow` header. Server-side failures name the operation: `500` bodies read
`<operation> failed: <cause>` (for example `list devices failed: ...`, or
`loading the photo failed: ...` from the authorization middleware) and command sends that cannot be
queued read `502 sending <command> failed: <cause>`; the cause in a `500` or `502` body is
sanitized of configured credentials and truncated at 300 bytes.

### Device-plane HTTP statuses

The device plane uses the same body shape for its `400`, `401`, `404`, `409`, `413` and `500`
responses; the one exception is a failed WebSocket handshake, where the HTTP library answers with a
plain-text `400` (see [Transport](/protocol/transport)).

| Status | Endpoint | Condition | Body |
| --- | --- | --- | --- |
| `200` | `GET /ws/register` | Registration accepted; ticket minted | Ticket object |
| `400` | `GET /ws/register` | Body is not a single JSON object, or a camera rule fails | problem+json |
| `400` | `GET /ws/device/{ticket}` | Request is not a valid WebSocket handshake | Plain text `Bad Request` |
| `401` | `GET /ws/register` | No `Authorization` header, a header without a `Bearer wdt_...` token, or a token/device pair that does not verify | problem+json, plus `WWW-Authenticate: Bearer realm="device"` |
| `404` | `GET /ws/device/{ticket}` | Ticket unknown or expired | problem+json, `device websocket not found` |
| `409` | `GET /ws/device/{ticket}` | Ticket is the device's live session and is still attached | problem+json, `device websocket ticket already attached` |
| `413` | `GET /ws/register` | Body larger than 1 MiB | problem+json, `request body too large` |
| `500` | `GET /ws/register` | Database failure while loading the device, or ticket generation failure | problem+json, `load device for registration failed: <cause>` / `issue device websocket ticket failed: <cause>` |
| `101` | `GET /ws/device/{ticket}` | Upgrade succeeded | WebSocket connection |

The `401` detail names the failing check: `the Authorization header is missing`,
`the Authorization header does not carry a Bearer token`,
`the Authorization header does not carry a device token`, or
`the device token is unknown or has been rotated` (shared by an unknown device and a wrong or
rotated token, so the endpoint never leaks whether a device exists). A `400` from a malformed body
reads `invalid JSON request body: <cause>` with the decoder message sanitized and truncated at 300
bytes; camera-rule details are listed in [Registration](/protocol/registration#failure-table).

### WebSocket-level failures

| Condition | Observable result | Server state |
| --- | --- | --- |
| Ticket lost the attach race after the `101` | Close frame `1008` (`policy violation`) with reason `ticket already attached` | This connection is closed; the winning connection stays live. |
| Inbound message larger than 16 MiB | Close frame `1009` (`message too big`), then the connection dies | The oversized message is not delivered; the ticket is released when the handler exits. |
| Server shuts the connection down deliberately (replacement, token rotation, device deletion) | Best-effort close frame `1000`; the TCP connection is closed without waiting for a reply, so a `1006` abnormal closure is equally possible | The session is released and the device goes offline. |
| Device silent past the 60-second read deadline | No close frame; the server closes the connection (typically observed as `1006`) | Same as any disconnect: ticket dropped, streams finalized as `failed`. |
| Device-side command failure | `ack` with `payload.ok: false` and `payload.error` | None; the command stays delivered, and the triggering HTTP call is not retroactively changed. |
| Malformed text frame, malformed binary header, unknown type/channel, or a frame for an unknown camera/stream | Nothing is sent back; the frame is dropped with a `debug` log and the connection survives | None |

Recommended client behavior: treat every close as a full disconnect. The ticket is dead the moment
the connection ends, so reconnection always starts with a fresh `GET /ws/register`.

## End-to-end sequence

```text
operator                     server                          device agent
   |                            |                                  |
   | POST /api/devices          |                                  |
   |--------------------------->| create device, mint token        |
   | 201 {device, token}        |                                  |
   |<---------------------------|                                  |
   |                            |        GET /ws/register          |
   |                            |<---------------------------------|
   |                            | 200 {device_websocket_id, ...}   |
   |                            |--------------------------------->|
   |                            |   GET /ws/device/{ticket}        |
   |                            |<---------------------------------|
   |                            | 101 Switching Protocols (attach) |
   |                            |--------------------------------->|
   | POST .../recording/start   |                                  |
   |--------------------------->| create stream (active)           |
   | 201 {stream} + Location    | control: start_recording         |
   |<---------------------------|--------------------------------->|
   |                            | control: ack {ok: true}          |
   |                            |<---------------------------------|
   |                            | recording.frame x N (binary)     |
   |                            |<---------------------------------|
   |                            | flush every 5 s / 150 frames     |
   | POST .../photo             |                                  |
   |--------------------------->| control: take_photo              |
   | 202 {command_id, ...}      |--------------------------------->|
   |<---------------------------| control: ack {ok: true}          |
   |                            |<---------------------------------|
   |                            | photo.photo (binary)             |
   |                            |<---------------------------------|
   | POST .../recording/stop    |                                  |
   |--------------------------->| control: stop_recording          |
   | 200 {stream completed}     |--------------------------------->|
   |<---------------------------| control: ack {ok: true}          |
   |                            |<---------------------------------|
   |                            | final flush -> last segment      |
   |                            |                                  |
   |                            |<---------- connection closes ----|
   |                            | ticket dropped, device offline   |
   |                            | streams drained and marked failed|
   |                            |                                  |
   |                            |<---------- GET /ws/register -----|
```

## Client implementation checklist

1. Store the `device_id` and the `wdt_` device token; treat the token as a credential. Send the
   full camera set (`camera_enum` `0..n-1`) to `GET /ws/register` on every startup and after every
   reconnect, announcing each camera's current `resolution` and `fps` together with the
   `supported_resolutions`, `supported_framerates` and `supported_codec` lists it accepts.
2. Parse `expires_at` with a full RFC 3339 parser: it is Go `time.Time` JSON and normally carries
   sub-second precision. Do not assume whole seconds.
3. Attach with `GET /ws/device/{device_websocket_id}` before the ticket expires. On `404`
   (expired/unknown) or `409` (a live session holds it), start over at step 1.
4. Never reuse a ticket: it is single-use and dies with its connection. There is no session resume.
5. Answer the server's protocol-level pings with pongs (most WebSocket libraries do this
   automatically) and answer the application `ping` message with a `pong`. Silence for 60 seconds
   gets the connection closed.
6. Keep channels separate: JSON text frames on `control`, binary frames on `recording` and `photo`.
   The server sends no binary frames.
7. Acknowledge every command by echoing its `id`; report failures by setting `payload.ok` to
   `false` and adding a `payload.error` string instead of dropping the message.
8. Build binary frames as `uint32` big-endian header length, the JSON header, then the raw bytes.
   Keep each whole frame at or below 16 MiB.
9. Tag recording frames with the `stream_id` from `start_recording` and with a `camera_enum` from
   your current registration; send `seq` monotonically and a `ts` in RFC 3339 (nanosecond
   precision recommended).
10. Send the `request_id` from `take_photo` on the matching photo frame. Photos are always JPEG:
    set `content_type` to `image/jpeg` or omit it — an absent value means the canonical one — and
    never send another value, which makes the server discard the photo with no record.
11. Ignore unknown message types and channels instead of failing: the server may add new ones. On
    the server side, unknown control types and unknown media channels are logged at `debug` and
    ignored.
12. After any close, re-register and re-attach. In-flight recordings are finalized server-side as
    `failed`; a new recording starts only with a new `start_recording` and a new stream id.
13. Expect abrupt disconnects: a newer connection of the same device replaces yours without a
    close handshake. Reconnect with exponential backoff rather than a tight loop.
14. Do not push media for streams the server has not announced, and stop pushing as soon as a
    `stop_recording` for that stream is acknowledged.
15. Keep your cameras at the parameters you announced: capture from exactly one camera at exactly
    one resolution and frame rate, and change them only when a `switch_camera` command names new
    values from your `supported_resolutions` / `supported_framerates` lists — a device never
    changes them on its own.
16. Record with the `codec` a `start_recording` command names when it is present, otherwise with
    your preferred codec — the first entry of `supported_codec`. A codec can be requested only
    there, never on a switch or a photo.
