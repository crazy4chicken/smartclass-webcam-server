// Package domain defines the core data types for the webcam server.
package domain

import "time"

// StreamStatus represents the lifecycle state of a recording session.
type StreamStatus string

const (
	StreamStatusActive    StreamStatus = "active"
	StreamStatusCompleted StreamStatus = "completed"
	StreamStatusFailed    StreamStatus = "failed"
)

// Device represents a registered camera device. Camera parameters are
// ephemeral: they live in the registration session, not in the database.
type Device struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Location  string     `json:"location,omitempty"`
	TeamID    string     `json:"team_id,omitempty"`
	OwnerID   string     `json:"owner_id,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	TokenHash []byte     `json:"-"`
}

// StreamMetadata snapshots the camera parameters a stream was started with:
// the resolution and frame rate the camera was at, the codec the caller asked
// for - empty when the device chose its preferred one - and every codec the
// camera supports.
type StreamMetadata struct {
	Resolution string   `json:"resolution,omitempty"`
	FPS        int      `json:"fps,omitempty"`
	Codec      string   `json:"codec,omitempty"`
	Codecs     []string `json:"codecs,omitempty"`
}

// Stream represents a recording session for one camera of a device.
type Stream struct {
	ID         string         `json:"id"`
	DeviceID   string         `json:"device_id"`
	CameraEnum int            `json:"camera_enum"`
	Status     StreamStatus   `json:"status"`
	StartedAt  time.Time      `json:"started_at"`
	EndedAt    *time.Time     `json:"ended_at,omitempty"`
	Metadata   StreamMetadata `json:"metadata"`
}

// StreamSegment is one uploaded video chunk stored in object storage.
type StreamSegment struct {
	ID         string    `json:"id"`
	StreamID   string    `json:"stream_id"`
	DeviceID   string    `json:"device_id"`
	CameraEnum int       `json:"camera_enum"`
	SegmentSeq int       `json:"segment_seq"`
	StorageKey string    `json:"-"`
	SizeBytes  int64     `json:"size_bytes"`
	DurationMS int       `json:"duration_ms,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Photo is one captured still stored in object storage.
type Photo struct {
	ID          string    `json:"id"`
	DeviceID    string    `json:"device_id"`
	CameraEnum  int       `json:"camera_enum"`
	StorageKey  string    `json:"-"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	RequestID   string    `json:"request_id,omitempty"`
	TakenAt     time.Time `json:"taken_at"`
	CreatedAt   time.Time `json:"created_at"`
}
