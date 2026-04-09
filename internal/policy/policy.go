package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"
)

// ─── Domain Types ──────────────────────────────────────────────────────────────

// FaultType represents the kind of chaos fault to inject
type FaultType string

const (
	FaultLatency   FaultType = "latency"
	FaultError     FaultType = "error"
	FaultPartition FaultType = "partition"
	FaultTimeout   FaultType = "timeout"
	FaultCorrupt   FaultType = "corrupt_response"
)

// Distribution describes the latency distribution type
type Distribution string

const (
	DistNormal      Distribution = "normal"
	DistUniform     Distribution = "uniform"
	DistExponential Distribution = "exponential"
)

// Policy is the top-level chaos policy definition
type Policy struct {
	ID          string      `json:"id" db:"id"`
	Name        string      `json:"name" db:"name"`
	ServiceID   string      `json:"service_id" db:"service_id"`
	Enabled     bool        `json:"enabled" db:"enabled"`
	Spec        PolicySpec  `json:"spec" db:"spec"`
	CreatedAt   time.Time   `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at" db:"updated_at"`
	ExpiresAt   *time.Time  `json:"expires_at,omitempty" db:"expires_at"`
}

// PolicySpec defines what chaos to inject and under what conditions
type PolicySpec struct {
	Target     TargetSpec      `json:"target"`
	Faults     []FaultSpec     `json:"faults"`
	Safeguards SafeguardSpec   `json:"safeguards"`
}

// TargetSpec narrows which requests are affected
type TargetSpec struct {
	Service   string   `json:"service"`
	Endpoints []string `json:"endpoints,omitempty"` // empty = all endpoints
	Methods   []string `json:"methods,omitempty"`   // empty = all methods
	Headers   map[string]string `json:"headers,omitempty"` // match on request headers
}

// FaultSpec describes a single fault to inject
type FaultSpec struct {
	Type        FaultType              `json:"type"`
	Probability float64                `json:"probability"` // 0.0 - 1.0
	Config      map[string]interface{} `json:"config"`
}

// SafeguardSpec provides safety controls to prevent runaway experiments
type SafeguardSpec struct {
	MaxDuration  string  `json:"max_duration"`   // e.g., "10m"
	BlastRadius  float64 `json:"blast_radius"`   // max fraction of requests
	KillSwitch   bool    `json:"kill_switch"`
}

// ─── Policy Evaluator ──────────────────────────────────────────────────────────

// EvalRequest is the input to the policy evaluator
type EvalRequest struct {
	ServiceID string
	Endpoint  string
	Method    string
	Headers   map[string]string
}

// EvalResult is the output — list of faults to apply
type EvalResult struct {
	ShouldInject bool
	Faults       []ResolvedFault
}

// ResolvedFault is a concrete fault ready to be applied by the proxy
type ResolvedFault struct {
	Type       FaultType
	DelayMs    int           // for latency faults
	StatusCode int           // for error faults
	Body       string        // for error faults
}

// Evaluator evaluates policies against requests
type Evaluator struct{}

// NewEvaluator creates a new policy evaluator
func NewEvaluator() *Evaluator {
	return &Evaluator{}
}

// Evaluate checks all matching policies and returns resolved faults.
// It enforces:
//  1. Policy enabled + not expired
//  2. Target matching (service, endpoint, method, headers)
//  3. Per-fault probabilistic gate
//  4. Per-policy blast radius safeguard (caps effective injection rate)
func (e *Evaluator) Evaluate(req EvalRequest, policies []*Policy) EvalResult {
	var resolvedFaults []ResolvedFault

	for _, p := range policies {
		if !p.Enabled {
			continue
		}
		if p.ExpiresAt != nil && time.Now().After(*p.ExpiresAt) {
			continue
		}
		if !matchesTarget(req, p.Spec.Target) {
			continue
		}

		// ── Blast radius gate ────────────────────────────────────────────────
		// BlastRadius is the maximum fraction of requests this policy can affect.
		// We draw an independent Bernoulli trial here — if the roll exceeds the
		// blast radius, we skip the entire policy for this request. This ensures
		// that even a probability=1.0 fault only affects BlastRadius*100% of traffic.
		if br := p.Spec.Safeguards.BlastRadius; br > 0 && rand.Float64() > br {
			continue
		}

		for _, fault := range p.Spec.Faults {
			// Per-fault probabilistic gate — secondary control for fine-grained targeting
			if rand.Float64() > fault.Probability {
				continue
			}
			resolved := resolveFault(fault)
			resolvedFaults = append(resolvedFaults, resolved)
		}
	}

	return EvalResult{
		ShouldInject: len(resolvedFaults) > 0,
		Faults:       resolvedFaults,
	}
}

