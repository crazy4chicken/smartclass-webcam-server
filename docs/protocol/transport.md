---
title: WebSocket transport
outline: 2
---

# WebSocket transport

`GET /ws/device/{device_websocket_id}` upgrades the device's HTTP request to the WebSocket that
carries every control message and all media. The ticket from
[registration](/protocol/registration) is the only credential: no teamusers token is sent here.

One connection carries three logical channels, multiplexed by the JSON envelope in each frame:

- `control` - text JSON frames, both directions ([Control channel](/protocol/control)).
- `recording` - binary frames, device to server ([Media channels](/protocol/media)).
- `photo` - binary frames, device to server ([Media channels](/protocol/media)).

## Path parameter

| Parameter | JSON type | Allowed values | Meaning |
| --- | --- | --- | --- |
| `device_websocket_id` | string (path segment) | Any path-segment string; an issued ticket is exactly 64 lowercase hex characters (`0-9`, `a-f`, e.g. `0b30557a9fc4e90e33587da2c7ec11365b80a5caef14395e83a8cdf2173c6186`). The value is matched against the registry verbatim. | The single-use ticket returned by `GET /ws/register`. It identifies the registration and, indirectly, the device. Any value that is not a known ticket simply answers `404`. |

The request must be a valid WebSocket upgrade (`Connection: Upgrade`, `Upgrade: websocket`,
`Sec-WebSocket-Key`, `Sec-WebSocket-Version: 13`). `Origin` is not checked: devices are not browsers
and the ticket, not an origin, authorizes the connection.

## Before the upgrade

The server performs these checks in order, before any `101` is written:

| # | Check | Failure |
| --- | --- | --- |
| 1 | `registry.Get(ticket)` - the ticket is known (pending or attached) and, when pending, not expired | `404` problem+json |
| 2 | The ticket is not the device's current live session | `409` problem+json |
| 3 | The HTTP request is a valid WebSocket handshake | `400` plain text from the HTTP library, with `Sec-Websocket-Version: 13` |

The handshake itself is bounded by a 10-second timeout. A failed handshake does **not** consume the
ticket: a pending ticket survives a `400` and can be retried within its TTL.

### Unknown or expired ticket

Both an id that was never issued, a ticket whose TTL elapsed before attaching, and a ticket that
died with its connection answer identically:

```json
{
  "type": "about:blank",
  "title": "Not Found",
  "status": 404,
  "detail": "device websocket not found",
  "instance": "/ws/device/0b30557a9fc4e90e33587da2c7ec11365b80a5caef14395e83a8cdf2173c6186"
}
```

### Replay while the session is live

Redeeming the ticket that is currently attached to the device's live connection answers:

```json
{
  "type": "about:blank",
  "title": "Conflict",
  "status": 409,
  "detail": "device websocket ticket already attached",
  "instance": "/ws/device/0b30557a9fc4e90e33587da2c7ec11365b80a5caef14395e83a8cdf2173c6186"
}
```

This pre-upgrade check catches the common case. It is not the authority: the race-free single-use
guarantee is enforced again after the upgrade (next section).

### Not a WebSocket request

A plain HTTP request with a valid pending ticket (for example `curl` without upgrade headers)
passes the two registry checks and then fails the handshake. The WebSocket library answers with a
plain-text `400 Bad Request` response, not a problem document:

```http
HTTP/1.1 400 Bad Request
Content-Type: text/plain; charset=utf-8
Sec-Websocket-Version: 13

Bad Request
```

## Attach and the post-upgrade race

A successful handshake answers `101 Switching Protocols`. The server then binds the ticket to the
connection in one atomic step. If the bind fails, the connection is closed immediately with:

```text
Close frame: code 1008 (policy violation), reason "ticket already attached"
```

That happens when two connections redeemed the same pending ticket at the same time: the first
`Attach` wins and consumes it, and every later one is rejected even though its HTTP upgrade already
succeeded. This is the only window in which a client can observe a `101` followed by a policy
close.

Recommended client behavior: treat `1008` as fatal for that ticket. Do not retry the same ticket;
start over at [registration](/protocol/registration).

