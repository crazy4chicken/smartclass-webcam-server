package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
)

// deviceTokenPrefix marks device tokens so they are distinguishable from user
// access tokens.
const deviceTokenPrefix = "wdt_"

// deviceTokenBytes is the length of the random part of a device token.
const deviceTokenBytes = 32

// GenerateDeviceToken returns a new device token and the SHA-256 hash stored
// with the device. The raw token is handed to the caller exactly once.
func GenerateDeviceToken() (raw string, hash []byte) {
	buf := make([]byte, deviceTokenBytes)
	// crypto/rand.Read never returns an error and always fills buf.
	_, _ = rand.Read(buf)
	raw = deviceTokenPrefix + base64.RawURLEncoding.EncodeToString(buf)
	return raw, HashDeviceToken(raw)
}

// HashDeviceToken returns the SHA-256 hash of a device token.
func HashDeviceToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// DeviceTokenFromRequest extracts the device token from the Authorization
// header of a device-plane request. It reports false when the header carries no
// wdt_ bearer token.
func DeviceTokenFromRequest(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", false
	}
	token := parts[1]
	if !strings.HasPrefix(token, deviceTokenPrefix) {
		return "", false
	}
	return token, true
}
