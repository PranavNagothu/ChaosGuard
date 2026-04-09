package policy_test

import (
	"testing"
	"time"

	"github.com/pnagothu/chaosguard/internal/policy"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

func makePolicy(id, serviceID string, enabled bool, faults []policy.FaultSpec) *policy.Policy {
	return &policy.Policy{
		ID:        id,
		Name:      "test-policy-" + id,
		ServiceID: serviceID,
		Enabled:   enabled,
		Spec: policy.PolicySpec{
			Target: policy.TargetSpec{Service: serviceID},
			Faults: faults,
		},
	}
}

func latencyFault(prob float64, minMs, maxMs int) policy.FaultSpec {
	return policy.FaultSpec{
		Type:        policy.FaultLatency,
		Probability: prob,
		Config: map[string]interface{}{
			"min_delay_ms": minMs,
			"max_delay_ms": maxMs,
			"distribution": "uniform",
		},
	}
}

func errorFault(prob float64, statusCode int) policy.FaultSpec {
	return policy.FaultSpec{
		Type:        policy.FaultError,
		Probability: prob,
		Config: map[string]interface{}{
			"status_code": statusCode,
			"body":        `{"error":"injected"}`,
		},
	}
}

// ─── Evaluator Tests ──────────────────────────────────────────────────────────

func TestEvaluator_NoFaultsWhenNoPolicies(t *testing.T) {
	e := policy.NewEvaluator()
	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}
	result := e.Evaluate(req, nil)

	if result.ShouldInject {
		t.Error("expected ShouldInject=false with no policies, got true")
	}
	if len(result.Faults) != 0 {
		t.Errorf("expected 0 faults, got %d", len(result.Faults))
	}
}

func TestEvaluator_DisabledPolicyIgnored(t *testing.T) {
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-a", false, []policy.FaultSpec{
		latencyFault(1.0, 100, 200), // probability=1, but policy is disabled
	})
	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}
	result := e.Evaluate(req, []*policy.Policy{p})

	if result.ShouldInject {
		t.Error("disabled policy should never inject faults")
	}
}

func TestEvaluator_ExpiredPolicyIgnored(t *testing.T) {
	e := policy.NewEvaluator()
	past := time.Now().Add(-1 * time.Hour)
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		latencyFault(1.0, 100, 200),
	})
	p.ExpiresAt = &past

	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}
	result := e.Evaluate(req, []*policy.Policy{p})

	if result.ShouldInject {
		t.Error("expired policy should never inject faults")
	}
}

func TestEvaluator_ActivePolicyNotYetExpired(t *testing.T) {
	e := policy.NewEvaluator()
	future := time.Now().Add(1 * time.Hour)
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		latencyFault(1.0, 100, 200),
	})
	p.ExpiresAt = &future

	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}
	result := e.Evaluate(req, []*policy.Policy{p})

	if !result.ShouldInject {
		t.Error("non-expired policy with probability=1 should inject")
	}
}

func TestEvaluator_TargetServiceMismatch(t *testing.T) {
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-b", true, []policy.FaultSpec{
		latencyFault(1.0, 100, 200),
	})
	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}
	result := e.Evaluate(req, []*policy.Policy{p})

	if result.ShouldInject {
		t.Error("policy targeting svc-b should not affect svc-a")
	}
}

func TestEvaluator_TargetEndpointFilter(t *testing.T) {
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		latencyFault(1.0, 100, 200),
	})
	p.Spec.Target.Endpoints = []string{"/api/checkout"}

	// Wrong endpoint — should not inject
	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api/products", Method: "GET"}
	result := e.Evaluate(req, []*policy.Policy{p})
	if result.ShouldInject {
		t.Error("policy with endpoint filter should not match /api/products")
	}

	// Correct endpoint — should inject
	req2 := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api/checkout", Method: "GET"}
	result2 := e.Evaluate(req, []*policy.Policy{p})
	_ = result2
	result3 := e.Evaluate(req2, []*policy.Policy{p})
	if !result3.ShouldInject {
		t.Error("policy with endpoint filter should match /api/checkout (prob=1.0)")
	}
}

func TestEvaluator_TargetMethodFilter(t *testing.T) {
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		latencyFault(1.0, 100, 200),
	})
	p.Spec.Target.Methods = []string{"POST"}

	// GET should not match
	getReq := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}
	if e.Evaluate(getReq, []*policy.Policy{p}).ShouldInject {
		t.Error("GET request should not match POST-only policy")
	}

	// POST should match
	postReq := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "POST"}
	if !e.Evaluate(postReq, []*policy.Policy{p}).ShouldInject {
		t.Error("POST request should match POST-only policy with probability=1.0")
	}
}

