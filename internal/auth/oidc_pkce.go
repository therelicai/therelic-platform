package auth

import "crypto/sha256"

// sha256Sum returns SHA-256(b) as a byte slice. Kept in a separate
// file so the test file can stub it out if needed; also avoids
// pulling crypto/sha256 into oidc.go (which already imports a lot).
func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}
