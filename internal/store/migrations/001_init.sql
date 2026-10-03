-- Initial schema: cameras, recording streams and their uploaded segments.

CREATE TABLE cameras (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    location   TEXT,
    status     TEXT NOT NULL DEFAULT 'offline',
    config     JSONB NOT NULL DEFAULT '{}',
    last_seen  TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE streams (
    id         TEXT PRIMARY KEY,
    camera_id  TEXT NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    status     TEXT NOT NULL DEFAULT 'active',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at   TIMESTAMPTZ,
    metadata   JSONB NOT NULL DEFAULT '{}'
);

CREATE TABLE stream_segments (
    id          TEXT PRIMARY KEY,
    stream_id   TEXT NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    camera_id   TEXT NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    segment_seq INTEGER NOT NULL,
    storage_key TEXT NOT NULL,
    size_bytes  BIGINT NOT NULL DEFAULT 0,
    duration_ms INTEGER,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_streams_camera ON streams(camera_id, started_at DESC);
CREATE INDEX idx_segments_stream ON stream_segments(stream_id, segment_seq);