func TestEvaluator_TargetHeaderFilter(t *testing.T) {
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		latencyFault(1.0, 100, 200),
	})
	p.Spec.Target.Headers = map[string]string{"X-Chaos-Test": "true"}

	// Missing header
	noHeader := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET", Headers: map[string]string{}}
	if e.Evaluate(noHeader, []*policy.Policy{p}).ShouldInject {
		t.Error("request without required header should not match")
	}

	// Correct header
	withHeader := policy.EvalRequest{
		ServiceID: "svc-a", Endpoint: "/api", Method: "GET",
		Headers: map[string]string{"X-Chaos-Test": "true"},
	}
	if !e.Evaluate(withHeader, []*policy.Policy{p}).ShouldInject {
		t.Error("request with correct header should match")
	}
}

func TestEvaluator_ProbabilisticGate_AlwaysInject(t *testing.T) {
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		latencyFault(1.0, 100, 200), // 100% probability
	})
	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}

	// Run 100 times — all should inject
	for i := 0; i < 100; i++ {
		if !e.Evaluate(req, []*policy.Policy{p}).ShouldInject {
			t.Errorf("probability=1.0 should always inject (failed on iteration %d)", i)
		}
	}
}

func TestEvaluator_ProbabilisticGate_NeverInject(t *testing.T) {
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		latencyFault(0.0, 100, 200), // 0% probability
	})
	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}

	for i := 0; i < 100; i++ {
		if e.Evaluate(req, []*policy.Policy{p}).ShouldInject {
			t.Errorf("probability=0.0 should never inject (failed on iteration %d)", i)
		}
	}
}

func TestEvaluator_ProbabilisticGate_Statistical(t *testing.T) {
	// With probability=0.5 and 10,000 trials, we expect ~50% injection.
	// Allow ±5% tolerance (45%-55%).
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		latencyFault(0.5, 100, 200),
	})
	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}

	const trials = 10_000
	injected := 0
	for i := 0; i < trials; i++ {
		if e.Evaluate(req, []*policy.Policy{p}).ShouldInject {
			injected++
		}
	}
	rate := float64(injected) / trials
	if rate < 0.45 || rate > 0.55 {
		t.Errorf("expected injection rate ~0.50 ±0.05, got %.3f", rate)
	}
}

func TestEvaluator_LatencyFaultResolution(t *testing.T) {
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		latencyFault(1.0, 100, 500),
	})
	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}
	result := e.Evaluate(req, []*policy.Policy{p})

	if !result.ShouldInject {
		t.Fatal("expected inject")
	}
	if len(result.Faults) != 1 {
		t.Fatalf("expected 1 fault, got %d", len(result.Faults))
	}
	f := result.Faults[0]
	if f.Type != policy.FaultLatency {
		t.Errorf("expected FaultLatency, got %s", f.Type)
	}
	if f.DelayMs < 100 || f.DelayMs > 500 {
		t.Errorf("delay %dms out of range [100, 500]", f.DelayMs)
	}
}

func TestEvaluator_ErrorFaultResolution(t *testing.T) {
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		errorFault(1.0, 503),
	})
	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}
	result := e.Evaluate(req, []*policy.Policy{p})

	if len(result.Faults) != 1 {
		t.Fatalf("expected 1 fault, got %d", len(result.Faults))
	}
	f := result.Faults[0]
	if f.Type != policy.FaultError {
		t.Errorf("expected FaultError, got %s", f.Type)
	}
	if f.StatusCode != 503 {
		t.Errorf("expected status 503, got %d", f.StatusCode)
	}
}

func TestEvaluator_MultipleFaultsFromSinglePolicy(t *testing.T) {
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		latencyFault(1.0, 100, 200),
		errorFault(1.0, 500),
	})
	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}
	result := e.Evaluate(req, []*policy.Policy{p})

	if len(result.Faults) != 2 {
		t.Errorf("expected 2 faults (both probability=1.0), got %d", len(result.Faults))
	}
}

func TestEvaluator_MultiplePolicies(t *testing.T) {
	e := policy.NewEvaluator()
	p1 := makePolicy("p1", "svc-a", true, []policy.FaultSpec{latencyFault(1.0, 100, 200)})
	p2 := makePolicy("p2", "svc-a", true, []policy.FaultSpec{errorFault(1.0, 429)})

	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}
	result := e.Evaluate(req, []*policy.Policy{p1, p2})

	if len(result.Faults) != 2 {
		t.Errorf("expected 2 faults from 2 policies, got %d", len(result.Faults))
	}
}

// ─── Blast Radius Tests ───────────────────────────────────────────────────────

