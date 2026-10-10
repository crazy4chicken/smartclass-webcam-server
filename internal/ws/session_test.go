package ws

import (
	"testing"
	"time"
)

// TestRegistrationResolveSwitch pins the write-back a camera switch performs
// once the device acknowledges it: an ok ack retargets only the camera the
// command named, a failed ack and an unknown command id change nothing, and
// readers of the live session cannot reach into it.
func TestRegistrationResolveSwitch(t *testing.T) {
	registry := NewRegistry(time.Minute)
	reg, err := registry.Create("device-1", []CameraCapability{
		{
			CameraEnum:           0,
			Resolution:           "1920x1080",
			FPS:                  30,
			SupportedResolutions: []string{"1920x1080", "1280x720"},
			SupportedFramerates:  []int{30, 15},
			SupportedCodec:       []string{"h264"},
		},
		{CameraEnum: 1, Resolution: "1280x720", FPS: 15, SupportedCodec: []string{"mjpeg"}},
	})
	if err != nil {
		t.Fatalf("create registration: %v", err)
	}

	// A queued switch changes nothing on its own: the device has not confirmed
	// it yet.
	fps := 15
	reg.QueueSwitch("cmd-ok", 0, "1280x720", &fps)
	if got, _ := reg.Camera(0); got.Resolution != "1920x1080" || got.FPS != 30 {
		t.Fatalf("camera 0 = %+v after queueing, want it still at 1920x1080/30", got)
	}

	// The ok ack applies the pair, leaves the other camera and the capability
	// lists alone, and is consumed.
	if !reg.ResolveSwitch("cmd-ok", true) {
		t.Fatal("ResolveSwitch(cmd-ok, true) reported no such switch")
	}
	if got, ok := reg.Camera(0); !ok || got.Resolution != "1280x720" || got.FPS != 15 {
		t.Fatalf("camera 0 = %+v (ok %v), want 1280x720 at 15 fps", got, ok)
	}
	if got, _ := reg.Camera(1); got.Resolution != "1280x720" || got.FPS != 15 {
		t.Fatalf("camera 1 = %+v, want it left at its registered parameters", got)
	}
	if reg.ResolveSwitch("cmd-ok", true) {
		t.Fatal("ResolveSwitch accepted an already resolved command id")
	}
	got, _ := reg.Camera(0)
	if len(got.SupportedResolutions) != 2 || got.SupportedResolutions[0] != "1920x1080" {
		t.Fatalf("supported resolutions = %v, want the announced list", got.SupportedResolutions)
	}
	if len(got.SupportedFramerates) != 2 || got.SupportedFramerates[0] != 30 {
		t.Fatalf("supported framerates = %v, want the announced list", got.SupportedFramerates)
	}

	// A failed ack drops the pending switch without applying it, and a command
	// the server never queued is unknown to the registration.
	reg.QueueSwitch("cmd-failed", 0, "1920x1080", nil)
	if reg.ResolveSwitch("cmd-failed", false) {
		t.Fatal("ResolveSwitch applied a switch the device reported as failed")
	}
	if again, _ := reg.Camera(0); again.Resolution != "1280x720" || again.FPS != 15 {
		t.Fatalf("camera 0 = %+v after a failed ack, want 1280x720/15", again)
	}
	if reg.ResolveSwitch("cmd-unknown", true) {
		t.Fatal("ResolveSwitch accepted a command id that was never queued")
	}
	reg.QueueSwitch("cmd-discarded", 0, "1920x1080", nil)
	reg.DiscardSwitch("cmd-discarded")
	if reg.ResolveSwitch("cmd-discarded", true) {
		t.Fatal("ResolveSwitch accepted a discarded command id")
	}

	// A parameter the command left out keeps whatever the camera is at when the
	// ack lands.
	reg.QueueSwitch("cmd-resolution-only", 0, "1920x1080", nil)
	if !reg.ResolveSwitch("cmd-resolution-only", true) {
		t.Fatal("ResolveSwitch(cmd-resolution-only, true) reported no such switch")
	}
	if got, _ := reg.Camera(0); got.Resolution != "1920x1080" || got.FPS != 15 {
		t.Fatalf("camera 0 = %+v, want 1920x1080 at the kept 15 fps", got)
	}

	// The caller may reuse the fps pointer it passed: the registration holds a
	// copy.
	kept := 30
	reg.QueueSwitch("cmd-copy", 0, "", &kept)
	kept = 99
	if !reg.ResolveSwitch("cmd-copy", true) {
		t.Fatal("ResolveSwitch(cmd-copy, true) reported no such switch")
	}
	if got, _ := reg.Camera(0); got.FPS != 30 {
		t.Fatalf("camera 0 fps = %d, want the copied 30", got.FPS)
	}

	cameras := reg.Cameras()
	if len(cameras) != 2 {
		t.Fatalf("Cameras() returned %d cameras, want 2", len(cameras))
	}
	cameras[0].Resolution = "mutated"
	if again, _ := reg.Camera(0); again.Resolution != "1920x1080" {
		t.Fatalf("camera 0 = %q after a caller mutated the returned slice, want 1920x1080", again.Resolution)
	}
}
