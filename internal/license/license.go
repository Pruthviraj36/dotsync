// Package license implements dotsync's on-premise license keys.
//
// A license key is an ed25519-signed claim: base64(json claims) + "." +
// hex(signature). Verifying one only needs the PUBLIC key (PublicKeyHex
// below — safe to publish, it can check signatures but never create them).
// Minting one needs the PRIVATE key, which must never be committed to this
// repo — see cmd/licensegen for that half.
//
// This is the technical half of the on-premise licensing model. The legal
// half is the LICENSE file (Elastic License 2.0) at the repo root, which
// separately prohibits removing or circumventing this check — the two
// reinforce each other: this package makes casual self-hosting-without-
// paying not work out of the box, and the license terms make deliberately
// stripping this check out and recompiling a license violation, not just
// an inconvenience to route around.
package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PublicKeyHex is the vendor's public key. Empty until you generate a
// keypair — see cmd/licensegen's "genkeypair" command — and paste the
// public half in here. Until it's set, Verify always fails closed (every
// self-hosted deploy without DOTSYNC_HOSTED=true refuses to start), which
// is intentional: it forces this setup step to actually happen rather than
// silently accepting any license key.
const PublicKeyHex = ""

// Claims is what's actually signed. Deliberately has no expiry field —
// the on-premise purchase is a one-time perpetual license, not a
// subscription, so there's nothing to expire.
type Claims struct {
	Licensee string `json:"licensee"`
	IssuedAt string `json:"issued_at"`
	Type     string `json:"type"` // always "onpremise" today, but versioned in case that changes
}

// Issue signs a new license key. Only ever called from cmd/licensegen,
// run locally by whoever holds the private key — never at server runtime.
func Issue(priv ed25519.PrivateKey, licensee string) (string, error) {
	if licensee == "" {
		return "", fmt.Errorf("licensee is required")
	}
	claims := Claims{
		Licensee: licensee,
		IssuedAt: time.Now().UTC().Format(time.RFC3339),
		Type:     "onpremise",
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode claims: %w", err)
	}
	sig := ed25519.Sign(priv, payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(sig), nil
}

// Verify checks a license key's signature and returns the claims it makes.
// This is the only thing that runs at server startup — it never needs the
// private key, only PublicKeyHex above.
func Verify(licenseKey string) (*Claims, error) {
	if PublicKeyHex == "" {
		return nil, fmt.Errorf("license verification isn't configured on this build yet — internal/license.PublicKeyHex is empty (run: go run ./cmd/licensegen genkeypair)")
	}
	pubBytes, err := hex.DecodeString(PublicKeyHex)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("this build's embedded public key is invalid — check internal/license.PublicKeyHex")
	}
	pub := ed25519.PublicKey(pubBytes)

	parts := strings.SplitN(strings.TrimSpace(licenseKey), ".", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("malformed license key")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("malformed license key payload")
	}
	sig, err := hex.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("malformed license key signature")
	}
	if !ed25519.Verify(pub, payload, sig) {
		return nil, fmt.Errorf("license key signature is invalid — this key wasn't issued for this build, or has been altered")
	}

	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("malformed license claims")
	}
	if claims.Type != "onpremise" {
		return nil, fmt.Errorf("unexpected license type %q", claims.Type)
	}
	return &claims, nil
}
