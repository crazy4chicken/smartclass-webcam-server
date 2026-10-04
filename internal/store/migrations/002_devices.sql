-- Device-centric schema: devices own streams; stream segments and photos are
-- the uploaded media.

DROP TABLE IF EXISTS stream_segments;
DROP TABLE IF EXISTS streams;
DROP TABLE IF EXISTS cameras;

CREATE TABLE devices (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    location   TEXT,
    team_id    TEXT,
    owner_id   TEXT,
    last_seen  TIMESTAMPTZ,
    token_hash BYTEA,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE streams (
    id          TEXT PRIMARY KEY,
    device_id   TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    camera_enum INTEGER NOT NULL,
    status      TEXT NOT NULL DEFAULT 'active',
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at    TIMESTAMPTZ,
    metadata    JSONB NOT NULL DEFAULT '{}'
);

CREATE UNIQUE INDEX idx_streams_active ON streams(device_id, camera_enum) WHERE status = 'active';

CREATE TABLE stream_segments (
    id          TEXT PRIMARY KEY,
    stream_id   TEXT NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    device_id   TEXT NOT NULL,
    camera_enum INTEGER NOT NULL,
    segment_seq INTEGER NOT NULL,
    storage_key TEXT NOT NULL,
    size_bytes  BIGINT NOT NULL DEFAULT 0,
    duration_ms INTEGER,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE photos (
    id           TEXT PRIMARY KEY,
    device_id    TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    camera_enum  INTEGER NOT NULL,
    storage_key  TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    size_bytes   BIGINT NOT NULL DEFAULT 0,
    request_id   TEXT,
    taken_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_streams_device ON streams(device_id, started_at DESC);
CREATE INDEX idx_segments_stream ON stream_segments(stream_id, segment_seq);
CREATE INDEX idx_photos_device ON photos(device_id, taken_at DESC);