func TestEvaluator_BlastRadius_ZeroMeansUnlimited(t *testing.T) {
	// BlastRadius=0 (unset) should not gate anything; probability=1.0 always fires.
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		latencyFault(1.0, 100, 200),
	})
	p.Spec.Safeguards.BlastRadius = 0

	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}
	for i := 0; i < 50; i++ {
		if !e.Evaluate(req, []*policy.Policy{p}).ShouldInject {
			t.Errorf("BlastRadius=0 should not gate faults (iteration %d)", i)
		}
	}
}

func TestEvaluator_BlastRadius_OneAllowsAll(t *testing.T) {
	// BlastRadius=1.0 means 100% of requests can be affected.
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		latencyFault(1.0, 100, 200),
	})
	p.Spec.Safeguards.BlastRadius = 1.0

	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}
	for i := 0; i < 50; i++ {
		if !e.Evaluate(req, []*policy.Policy{p}).ShouldInject {
			t.Errorf("BlastRadius=1.0 should never gate faults (iteration %d)", i)
		}
	}
}

func TestEvaluator_BlastRadius_StatisticalCap(t *testing.T) {
	// BlastRadius=0.2 with fault prob=1.0: effective injection rate should be ~20%.
	// With 10,000 trials, allow ±5% tolerance.
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		latencyFault(1.0, 100, 200), // prob=1.0, so blast radius is the only gate
	})
	p.Spec.Safeguards.BlastRadius = 0.2

	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}
	const trials = 10_000
	injected := 0
	for i := 0; i < trials; i++ {
		if e.Evaluate(req, []*policy.Policy{p}).ShouldInject {
			injected++
		}
	}
	rate := float64(injected) / trials
	if rate < 0.15 || rate > 0.25 {
		t.Errorf("expected blast radius ~0.20 ±0.05, got %.3f (%d/%d injected)", rate, injected, trials)
	}
}



func TestLocalCache_SetAndGet(t *testing.T) {
	c := policy.NewLocalCache()
	p := makePolicy("p1", "svc-x", true, nil)
	c.Set("svc-x", []*policy.Policy{p})

	got := c.Get("svc-x")
	if len(got) != 1 {
		t.Fatalf("expected 1 policy, got %d", len(got))
	}
	if got[0].ID != "p1" {
		t.Errorf("expected ID=p1, got %s", got[0].ID)
	}
}

func TestLocalCache_GetEmptyService(t *testing.T) {
	c := policy.NewLocalCache()
	got := c.Get("nonexistent")
	if got != nil {
		t.Errorf("expected nil for unknown service, got %v", got)
	}
}

func TestLocalCache_SetReplaces(t *testing.T) {
	c := policy.NewLocalCache()
	p1 := makePolicy("p1", "svc-x", true, nil)
	p2 := makePolicy("p2", "svc-x", true, nil)

	c.Set("svc-x", []*policy.Policy{p1})
	c.Set("svc-x", []*policy.Policy{p2}) // replace

	got := c.Get("svc-x")
	if len(got) != 1 || got[0].ID != "p2" {
		t.Error("expected cache to be replaced with p2 only")
	}
}

func TestLocalCache_ConcurrentReadWrite(t *testing.T) {
	c := policy.NewLocalCache()
	done := make(chan struct{})

	// Writers
	for i := 0; i < 10; i++ {
		go func() {
			p := makePolicy("p1", "svc-x", true, nil)
			for j := 0; j < 100; j++ {
				c.Set("svc-x", []*policy.Policy{p})
			}
			done <- struct{}{}
		}()
	}
	// Readers
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				_ = c.Get("svc-x")
			}
			done <- struct{}{}
		}()
	}

	for i := 0; i < 20; i++ {
		<-done
	}
}

// ─── Benchmarks ───────────────────────────────────────────────────────────────

func BenchmarkEvaluator_SinglePolicy(b *testing.B) {
	e := policy.NewEvaluator()
	p := makePolicy("p1", "svc-a", true, []policy.FaultSpec{
		latencyFault(0.3, 100, 500),
		errorFault(0.05, 503),
	})
	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api/checkout", Method: "POST"}
	policies := []*policy.Policy{p}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			e.Evaluate(req, policies)
		}
	})
}

func BenchmarkEvaluator_TenPolicies(b *testing.B) {
	e := policy.NewEvaluator()
	policies := make([]*policy.Policy, 10)
	for i := range policies {
		policies[i] = makePolicy(
			"p"+string(rune('0'+i)), "svc-a", true,
			[]policy.FaultSpec{latencyFault(0.1, 50, 200)},
		)
	}
	req := policy.EvalRequest{ServiceID: "svc-a", Endpoint: "/api", Method: "GET"}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			e.Evaluate(req, policies)
		}
	})
}

func BenchmarkLocalCache_Get(b *testing.B) {
	c := policy.NewLocalCache()
	p := makePolicy("p1", "svc-a", true, nil)
	c.Set("svc-a", []*policy.Policy{p})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Get("svc-a")
		}
	})
}
