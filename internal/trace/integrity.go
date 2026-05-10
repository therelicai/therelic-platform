package trace

// Mirror of the runtime's trace.IntegrityChain / VerifyChain logic.
// We keep a copy here rather than importing the runtime module
// because the platform is a separate deployable and a separate
// license (BSL 1.1 vs Apache); cross-module compilation would force
// either an awkward shared package or a vendoring relationship.
//
// The wire format is the canonical one the runtime emits via
// sealEventLine: every event line ends with either
//   ,"hmac":"<hex>"}    (objects with one or more other fields)
//   {"hmac":"<hex>"}    (the degenerate empty-object case)
// and the chain HMAC for line N covers the canonical line bytes
// (without the hmac suffix) plus the previous line's MAC.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

var (
	errMissingHMAC   = errors.New("event missing hmac field")
	errMACMismatch   = errors.New("hmac mismatch")
	hmacSuffix       = []byte(`,"hmac":"`)
	hmacSuffixEmpty  = []byte(`"hmac":"`)
	chainKeyContext  = []byte("relic-trace-chain-v1:")
)

// generateChainKey derives the per-run HMAC key the same way the
// runtime does. Keeping the context string in lockstep is critical —
// any drift here silently breaks verification across the whole fleet.
func generateChainKey(runID string, masterSecret []byte) []byte {
	mac := hmac.New(sha256.New, masterSecret)
	mac.Write(chainKeyContext)
	mac.Write([]byte(runID))
	return mac.Sum(nil)
}

// verifyChain walks the sealed event lines and checks the rolling MAC.
// Returns an error pointing at the first line that fails. The caller
// is expected to wrap it with ErrChainBroken before surfacing to the
// API layer.
func verifyChain(lines [][]byte, key []byte) error {
	var prevHMAC []byte
	for i, line := range lines {
		canonical, stored, err := splitSealedLine(line)
		if err != nil {
			return fmt.Errorf("event %d: %w", i, err)
		}
		mac := hmac.New(sha256.New, key)
		mac.Write(canonical)
		if prevHMAC != nil {
			mac.Write(prevHMAC)
		}
		computed := mac.Sum(nil)
		computedHex := hex.EncodeToString(computed)
		if !hmac.Equal([]byte(computedHex), []byte(stored)) {
			return fmt.Errorf("event %d: %w", i, errMACMismatch)
		}
		prevHMAC = computed
	}
	return nil
}

// splitSealedLine recovers the canonical bytes and the hex MAC from a
// sealed event line by byte-level inspection. We deliberately avoid
// JSON round-tripping (encoding/json sorts map keys, which would
// re-canonicalize the bytes the writer signed and break verification
// for any event whose source struct isn't in alphabetical order).
func splitSealedLine(line []byte) (canonical []byte, mac string, err error) {
	if len(line) < 2 || line[0] != '{' || line[len(line)-1] != '}' {
		return nil, "", fmt.Errorf("not a JSON object")
	}
	idx := bytes.LastIndex(line, hmacSuffix)
	leadingComma := true
	if idx < 0 {
		idx = bytes.LastIndex(line, hmacSuffixEmpty)
		if idx != 1 {
			return nil, "", errMissingHMAC
		}
		leadingComma = false
	}
	macStart := idx + len(hmacSuffix)
	if !leadingComma {
		macStart = idx + len(hmacSuffixEmpty)
	}
	macEnd := len(line) - 2
	if macEnd <= macStart {
		return nil, "", fmt.Errorf("malformed hmac field")
	}
	macBytes := line[macStart:macEnd]
	if _, err := hex.DecodeString(string(macBytes)); err != nil {
		return nil, "", fmt.Errorf("hmac is not hex: %w", err)
	}
	canonical = make([]byte, 0, idx+1)
	canonical = append(canonical, line[:idx]...)
	canonical = append(canonical, '}')
	return canonical, string(macBytes), nil
}
