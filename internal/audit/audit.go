package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/pnagothu/chaosguard/internal/policy"
)

// EventType categorizes audit log entries
type EventType string

const (
	EventPolicyCreated  EventType = "policy.created"
	EventPolicyDisabled EventType = "policy.disabled"
	EventPolicyDeleted  EventType = "policy.deleted"
	EventChaosInjected  EventType = "chaos.injected"
	EventKillSwitch     EventType = "kill_switch.triggered"
)

// Event is a single audit log entry
type Event struct {
	ID        string                 `json:"id"`
	Type      EventType              `json:"type"`
	ServiceID string                 `json:"service_id"`
	PolicyID  string                 `json:"policy_id,omitempty"`
	Actor     string                 `json:"actor"`
	Payload   map[string]interface{} `json:"payload"`
	CreatedAt time.Time              `json:"created_at"`
}

// ListOptions configures the audit event query.
type ListOptions struct {
	ServiceID string // required
	Limit     int    // max rows; defaults to 50
	Offset    int    // for pagination
}

// DBClient is the minimal interface needed for audit persistence and queries.
type DBClient interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) error
	QueryContext(ctx context.Context, query string, args ...interface{}) (policy.Rows, error)
}

// Writer writes and reads audit events.
type Writer struct {
	db DBClient
}

// NewWriter creates a new audit writer.
func NewWriter(db DBClient) *Writer {
	return &Writer{db: db}
}

// Log persists an audit event. Non-blocking if callers wrap in goroutine.
func (w *Writer) Log(ctx context.Context, eventType EventType, serviceID, policyID, actor string, payload map[string]interface{}) {
	event := Event{
		ID:        fmt.Sprintf("evt_%d", time.Now().UnixNano()),
		Type:      eventType,
		ServiceID: serviceID,
		PolicyID:  policyID,
		Actor:     actor,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}

	payloadJSON, err := json.Marshal(event.Payload)
	if err != nil {
		log.Printf("audit: failed to marshal payload: %v", err)
		return
	}

	err = w.db.ExecContext(ctx,
		`INSERT INTO audit_events (id, type, service_id, policy_id, actor, payload, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		event.ID, event.Type, event.ServiceID, event.PolicyID,
		event.Actor, payloadJSON, event.CreatedAt,
	)
	if err != nil {
		log.Printf("audit: failed to write event: %v", err)
	}
}

// List retrieves paginated audit events for a service, newest first.
func (w *Writer) List(ctx context.Context, opts ListOptions) ([]*Event, error) {
	if opts.Limit <= 0 {
		opts.Limit = 50
	}
	if opts.Limit > 500 {
		opts.Limit = 500
	}

	rows, err := w.db.QueryContext(ctx,
		`SELECT id, type, service_id, policy_id, actor, payload, created_at
		 FROM audit_events
		 WHERE service_id = $1
		 ORDER BY created_at DESC
		 LIMIT $2 OFFSET $3`,
		opts.ServiceID, opts.Limit, opts.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("querying audit events: %w", err)
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		var e Event
		var payloadJSON []byte
		if err := rows.Scan(&e.ID, &e.Type, &e.ServiceID, &e.PolicyID,
			&e.Actor, &payloadJSON, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning audit event: %w", err)
		}
		if len(payloadJSON) > 0 {
			if err := json.Unmarshal(payloadJSON, &e.Payload); err != nil {
				log.Printf("audit: failed to unmarshal payload for event %s: %v", e.ID, err)
			}
		}
		events = append(events, &e)
	}
	return events, nil
}
