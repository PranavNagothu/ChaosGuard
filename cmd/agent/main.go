package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pnagothu/chaosguard/internal/agent"
	"github.com/pnagothu/chaosguard/internal/metrics"
	"github.com/pnagothu/chaosguard/internal/policy"
	"github.com/pnagothu/chaosguard/internal/proxy"
)

func main() {
	log.Println("🚀 Starting ChaosGuard Proxy Agent...")

	controlPlaneURL := envOrDefault("CONTROL_PLANE_URL", "http://localhost:8080")
	serviceID := envOrDefault("SERVICE_ID", "default-service")
	upstreamURL := envOrDefault("UPSTREAM_URL", "http://localhost:9090")
	listenAddr := envOrDefault("LISTEN_ADDR", ":8181")

	// ─── Policy Sync ─────────────────────────────────────────────────────────
	// Agent periodically polls the control plane and subscribes to Redis
	// pub/sub for real-time policy updates
	policyCache := policy.NewLocalCache()
	ag := agent.New(controlPlaneURL, serviceID, policyCache)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := ag.Run(ctx); err != nil {
			log.Printf("agent sync error: %v", err)
		}
	}()

	// ─── Metrics ─────────────────────────────────────────────────────────────
	collector := metrics.NewCollector()

	// ─── Reverse Proxy with Fault Injection ──────────────────────────────────
	faultProxy := proxy.New(proxy.Config{
		UpstreamURL: upstreamURL,
		ServiceID:   serviceID,
		PolicyCache: policyCache,
		Metrics:     collector,
	})

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      faultProxy,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("✅ Proxy agent listening on %s → %s", listenAddr, upstreamURL)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("agent server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("🛑 Agent shutting down...")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	srv.Shutdown(shutCtx)
	log.Println("✅ Agent stopped cleanly")
}

func envOrDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
