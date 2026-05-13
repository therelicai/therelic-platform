package trace

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/therelicai/therelic-platform/internal/policy"
)

// ExtractActionEvents walks a gzipped NDJSON trace and yields one
// policy.ActionEvent per t:"action" line. Run-start, run-end,
// policy_reload, and any future event types are ignored — Simulate
// only cares about the action stream.
//
// Defensive parsing mirrors Parse() in parser.go: lines larger than
// MaxEventSize abort, the parser stops at MaxEvents to bound CPU, and
// any single malformed line is skipped rather than aborting the whole
// trace. The HMAC chain is not verified here — the integrity check
// already ran at upload time, and the simulator is read-only.
//
// The caller is responsible for size-limiting r upstream; we don't
// re-cap because the bytes we're consuming are already bucket-resident
// objects, not user uploads.
func ExtractActionEvents(r io.Reader) ([]policy.ActionEvent, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("trace: gzip: %w", err)
	}
	defer gz.Close()

	scanner := bufio.NewScanner(gz)
	scanner.Buffer(make([]byte, 0, 64*1024), MaxEventSize)

	events := make([]policy.ActionEvent, 0, 64)
	eventCount := 0

	for scanner.Scan() {
		if eventCount >= MaxEvents {
			break
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		eventCount++

		// Cheap type sniff before full decode — most lines are action
		// events, but skipping run/policy_reload lines this way saves
		// a json.Unmarshal into the ActionEvent struct.
		var typed struct {
			T string `json:"t"`
		}
		if err := json.Unmarshal(line, &typed); err != nil {
			continue
		}
		if typed.T != "action" {
			continue
		}

		var ev policy.ActionEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return events, ErrTooLarge
		}
		return events, fmt.Errorf("trace: scan: %w", err)
	}
	return events, nil
}
