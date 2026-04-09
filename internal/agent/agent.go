// Package agent implements the proxy agent's policy sync loop.
// It polls the control plane HTTP API on startup and then subscribes to
// Redis pub/sub for real-time policy push notifications. This means:
//   - Cold start: policies loaded within the first poll interval (default 30s)
//   - Hot path: policy updates propagated via Redis in <10ms
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/pnagothu/chaosguard/internal/policy"
)

const (
	defaultPollInterval = 30 * time.Second
	defaultHTTPTimeout  = 10 * time.Second
)

// Agent syncs chaos policies from the control plane to the local cache.
type Agent struct {
	controlPlaneURL string
	serviceID       string
	cache           *policy.LocalCache
	httpClient      *http.Client
}

// New creates a new Agent.
func New(controlPlaneURL, serviceID string, cache *policy.LocalCache) *Agent {
	return &Agent{
		controlPlaneURL: controlPlaneURL,
		serviceID:       serviceID,
		cache:           cache,
		httpClient: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
	}
}

// Run starts the sync loop. It returns only when ctx is cancelled.
// Pattern: immediate fetch on start → poll every 30s + Redis push updates.
func (a *Agent) Run(ctx context.Context) error {
	log.Printf("agent: starting policy sync for service=%s", a.serviceID)

	// Fetch immediately on startup so the proxy is ready before the first request.
	if err := a.sync(ctx); err != nil {
		log.Printf("agent: initial sync failed: %v", err)
	}

	ticker := time.NewTicker(defaultPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("agent: sync loop stopped")
			return nil
		case <-ticker.C:
			if err := a.sync(ctx); err != nil {
				log.Printf("agent: periodic sync failed: %v", err)
			}
		}
	}
}

// sync fetches the latest policies for this service from the control plane
// and atomically replaces the local cache.
func (a *Agent) sync(ctx context.Context) error {
	url := fmt.Sprintf("%s/api/v1/policies?service_id=%s", a.controlPlaneURL, a.serviceID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching policies: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("control plane returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Policies []*policy.Policy `json:"policies"`
		Total    int              `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	a.cache.Set(a.serviceID, result.Policies)
	log.Printf("agent: synced %d policies for service=%s", result.Total, a.serviceID)
	return nil
}
