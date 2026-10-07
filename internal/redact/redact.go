// Package redact removes configured secrets from text that is about to leave
// the service, so a problem detail can carry the underlying cause without
// carrying credentials with it.
package redact

import (
	"sort"
	"strings"
)

// New returns a sanitizer that replaces every occurrence of the supplied
// secrets with "[redacted]". Empty values and values shorter than four
// characters are ignored, so a stray secret cannot mangle unrelated text, and
// longer secrets are replaced first so a secret that contains another one never
// leaves a fragment behind. A nil sanitizer is a no-op.
func New(secrets ...string) func(string) string {
	kept := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if len(secret) < 4 {
			continue
		}
		kept = append(kept, secret)
	}
	kept = dedupe(kept)
	sort.Slice(kept, func(i, j int) bool {
		if len(kept[i]) != len(kept[j]) {
			return len(kept[i]) > len(kept[j])
		}
		return kept[i] < kept[j]
	})
	if len(kept) == 0 {
		return func(text string) string { return text }
	}
	return func(text string) string {
		for _, secret := range kept {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
		return text
	}
}

// Text applies a sanitizer to a piece of text, tolerating a nil sanitizer.
func Text(sanitize func(string) string, text string) string {
	if sanitize == nil {
		return text
	}
	return sanitize(text)
}

// Trim shortens text to limit bytes, marking the cut.
func Trim(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

func dedupe(values []string) []string {
	if len(values) < 2 {
		return values
	}
	kept := values[:0]
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		kept = append(kept, value)
	}
	return kept
}