func matchesTarget(req EvalRequest, target TargetSpec) bool {
	if target.Service != "" && target.Service != req.ServiceID {
		return false
	}
	if len(target.Endpoints) > 0 && !containsString(target.Endpoints, req.Endpoint) {
		return false
	}
	if len(target.Methods) > 0 && !containsString(target.Methods, req.Method) {
		return false
	}
	for k, v := range target.Headers {
		if req.Headers[k] != v {
			return false
		}
	}
	return true
}

func resolveFault(fault FaultSpec) ResolvedFault {
	resolved := ResolvedFault{Type: fault.Type}
	switch fault.Type {
	case FaultLatency:
		minMs := intFromConfig(fault.Config, "min_delay_ms", 100)
		maxMs := intFromConfig(fault.Config, "max_delay_ms", 1000)
		dist := stringFromConfig(fault.Config, "distribution", "uniform")
		resolved.DelayMs = sampleDelay(minMs, maxMs, Distribution(dist))
	case FaultError:
		resolved.StatusCode = intFromConfig(fault.Config, "status_code", 503)
		resolved.Body = stringFromConfig(fault.Config, "body", `{"error":"chaos injected"}`)
	}
	return resolved
}

func sampleDelay(minMs, maxMs int, dist Distribution) int {
	switch dist {
	case DistNormal:
		mean := float64(minMs+maxMs) / 2
		stddev := float64(maxMs-minMs) / 6
		return clampInt(int(rand.NormFloat64()*stddev+mean), minMs, maxMs)
	case DistExponential:
		lambda := 1.0 / (float64(maxMs-minMs) / 3)
		return clampInt(minMs+int(rand.ExpFloat64()/lambda), minMs, maxMs)
	default: // uniform
		return minMs + rand.Intn(maxMs-minMs+1)
	}
}

// ─── Local In-Memory Cache (used by agent) ─────────────────────────────────────

// LocalCache is a thread-safe in-memory policy cache for the proxy agent
type LocalCache struct {
	mu       sync.RWMutex
	policies map[string][]*Policy // serviceID → policies
	version  int64
}

// NewLocalCache creates a new local policy cache
func NewLocalCache() *LocalCache {
	return &LocalCache{
		policies: make(map[string][]*Policy),
	}
}

// Set replaces all policies for a service
func (c *LocalCache) Set(serviceID string, policies []*Policy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.policies[serviceID] = policies
	c.version++
	log.Printf("policy cache updated: service=%s policies=%d version=%d",
		serviceID, len(policies), c.version)
}

// Get returns policies for a service (read-only)
func (c *LocalCache) Get(serviceID string) []*Policy {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.policies[serviceID]
}

// ─── Policy Store (server-side, backed by PostgreSQL + Redis) ─────────────────

// Store manages policy persistence and pub/sub notification
type Store struct {
	db    DBClient
	redis RedisClient
}

// DBClient is an interface for database operations (allows mocking in tests)
type DBClient interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (Rows, error)
	ExecContext(ctx context.Context, query string, args ...interface{}) error
	QueryRowContext(ctx context.Context, query string, args ...interface{}) RowScanner
}

// RowScanner is the interface for a single-row scan result.
type RowScanner interface {
	Scan(dest ...interface{}) error
}

