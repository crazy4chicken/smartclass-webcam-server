package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/crazy4chicken/smartclass-webcam-server/internal/ws"
)

// TestDeviceProtocol drives the device plane end to end: a device registers its
// cameras over HTTP, attaches over the WebSocket, and receives the control
// commands the API issues. It pins what the upgrade promises: the switch
// carries the resolution and frame rate the caller named and records them in
// the live session once the device acknowledges them, a failed acknowledgement
// records nothing, a recording carries the codec it alone may name and
// snapshots those parameters, a parameter the camera did not declare is
// refused, and a photo that is not JPEG is discarded.
func TestDeviceProtocol(t *testing.T) {
	m := newMatrix(t)

	device, deviceToken := m.createDevice("protocol device")
	deviceToken = m.rotateToken(device)

	status, body := m.liveCall(http.MethodGet, "/ws/register", registration(device, "1920x1080", 30), deviceToken)
	if status != http.StatusOK {
		t.Fatalf("register status = %d, want %d (body %s)", status, http.StatusOK, body)
	}
	var ticket struct {
		DeviceWebsocketID string `json:"device_websocket_id"`
	}
	if err := json.Unmarshal(body, &ticket); err != nil || ticket.DeviceWebsocketID == "" {
		t.Fatalf("registration carries no ticket: %s", body)
	}

	// Attach the WebSocket with the ticket; the connection is the device's live
	// session from here on, and its announced cameras surface on the device.
	socketURL := "ws" + strings.TrimPrefix(m.listen(), "http") + "/ws/device/" + ticket.DeviceWebsocketID
	conn, response, err := websocket.DefaultDialer.Dial(socketURL, nil)
	if err != nil {
		t.Fatalf("dial device websocket: %v (response %v)", err, response)
	}
	t.Cleanup(func() { _ = conn.Close() })

	m.expectLive(http.MethodGet, "/api/devices/"+device+"/", "", m.token(operator), http.StatusOK,
		`"resolution":"1920x1080"`, `"fps":30`, `"supported_resolutions":["1920x1080","1280x720"]`, `"supported_framerates":[30,15]`)

	// A parameter the camera did not declare is refused, and nothing reaches
	// the device.
	m.expectLive(http.MethodPost, "/api/devices/"+device+"/camera/switch", `{"camera_enum":0,"resolution":"3840x2160"}`, m.token(operator),
		http.StatusBadRequest, `resolution "3840x2160" is not supported by camera_enum 0 on device "`+device+`"`)
	m.expectLive(http.MethodPost, "/api/devices/"+device+"/camera/switch", `{"camera_enum":0,"fps":60}`, m.token(operator),
		http.StatusBadRequest, `fps 60 is not supported by camera_enum 0 on device "`+device+`"`)
	m.expectLive(http.MethodPost, "/api/devices/"+device+"/recording/start", `{"camera_enum":0,"codec":"hevc"}`, m.token(operator),
		http.StatusBadRequest, `codec "hevc" is not supported by camera_enum 0 on device "`+device+`"`)

	// The switch carries the parameters the caller named, and the device has
	// not confirmed it yet, so nothing is recorded.
	m.expectLive(http.MethodPost, "/api/devices/"+device+"/camera/switch", `{"camera_enum":0,"resolution":"1280x720","fps":15}`, m.token(operator),
		http.StatusAccepted, `"camera_enum":0`)
	command := readDeviceCommand(t, conn)
	if command.Type != ws.CommandSwitchCamera {
		t.Fatalf("command = %q, want %q", command.Type, ws.CommandSwitchCamera)
	}
	assertPayload(t, command, map[string]any{
		ws.PKCameraEnum: float64(0),
		ws.PKResolution: "1280x720",
		ws.PKFPS:        float64(15),
	})
	m.expectLive(http.MethodGet, "/api/devices/"+device+"/", "", m.token(operator), http.StatusOK,
		`"resolution":"1920x1080"`, `"fps":30`)

	// A switch without parameters leaves the parameters to the device.
	m.expectLive(http.MethodPost, "/api/devices/"+device+"/camera/switch", `{"camera_enum":0}`, m.token(operator),
		http.StatusAccepted, `"camera_enum":0`)
	assertPayload(t, readDeviceCommand(t, conn), map[string]any{ws.PKCameraEnum: float64(0)})

	// The device acknowledges the parameterized switch, which applies it: the
	// device detail now reports the pair it named and the next stream snapshots
	// it. The acknowledgement is asynchronous, so the read is retried.
	sendDeviceAck(t, conn, command.ID, true)
	waitForDetail(t, m, device, `"resolution":"1280x720"`, `"fps":15`)

	// A recording names its codec, and the stream records it together with the
	// parameters the acknowledged switch applied, not the ones it registered
	// with.
	m.expectLive(http.MethodPost, "/api/devices/"+device+"/recording/start", `{"camera_enum":0,"codec":"mjpeg"}`, m.token(operator),
		http.StatusCreated, `"status":"active"`, `"codec":"mjpeg"`, `"resolution":"1280x720"`, `"fps":15`)
	start := readDeviceCommand(t, conn)
	if start.Type != ws.CommandStartRecording {
		t.Fatalf("command = %q, want %q", start.Type, ws.CommandStartRecording)
	}
	streamID, _ := start.Payload[ws.PKStreamID].(string)
	if streamID == "" {
		t.Fatalf("start_recording carries no stream_id: %v", start.Payload)
	}
	assertPayload(t, start, map[string]any{ws.PKCameraEnum: float64(0), ws.PKCodec: "mjpeg", ws.PKStreamID: streamID})
	m.expectLive(http.MethodGet, "/api/streams/"+streamID+"/", "", m.token(operator), http.StatusOK,
		`"codec":"mjpeg"`, `"codecs":["h264","mjpeg"]`)

	// Stopping the recording carries the stream it finishes.
	m.expectLive(http.MethodPost, "/api/devices/"+device+"/recording/stop", `{"camera_enum":0}`, m.token(operator),
		http.StatusOK, `"status":"completed"`)
	assertPayload(t, readDeviceCommand(t, conn), map[string]any{
		ws.PKCameraEnum: float64(0),
		ws.PKStreamID:   streamID,
	})

	// A photo command carries no format: still images are JPEG.
	m.expectLive(http.MethodPost, "/api/devices/"+device+"/photo", `{"camera_enum":0}`, m.token(operator),
		http.StatusAccepted, `"request_id"`)
	photo := readDeviceCommand(t, conn)
	if photo.Type != ws.CommandTakePhoto {
		t.Fatalf("command = %q, want %q", photo.Type, ws.CommandTakePhoto)
	}
	requestID, _ := photo.Payload[ws.PKRequestID].(string)
	if requestID == "" {
		t.Fatalf("take_photo carries no request_id: %v", photo.Payload)
	}

	// A frame that is not JPEG is discarded, and the JPEG that follows is
	// stored with the canonical content type.
	m.sendDeviceFrame(t, conn, ws.ChannelPhoto, ws.MediaPhoto, map[string]any{
		ws.PKCameraEnum: 0, ws.PKRequestID: requestID, ws.PKContentType: "image/png",
	}, []byte("\x89PNG\r\n\x1a\nnot-a-jpeg"))
	time.Sleep(300 * time.Millisecond)
	if photos := m.photos(device); len(photos) != 0 {
		t.Fatalf("a non-JPEG photo was stored: %v", photos)
	}

	m.sendDeviceFrame(t, conn, ws.ChannelPhoto, ws.MediaPhoto, map[string]any{
		ws.PKCameraEnum: 0, ws.PKRequestID: requestID, ws.PKContentType: "image/jpeg",
	}, []byte("\xff\xd8\xff\xe0jpeg-bytes"))
	photos := m.waitForPhotos(device, 1)
	if photos[0]["content_type"] != "image/jpeg" {
		t.Fatalf("photo content type = %v, want image/jpeg", photos[0]["content_type"])
	}
	if photos[0]["request_id"] != requestID {
		t.Fatalf("photo request id = %v, want %s", photos[0]["request_id"], requestID)
	}

	// A device that pads its resolution strings is still switchable to them:
	// the live session keeps the values the registration was validated against.
	padded, paddedToken := m.createDevice("padded device")
	paddedToken = m.rotateToken(padded)
	status, body = m.liveCall(http.MethodGet, "/ws/register",
		`{"device_id":"`+padded+`","cameras":[{"camera_enum":0,"resolution":" 1920x1080 ","fps":30,`+
			`"supported_resolutions":[" 1920x1080 ","1280x720"],"supported_framerates":[30,15],`+
			`"supported_codec":["h264"]}]}`, paddedToken)
	if status != http.StatusOK {
		t.Fatalf("padded register status = %d, want %d (body %s)", status, http.StatusOK, body)
	}
	var paddedTicket struct {
		DeviceWebsocketID string `json:"device_websocket_id"`
	}
	if err := json.Unmarshal(body, &paddedTicket); err != nil || paddedTicket.DeviceWebsocketID == "" {
		t.Fatalf("padded registration carries no ticket: %s", body)
	}
	paddedConn, paddedResponse, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(m.listen(), "http")+"/ws/device/"+paddedTicket.DeviceWebsocketID, nil)
	if err != nil {
		t.Fatalf("dial padded device websocket: %v (response %v)", err, paddedResponse)
	}
	t.Cleanup(func() { _ = paddedConn.Close() })

	m.expectLive(http.MethodPost, "/api/devices/"+padded+"/camera/switch", `{"camera_enum":0,"resolution":"1920x1080"}`, m.token(operator),
		http.StatusAccepted, `"camera_enum":0`)
	paddedSwitch := readDeviceCommand(t, paddedConn)
	assertPayload(t, paddedSwitch, map[string]any{
		ws.PKCameraEnum: float64(0),
		ws.PKResolution: "1920x1080",
	})

	// A switch that names only the resolution leaves the frame rate at the
	// value the registration reported, and the trimmed pair surfaces on the
	// device once the device acks it: the live session never holds a parameter
	// it was not validated against.
	sendDeviceAck(t, paddedConn, paddedSwitch.ID, true)
	waitForDetail(t, m, padded, `"resolution":"1920x1080"`, `"fps":30`)

	// A failed acknowledgement is not applied: the camera keeps the parameters
	// it had.
	m.expectLive(http.MethodPost, "/api/devices/"+padded+"/camera/switch", `{"camera_enum":0,"resolution":"1280x720"}`, m.token(operator),
		http.StatusAccepted, `"camera_enum":0`)
	failedSwitch := readDeviceCommand(t, paddedConn)
	assertPayload(t, failedSwitch, map[string]any{
		ws.PKCameraEnum: float64(0),
		ws.PKResolution: "1280x720",
	})
	sendDeviceAck(t, paddedConn, failedSwitch.ID, false)
	time.Sleep(300 * time.Millisecond)
	m.expectLive(http.MethodGet, "/api/devices/"+padded+"/", "", m.token(operator), http.StatusOK,
		`"resolution":"1920x1080"`, `"fps":30`)
}

