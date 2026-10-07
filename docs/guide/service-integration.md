---
title: Service Integration
outline: 2
---

# Service Integration

This page is for backend services that consume recorded media from the webcam
server: discovering streams and photos over the management API, downloading the
recorded bytes, and reading them directly from object storage when needed. Two
access paths are available:

- **Presigned URLs (normal path).** Read endpoints return short-lived
  `download_url` values minted by the configured storage backend, so the
  consumer never holds storage credentials.
- **Direct object-storage access.** A consumer that needs bulk transfer or
  prefix-based scans can read the backing bucket itself using the key layout
  below. That path bypasses the API and its permission checks, so the storage
  deployment must authorize the consumer separately.

## Access model

Every `/api` request carries a teamusers-issued bearer token:

```http
GET /api/devices/{device_id}/streams HTTP/1.1
Authorization: Bearer <teamusers access token>
```

Reads are authorized by `cam:read:<scope>`: `any` reaches all devices, `team`
the caller's team, `own` the caller's devices. The consuming service
authenticates with its own teamusers service credential (or a dedicated user
account) whose grants include `cam:read` at a scope covering the devices it
reads; there is no separate media-only permission. The scope ladder and the
key grammar are documented in
[Permissions and access control](/guide/permissions).

A missing or invalid token answers `401`; a token without a matching grant
answers `403` with an RFC 9457 `application/problem+json` body whose `detail` is
`permission denied` followed by every key the ladder tried and the cause each
check reported.

## Finding streams

`GET /api/devices/{device_id}/streams` lists a device's recording sessions,
newest first (`started_at DESC, id DESC`):

```sh
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$WEBCAM_URL/api/devices/$DEVICE_ID/streams?limit=10"
```

```json
{
  "items": [
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
  ]
}
```

A stream carries `id`, `device_id`, `camera_enum`, `status` (`active`,
`completed`, or `failed`), `started_at`, `ended_at` (absent while the stream is
active), and `metadata` - the `resolution`, `fps`, and `codecs` announced by the
device when the recording started.

`limit` is optional on every list: the default is `100`, values above `1000`
are clamped, and a non-positive or non-numeric value is rejected with `400`.
See [Collections](/api/overview#collections) for the full convention. Results
are not paginated: request the newest `limit` and poll again for new sessions.

## Downloading segments

Segment bytes are never returned inline. Fetching a stream returns it together
with its segments, each carrying a presigned `download_url`:

```sh
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$WEBCAM_URL/api/streams/$STREAM_ID/" |
  jq '.segments[] | {segment_seq, size_bytes, duration_ms, download_url}'
```

Each segment has `id`, `stream_id`, `device_id`, `camera_enum`,
`segment_seq` (the `seq` of the segment's first frame, `0` when the device sent
none), `size_bytes` (length prefixes included), `created_at` (upload time), and
`duration_ms` (omitted when zero).

- Segments are ordered by `segment_seq` ascending - playback order.
- `download_url` is an absolute URL minted **per response** and valid for
  **15 minutes**. Re-request the stream detail when one expires; every call
  returns fresh URLs, so do not cache them longer than that.
- When the storage backend cannot mint a URL the field is simply **omitted**
  from that segment (the response is still `200`). Treat a missing
  `download_url` as "retry later or read the object directly".
- `GET /api/streams/{stream_id}/segments` lists the same segment records in the
  `items` envelope but **never** carries `download_url`; use it for polling and
  bookkeeping, not for fetching bytes.
- `duration_ms` is an estimate derived from the stream's registered `fps` (or
  from frame timestamps when no `fps` was registered), not a measurement.

## Photos

`GET /api/devices/{device_id}/photos` lists a device's photos, newest first
(`taken_at DESC, id DESC`). Each photo carries `id`, `device_id`,
`camera_enum`, `content_type`, `size_bytes`, `request_id` (omitted when empty),
`taken_at`, and `created_at`.

`GET /api/photos/{photo_id}/` returns one photo plus a presigned
`download_url` with the same per-response, 15-minute semantics as segments. The
URL serves the image bytes exactly as the device uploaded them; `content_type`
is stored verbatim from the device (canonically `image/jpeg`) with no sniffing
or normalization.

A photo's `request_id` correlates it with the `take_photo` command that asked
for it. There is no lookup-by-`request_id` endpoint, so match it while listing
the device's photos.

The generated references describe every field:
[Streams](/api/reference/streams) and [Photos](/api/reference/photos).

## Direct object-storage access

The backing bucket is `WEBCAM_FILEHOUSE_BUCKET` (default `webcam-segments`) for
nsc-filehouse, or `WEBCAM_S3_BUCKET` (default `webcam-streams`) for an
S3-compatible endpoint. Objects are laid out as:

