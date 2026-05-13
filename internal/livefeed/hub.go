// Package livefeed is the slice 14 pub/sub backplane that powers the
// "Live view" in therelic-app. The runtime POSTs sealed IntentEvent /
// ActionEvent lines to the platform's /v1/intents handler, which calls
// Hub.Publish; subscribers (one per open SSE connection on
// /v1/orgs/:id/live) receive filtered events through Hub.Subscribe.
//
// Implementation choices for slice 14:
//
//   - One Postgres channel ("relic_live") for the whole platform.
//     NOTIFY payload carries the org_id; subscribers filter by their
//     authenticated org. This keeps the channel count to one regardless
//     of tenant cardinality.
//
//   - LISTEN is held by exactly one goroutine per Hub instance,
//     binding a dedicated pgx connection out of the pool. Subscriber
//     fanout happens in-process: cheap, no extra infrastructure.
//
//   - Subscribers receive on a buffered channel; when their channel is
//     full (slow consumer), the event is dropped and a counter is
//     bumped. The runtime's batch trace push at end-of-run remains the
//     durable record.
//
//   - Postgres NOTIFY caps payloads at 8000 bytes. The intent events
//     the runtime emits are post-redaction and typically <1 KiB; we
//     refuse oversized payloads (the publisher returns an error so
//     /v1/intents can respond 413 to a malformed upload), keeping the
//     hub simple. Truncation is a slice-15 concern if it ever bites.
package livefeed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChannelName is the Postgres LISTEN channel the hub uses. Exported
// because tests need it for direct NOTIFY round-trips, and operators
// may want it for psql debugging.
const ChannelName = "relic_live"

// maxNotifyPayload mirrors the Postgres NOTIFY payload cap (8000
// bytes). We measure the raw JSON envelope, not just the event, so the
// org_id prefix is included in the budget.
const maxNotifyPayload = 8000

// subscriberBuffer is the per-subscriber channel depth. Three seconds
// of steady-state human-rate agent traffic fits comfortably under
// this. Slow consumers drop oldest, the SSE writer logs a warning.
const subscriberBuffer = 128

// Event is the normalized envelope a subscriber sees. The Payload
// field is the runtime's sealed event JSON exactly as it left the
// .trtrace file, so the dashboard sees what the integrity verifier
// would later confirm. Type / Agent / Verdict are convenience fields
// extracted at publish time so subscribers can filter without
// re-parsing the payload.
type Event struct {
	OrgID   string          `json:"org_id"`
	Type    string          `json:"type"` // "intent" | "action"
	Agent   string          `json:"agent,omitempty"`
	Run     string          `json:"run,omitempty"`
	Verdict string          `json:"verdict,omitempty"` // action events only
	Payload json.RawMessage `json:"payload"`
}

// Filter is the slice 14 selector grammar — single agent name plus
// optional tool and verdict. Empty strings mean "match all." Slice 15
// extends this with label-match selectors; the wire shape on
// /v1/orgs/:id/live is forward-compatible because filters are passed
// as query parameters and unknown params are ignored.
type Filter struct {
	AgentName string
	Tool      string
	Verdict   string
}

// Matches reports whether ev passes f. Always true for events whose
// org doesn't match the subscriber — but the hub enforces org
// scoping at dispatch time, so callers never see foreign org events.
func (f Filter) Matches(ev Event) bool {
	if f.AgentName != "" && ev.Agent != f.AgentName {
		return false
	}
	if f.Verdict != "" && ev.Verdict != f.Verdict {
		return false
	}
	if f.Tool != "" {
		// Extract target/tool from payload lazily — most filters are
		// empty so we keep the common path allocation-free.
		var probe struct {
			Target string `json:"target"`
		}
		if err := json.Unmarshal(ev.Payload, &probe); err != nil || probe.Target != f.Tool {
			return false
		}
	}
	return true
}

// Subscription is a per-connection handle. The owner reads events
// from C and calls Close exactly once when its consumer disconnects.
type Subscription struct {
	OrgID   string
	Filter  Filter
	C       <-chan Event
	closeFn func()
}

// Close detaches the subscription. Idempotent.
func (s *Subscription) Close() {
	if s == nil || s.closeFn == nil {
		return
	}
	s.closeFn()
}

// Hub is the in-process fanout for live events. Construct with New,
// Start the LISTEN loop, Close on shutdown.
type Hub struct {
	pool   *pgxpool.Pool
	logger *slog.Logger

	mu     sync.RWMutex
	subs   map[*subscriber]struct{}
	closed bool

	stopCh  chan struct{}
	done    chan struct{}

	// dropped counts events the publisher couldn't enqueue to at
	// least one subscriber's channel because the buffer was full.
	// Exposed via Dropped() for observability and tests.
	dropped atomic.Uint64
}

type subscriber struct {
	orgID  string
	filter Filter
	ch     chan Event
}

// New constructs a Hub against an existing pgxpool. The hub does not
// take ownership of the pool — the caller manages its lifecycle.
func New(pool *pgxpool.Pool, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		pool:   pool,
		logger: logger,
		subs:   make(map[*subscriber]struct{}),
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
	}
}

