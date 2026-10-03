// Package ws implements the camera WebSocket layer: the message envelope
// exchanged with camera clients, the connection hub, and per-connection pumps.
package ws

// Message is the JSON envelope exchanged with camera clients. Payload is
// free-form so each message type can carry its own schema.
type Message struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
}

// Message types sent from the server to a camera.
const (
	MsgStartStream = "start_stream"
	MsgStopStream  = "stop_stream"
	MsgConfigure   = "configure"
	MsgPing        = "ping"
)

// Message types sent from a camera to the server.
const (
	MsgPong          = "pong"
	MsgStreamStarted = "stream_started"
	MsgStreamStopped = "stream_stopped"
	MsgFrame         = "frame"
	MsgStatus        = "status"
	MsgError         = "error"
)

// Payload keys used by frame messages.
const (
	payloadKeyStreamID = "stream_id"
	payloadKeyData     = "data"
	payloadKeySeq      = "seq"
	payloadKeyTS       = "ts"
)