```text
{device_id}/streams/{stream_id}/{YYYYMMDDTHHMMSS}_{first_seq}.bin
{device_id}/photos/{photo_id}
```

- The segment timestamp is the **flush** time (UTC, second precision), not the
  capture time of the first frame; `first_seq` is the segment's `segment_seq`.
  It normally matches the record's `created_at` second.
- Photo objects have no file extension; the object's content type carries the
  format (`Upload` forwards the declared `content_type`).
- Both layouts are prefix-friendly: `{device_id}/streams/{stream_id}/`
  enumerates one recording and `{device_id}/photos/` one device's images,
  provided the storage API and the consumer's credentials allow listing.
- The API never exposes `storage_key`; direct access is governed entirely by
  the storage deployment's permissions (for filehouse, a teamusers bearer token
  accepted by its catalog).

## Working with the data

A segment object is a run of frames, each preceded by its 4-byte big-endian
length: `[uint32 BE len][frame][uint32 BE len][frame]...`. Split it before
decoding:

```python
import struct

def frames(blob: bytes):
    """Split one segment object into its raw encoded frames."""
    off = 0
    while off < len(blob):
        (n,) = struct.unpack_from(">I", blob, off)
        off += 4
        yield blob[off:off + n]
        off += n
```

- There is no container header, no per-frame timestamp, and no checksum; each
  frame is exactly the encoded payload the device sent, in arrival order.
- **The codec is not recorded.** `metadata.codecs` is the list the device
  announced at registration, not the codec actually in use. Determine it
  yourself (for example by inspecting the frame bitstream) or take it from the
  device's configuration, then pick the matching demuxer - an H.264 stream can
  be fed to `ffmpeg -f h264 -i frames.h264 ...`, other codecs need their own.
- Concatenating the split frames of consecutive segments in `segment_seq` order
  reconstructs the recording's frame sequence. A segment does not necessarily
  start at a keyframe, so seek to a segment boundary only if you can tolerate
  decoding from there.
- Photo objects are the raw image bytes, with no transformation applied.

## Constraints

- **No push notifications.** The server has no event feed or webhook; poll the
  list endpoints for new streams and photos.
- **Deleting a device cascades only database rows.** `DELETE
  /api/devices/{device_id}/` removes the device and its stream, segment, and
  photo records, but the stored objects are left behind - the service never
  deletes objects. A re-created device gets a fresh `device_id`, so the old
  prefixes become unreachable through the API and can only be cleaned up
  through the storage backend.
- **A no-op backend discards payloads.** With neither `WEBCAM_FILEHOUSE_URL`
  nor `WEBCAM_S3_ACCESS_KEY` configured (a bare local dev run), sessions and
  records are still created, but nothing is stored and `download_url` is always
  empty.
- **Retrieval is after the fact.** Segments appear as the server flushes them
  (a few seconds of buffering), and there is no live-view endpoint; polls see
  media only once it is stored.

## Worked example

With `WEBCAM_URL` pointing at the deployment and `TOKEN` holding a teamusers
access token that carries `cam:read`:

```sh
WEBCAM_URL='https://webcam.example.com'
TOKEN='<teamusers access token>'
DEVICE_ID='01J8ZK9WQ7X3YV0M4N5P6Q7R8S'

# 1. Find the device's streams, newest first.
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$WEBCAM_URL/api/devices/$DEVICE_ID/streams?limit=10" |
  jq '.items[] | {id, status, started_at, ended_at}'

# 2. Fetch a stream and inspect its segments.
STREAM_ID='01J8ZKQ3B5N7P9R1T3V5X7Z9B2'
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$WEBCAM_URL/api/streams/$STREAM_ID/" |
  jq '.segments | sort_by(.segment_seq)[] | {segment_seq, size_bytes, download_url}'

# 3. Download the segments in playback order (URLs expire after 15 minutes).
i=0
for url in $(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$WEBCAM_URL/api/streams/$STREAM_ID/" |
    jq -r '.segments | sort_by(.segment_seq)[].download_url'); do
  i=$((i + 1))
  curl -fsS -o "segment_$i.bin" "$url"
done

# 4. List photos and download the newest one.
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$WEBCAM_URL/api/devices/$DEVICE_ID/photos?limit=5" |
  jq '.items[] | {id, content_type, taken_at}'

PHOTO_ID='01J8ZKQ3B5N7P9R1T3V5X7Z9B5'
curl -fsS -H "Authorization: Bearer $TOKEN" \
  "$WEBCAM_URL/api/photos/$PHOTO_ID/" |
  jq -r '.download_url' |
  xargs -I{} curl -fsS -o photo.jpg '{}'
```