After a successful attach the ticket is **attached**: it is the device's live session and the only
connection allowed to carry that device's traffic.

## Connection replacement

A device holds one live session at a time. Registering again mints a new pending ticket while the
old session keeps running; the replacement happens when the new connection attaches:

- The new connection becomes the device's live session.
- The server closes the old connection right away, without a close handshake, so the old agent
  typically observes an abnormal closure (`1006`).
- The old connection's recordings are **not** finalized: only the newest connection finalizes a
  device's media when *it* disconnects. Media buffered by the old session is drained when its own
  handler exits only if it is still the newest connection.
- The old ticket is removed when its handler exits; it never becomes attachable again.

This is a deliberate "newest connection wins" rule: reconnect by registering and attaching, and
never run two agents for one device id.

## What a close does to server state

Whatever ends the connection - the device closing, the server closing, a replaced connection, or
the 60-second read deadline - the handler exits and performs the same release, in this order:

| # | Effect | Detail |
| --- | --- | --- |
| 1 | The ticket is dropped | The registration is removed from every index. The ticket answers `404` from now on, regardless of `expires_at`. |
| 2 | The device is offline | The management plane reports it as offline (`GET /api/devices/{device_id}/` has `online: false`) and command calls answer `409` while it has no live session. |
| 3 | Media is drained (only for the newest connection) | Every accumulator of the device is stopped within a 30-second budget, its remaining frames are flushed as a final segment, and each stream is marked `failed`. A connection that was already replaced leaves the live session's media alone. |
| 4 | The connection is removed from the hub | Later command sends fail; nothing is queued for a disconnected device. |
| 5 | Later frames are dropped | With the registration gone, frames for any camera are discarded with a `debug` log if they still arrive. |

A stream stopped by `POST /api/devices/{device_id}/recording/stop` is finalized as `completed`; a
stream ended by a disconnect is finalized as `failed`. Both keep their uploaded segments, which are
listed by the read endpoints.

## Text frames: the JSON envelope

Every text frame is one JSON object carrying the shared `Message` envelope:

```json
{
  "channel": "control",
  "type": "start_recording",
  "id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B1",
  "payload": {"camera_enum": 0, "stream_id": "01J8ZKQ3B5N7P9R1T3V5X7Z9B2"}
}
```

