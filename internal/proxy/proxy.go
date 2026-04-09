package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/pnagothu/chaosguard/internal/metrics"
	"github.com/pnagothu/chaosguard/internal/policy"
)

// Config holds proxy configuration
type Config struct {
	UpstreamURL string
	ServiceID   string
	PolicyCache *policy.LocalCache
	Metrics     *metrics.Collector
}

// FaultProxy is an HTTP reverse proxy that injects chaos faults
type FaultProxy struct {
	rp        *httputil.ReverseProxy
	config    Config
	evaluator *policy.Evaluator
}

// New creates a new FaultProxy
func New(cfg Config) *FaultProxy {
	upstream, err := url.Parse(cfg.UpstreamURL)
	if err != nil {
		log.Fatalf("invalid upstream URL %q: %v", cfg.UpstreamURL, err)
	}

	rp := httputil.NewSingleHostReverseProxy(upstream)
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error: %v", err)
		http.Error(w, "upstream error", http.StatusBadGateway)
	}

	return &FaultProxy{
		rp:        rp,
		config:    cfg,
		evaluator: policy.NewEvaluator(),
	}
}

// ServeHTTP intercepts each request, evaluates chaos policies, and either
// injects faults or forwards the request to the upstream service.
func (fp *FaultProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Build evaluation request
	headers := make(map[string]string)
	for k := range r.Header {
		headers[k] = r.Header.Get(k)
	}
	evalReq := policy.EvalRequest{
		ServiceID: fp.config.ServiceID,
		Endpoint:  r.URL.Path,
		Method:    r.Method,
		Headers:   headers,
	}

	// Get current policies from local cache (zero DB calls on hot path)
	policies := fp.config.PolicyCache.Get(fp.config.ServiceID)
	result := fp.evaluator.Evaluate(evalReq, policies)

	if result.ShouldInject {
		fp.config.Metrics.IncrChaosInjections(fp.config.ServiceID)
		for _, fault := range result.Faults {
			if err := fp.applyFault(w, r, fault); err != nil {
				// Fault caused early return (e.g. error injection)
				fp.config.Metrics.ObserveRequestDuration(fp.config.ServiceID, r.URL.Path,
					"chaos", time.Since(start).Seconds())
				return
			}
		}
	}

	// Add observability header so downstream tracing knows chaos was active
	if result.ShouldInject {
		r.Header.Set("X-ChaosGuard-Active", "true")
		r.Header.Set("X-ChaosGuard-Service", fp.config.ServiceID)
	}

	// Forward to upstream
	fp.rp.ServeHTTP(w, r)
	fp.config.Metrics.ObserveRequestDuration(fp.config.ServiceID, r.URL.Path,
		"forwarded", time.Since(start).Seconds())
}

// applyFault applies a single resolved fault. Returns an error if the request
// was terminated (e.g., error injection), nil if processing should continue.
func (fp *FaultProxy) applyFault(w http.ResponseWriter, r *http.Request, fault policy.ResolvedFault) error {
	switch fault.Type {
	case policy.FaultLatency:
		delay := time.Duration(fault.DelayMs) * time.Millisecond
		log.Printf("chaos: injecting %dms latency on %s %s", fault.DelayMs, r.Method, r.URL.Path)

		// Use context-aware sleep so we respect client disconnections
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return context.Canceled
		}
		return nil // continue to upstream

	case policy.FaultError:
		log.Printf("chaos: injecting %d error on %s %s", fault.StatusCode, r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-ChaosGuard-Injected", "error")
		w.WriteHeader(fault.StatusCode)
		fmt.Fprint(w, fault.Body)
		return fmt.Errorf("error injected") // signal early return

	case policy.FaultPartition:
		log.Printf("chaos: injecting network partition on %s %s", r.Method, r.URL.Path)
		// Close the connection without response — simulates network partition
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			if conn != nil {
				conn.Close()
			}
		}
		return fmt.Errorf("partition injected")

	case policy.FaultTimeout:
		// Sleep longer than upstream timeout to trigger a timeout
		log.Printf("chaos: injecting timeout on %s %s", r.Method, r.URL.Path)
		select {
		case <-time.After(60 * time.Second):
		case <-r.Context().Done():
		}
		return context.DeadlineExceeded
	}
	return nil
}

// ─── Request Logging Middleware ────────────────────────────────────────────────

type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// LoggingMiddleware logs all proxied requests
func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
		b, _ := json.Marshal(map[string]interface{}{
			"method":   r.Method,
			"path":     r.URL.Path,
			"status":   rw.statusCode,
			"duration": time.Since(start).String(),
			"chaos":    r.Header.Get("X-ChaosGuard-Active") == "true",
		})
		log.Println(string(b))
	})
}
