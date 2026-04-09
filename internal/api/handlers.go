package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/pnagothu/chaosguard/internal/audit"
	"github.com/pnagothu/chaosguard/internal/metrics"
	"github.com/pnagothu/chaosguard/internal/orchestrator"
	"github.com/pnagothu/chaosguard/internal/policy"
)


// NewRouter creates and returns the main HTTP router for the control plane API
func NewRouter(
	orch *orchestrator.Orchestrator,
	policyStore *policy.Store,
	auditWriter *audit.Writer,
	metricsCollector *metrics.Collector,
) *http.ServeMux {
	mux := http.NewServeMux()

	h := &Handlers{
		orch:    orch,
		store:   policyStore,
		audit:   auditWriter,
		metrics: metricsCollector,
	}

	// Policy management
	mux.HandleFunc("POST /api/v1/policies", h.CreatePolicy)
	mux.HandleFunc("GET /api/v1/policies", h.ListPolicies)
	mux.HandleFunc("GET /api/v1/policies/{id}", h.GetPolicy)
	mux.HandleFunc("DELETE /api/v1/policies/{id}", h.DisablePolicy)

	// Kill switch — emergency disable all chaos for a service
	mux.HandleFunc("POST /api/v1/services/{serviceID}/kill", h.KillSwitch)

	// Audit log
	mux.HandleFunc("GET /api/v1/audit", h.ListAuditEvents)

	// Health check
	mux.HandleFunc("GET /healthz", h.HealthCheck)

	return mux
}

// Handlers holds all HTTP handler dependencies
type Handlers struct {
	orch    *orchestrator.Orchestrator
	store   *policy.Store
	audit   *audit.Writer
	metrics *metrics.Collector
}

// ─── Policy Handlers ──────────────────────────────────────────────────────────

// CreatePolicy handles POST /api/v1/policies
func (h *Handlers) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string          `json:"name"`
		ServiceID string          `json:"service_id"`
		Spec      policy.PolicySpec `json:"spec"`
		TTL       string          `json:"ttl,omitempty"` // e.g. "10m"
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.Name == "" || req.ServiceID == "" {
		writeError(w, http.StatusBadRequest, "name and service_id are required")
		return
	}

	p := &policy.Policy{
		Name:      req.Name,
		ServiceID: req.ServiceID,
		Enabled:   true,
		Spec:      req.Spec,
	}

	if req.TTL != "" {
		d, err := time.ParseDuration(req.TTL)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid ttl: "+err.Error())
			return
		}
		exp := time.Now().Add(d)
		p.ExpiresAt = &exp
	}

	if err := h.store.Create(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create policy: "+err.Error())
		return
	}

	// Async audit log — non-blocking
	go h.audit.Log(r.Context(), audit.EventPolicyCreated,
		p.ServiceID, p.ID, actorFromRequest(r),
		map[string]interface{}{"policy_name": p.Name})

	writeJSON(w, http.StatusCreated, p)
}

// ListPolicies handles GET /api/v1/policies?service_id=xxx
func (h *Handlers) ListPolicies(w http.ResponseWriter, r *http.Request) {
	serviceID := r.URL.Query().Get("service_id")
	if serviceID == "" {
		writeError(w, http.StatusBadRequest, "service_id query param required")
		return
	}

	policies, err := h.store.GetByService(r.Context(), serviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list policies: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"policies": policies,
		"total":    len(policies),
	})
}

// GetPolicy handles GET /api/v1/policies/{id}
func (h *Handlers) GetPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "policy not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// DisablePolicy handles DELETE /api/v1/policies/{id} (soft delete / kill switch)
func (h *Handlers) DisablePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := h.store.Disable(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to disable policy: "+err.Error())
		return
	}

	go h.audit.Log(r.Context(), audit.EventPolicyDisabled,
		"", id, actorFromRequest(r), nil)

	writeJSON(w, http.StatusOK, map[string]string{"status": "disabled", "id": id})
}

// KillSwitch handles POST /api/v1/services/{serviceID}/kill
// Immediately disables ALL chaos for a service — emergency use
func (h *Handlers) KillSwitch(w http.ResponseWriter, r *http.Request) {
	serviceID := r.PathValue("serviceID")

	count, err := h.orch.DisableAll(r.Context(), serviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "kill switch failed: "+err.Error())
		return
	}

	go h.audit.Log(r.Context(), audit.EventKillSwitch,
		serviceID, "", actorFromRequest(r),
		map[string]interface{}{"policies_disabled": count})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":            "killed",
		"service":           serviceID,
		"policies_disabled": count,
	})
}

// ListAuditEvents handles GET /api/v1/audit?service_id=xxx&limit=50&offset=0
func (h *Handlers) ListAuditEvents(w http.ResponseWriter, r *http.Request) {
	serviceID := r.URL.Query().Get("service_id")
	if serviceID == "" {
		writeError(w, http.StatusBadRequest, "service_id query param required")
		return
	}

	limit := 50
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		fmt.Sscanf(o, "%d", &offset)
	}

	events, err := h.audit.List(r.Context(), audit.ListOptions{
		ServiceID: serviceID,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list audit events: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"events": events,
		"total":  len(events),
		"limit":  limit,
		"offset": offset,
	})
}


// HealthCheck handles GET /healthz
func (h *Handlers) HealthCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "ok",
		"service":   "chaosguard-control-plane",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func actorFromRequest(r *http.Request) string {
	if actor := r.Header.Get("X-Actor"); actor != "" {
		return actor
	}
	return "anonymous"
}