The field semantics, their allowed values and the message catalog are the
[Control channel](/protocol/control#message-envelope) specification. The transport-level behavior:

- Text frames are dispatched on `type` alone - the `channel` field is ignored on text frames
  (another value is still handled as `control`), though `control` is what peers expect. Binary
  frames are dispatched on `channel`, with `recording` and `photo` the only values that have a
  handler.
- Unknown types and channels are ignored with a `debug` log, never answered.
- Malformed text JSON is discarded with a `debug` log entry; the connection stays open.
- Unknown envelope fields are ignored, so a client can add its own metadata without breaking the
  server.

## Binary frames: byte layout

Media travels as binary WebSocket frames, one message per frame. The frame starts with the header
length, followed by the JSON envelope, followed by the raw media bytes:

```text
 0                    4                                    4+N                  end
 +--------------------+------------------------------------+--------------------+
 | uint32 big-endian N|  N bytes UTF-8 JSON (envelope)     |  raw media bytes   |
 +--------------------+------------------------------------+--------------------+
    header length          1 <= N <= 65536                   may be empty
```

| Offset | Size | Content |
| --- | --- | --- |
| `0` | 4 bytes | `N`: the header length in bytes, unsigned 32-bit big-endian. |
| `4` | `N` bytes | The JSON envelope - the same `Message` shape as a text frame, with `channel` set to `recording` or `photo` and `type` to the media type. `N` must be in `1..65536` inclusive. |
| `4 + N` | to the end of the frame | The media payload, byte for byte. It may be empty. The server treats these bytes as opaque: it never parses or validates them, so the payload bytes need not match the announced codec (announced values, each one of `h264`, `h265`, `mjpeg`, `mpeg4`, `vp8`, `vp9`, `av1`, are in [Codec values](/protocol/registration#codec-values)). |

The envelope's `payload` never carries a base64 `data` field; the bytes after the header are the
media itself.

### Decode rules

`DecodeBinary`-equivalent server logic, in order:

| Condition | Result | Server behavior |
| --- | --- | --- |
| Frame shorter than 4 bytes | `ErrFrameTooShort` | Dropped, `debug` log, connection survives |
| `N == 0` or `N > 65536` | `ErrHeaderSize` | Dropped, `debug` log, connection survives |
| `N` exceeds the bytes actually present | `ErrFrameTooShort` | Dropped, `debug` log, connection survives |
| Header is not valid JSON | `ErrHeaderInvalid` (wraps the JSON error) | Dropped, `debug` log, connection survives |
| Frame valid | decoded | Dispatched on `channel`; see the matrix below |

A malformed binary header never closes the connection and never produces a response.

The whole frame is also subject to the transport read limit: a frame larger than 16 MiB makes the
read fail, the server sends one close frame with code `1009` (`message too big`) and the connection
dies. Splitting a large payload across smaller messages is not supported; there is no continuation
or reassembly beyond the frame.

### Encode rules

The encoder marshals the envelope to compact JSON and refuses a header whose length is 0 or greater
than 65536. It does not enforce the 16 MiB total; that limit is the receiver's. A frame is exactly
`4 + N + len(data)` bytes.

### Worked example

A recording frame for camera `0`, stream `01J8ZKQ3B5N7P9R1T3V5X7Z9B2`, sequence `42`, captured at
`2026-10-04T10:00:00Z` uses this header JSON - exactly the compact form the Go encoder produces,
with payload keys sorted:

```json
{"channel":"recording","type":"frame","payload":{"camera_enum":0,"seq":42,"stream_id":"01J8ZKQ3B5N7P9R1T3V5X7Z9B2","ts":"2026-10-04T10:00:00Z"}}
```

That JSON is 144 bytes, so the length prefix is `0x00000090`. The header bytes, offset `0` at the
first byte of the JSON:

```text
00000000  7b 22 63 68 61 6e 6e 65 6c 22 3a 22 72 65 63 6f
00000010  72 64 69 6e 67 22 2c 22 74 79 70 65 22 3a 22 66
00000020  72 61 6d 65 22 2c 22 70 61 79 6c 6f 61 64 22 3a
00000030  7b 22 63 61 6d 65 72 61 5f 65 6e 75 6d 22 3a 30
00000040  2c 22 73 65 71 22 3a 34 32 2c 22 73 74 72 65 61
00000050  6d 5f 69 64 22 3a 22 30 31 4a 38 5a 4b 51 33 42
00000060  35 4e 37 50 39 52 31 54 33 56 35 58 37 5a 39 42
00000070  32 22 2c 22 74 73 22 3a 22 32 30 32 36 2d 31 30
00000080  2d 30 34 54 31 30 3a 30 30 3a 30 30 5a 22 7d 7d
```

The first four bytes on the wire are then `00 00 00 90`, followed by the 144 bytes above, followed
by the encoded frames until the end of the WebSocket message. A receiver reads `N` from the first
four bytes, parses `bytes[4:4+N]` as JSON, and takes `bytes[4+N:]` as the media payload.

### Go helpers

The server implements this layout in its `internal/ws` package with the two exported helpers below.
Because it is an `internal` package, a separate client module cannot import it; treat the
signatures as the reference for your own encoder and decoder.

```go
func EncodeBinary(m Message, data []byte) ([]byte, error)
func DecodeBinary(raw []byte) (Message, []byte, error)
```

`DecodeBinary` returns the decoded envelope and the trailing media bytes; the errors it can wrap
are `ErrFrameTooShort`, `ErrHeaderSize` and `ErrHeaderInvalid`.

## Channels and message types

| Channel | Frame | Direction | Accepted type | Required payload keys | Full reference |
| --- | --- | --- | --- | --- | --- |
| `control` | Text | Server to device | `switch_camera`, `start_recording`, `stop_recording`, `take_photo`, `ping` | Per type | [Control channel](/protocol/control) |
| `control` | Text | Device to server | `ack`, `pong`, `status`, `error` | Per type | [Control channel](/protocol/control) |
| `recording` | Binary | Device to server | `frame` | `stream_id` (non-empty string), `camera_enum` (number) | [Media channels](/protocol/media), [Codec values](/protocol/registration#codec-values) |
| `photo` | Binary | Device to server | `photo` | `camera_enum` (number) | [Media channels](/protocol/media) |

Dispatch rules the server applies to inbound frames:

- Binary frames are dispatched on `channel`; the `type` must match the channel's media type or the
  frame is ignored with a `debug` log.
- A recording frame without `stream_id` or `camera_enum`, or a photo without `camera_enum`, is
  dropped with a `debug` log.
- An unknown channel, or a binary frame on `control`, is ignored.
- `camera_enum` and `seq` are read as JSON numbers; a fractional value is truncated toward zero. A
  value that is not a number (for example a string) counts as absent.

## Keepalive

The canonical numbers - server ping cadence, read/write deadlines, inbound frame limit, header
size range, command queue depth, upgrade timeout and buffers - are in the
[protocol overview](/protocol/#limits-and-timings). This page only spells out the device side:
the server sends a protocol-level ping followed by an application `ping` on the control channel,
and a device that stays silent is disconnected.

Device obligations:

- Answer protocol-level pings with pongs. Most WebSocket libraries do this automatically; if yours
  does not, do it explicitly, because the server's read deadline is refreshed by that pong.
- Answer the application `ping` with a `pong` message. The server does not track per-ping
  responses, but the app-level round trip refreshes the socket through normal traffic.
- Any other inbound traffic (media frames, `ack`, `status`) also refreshes the deadline. A device
  that is silent for 60 seconds is disconnected, and the disconnect is indistinguishable from any
  other close.

## Oversized and garbled frames

| Condition | Server behavior | Client advice |
| --- | --- | --- |
| Text frame is not valid JSON | Dropped, `debug` log, connection survives | Fix the encoder; do not expect an error reply |
| Text frame has an unknown `type` or `channel` | Ignored, `debug` log, connection survives | Ignore unknown server messages the same way (forward compatibility) |
| Binary frame shorter than 4 bytes, header length outside `1..65536`, declared header length beyond the frame, or non-JSON header | Dropped, `debug` log, connection survives | Fix the framing; the connection is not closed |
| Binary frame larger than 16 MiB | Close `1009`, connection dies | Keep every whole frame at or below 16 MiB |
| Frame for an unknown stream or a camera not in the current registration | Dropped, `debug` log, connection survives | Only send frames for `stream_id`s the server announced and `camera_enum`s from your registration |

## Reconnection

There is no session resume. The ticket dies with the connection, and the server keeps no state to
re-attach to. The reconnect procedure is always:

1. `GET /ws/register` with the device token and the current camera list
   ([registration](/protocol/registration)).
2. `GET /ws/device/{device_websocket_id}` with the new ticket ([attach](#attach-and-the-post-upgrade-race)).
3. Wait for the next `start_recording` command; a recording that was interrupted is marked `failed`
   server-side and is never resumed under the old stream id.

Practical guidance:

- Reconnect with exponential backoff and jitter (for example 1s, 2s, 4s, capped) instead of a tight
  loop. A `404` on attach means the new ticket is unusable and a fresh registration is needed.
- A `409` on attach means some connection still holds the ticket as the live session. Registering
  again and attaching with the *new* ticket replaces that session; the newest connection wins.
- A persistent `401` on the registration call means the device token no longer works (rotated or
  the device was deleted). That needs an operator to re-provision the credential; retrying cannot
  fix it.
- Expect abrupt closures: a replaced connection, a device deletion, a token rotation and a read
  timeout all end the connection without a graceful close handshake, so a `1006` is normal
  operation, not necessarily a bug.
- After reconnecting, re-announce the full camera set: the registration from the previous
  connection is gone, and commands are validated against the new one.
