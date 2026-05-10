package trace

// Verification tests for ParseAndVerify. These pair with the runtime's
// sealEventLine logic — the bytes here have to exactly match what the
// runtime produces, otherwise the test passes but the real upload
// path silently fails. We construct the sealed lines using the same
// suffix splice (`,"hmac":"…"`}) that the runtime's writer emits.

import (
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// sealLine mirrors the runtime's sealEventLine. Kept here verbatim so
// the parser test exercises real bytes, not a synthetic stand-in.
func sealLine(prevHMAC []byte, key []byte, body string) (line []byte, mac []byte) {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(body))
	if prevHMAC != nil {
		m.Write(prevHMAC)
	}
	mac = m.Sum(nil)
	macHex := hex.EncodeToString(mac)

	// Mirror sealEventLine's structural rule: events with at least one
	// field need a leading comma before the hmac key.
	switch body {
	case "{}":
		line = []byte(`{"hmac":"` + macHex + `"}`)
	default:
		line = []byte(body[:len(body)-1] + `,"hmac":"` + macHex + `"}`)
	}
	return line, mac
}

// buildSealedTrace produces a gzipped NDJSON trace whose events are
// HMAC-chained with the given per-run key. The first event is a
// run-start, the rest are actions, and the chain rolls correctly.
func buildSealedTrace(t *testing.T, runID string, key []byte, eventBodies []string) []byte {
	t.Helper()
	var lines [][]byte
	var prev []byte
	for _, body := range eventBodies {
		line, mac := sealLine(prev, key, body)
		lines = append(lines, line)
		prev = mac
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	for _, l := range lines {
		zw.Write(l)
		zw.Write([]byte("\n"))
	}
	zw.Close()
	return gz.Bytes()
}

func TestParseAndVerify_HappyPath(t *testing.T) {
	master := []byte("master-secret-with-enough-entropy-please")
	runID := "01HXTESTRUN"
	key := generateChainKey(runID, master)

	body := buildSealedTrace(t, runID, key, []string{
		`{"v":1,"t":"run","run":"` + runID + `","agent":"a","status":"start"}`,
		`{"v":1,"t":"action","run":"` + runID + `","auth":"allow"}`,
		`{"v":1,"t":"action","run":"` + runID + `","auth":"deny"}`,
	})

	s, err := ParseAndVerify(bytes.NewReader(body), master, false)
	if err != nil {
		t.Fatalf("ParseAndVerify: %v", err)
	}
	if !s.HasIntegrityChain {
		t.Error("HasIntegrityChain should be true")
	}
	if !s.ChainVerified {
		t.Error("ChainVerified should be true on happy path")
	}
}

func TestParseAndVerify_DetectsTampering(t *testing.T) {
	master := []byte("master-secret-with-enough-entropy-please")
	runID := "01HXTAMPER"
	key := generateChainKey(runID, master)

	body := buildSealedTrace(t, runID, key, []string{
		`{"v":1,"t":"run","run":"` + runID + `","agent":"a","status":"start"}`,
		`{"v":1,"t":"action","run":"` + runID + `","auth":"allow"}`,
	})

	// Flip "allow" → "deny" in the gzipped payload. We have to unzip,
	// edit, and rezip to land the change in the actual bytes the
	// parser sees.
	dec, _ := gzip.NewReader(bytes.NewReader(body))
	plain, _ := readAll(dec)
	plain = []byte(strings.Replace(string(plain), `"allow"`, `"deny"`, 1))
	var reZip bytes.Buffer
	zw := gzip.NewWriter(&reZip)
	zw.Write(plain)
	zw.Close()

	s, err := ParseAndVerify(bytes.NewReader(reZip.Bytes()), master, false)
	if !errors.Is(err, ErrChainBroken) {
		t.Fatalf("expected ErrChainBroken, got %v", err)
	}
	if s == nil || !s.HasIntegrityChain {
		t.Error("HasIntegrityChain (presence claim) should still be true even when verification fails")
	}
	if s != nil && s.ChainVerified {
		t.Error("ChainVerified must NOT be true after tampering")
	}
}

func TestParseAndVerify_NoMasterSecretSkipsVerification(t *testing.T) {
	body := gzipNDJSON(
		`{"v":1,"t":"run","run":"r1","agent":"a","status":"start"}`,
		`{"v":1,"t":"action","run":"r1","auth":"allow","hmac":"deadbeef"}`,
	)
	s, err := ParseAndVerify(bytes.NewReader(body), nil, false)
	if err != nil {
		t.Fatalf("ParseAndVerify: %v", err)
	}
	if !s.HasIntegrityChain {
		t.Error("HasIntegrityChain should reflect presence claim")
	}
	if s.ChainVerified {
		t.Error("ChainVerified must be false when no master secret")
	}
}

func TestParseAndVerify_RequireChainRejectsUnsealed(t *testing.T) {
	master := []byte("master-secret-with-enough-entropy-please")
	body := gzipNDJSON(
		`{"v":1,"t":"run","run":"r1","agent":"a","status":"start"}`,
		`{"v":1,"t":"action","run":"r1","auth":"allow"}`,
	)
	_, err := ParseAndVerify(bytes.NewReader(body), master, true)
	if !errors.Is(err, ErrChainExpected) {
		t.Fatalf("expected ErrChainExpected, got %v", err)
	}
}

func TestParseAndVerify_RequireChainAllowsSealed(t *testing.T) {
	master := []byte("master-secret-with-enough-entropy-please")
	runID := "01HXREQUIRE"
	key := generateChainKey(runID, master)

	body := buildSealedTrace(t, runID, key, []string{
		`{"v":1,"t":"run","run":"` + runID + `","agent":"a","status":"start"}`,
		`{"v":1,"t":"action","run":"` + runID + `","auth":"allow"}`,
	})
	s, err := ParseAndVerify(bytes.NewReader(body), master, true)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !s.ChainVerified {
		t.Error("ChainVerified should be true")
	}
}

func TestParseAndVerify_WrongMasterSecret(t *testing.T) {
	right := []byte("master-secret-with-enough-entropy-please")
	wrong := []byte("a-totally-different-master-secret-here")
	runID := "01HXWRONGKEY"
	key := generateChainKey(runID, right)

	body := buildSealedTrace(t, runID, key, []string{
		`{"v":1,"t":"run","run":"` + runID + `","agent":"a","status":"start"}`,
		`{"v":1,"t":"action","run":"` + runID + `","auth":"allow"}`,
	})
	_, err := ParseAndVerify(bytes.NewReader(body), wrong, false)
	if !errors.Is(err, ErrChainBroken) {
		t.Fatalf("expected ErrChainBroken, got %v", err)
	}
}

// readAll avoids a direct io.ReadAll import; the test file already
// pulls bytes via gzip and we want to stay focused on parse semantics.
func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return out, nil
			}
			return out, err
		}
	}
}
