// Package orchestrator provides the control plane logic for ChaosGuard.
// It coordinates policy lifecycle operations across the policy store,
// audit writer, and connected agents.
package orchestrator

import (
	"context"
	"fmt"
	"log"

	"github.com/pnagothu/chaosguard/internal/audit"
	"github.com/pnagothu/chaosguard/internal/metrics"
	"github.com/pnagothu/chaosguard/internal/policy"
)

// PolicyStore is the minimal interface the orchestrator needs from the policy store.
type PolicyStore interface {
	GetByService(ctx context.Context, serviceID string) ([]*policy.Policy, error)
	Disable(ctx context.Context, policyID string) error
}

// Orchestrator coordinates control-plane operations.
type Orchestrator struct {
	store   PolicyStore
	audit   *audit.Writer
	metrics *metrics.Collector
	redis   RedisPublisher
}

// RedisPublisher is a minimal interface for pub/sub notification.
type RedisPublisher interface {
	Publish(ctx context.Context, channel string, message interface{}) error
}

// New creates a new Orchestrator with all required dependencies.
func New(
	store PolicyStore,
	audit *audit.Writer,
	metrics *metrics.Collector,
	redis RedisPublisher,
) *Orchestrator {
	return &Orchestrator{
		store:   store,
		audit:   audit,
		metrics: metrics,
		redis:   redis,
	}
}

// DisableAll disables every active chaos policy for a given service.
// This is the kill-switch operation — it must be fast and reliable.
// Returns the count of policies disabled.
func (o *Orchestrator) DisableAll(ctx context.Context, serviceID string) (int, error) {
	policies, err := o.store.GetByService(ctx, serviceID)
	if err != nil {
		return 0, fmt.Errorf("fetching policies for service %q: %w", serviceID, err)
	}

	disabled := 0
	var lastErr error
	for _, p := range policies {
		if !p.Enabled {
			continue
		}
		if err := o.store.Disable(ctx, p.ID); err != nil {
			log.Printf("orchestrator: failed to disable policy %s: %v", p.ID, err)
			lastErr = err
			continue
		}
		disabled++
	}

	if disabled > 0 {
		// Broadcast a single kill-switch event to all agents listening on this service.
		// This is faster than waiting for the periodic poll cycle.
		if err := o.redis.Publish(ctx, "policy:"+serviceID, "kill_switch"); err != nil {
			log.Printf("orchestrator: redis publish failed for kill_switch on %s: %v", serviceID, err)
		}
		o.metrics.SetActivePolicies(serviceID, 0)
		log.Printf("orchestrator: kill switch fired for service=%s, disabled=%d", serviceID, disabled)
	}

	if lastErr != nil {
		return disabled, fmt.Errorf("some policies could not be disabled (last error: %w)", lastErr)
	}
	return disabled, nil
}
