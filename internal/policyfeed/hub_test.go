package policyfeed

import (
	"log/slog"
	"testing"
	"time"
)

// newTestHub builds a Hub without binding a real LISTEN connection.
// Tests exercise the in-process dispatch path; the Postgres round-trip
// is covered by the integration tag (build tag `integration`) where a
// real database is available.
func newTestHub() *Hub {
	return &Hub{
		logger: slog.Default(),
		subs:   make(map[*subscriber]struct{}),
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// TestDispatch_AgentScoped pins the slice-15 invariant: a notification
// for agent-A reaches only the subscription bound to agent-A, even
// among same-org subscribers. Without this, two agents in the same org
// would receive each other's policy update messages and pull the wrong
// policy.
func TestDispatch_AgentScoped(t *testing.T) {
	h := newTestHub()

	subA := h.Subscribe("org-A", "agent-1")
	defer subA.Close()
	subB := h.Subscribe("org-A", "agent-2")
	defer subB.Close()

	h.dispatch(Notification{OrgID: "org-A", AgentName: "agent-1", PolicyHash: "abc"})

	select {
	case got := <-subA.C:
		if got.AgentName != "agent-1" {
			t.Errorf("agent-1 sub got %q", got.AgentName)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("agent-1 subscriber missed its notification")
	}

	select {
	case got := <-subB.C:
		t.Fatalf("agent-2 subscriber saw a cross-agent notification: %+v", got)
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

// TestDispatch_OrgScoped same-name-different-org isolation — slice-15
// supports the case where two orgs run agents with the same human
// name (org-A's "code-assist" must not see org-B's "code-assist"
// notification).
func TestDispatch_OrgScoped(t *testing.T) {
	h := newTestHub()

	subA := h.Subscribe("org-A", "code-assist")
	defer subA.Close()
	subB := h.Subscribe("org-B", "code-assist")
	defer subB.Close()

	h.dispatch(Notification{OrgID: "org-A", AgentName: "code-assist", PolicyHash: "abc"})

	select {
	case got := <-subA.C:
		if got.OrgID != "org-A" {
			t.Errorf("org-A sub got %q", got.OrgID)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("org-A subscriber missed its notification")
	}

	select {
	case got := <-subB.C:
		t.Fatalf("org-B subscriber saw a cross-tenant notification: %+v", got)
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

// TestDispatch_DropsOnSlowConsumer matches livefeed: a runtime that
// stops reading doesn't block the publisher. The dropped counter
// advances so operators can see the issue in /metrics.
func TestDispatch_DropsOnSlowConsumer(t *testing.T) {
	h := newTestHub()
	slow := h.Subscribe("org-A", "agent-1")
	// don't read

	overflow := subscriberBuffer + 4
	for i := 0; i < overflow; i++ {
		h.dispatch(Notification{OrgID: "org-A", AgentName: "agent-1", PolicyHash: "h"})
	}
	if h.Dropped() < 4 {
		t.Errorf("Dropped=%d, want at least 4 once buffer fills", h.Dropped())
	}
	slow.Close()
}

func TestSubscribers_CountAdjusts(t *testing.T) {
	h := newTestHub()
	if got := h.Subscribers(); got != 0 {
		t.Fatalf("starting count: %d", got)
	}
	s1 := h.Subscribe("org-A", "agent-1")
	s2 := h.Subscribe("org-A", "agent-2")
	if got := h.Subscribers(); got != 2 {
		t.Errorf("after 2 subscribes: %d, want 2", got)
	}
	s1.Close()
	if got := h.Subscribers(); got != 1 {
		t.Errorf("after 1 close: %d, want 1", got)
	}
	s2.Close()
	if got := h.Subscribers(); got != 0 {
		t.Errorf("after 2 closes: %d, want 0", got)
	}
}
