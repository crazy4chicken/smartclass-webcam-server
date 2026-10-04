---
title: Media Channels
outline: 2
---

# Media Channels

Two binary channels carry media **from the device to the server only**: `recording` for video
frames and `photo` for still images. The server never sends binary frames, and it does not relay
media to anyone in real time — there is no live-view endpoint. Operators reach recorded media
through the read APIs described under [Retrieval](#retrieval).

Framing is shared with the control channel's envelope and is specified in [WebSocket
Transport](/protocol/transport): a binary frame is a 4-byte big-endian header length `N`, exactly
`N` bytes of UTF-8 JSON header, then the raw media bytes to the end of the frame. `N` must be in
`1..65536`, and the whole frame must stay within the connection's 16 MiB read limit.

## Recording channel

Channel `recording` carries one message type, `frame`: one encoded video frame.

### `frame` payload

| Field | JSON type | Required | Allowed values | Default | Meaning |
| --- | --- | --- | --- | --- | --- |
| `camera_enum` | number | Yes | JSON number in `0..n-1` of the device's **current** registration. Non-integer numbers are truncated toward zero before matching (`1.9` matches camera `1`); a value that is not a number (for example a string) counts as absent and the frame is dropped. | — | Camera that produced the frame. |
| `stream_id` | string | Yes | Any non-empty string; in practice must equal the 26-character ULID `stream_id` delivered by the `start_recording` command of an active stream for the same device and camera (`""`, or a non-string, drops the frame). | — | Stream the frame belongs to. |
| `seq` | number | No | Any JSON number; non-integers are truncated toward zero, a non-number counts as absent. Only the first and last values that arrive in a flush batch are recorded. | `0` when absent or not a number | Ordering hint for consumers; see [Ordering and duplicates](#ordering-and-duplicates). |
| `ts` | string | No | RFC3339Nano string, e.g. `2026-10-04T10:00:00Z` or `2026-10-04T10:00:00.512384921Z`. A missing key, a non-string or an unparsable value is treated as "no timestamp". | absent | Capture time, used only when a segment has no `fps` to derive its duration from. |

The `id` envelope field is not used on media messages: leave it out. The `payload` object itself is
required, because both `camera_enum` and `stream_id` live in it.

### Frame body

The bytes after the JSON header are the encoded video payload — typically one access unit or one
encoded picture in one of the announced codecs, each of which is exactly one of `h264`, `h265`,
`mjpeg`, `mpeg4`, `vp8`, `vp9`, `av1` (see [Codec values](/protocol/registration#codec-values)).
The server never parses or validates them: it buffers them and later stores them byte for byte
inside a segment. There is no inner container, no timestamp, no checksum: each WebSocket message
is exactly one frame.

### Complete binary example

A frame for `camera_enum` 0, `stream_id` `01J8ZKQ3B5N7P9R1T3V5X7Z9B2`, `seq` 42, captured at
`2026-10-04T10:00:00Z`:

Header JSON (144 bytes in this encoding — key order and whitespace are free, but `N` must match
the actual byte length):

```json
{"channel":"recording","type":"frame","payload":{"camera_enum":0,"seq":42,"stream_id":"01J8ZKQ3B5N7P9R1T3V5X7Z9B2","ts":"2026-10-04T10:00:00Z"}}
```

The complete frame is 164 bytes: 4 header-length bytes + 144 header bytes + 16 example payload
bytes.

```text
00 00 00 90 7b 22 63 68 61 6e 6e 65 6c 22 3a 22
72 65 63 6f 72 64 69 6e 67 22 2c 22 74 79 70 65
22 3a 22 66 72 61 6d 65 22 2c 22 70 61 79 6c 6f
61 64 22 3a 7b 22 63 61 6d 65 72 61 5f 65 6e 75
6d 22 3a 30 2c 22 73 65 71 22 3a 34 32 2c 22 73
74 72 65 61 6d 5f 69 64 22 3a 22 30 31 4a 38 5a
4b 51 33 42 35 4e 37 50 39 52 31 54 33 56 35 58
37 5a 39 42 32 22 2c 22 74 73 22 3a 22 32 30 32
36 2d 31 30 2d 30 34 54 31 30 3a 30 30 3a 30 30
5a 22 7d 7d 00 00 00 01 67 42 00 1e 90 a1 b2 c3
d4 e5 f6 aa
```

- `00 00 00 90` — header length `144`.
- The next 144 bytes are the header JSON above.
- `00 00 00 01 67 42 00 1e 90 a1 b2 c3 d4 e5 f6 aa` — the 16 raw payload bytes of this example.
  A real frame is the encoder output and is usually much larger; this one is short so the whole
  frame fits on the page.

### Acceptance rules

A frame is accepted only when **all** of these hold; otherwise it is silently dropped with a
debug-level server log and the connection stays open.

| # | Check | Where it is enforced |
| --- | --- | --- |
| 1 | The binary frame decodes: at least 4 bytes, header length `1..65536`, declared length present in the frame, header a valid JSON object. | Binary framing decoder |
| 2 | `channel` is `recording` and `type` is `frame`. | Connection dispatch |
| 3 | `payload.stream_id` is a non-empty string and `payload.camera_enum` is a JSON number. | Connection dispatch |
| 4 | The camera is part of the camera list of the device's **currently attached session** (`GET /ws/register` + upgrade). A new registration takes effect for frames only once its connection attaches; a disconnected device has no session, so all its frames are dropped. | Media manager |
| 5 | An accumulator for `stream_id` exists, it belongs to this device, and it was created for the same `camera_enum`. Accumulators exist only between a successful `recording/start` and its stop or the device's disconnect. | Media manager |

Two consequences worth planning for:

- Frames are dropped, never rejected: there is no error response, no ack and no close. A device
  cannot learn from the protocol that its frames are being discarded; only the server log says so.
- Frames that race ahead of `start_recording` are accepted, because the accumulator is registered
  before the command is queued. Frames sent after `stop_recording` (or after the stream was
  dropped by a disconnect) are dropped even if the device has not yet processed the stop.

### Ordering and duplicates

The server applies **no** ordering logic:

- Frames are appended in **arrival order**, regardless of `seq`.
- Duplicate `seq` values are stored as separate frames; out-of-order `seq` values are not
  reordered; gaps are not detected or reported.
- `seq` matters in exactly two places: the first frame of a flush batch becomes the segment's
  `segment_seq` (and part of its storage key), and the last frame's `seq` is logged. Nothing else
  uses it.
- The recommended device behavior is a monotonically increasing integer counter per stream
  (start at `0`, increment by one per frame) so consumers can detect gaps and sort segments. The
  server does not enforce it.

Because `duration_ms` can be derived from the registered `fps` rather than from timestamps,
a device that drops frames while still nominally producing `fps` frames per second makes the
server overstate the segment duration. Either send an accurate `ts` on every frame or keep the
real frame rate.

### Size limits

The canonical per-connection limits live in the [protocol overview](/protocol/#limits-and-timings);
this table only adds the media-specific behavior when they are exceeded.

| Limit | Value | Behavior when exceeded |
| --- | --- | --- |
| Whole binary frame (length prefix + header + body) | 16 MiB (16 777 216 bytes) | The WebSocket read limit trips: the server sends close code `1009` and drops the connection. If it was still the device's current connection, its recordings are finalized as `failed` (see [Stream status](#stream-status-transitions)). |
| JSON header length `N` | `1..65536` bytes | The frame is discarded with a debug log; the connection survives. |

The per-frame overhead is `4 + N` bytes, so the payload itself must stay below
`16 MiB - 4 - N`. The header is tiny next to that budget: the payload can use virtually the whole
16 MiB.

### Server pipeline

The device never uploads to object storage directly. The server buffers frames per stream and
flushes them as segments.

**Accumulator.** `recording/start` creates the stream row and then registers exactly one frame
accumulator for its `stream_id` — keyed by the stream, and carrying the stream's device, camera
and registered `fps`. Frames are matched against all three. The accumulator lives until the stream
is stopped, the device disconnects, or the stream is replaced by another start for the same id
(which cannot happen with ULID ids).

**Flush triggers.** A flush uploads whatever is buffered:

| Trigger | Detail |
| --- | --- |
| Timer | Every 5 seconds, when the buffer is non-empty. |
| Frame count | As soon as 150 frames are buffered. The flush runs asynchronously on the stream's flush goroutine. |
| Stop | The final flush when the stream is stopped or the device disconnects. |

Buffer contents are reset at the start of a flush, so if the upload fails those frames are lost —
the segment is not retried and the buffer is not kept. The failure is written to the server log
and remembered as the stream's last flush error, which the stop path logs if it is still current.

**Segment composition.** A segment is the batch's frames concatenated with a 4-byte big-endian
length prefix before each one:

```text
[uint32 BE len(frame 1)][frame 1][uint32 BE len(frame 2)][frame 2] ... [frame n]
```

Example: two frames of 3 and 2 bytes become `00 00 00 03 aa bb cc 00 00 00 02 dd ee`. The object
is uploaded with `Content-Type: application/octet-stream`; it has no container header, so consumers
must split it with the length prefixes to recover the frames. Which codec the frames use is **not**
recorded anywhere — the stream's `metadata.codecs` lists every codec the device announced (each
one of `h264`, `h265`, `mjpeg`, `mpeg4`, `vp8`, `vp9`, `av1`), not the negotiated one.

**Storage key.** Segments are stored under:

```text
{device_id}/streams/{stream_id}/{YYYYMMDDTHHMMSS}_{first_seq}.bin
```

`YYYYMMDDTHHMMSS` is the **flush** time (UTC, second precision), not the capture time of the first
frame, and `first_seq` is the `seq` of the first frame in the batch. For example:

```text
01J8ZK9WQ7X3YV0M4N5P6Q7R8S/streams/01J8ZKQ3B5N7P9R1T3V5X7Z9B2/20261004T100105_42.bin
```

**Segment record.** After a successful upload the server inserts one `stream_segments` row:

| Field | JSON type | Allowed values | Meaning |
| --- | --- | --- | --- |
| `id` | string | ULID, 26 characters; generated by the server at flush time. | Segment id. |
| `stream_id` | string | 26-character ULID of the owning stream. | Owning stream. |
| `device_id` | string | 26-character ULID of the owning device. | Owning device. |
| `camera_enum` | number | Integer; the camera of the frames (the accumulator's camera). | Camera of the frames. |
| `segment_seq` | number | Integer; `int(first_seq)` of the batch, `0` when the device never sent `seq`. | First frame's `seq`. |
| `size_bytes` | number | Non-negative byte count of the stored object (prefixes included). | Object size. |
| `duration_ms` | number | Non-negative integer; see the formula below. Omitted from API responses when `0`. | Computed duration. |
| `created_at` | string | RFC3339 timestamp of the insert (flush) time. | Insert time. |
| `storage_key` | string | `{device_id}/streams/{stream_id}/{YYYYMMDDTHHMMSS}_{first_seq}.bin`. Never returned by the API (`json:"-"`); see [Retrieval](#retrieval). | Object key. |

**`duration_ms` formula**, applied with the stream's registered `fps` (the snapshot taken at
`recording/start`):

1. if `fps > 0`: `frames * 1000 / fps` (integer division; 150 frames at 30 fps is exactly 5000 ms);
2. else if a first timestamp exists and the last is later than it: the millisecond delta between
   the last and first frame timestamps of the batch;
3. else `0`.

Because the recorded `fps` is the registration value, a device that streams at a different rate
than it announced produces segments whose `duration_ms` reflects the announced rate.

### Stream status transitions

Allowed `status` values: exactly `active`, `completed` or `failed`; a stream is created `active`
and finishes as one of the other two.

```text
                    recording/start                    recording/stop (HTTP 200)
   (no stream) -----------------------> active ------------------------------> completed
                                          |
                                          |  start command undeliverable (HTTP 502)
                                          |  or device connection closed
                                          v
                                        failed
```

| Trigger | Resulting `status` | Media handling | `ended_at` |
| --- | --- | --- | --- |
| `recording/start` succeeds | `active` | Accumulator registered; frames accepted. | Not set |
| `recording/start` cannot deliver the command (`502`) | `failed` | Accumulator discarded; buffered frames are drained to storage but the row is still failed. | Set |
| `recording/stop` succeeds | `completed` | Final flush runs first; if it fails, the error is logged but the stream is still completed. | Set |
| Device connection closes while it is the current connection | `failed` for every one of its streams | Every accumulator is drained with a final flush (30-second budget), then finished. Streams were not stopped by a command, hence `failed`. | Set |
| Connection replaced by a newer one for the same device | Unchanged | The replaced connection does not finalize the device's media or touch its live session. | Unchanged |

The finish operation sets `ended_at` only once: a later finish changes `status` but keeps the
first `ended_at`. Streams are created `active`, never re-activated, and `failed`/`completed`
streams do not block a new start for the same camera — the uniqueness rule is per device and
camera over `active` streams only.

## Photo channel

Channel `photo` carries one message type, `photo`: one still image.

### `photo` payload

| Field | JSON type | Required | Allowed values | Default | Meaning |
| --- | --- | --- | --- | --- | --- |
| `camera_enum` | number | Yes | JSON number in `0..n-1` of the device's current registration; non-integers are truncated toward zero before matching, and a non-number counts as absent (the photo is dropped). | — | Camera that captured the image. |
| `request_id` | string | No | Any string; conventionally the 26-character ULID `request_id` from a `take_photo` command, when the photo answers one. The empty string or a non-string is treated as absent; the value is never validated or matched against outstanding requests. | absent | Correlation id. |
| `content_type` | string | No | Any non-empty string, stored verbatim (no content sniffing, no normalisation). Canonical value: `image/jpeg`. The empty string or a non-string falls back to the default. | `application/octet-stream` | Media type of the stored object. |
| `ts` | string | No | RFC3339Nano string, e.g. `2026-10-04T10:00:05Z`. A missing key, a non-string or an unparsable value is replaced by the server's receive time. | server `now()` | `taken_at` of the photo record. |

`stream_id` and `seq` do not apply to photos; do not send them. The `id` envelope field is unused.

### Complete binary example

A photo for `camera_enum` 0, answering `take_photo` request
`01J8ZKQ3B5N7P9R1T3V5X7Z9B3`, taken at `2026-10-04T10:00:05Z`:

Header JSON (160 bytes in this encoding):

```json
{"channel":"photo","type":"photo","payload":{"camera_enum":0,"content_type":"image/jpeg","request_id":"01J8ZKQ3B5N7P9R1T3V5X7Z9B3","ts":"2026-10-04T10:00:05Z"}}
```

The complete frame is 176 bytes: 4 header-length bytes + 160 header bytes + 12 example payload
bytes (a real JPEG keeps going after its start-of-image marker).

```text
00 00 00 a0 7b 22 63 68 61 6e 6e 65 6c 22 3a 22
70 68 6f 74 6f 22 2c 22 74 79 70 65 22 3a 22 70
68 6f 74 6f 22 2c 22 70 61 79 6c 6f 61 64 22 3a
7b 22 63 61 6d 65 72 61 5f 65 6e 75 6d 22 3a 30
2c 22 63 6f 6e 74 65 6e 74 5f 74 79 70 65 22 3a
22 69 6d 61 67 65 2f 6a 70 65 67 22 2c 22 72 65
71 75 65 73 74 5f 69 64 22 3a 22 30 31 4a 38 5a
4b 51 33 42 35 4e 37 50 39 52 31 54 33 56 35 58
37 5a 39 42 33 22 2c 22 74 73 22 3a 22 32 30 32
36 2d 31 30 2d 30 34 54 31 30 3a 30 30 3a 30 35
5a 22 7d 7d ff d8 ff e0 00 10 4a 46 49 46 00 01
```

- `00 00 00 a0` — header length `160`.
- The next 160 bytes are the header JSON above.
- `ff d8 ff e0 00 10 4a 46 49 46 00 01` — the 12 example payload bytes; a real photo is the whole
  image file.

### Upload semantics

- On receipt the server mints a photo id (ULID) and stores the body at
  `{device_id}/photos/{photo_id}` — no file extension, the object's content type carries the
  format. The protocol defines no photo quota, and the server does not expire photos on its own.
- The upload runs synchronously on that connection's read goroutine with a 30-second timeout, so a
  slow upload delays the next messages from the same device. Other devices are unaffected.
- The photo record is written **only after the upload succeeds**:

| Field | JSON type | Allowed values | Meaning |
| --- | --- | --- | --- |
| `id` | string | ULID, 26 characters; generated by the server. | Photo id. |
| `device_id` | string | 26-character ULID of the owning device. | Owning device. |
| `camera_enum` | number | Integer of the device's current registration. | Camera that captured it. |
| `content_type` | string | Any string, stored verbatim; `application/octet-stream` when the payload had none. | Uploaded content type. |
| `size_bytes` | number | Non-negative byte count of the image body. | Body length in bytes. |
| `request_id` | string | Any string; the empty string when the payload carried none. Omitted from API responses when empty. | Correlation id. |
| `taken_at` | string | RFC3339 timestamp: the `ts` payload value, or the server's receive time. | Capture time. |
| `created_at` | string | RFC3339 timestamp of the record insert. | Insertion time. |
| `storage_key` | string | `{device_id}/photos/{photo_id}`. Never returned by the API (`json:"-"`). | Object key. |

Failure handling:

- Camera not in the current registration: the photo is dropped before any upload, with a debug
  log. No object and no row.
- Upload failure (storage unreachable, `4xx/5xx` from the backend, timeout): error log, no row.
  The device is told nothing.
- Row insertion failure after a successful upload: error log, no row; the stored object stays
  behind as an orphan.
- No deduplication: two `photo.photo` messages with the same `request_id` produce two records.

### Correlating with `take_photo`

1. The operator calls `POST /api/devices/{device_id}/photo` and receives
   `202` with `request_id` (see [Control Channel](/protocol/control)).
2. The device captures, sets that exact `request_id` on its `photo.photo` message, and uploads it.
3. The operator finds the record by listing the device's photos and matching `request_id`, for
   example `GET /api/devices/{device_id}/photos`. There is no lookup-by-`request_id` endpoint.

The command's `ack` only confirms that the device accepted the capture; it says nothing about the
upload, and a failed upload never surfaces to the operator. A photo may also be uploaded without
any command (unsolicited), in which case `request_id` is absent.

## Retrieval

Recorded media is read through the management plane with a `cam:read:<scope>` permission:
list a device's streams, segments or photos, then download the bytes behind the presigned
`download_url` (15 minutes, minted fresh per response; the field is absent when presigning fails).

The complete workflow - endpoints, ordering, resource fields, examples, direct object-storage
access and the segment byte layout - is the [service integration guide](/guide/service-integration).
Schemas: [streams](/api/reference/streams) and [photos](/api/reference/photos) references; the
shared `items`/`limit` convention: [API overview](/api/overview#collections).

## Drop and ignore conditions

Everything above consolidated; all of these are logged and none of them is reported to the device.

| Condition | Behavior | Connection |
| --- | --- | --- |
| Binary frame shorter than 4 bytes | Discarded at decode, debug log. | Open |
| Declared header length is `0` or larger than `65536` | Discarded at decode, debug log. | Open |
| Declared header length exceeds the bytes actually received | Discarded at decode, debug log. | Open |
| Header is not valid JSON (or `payload` is not an object) | Discarded at decode, debug log. | Open |
| Binary frame on a channel other than `recording`/`photo` | Ignored at dispatch, debug log. | Open |
| `recording` frame whose `type` is not `frame` | Ignored at dispatch, debug log. | Open |
| `photo` message whose `type` is not `photo` | Ignored at dispatch, debug log. | Open |
| `frame` without a non-empty string `stream_id`, or without a numeric `camera_enum` | Discarded at dispatch, debug log. | Open |
| `photo` without a numeric `camera_enum` | Discarded at dispatch, debug log. | Open |
| `frame` for a camera not in the device's current registration | Discarded by the media manager, debug log. | Open |
| `frame` whose `stream_id` has no accumulator, or whose accumulator belongs to another device or camera, or whose stream was stopped | Discarded by the media manager, debug log. | Open |
| `photo` for a camera not in the current registration | Discarded by the media manager, debug log. | Open |
| Photo upload or record write fails | Photo dropped, error log, no record. | Open |
| Whole binary frame above 16 MiB | Read limit trips: close `1009`, connection is dropped; if it was the device's current connection, its streams are finalized as `failed`. | Closed |

Fields that are merely malformed do not cause a drop: a non-numeric `seq` becomes `0`, an
unparsable `ts` is treated as absent (frames) or replaced by the receive time (photos), and an
empty or non-string `request_id`/`content_type` becomes absent or the default content type.
