package httpapi

import "testing"

// TestJPEGOrEmpty pins the photo rule: the protocol fixes still images to JPEG,
// so an omitted content type means the canonical one and anything else is
// discarded before a record is written.
func TestJPEGOrEmpty(t *testing.T) {
	cases := []struct {
		declared string
		want     string
		wantOK   bool
	}{
		{declared: "", want: "image/jpeg", wantOK: true},
		{declared: "image/jpeg", want: "image/jpeg", wantOK: true},
		{declared: " Image/JPEG ", want: "image/jpeg", wantOK: true},
		{declared: "image/png", want: "image/png", wantOK: false},
		{declared: "application/octet-stream", want: "application/octet-stream", wantOK: false},
	}
	for _, tc := range cases {
		got, ok := jpegOrEmpty(tc.declared)
		if got != tc.want || ok != tc.wantOK {
			t.Fatalf("jpegOrEmpty(%q) = (%q, %v), want (%q, %v)", tc.declared, got, ok, tc.want, tc.wantOK)
		}
	}
}
