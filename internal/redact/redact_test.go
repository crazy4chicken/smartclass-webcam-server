package redact

import (
	"strings"
	"testing"
)

// TestNewRedactsConfiguredSecrets pins what a sanitizer may and may not touch:
// every occurrence of a configured secret disappears, short or empty values are
// left alone so they cannot mangle unrelated text, and a secret that contains
// another secret leaves no fragment behind.
func TestNewRedactsConfiguredSecrets(t *testing.T) {
	sanitize := New(" hunter2-hunter2 ", "", "ab", "lengthy-token", "lengthy-token-value", "lengthy-token")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "replaces every occurrence",
			in:   "connect failed: password=hunter2-hunter2 host=db (password=hunter2-hunter2)",
			want: "connect failed: password=[redacted] host=db (password=[redacted])",
		},
		{
			name: "keeps text without secrets",
			in:   "device not found",
			want: "device not found",
		},
		{
			name: "ignores short and empty secrets",
			in:   "about absolute absence",
			want: "about absolute absence",
		},
		{
			name: "replaces the longer secret first",
			in:   "token=lengthy-token-value",
			want: "token=[redacted]",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitize(tc.in); got != tc.want {
				t.Fatalf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestTextToleratesNil verifies the helper used by callers that may not have a
// sanitizer configured.
func TestTextToleratesNil(t *testing.T) {
	if got := Text(nil, "unchanged"); got != "unchanged" {
		t.Fatalf("Text(nil, ...) = %q, want the input", got)
	}
	if got := Text(New("secret-value"), "secret-value"); got != "[redacted]" {
		t.Fatalf("Text with a sanitizer = %q, want the redacted value", got)
	}
}

// TestTrim pins the truncation marker so a cause cut short stays recognisable
// as a cut.
func TestTrim(t *testing.T) {
	long := strings.Repeat("x", 400)
	trimmed := Trim(long, 300)
	if !strings.HasSuffix(trimmed, "...") {
		t.Fatalf("Trim did not mark the cut: %q", trimmed[len(trimmed)-10:])
	}
	if len(trimmed) != 303 {
		t.Fatalf("Trim length = %d, want 303", len(trimmed))
	}
	if got := Trim("short", 300); got != "short" {
		t.Fatalf("Trim shortened a short value: %q", got)
	}
	if got := Trim(long, 0); got != long {
		t.Fatalf("Trim with a non-positive limit changed the value")
	}
}
