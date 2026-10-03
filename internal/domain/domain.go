// Package domain defines the core data types for the webcam server.
package domain

import "time"

// CameraStatus represents the connection state of a camera.
type CameraStatus string

const (
	CameraStatusOffline   CameraStatus = "offline"
	CameraStatusOnline    CameraStatus = "online"
	CameraStatusStreaming CameraStatus = "streaming"
)

// StreamStatus represents the lifecycle state of a recording session.
type StreamStatus string

const (
	StreamStatusActive    StreamStatus = "active"
	StreamStatusCompleted StreamStatus = "completed"
	StreamStatusFailed    StreamStatus = "failed"
)

// Camera represents a registered camera device.
type Camera struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Location  string       `json:"location,omitempty"`
	Status    CameraStatus `json:"status"`
	Config    CameraConfig `json:"config"`
	LastSeen  *time.Time   `json:"last_seen,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

// CameraConfig holds the current camera operational configuration.
type CameraConfig struct {
	Resolution string `json:"resolution,omitempty"` // e.g. "1920x1080"
	FPS        int    `json:"fps,omitempty"`
	Codec      string `json:"codec,omitempty"`   // e.g. "h264", "mjpeg"
	Bitrate    int    `json:"bitrate,omitempty"` // kbps
}

// Stream represents a recording session for a camera.
type Stream struct {
	ID        string         `json:"id"`
	CameraID  string         `json:"camera_id"`
	Status    StreamStatus   `json:"status"`
	StartedAt time.Time      `json:"started_at"`
	EndedAt   *time.Time     `json:"ended_at,omitempty"`
	Metadata  StreamMetadata `json:"metadata"`
}

// StreamMetadata holds stream-level recording parameters.
type StreamMetadata struct {
	Resolution  string `json:"resolution,omitempty"`
	FPS         int    `json:"fps,omitempty"`
	Codec       string `json:"codec,omitempty"`
	TotalFrames int64  `json:"total_frames,omitempty"`
}

// StreamSegment is one uploaded video chunk stored in object storage.
type StreamSegment struct {
	ID         string    `json:"id"`
	StreamID   string    `json:"stream_id"`
	CameraID   string    `json:"camera_id"`
	SegmentSeq int       `json:"segment_seq"`
	StorageKey string    `json:"storage_key"`
	SizeBytes  int64     `json:"size_bytes"`
	DurationMs int       `json:"duration_ms,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}