// Start binds a dedicated connection from the pool and begins
// listening. Returns immediately after the LISTEN is established;
// fanout continues in a goroutine until Close is called.
//
// Start MUST be called exactly once per Hub. A failure to LISTEN is
// fatal — without it the hub can't deliver events from other API
// replicas, and the live view would silently miss cross-replica
// publishes. Callers should treat this like a DB ping failure at
// boot.
func (h *Hub) Start(ctx context.Context) error {
	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("livefeed: acquire conn: %w", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("LISTEN %s", pgx.Identifier{ChannelName}.Sanitize())); err != nil {
		conn.Release()
		return fmt.Errorf("livefeed: LISTEN: %w", err)
	}
	go h.run(conn)
	return nil
}

// run is the dispatch loop. It reads notifications from Postgres and
// fans them out to matching subscribers. On connection error it
// releases the conn and exits — the API server fails health checks
// rather than silently degrading the live feed.
func (h *Hub) run(conn *pgxpool.Conn) {
	defer close(h.done)
	defer conn.Release()

	ctx := context.Background()
	for {
		// Use a short context per WaitForNotification so Close can
		// interrupt cleanly. pgx wraps the underlying pgconn call;
		// when the deadline trips we loop and check stopCh.
		notifyCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		n, err := conn.Conn().WaitForNotification(notifyCtx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				select {
				case <-h.stopCh:
					return
				default:
					continue
				}
			}
			h.logger.Error("livefeed: notification wait failed", "error", err)
			return
		}
		var ev Event
		if err := json.Unmarshal([]byte(n.Payload), &ev); err != nil {
			h.logger.Warn("livefeed: malformed notify payload", "error", err)
			continue
		}
		h.dispatch(ev)
	}
}

// dispatch fans an event out to matching subscribers. Slow consumers
// drop the event and bump the counter — the live view is best-effort
// and the durable trace upload at end-of-run is the source of truth.
func (h *Hub) dispatch(ev Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs {
		if s.orgID != ev.OrgID {
			continue
		}
		if !s.filter.Matches(ev) {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			h.dropped.Add(1)
		}
	}
}

// Publish encodes the event and issues a Postgres NOTIFY on the
// shared channel. Other replicas with active LISTENers (and this
// process's own listener) will receive the event.
//
// Publish is synchronous — it returns once Postgres has accepted the
// NOTIFY. Slice 14's intent stream is human-rate, so the cost of one
// round-trip per intent is acceptable. A future slice can introduce
// batching if write throughput becomes a constraint.
func (h *Hub) Publish(ctx context.Context, ev Event) error {
	if ev.OrgID == "" {
		return errors.New("livefeed: org_id required")
	}
	if ev.Type != "intent" && ev.Type != "action" {
		return fmt.Errorf("livefeed: unsupported event type %q", ev.Type)
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("livefeed: marshal: %w", err)
	}
	if len(payload) > maxNotifyPayload {
		return fmt.Errorf("livefeed: payload %d bytes exceeds NOTIFY cap (%d)", len(payload), maxNotifyPayload)
	}
	// pg_notify(channel, payload) is the parameterized form; the
	// "NOTIFY channel, 'payload'" syntax doesn't support placeholders,
	// which would force string concatenation and a quote-escape mess.
	_, err = h.pool.Exec(ctx, "SELECT pg_notify($1, $2)", ChannelName, string(payload))
	if err != nil {
		return fmt.Errorf("livefeed: pg_notify: %w", err)
	}
	return nil
}

// Subscribe registers a new subscription. The returned channel is
// closed by Subscription.Close() — readers should treat the channel
// closing as "we lost the feed; reconnect" if they care to
// distinguish.
func (h *Hub) Subscribe(orgID string, filter Filter) *Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		// Closed hub returns a dead subscription so the caller can
		// detect via the channel close. Avoids a panic in shutdown
		// races.
		closed := make(chan Event)
		close(closed)
		return &Subscription{OrgID: orgID, Filter: filter, C: closed, closeFn: func() {}}
	}
	s := &subscriber{
		orgID:  orgID,
		filter: filter,
		ch:     make(chan Event, subscriberBuffer),
	}
	h.subs[s] = struct{}{}
	closeFn := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[s]; ok {
			delete(h.subs, s)
			close(s.ch)
		}
	}
	return &Subscription{
		OrgID:   orgID,
		Filter:  filter,
		C:       s.ch,
		closeFn: closeFn,
	}
}

// Close stops the listener loop and closes every active subscription.
// Safe to call multiple times.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	close(h.stopCh)
	for s := range h.subs {
		close(s.ch)
		delete(h.subs, s)
	}
	h.mu.Unlock()
	<-h.done
}

// Dropped reports the cumulative count of events the hub couldn't
// hand off to a subscriber due to channel-full conditions.
func (h *Hub) Dropped() uint64 {
	return h.dropped.Load()
}

// Subscribers returns the current subscription count. Diagnostic
// helper for tests and the /metrics endpoint.
func (h *Hub) Subscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
