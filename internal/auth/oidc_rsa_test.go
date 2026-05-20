package auth

import (
	"crypto/rand"
	"crypto/rsa"
)

// rsaGen wraps rsa.GenerateKey so the test file doesn't import
// crypto/rand directly (kept tidy for readability).
func rsaGen() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 2048)
}
