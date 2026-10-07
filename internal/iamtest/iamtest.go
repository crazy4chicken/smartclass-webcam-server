// Package iamtest provides a teamusers stand-in for tests: it publishes the
// JWKS document an SDK verifier fetches, mints EdDSA access tokens from any
// claim set, and answers v2 permission snapshots with the grants a test
// configures.
package iamtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// PublishedKid is the key ID of the key the stub publishes in its JWKS
// document. Tokens minted with any other kid name a key the stub does not
// publish.
const PublishedKid = "key-1"

// Stub is a running teamusers stand-in.
type Stub struct {
	server   *httptest.Server
	key      ed25519.PrivateKey
	foreign  ed25519.PrivateKey
	audience string

	mu       sync.Mutex
	snapshot map[string]snapshot
}

// snapshot is the v2 permission document the stub answers for one subject.
type snapshot struct {
	permVer int64
	grants  []string
}

// New starts a stub that expects tokens for the supplied audience. The stub is
// closed when the test ends.
func New(tb testing.TB, audience string) *Stub {
	tb.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatalf("iamtest: generate key: %v", err)
	}
	_, foreign, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatalf("iamtest: generate foreign key: %v", err)
	}
	stub := &Stub{key: key, foreign: foreign, audience: audience, snapshot: map[string]snapshot{}}
	stub.server = httptest.NewServer(http.HandlerFunc(stub.serve))
	tb.Cleanup(stub.server.Close)
	return stub
}

// URL is the issuer base URL the verifier and permission client target.
func (s *Stub) URL() string { return s.server.URL }

// Audience is the token audience the stub mints.
func (s *Stub) Audience() string { return s.audience }

// SetGrants replaces the permission snapshot the stub answers for subject: the
// grants are raw v2 grant objects, e.g. {"key":"cam:read:any"}.
func (s *Stub) SetGrants(subject string, permVer int64, grants ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot[subject] = snapshot{permVer: permVer, grants: grants}
}

// Claims returns a complete, valid claim set for subject.
func (s *Stub) Claims(subject, team string, permVer int64) map[string]any {
	claims := map[string]any{
		"iss":      "teamusers",
		"aud":      s.audience,
		"sub":      subject,
		"kind":     "user",
		"perm_ver": permVer,
		"exp":      time.Now().Add(time.Hour).Unix(),
	}
	if team != "" {
		claims["team"] = team
	}
	return claims
}

// Token signs claims with the stub's published key under kid.
func (s *Stub) Token(kid string, claims map[string]any) string {
	return s.sign(s.key, kid, claims)
}

// ForeignToken signs claims with a key the stub never publishes.
func (s *Stub) ForeignToken(kid string, claims map[string]any) string {
	return s.sign(s.foreign, kid, claims)
}

func (s *Stub) sign(key ed25519.PrivateKey, kid string, claims map[string]any) string {
	header := map[string]any{"alg": "EdDSA", "typ": "JWT"}
	if kid != "" {
		header["kid"] = kid
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		panic(err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	signingInput := encode(headerJSON) + "." + encode(claimsJSON)
	return signingInput + "." + encode(ed25519.Sign(key, []byte(signingInput)))
}

func (s *Stub) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/.well-known/jwks.json"):
		s.serveJWKS(w)
	case strings.HasPrefix(r.URL.Path, "/authz/permissions/"):
		s.serveSnapshot(w, r)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"unknown stub route"}`))
	}
}

func (s *Stub) serveJWKS(w http.ResponseWriter) {
	document := map[string]any{"keys": []any{map[string]any{
		"kty": "OKP",
		"crv": "Ed25519",
		"x":   encode(s.key.Public().(ed25519.PublicKey)),
		"kid": PublishedKid,
		"alg": "EdDSA",
		"use": "sig",
	}}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(document)
}

func (s *Stub) serveSnapshot(w http.ResponseWriter, r *http.Request) {
	subject := strings.TrimPrefix(r.URL.Path, "/authz/permissions/")
	s.mu.Lock()
	entry, ok := s.snapshot[subject]
	s.mu.Unlock()
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no snapshot for subject"}`))
		return
	}
	grants := entry.grants
	if grants == nil {
		grants = []string{}
	}
	body := map[string]any{
		"version":  2,
		"user_id":  subject,
		"perm_ver": entry.permVer,
		"grants":   json.RawMessage("[" + strings.Join(grants, ",") + "]"),
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		panic(fmt.Sprintf("iamtest: encode snapshot: %v", err))
	}
}

// encode renders bytes as unpadded base64url.
func encode(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }
