// Package update is the desktop auto-update core: a signed manifest feed,
// verified downloads, a bundle swap on macOS and a persisted state machine.
// It has no Wails dependency; the desktop app wires it in through Manager hooks.
package update

import (
	"crypto/ed25519"
	"encoding/base64"
)

// Embedded manifest-signing public keys (standard base64 of the 32-byte key).
// A manifest is accepted when ANY non-empty key verifies it, which allows
// rotation: ship a build that knows NextKey, then start signing with it.
const (
	CurrentKeyB64 = "yHu1GsL7nQq3yEIVQD113Uk05SYcccLJSx1u9AYM/C4="
	NextKeyB64    = "" // placeholder slot for rotation; skipped while empty
)

// PublicKeys returns the embedded public keys, skipping empty or invalid slots.
func PublicKeys() []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, s := range []string{CurrentKeyB64, NextKeyB64} {
		if k, err := ParsePublicKey(s); err == nil {
			out = append(out, k)
		}
	}
	return out
}

// ParsePublicKey decodes a base64 ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, ErrBadKey
	}
	return ed25519.PublicKey(b), nil
}
