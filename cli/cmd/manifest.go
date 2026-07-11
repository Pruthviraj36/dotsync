package cmd

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/Pruthviraj36/dotsync/cli/identity"
)

// digestOf returns sha256(ciphertext) — what gets signed on push and
// re-checked on pull. Computed identically on both ends so the signature
// is meaningful without the server ever needing to understand it.
func digestOf(ciphertext []byte) [32]byte {
	return sha256.Sum256(ciphertext)
}

// revString renders a digest as a short, git-style display hash, e.g. "a4c9…21f".
func revString(digest [32]byte) string {
	full := hex.EncodeToString(digest[:])
	if len(full) < 7 {
		return full
	}
	return full[:4] + "…" + full[len(full)-3:]
}

// ensureIdentityAndSign loads (or creates) the local ed25519 identity and
// signs the digest of the given ciphertext. Returns the signature, whether
// a new identity was just created, and the identity's public key.
func ensureIdentityAndSign(ciphertext []byte) (signature []byte, created bool, pub ed25519.PublicKey, err error) {
	priv, pub, created, err := identity.Ensure()
	if err != nil {
		return nil, false, nil, fmt.Errorf("ed25519 identity: %w", err)
	}
	digest := digestOf(ciphertext)
	signature = ed25519.Sign(priv, digest[:])
	return signature, created, pub, nil
}

// humanSize renders a byte count the way `git`/`du` do — "2.1 KB", not "2150 B".
func humanSize(n int) string {
	const unit = 1024.0
	f := float64(n)
	if f < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB"}
	f /= unit
	for _, u := range units {
		if f < unit {
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= unit
	}
	return fmt.Sprintf("%.1f TB", f)
}

// verifySignature checks a push's signature against the pusher's public key.
// Three outcomes matter here:
//   - no signature and/or no pubkey on file (legacy push, or the pusher
//     hasn't generated an identity yet) → ok=false, err=nil: nothing to
//     verify, caller should say so rather than claim a check that didn't happen.
//   - signature present, pubkey present, and it checks out → ok=true, err=nil.
//   - signature present, pubkey present, and it does NOT check out → err is
//     set. Callers should treat this as a hard stop, not a warning: a
//     mismatched signature means either corruption or tampering.
func verifySignature(ciphertext, signature []byte, pubkeyHex string) (ok bool, err error) {
	if len(signature) == 0 || pubkeyHex == "" {
		return false, nil
	}
	pub, err := identity.FromHex(pubkeyHex)
	if err != nil {
		return false, fmt.Errorf("stored public key is invalid: %w", err)
	}
	digest := digestOf(ciphertext)
	if !ed25519.Verify(pub, digest[:], signature) {
		return false, fmt.Errorf("signature does not match — this data may have been tampered with, or was corrupted in transit")
	}
	return true, nil
}
