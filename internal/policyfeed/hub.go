// Package policyfeed is the slice-15 pub/sub backplane for
// agent-facing policy update notifications. Distinct from
// internal/livefeed (the dashboard-facing intent/action stream):
//
//   - Audience: agent runtimes (not human dashboards).
//   - Auth at the API boundary: org-scoped API key (not user JWT).
//   - Event shape: "your policy changed; pull and apply" — small,
//     single-purpose notification, not the live action stream.
//   - Subscriber filter: per-agent (one runtime per subscription),
//     not per-org with selector filtering.
//
// Same underlying mechanic as livefeed (Postgres LISTEN/NOTIFY on a
// dedicated channel, per-subscriber bounded channels with
// drop-on-overflow), different shape so the two surfaces can't drift
// into each other.
package policyfeed

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

// ChannelName is the Postgres LISTEN channel for slice-15 policy
// updates. Distinct from livefeed.ChannelName so the two streams
// don't share a topic.
const ChannelName = "relic_policy_updates"

// subscriberBuffer is the per-subscription channel depth. Policy
// updates are sparse (a few per day per agent at peak), so 16 is
// plenty.
const subscriberBuffer = 16

// maxNotifyPayload mirrors Postgres NOTIFY's 8000-byte cap. Our
// notification payload is fixed-shape and small (a few hundred bytes
// max); the cap is here as a defensive guardrail.
const maxNotifyPayload = 8000

// Notification is the payload a runtime sees when its policy_set
// changes. The runtime uses Hash + Version to deduplicate (if it
// already applied this hash, no need to re-pull) and pulls the YAML
// via the existing /v1/agents/:name/policy endpoint.
type Notification struct {
	OrgID         string    `json:"org_id"`
	AgentName     string    `json:"agent_name"`
	PolicyHash    string    `json:"policy_hash"`
	Version       int       `json:"version"`
	PolicySetName string    `json:"policy_set_name"`
	PublishedAt   time.Time `json:"published_at"`
}

// Subscription is a per-runtime handle. Owner reads notifications
// from C and calls Close exactly once when the SSE connection drops.
type Subscription struct {
	OrgID     string
	AgentName string
	C         <-chan Notification
	closeFn   func()
}

func (s *Subscription) Close() {
	if s == nil || s.closeFn == nil {
		return
	}
	s.closeFn()
}

// Hub is the in-process fanout for policy update notifications.
// Construct with New, Start before HTTP accepts traffic, Close on
// shutdown.
type Hub struct {
	pool   *pgxpool.Pool
	logger *slog.Logger

	mu     sync.RWMutex
	subs   map[*subscriber]struct{}
	closed bool

	stopCh chan struct{}
	done   chan struct{}

	dropped atomic.Uint64
}

type subscriber struct {
	orgID     string
	agentName string
	ch        chan Notification
}

// New constructs a Hub. Pool ownership stays with the caller.
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

// Start binds a dedicated LISTEN connection and begins fanout. Like
// livefeed.Hub.Start, a failure here is fatal — agents would silently
// stop receiving policy updates.
func (h *Hub) Start(ctx context.Context) error {
	conn, err := h.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("policyfeed: acquire conn: %w", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("LISTEN %s", pgx.Identifier{ChannelName}.Sanitize())); err != nil {
		conn.Release()
		return fmt.Errorf("policyfeed: LISTEN: %w", err)
	}
	go h.run(conn)
	return nil
}

func (h *Hub) run(conn *pgxpool.Conn) {
	defer close(h.done)
	defer conn.Release()

	ctx := context.Background()
	for {
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
			h.logger.Error("policyfeed: notification wait failed", "error", err)
			return
		}
		var note Notification
		if err := json.Unmarshal([]byte(n.Payload), &note); err != nil {
			h.logger.Warn("policyfeed: malformed payload", "error", err)
			continue
		}
		h.dispatch(note)
	}
}

func (h *Hub) dispatch(note Notification) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.subs {
		if s.orgID != note.OrgID || s.agentName != note.AgentName {
			continue
		}
		select {
		case s.ch <- note:
		default:
			h.dropped.Add(1)
		}
	}
}

// Publish sends a notification on the shared channel. Synchronous —
// returns once Postgres acknowledges. Slice-15 publish rate is human-
// scale (an operator saves a policy_set, maybe once a minute at peak),
// so the per-call cost is negligible.
func (h *Hub) Publish(ctx context.Context, note Notification) error {
	if note.OrgID == "" {
		return errors.New("policyfeed: org_id required")
	}
	if note.AgentName == "" {
		return errors.New("policyfeed: agent_name required")
	}
	if note.PolicyHash == "" {
		return errors.New("policyfeed: policy_hash required")
	}
	if note.PublishedAt.IsZero() {
		note.PublishedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(note)
	if err != nil {
		return fmt.Errorf("policyfeed: marshal: %w", err)
	}
	if len(payload) > maxNotifyPayload {
		return fmt.Errorf("policyfeed: payload %d bytes exceeds NOTIFY cap (%d)", len(payload), maxNotifyPayload)
	}
	_, err = h.pool.Exec(ctx, "SELECT pg_notify($1, $2)", ChannelName, string(payload))
	if err != nil {
		return fmt.Errorf("policyfeed: pg_notify: %w", err)
	}
	return nil
}

// Subscribe registers a single-agent subscription. The platform's SSE
// handler is the only caller; one open SSE connection per running
// agent means we typically have N subscriptions per org for N active
// agents.
func (h *Hub) Subscribe(orgID, agentName string) *Subscription {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		closed := make(chan Notification)
		close(closed)
		return &Subscription{OrgID: orgID, AgentName: agentName, C: closed, closeFn: func() {}}
	}
	s := &subscriber{
		orgID:     orgID,
		agentName: agentName,
		ch:        make(chan Notification, subscriberBuffer),
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
		OrgID:     orgID,
		AgentName: agentName,
		C:         s.ch,
		closeFn:   closeFn,
	}
}

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

// Dropped reports the cumulative count of notifications a subscriber
// couldn't accept (channel full). Diagnostic helper for /metrics.
func (h *Hub) Dropped() uint64 {
	return h.dropped.Load()
}

// Subscribers returns the current subscription count.
func (h *Hub) Subscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
