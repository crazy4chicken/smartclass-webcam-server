// Package ws implements the device WebSocket plane: the message envelope
// exchanged with devices, the device-keyed connection hub, the per-connection
// pumps and the registry of single-use connection tickets.
package ws

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// Channels multiplexed on one device connection.
const (
	ChannelControl   = "control"
	ChannelRecording = "recording"
	ChannelPhoto     = "photo"
)

// Control messages sent from the server to a device.
const (
	CommandSwitchCamera   = "switch_camera"
	CommandStartRecording = "start_recording"
	CommandStopRecording  = "stop_recording"
	CommandTakePhoto      = "take_photo"
	ControlPing           = "ping"
)

// Control messages sent from a device to the server.
const (
	ControlAck    = "ack"
	ControlPong   = "pong"
	ControlStatus = "status"
	ControlError  = "error"
)

// Media types sent from a device to the server.
const (
	MediaFrame = "frame" // channel "recording"
	MediaPhoto = "photo" // channel "photo"
)

// Payload keys shared by control and media messages.
const (
	PKStreamID    = "stream_id"
	PKCameraEnum  = "camera_enum"
	PKResolution  = "resolution"
	PKFPS         = "fps"
	PKCodec       = "codec"
	PKSeq         = "seq"
	PKTS          = "ts"
	PKRequestID   = "request_id"
	PKContentType = "content_type"
)

// maxHeaderSize bounds the JSON header of a binary media frame in bytes.
const maxHeaderSize = 65536

// Errors returned when decoding a malformed binary media frame.
var (
	// ErrFrameTooShort is returned when a frame is shorter than the header
	// length it declares.
	ErrFrameTooShort = errors.New("ws: media frame shorter than its declared header")
	// ErrHeaderSize is returned when the declared header length is outside
	// 1..65536.
	ErrHeaderSize = errors.New("ws: media frame header length out of range")
	// ErrHeaderInvalid is returned when the header is not valid JSON.
	ErrHeaderInvalid = errors.New("ws: media frame header is not valid JSON")
)

// Message is the JSON envelope exchanged with devices. Text frames carry the
// control channel in both directions; binary media frames prefix the JSON
// header with the same envelope to the raw media bytes.
type Message struct {
	Channel string         `json:"channel"`
	Type    string         `json:"type"`
	ID      string         `json:"id,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

// EncodeBinary builds a binary media frame: uint32 big-endian header length,
// the JSON header, then the raw media bytes.
func EncodeBinary(m Message, data []byte) ([]byte, error) {
	header, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("ws: encode media header: %w", err)
	}
	if len(header) == 0 || len(header) > maxHeaderSize {
		return nil, fmt.Errorf("ws: media header length %d out of range", len(header))
	}

	frame := make([]byte, 4+len(header)+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(header)))
	copy(frame[4:], header)
	copy(frame[4+len(header):], data)
	return frame, nil
}

// DecodeBinary splits a binary media frame into its message header and the raw
// media bytes.
func DecodeBinary(raw []byte) (Message, []byte, error) {
	var m Message
	if len(raw) < 4 {
		return m, nil, ErrFrameTooShort
	}

	n := int(binary.BigEndian.Uint32(raw[:4]))
	if n <= 0 || n > maxHeaderSize {
		return m, nil, ErrHeaderSize
	}
	if len(raw)-4 < n {
		return m, nil, ErrFrameTooShort
	}
	if err := json.Unmarshal(raw[4:4+n], &m); err != nil {
		return m, nil, fmt.Errorf("%w: %v", ErrHeaderInvalid, err)
	}
	return m, raw[4+n:], nil
}
