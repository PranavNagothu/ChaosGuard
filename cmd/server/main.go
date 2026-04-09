package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pnagothu/chaosguard/internal/api"
	"github.com/pnagothu/chaosguard/internal/audit"
	"github.com/pnagothu/chaosguard/internal/metrics"
	"github.com/pnagothu/chaosguard/internal/orchestrator"
	"github.com/pnagothu/chaosguard/internal/policy"
	"github.com/pnagothu/chaosguard/internal/store"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	log.Println("🚀 Starting ChaosGuard Control Plane...")

	// ─── Database ────────────────────────────────────────────────────────────
	db, err := store.NewPostgres(envOrDefault("DATABASE_URL",
		"postgres://chaosguard:chaosguard@localhost:5432/chaosguard?sslmode=disable"))
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(); err != nil {
		log.Fatalf("migration failed: %v", err)
	}

	// ─── Redis ───────────────────────────────────────────────────────────────
	redisClient := store.NewRedis(envOrDefault("REDIS_URL", "redis://localhost:6379"))

	// ─── Core Services ───────────────────────────────────────────────────────
	policyStore := policy.NewStore(db, redisClient)
	auditWriter := audit.NewWriter(db)
	metricsCollector := metrics.NewCollector()
	orch := orchestrator.New(policyStore, auditWriter, metricsCollector, redisClient)

	// ─── HTTP Server ─────────────────────────────────────────────────────────
	router := api.NewRouter(orch, policyStore, auditWriter, metricsCollector)

	// Prometheus metrics endpoint
	router.Handle("/metrics", promhttp.Handler())

	// Dashboard — served from ./dashboard/ directory
	// Access at http://localhost:8080/dashboard/
	dashboardFS := http.FileServer(http.Dir("./dashboard"))
	router.Handle("/dashboard/", http.StripPrefix("/dashboard/", dashboardFS))


	srv := &http.Server{
		Addr:         envOrDefault("LISTEN_ADDR", ":8080"),
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// ─── Graceful Shutdown ────────────────────────────────────────────────────
	go func() {
		log.Printf("✅ Control plane listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("🛑 Shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("forced shutdown: %v", err)
	}
	log.Println("✅ ChaosGuard server stopped cleanly")
}

func envOrDefault(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