// expectLive issues one request over the listener and asserts its status and
// every expected fragment of its body.
func (m *matrix) expectLive(method, path, body, token string, status int, contains ...string) []byte {
	m.t.Helper()
	got, payload := m.liveCall(method, path, body, token)
	if got != status {
		m.t.Fatalf("%s %s status = %d, want %d (body %s)", method, path, got, status, payload)
	}
	for _, want := range contains {
		if !strings.Contains(string(payload), want) && !strings.Contains(causeOf(m.t, string(payload)), want) {
			m.t.Fatalf("%s %s body %s does not name %s", method, path, payload, want)
		}
	}
	return payload
}

// liveCall issues one request over the listener the WebSocket shares.
func (m *matrix) liveCall(method, path, body, token string) (int, []byte) {
	m.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, m.listen()+path, reader)
	if err != nil {
		m.t.Fatalf("build %s %s: %v", method, path, err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		m.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		m.t.Fatalf("read %s %s: %v", method, path, err)
	}
	m.t.Logf("%-24s %-6s %-44s -> %d %s", "device", method, path, response.StatusCode, truncate(string(payload), 120))
	return response.StatusCode, payload
}

// sendDeviceAck acknowledges commandID as the device would: the server applies
// a switch_camera only when the acknowledgement carries ok: true.
func sendDeviceAck(t *testing.T, conn *websocket.Conn, commandID string, ok bool) {
	t.Helper()
	raw, err := json.Marshal(ws.Message{
		Channel: ws.ChannelControl,
		Type:    ws.ControlAck,
		ID:      commandID,
		Payload: map[string]any{ws.PKOk: ok},
	})
	if err != nil {
		t.Fatalf("encode ack: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("write ack: %v", err)
	}
	t.Logf("%-24s %-6s %-44s -> ack ok=%v", "device sent", "-", commandID, ok)
}

// waitForDetail polls the device detail until it contains every fragment, so an
// acknowledgement applied asynchronously is visible before it is asserted.
func waitForDetail(t *testing.T, m *matrix, deviceID string, contains ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last []byte
	for time.Now().Before(deadline) {
		status, body := m.liveCall(http.MethodGet, "/api/devices/"+deviceID+"/", "", m.token(operator))
		last = body
		if status == http.StatusOK {
			matched := true
			for _, want := range contains {
				if !strings.Contains(string(body), want) {
					matched = false
					break
				}
			}
			if matched {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("device detail %s never named %v", last, contains)
}

// readDeviceCommand reads the next control command the server sends the device.
func readDeviceCommand(t *testing.T, conn *websocket.Conn) ws.Message {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read device command: %v", err)
	}
	var message ws.Message
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatalf("decode device command %q: %v", raw, err)
	}
	t.Logf("%-24s %-6s %-44s -> %s %s", "device received", "-", "-", message.Type, truncate(string(raw), 120))
	return message
}

// assertPayload checks that a command carries exactly the expected payload.
func assertPayload(t *testing.T, message ws.Message, want map[string]any) {
	t.Helper()
	if len(message.Payload) != len(want) {
		t.Fatalf("%s payload = %v, want %v", message.Type, message.Payload, want)
	}
	for key, value := range want {
		if message.Payload[key] != value {
			t.Fatalf("%s payload[%s] = %v, want %v", message.Type, key, message.Payload[key], value)
		}
	}
}

// sendDeviceFrame pushes one binary media frame as a device would.
func (m *matrix) sendDeviceFrame(t *testing.T, conn *websocket.Conn, channel, mediaType string, payload map[string]any, data []byte) {
	t.Helper()
	raw, err := ws.EncodeBinary(ws.Message{Channel: channel, Type: mediaType, Payload: payload}, data)
	if err != nil {
		t.Fatalf("encode %s frame: %v", mediaType, err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, raw); err != nil {
		t.Fatalf("write %s frame: %v", mediaType, err)
	}
}

// photos lists the device's stored photos.
func (m *matrix) photos(deviceID string) []map[string]any {
	m.t.Helper()
	_, payload := m.liveCall(http.MethodGet, "/api/devices/"+deviceID+"/photos", "", m.token(operator))
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(payload, &list); err != nil {
		m.t.Fatalf("decode photo list %s: %v", payload, err)
	}
	return list.Items
}

// waitForPhotos polls the device's photo list until it holds want items, so a
// frame the device pushed is visible before it is asserted.
func (m *matrix) waitForPhotos(deviceID string, want int) []map[string]any {
	m.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		photos := m.photos(deviceID)
		if len(photos) >= want {
			return photos
		}
		if time.Now().After(deadline) {
			m.t.Fatalf("photo list holds %d items, want %d", len(photos), want)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