// RedisClient is an interface for Redis operations
type RedisClient interface {
	Publish(ctx context.Context, channel string, message interface{}) error
	Subscribe(ctx context.Context, channels ...string) (<-chan string, error)
}

// Rows is an interface for database result rows
type Rows interface {
	Next() bool
	Scan(dest ...interface{}) error
	Close() error
}

// NewStore creates a new policy store
func NewStore(db DBClient, redis RedisClient) *Store {
	return &Store{db: db, redis: redis}
}

// Create persists a new policy and notifies agents via Redis pub/sub
func (s *Store) Create(ctx context.Context, p *Policy) error {
	p.ID = generateID()
	p.CreatedAt = time.Now().UTC()
	p.UpdatedAt = p.CreatedAt

	specJSON, err := json.Marshal(p.Spec)
	if err != nil {
		return fmt.Errorf("marshaling spec: %w", err)
	}

	err = s.db.ExecContext(ctx,
		`INSERT INTO policies (id, name, service_id, enabled, spec, created_at, updated_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		p.ID, p.Name, p.ServiceID, p.Enabled, specJSON, p.CreatedAt, p.UpdatedAt, p.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("inserting policy: %w", err)
	}

	// Notify all agents watching this service via Redis pub/sub
	return s.redis.Publish(ctx, "policy:"+p.ServiceID, "updated")
}

// GetByService returns all active policies for a service
func (s *Store) GetByService(ctx context.Context, serviceID string) ([]*Policy, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, service_id, enabled, spec, created_at, updated_at, expires_at
		 FROM policies
		 WHERE service_id = $1 AND enabled = true
		 ORDER BY created_at DESC`,
		serviceID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying policies: %w", err)
	}
	defer rows.Close()

	var policies []*Policy
	for rows.Next() {
		var p Policy
		var specJSON []byte
		err := rows.Scan(&p.ID, &p.Name, &p.ServiceID, &p.Enabled,
			&specJSON, &p.CreatedAt, &p.UpdatedAt, &p.ExpiresAt)
		if err != nil {
			return nil, fmt.Errorf("scanning policy: %w", err)
		}
		if err := json.Unmarshal(specJSON, &p.Spec); err != nil {
			return nil, fmt.Errorf("unmarshaling spec: %w", err)
		}
		policies = append(policies, &p)
	}
	return policies, nil
}

// Disable deactivates a policy (kill switch)
func (s *Store) Disable(ctx context.Context, policyID string) error {
	err := s.db.ExecContext(ctx,
		`UPDATE policies SET enabled = false, updated_at = $1 WHERE id = $2`,
		time.Now().UTC(), policyID,
	)
	if err != nil {
		return fmt.Errorf("disabling policy: %w", err)
	}
	// Notify agents
	return s.redis.Publish(ctx, "policy:all", "updated")
}

// GetByID returns a single policy by its ID.
func (s *Store) GetByID(ctx context.Context, id string) (*Policy, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, service_id, enabled, spec, created_at, updated_at, expires_at
		 FROM policies WHERE id = $1`,
		id,
	)
	var p Policy
	var specJSON []byte
	err := row.Scan(&p.ID, &p.Name, &p.ServiceID, &p.Enabled,
		&specJSON, &p.CreatedAt, &p.UpdatedAt, &p.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("scanning policy %s: %w", id, err)
	}
	if err := json.Unmarshal(specJSON, &p.Spec); err != nil {
		return nil, fmt.Errorf("unmarshaling spec: %w", err)
	}
	return &p, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func intFromConfig(cfg map[string]interface{}, key string, def int) int {
	if v, ok := cfg[key]; ok {
		switch val := v.(type) {
		case int:
			return val
		case float64:
			return int(val)
		}
	}
	return def
}

func stringFromConfig(cfg map[string]interface{}, key, def string) string {
	if v, ok := cfg[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func generateID() string {
	return fmt.Sprintf("pol_%d", time.Now().UnixNano())
}
