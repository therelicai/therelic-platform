package livefeed

import (
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// newTestHub returns a Hub with a nil pgxpool. We never call Start
// here — the unit tests exercise the in-process dispatch path only.
// Postgres LISTEN/NOTIFY round-trips are covered by the integration
// suite (build tag `integration`) where a real database is available.
func newTestHub() *Hub {
	return &Hub{
		logger: slog.Default(),
		subs:   make(map[*subscriber]struct{}),
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
}

func TestFilter_Matches(t *testing.T) {
	ev := Event{
		OrgID:   "org-A",
		Type:    "action",
		Agent:   "code-assist",
		Verdict: "deny",
		Payload: json.RawMessage(`{"target":"exec_shell","method":"tool_call"}`),
	}
	cases := []struct {
		name string
		f    Filter
		want bool
	}{
		{"empty matches all", Filter{}, true},
		{"agent match", Filter{AgentName: "code-assist"}, true},
		{"agent miss", Filter{AgentName: "other"}, false},
		{"verdict match", Filter{Verdict: "deny"}, true},
		{"verdict miss", Filter{Verdict: "allow"}, false},
		{"tool match", Filter{Tool: "exec_shell"}, true},
		{"tool miss", Filter{Tool: "web_search"}, false},
		{"all match", Filter{AgentName: "code-assist", Verdict: "deny", Tool: "exec_shell"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.Matches(ev); got != tc.want {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

// TestDispatch_OrgScoped pins the cross-tenant isolation invariant.
// An event published for org-A must never reach a subscriber on
// org-B even when filters would otherwise match. This is the
// non-negotiable property the live feed promises every tenant.
func TestDispatch_OrgScoped(t *testing.T) {
	h := newTestHub()

	subA := h.Subscribe("org-A", Filter{})
	defer subA.Close()
	subB := h.Subscribe("org-B", Filter{})
	defer subB.Close()

	ev := Event{
		OrgID:   "org-A",
		Type:    "action",
		Agent:   "any",
		Verdict: "allow",
		Payload: json.RawMessage(`{"target":"web_search"}`),
	}
	h.dispatch(ev)

	select {
	case got := <-subA.C:
		if got.OrgID != "org-A" {
			t.Errorf("org-A subscriber got %q", got.OrgID)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("org-A subscriber missed its own event")
	}

	select {
	case ev := <-subB.C:
		t.Fatalf("org-B subscriber received cross-tenant event: %+v", ev)
	case <-time.After(50 * time.Millisecond):
		// expected — no event for org-B
	}
}

// TestDispatch_FiltersWithinOrg verifies same-org subscribers see
// only events matching their filter. The "every-agent" Live view
// uses an empty filter; per-agent panes use AgentName.
func TestDispatch_FiltersWithinOrg(t *testing.T) {
	h := newTestHub()

	all := h.Subscribe("org-A", Filter{})
	defer all.Close()
	onlyCode := h.Subscribe("org-A", Filter{AgentName: "code-assist"})
	defer onlyCode.Close()

	h.dispatch(Event{
		OrgID:   "org-A",
		Type:    "intent",
		Agent:   "code-assist",
		Payload: json.RawMessage(`{"target":"web_search"}`),
	})
	h.dispatch(Event{
		OrgID:   "org-A",
		Type:    "intent",
		Agent:   "other-agent",
		Payload: json.RawMessage(`{"target":"web_search"}`),
	})

	drainCount := func(c <-chan Event) int {
		count := 0
		deadline := time.Now().Add(150 * time.Millisecond)
		for time.Now().Before(deadline) {
			select {
			case <-c:
				count++
			case <-time.After(20 * time.Millisecond):
			}
		}
		return count
	}
	if n := drainCount(all.C); n != 2 {
		t.Errorf("'all' subscriber: got %d events, want 2", n)
	}
	if n := drainCount(onlyCode.C); n != 1 {
		t.Errorf("'onlyCode' subscriber: got %d events, want 1", n)
	}
}

// TestDispatch_DropsOnSlowConsumer proves the bounded-channel
// contract: a subscriber that doesn't read keeps the publisher
// unblocked. The dropped counter advances so operators can see the
// problem in /metrics.
func TestDispatch_DropsOnSlowConsumer(t *testing.T) {
	h := newTestHub()
	slow := h.Subscribe("org-A", Filter{})
	// deliberately don't read

	// Push more than subscriberBuffer events. The first ~128 land in
	// the channel; the rest drop and bump the counter.
	overflow := subscriberBuffer + 5
	for i := 0; i < overflow; i++ {
		h.dispatch(Event{OrgID: "org-A", Type: "intent", Payload: json.RawMessage(`{}`)})
	}
	if h.Dropped() < 5 {
		t.Errorf("Dropped=%d, want at least 5 once buffer fills", h.Dropped())
	}
	// Close without reading: the channel close path must not race.
	slow.Close()
}

// TestSubscribers_CountAdjusts confirms Subscribers() returns the
// live count and that Close removes the entry.
func TestSubscribers_CountAdjusts(t *testing.T) {
	h := newTestHub()
	if got := h.Subscribers(); got != 0 {
		t.Fatalf("starting count: %d", got)
	}
	subs := make([]*Subscription, 3)
	for i := range subs {
		subs[i] = h.Subscribe("org-A", Filter{})
	}
	if got := h.Subscribers(); got != 3 {
		t.Errorf("after Subscribe x3: %d, want 3", got)
	}
	subs[1].Close()
	if got := h.Subscribers(); got != 2 {
		t.Errorf("after Close: %d, want 2", got)
	}
	subs[0].Close()
	subs[2].Close()
	if got := h.Subscribers(); got != 0 {
		t.Errorf("after all Close: %d, want 0", got)
	}
	// Idempotent
	subs[0].Close()
}

// Race-free fixture used in benchmarks to confirm dispatch is
// allocation-light when filters are empty (the common case). Not run
// by `go test` by default; kept here so the benchmark lives next to
// the code it measures.
var _ sync.Mutex
