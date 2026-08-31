package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/joshuanmoses/piCloud/backend/internal/health"
	"github.com/joshuanmoses/piCloud/backend/internal/logging"
	"github.com/joshuanmoses/piCloud/backend/internal/version"
)

// HealthResponse is the body of GET /health.
//
// It carries exactly enough for a human or an orchestrator to tell whether the
// service works, and nothing that describes the infrastructure: no hostnames,
// no ports, no credentials, no filesystem paths, no server versions.
type HealthResponse struct {
	Status    health.Status `json:"status"`
	Database  health.Status `json:"database"`
	Storage   health.Status `json:"storage"`
	Version   string        `json:"version"`
	UptimeSec int64         `json:"uptime_seconds"`
	Checks    []HealthCheck `json:"checks,omitempty"`
}

// HealthCheck is a per-subsystem result.
type HealthCheck struct {
	Name      string        `json:"name"`
	Status    health.Status `json:"status"`
	LatencyMs int64         `json:"latency_ms,omitempty"`
	Detail    string        `json:"detail,omitempty"`
}

// healthCache smooths out probe cost. /health is polled by Docker, Caddy, and
// any uptime monitor the operator points at it; running a database round-trip
// and a filesystem write probe on every one of those would be a self-inflicted
// load problem on a Raspberry Pi.
type healthCache struct {
	mu       sync.Mutex
	ttl      time.Duration
	cachedAt time.Time
	value    HealthResponse
	status   int
}

func (c *healthCache) get() (HealthResponse, int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cachedAt.IsZero() || time.Since(c.cachedAt) > c.ttl {
		return HealthResponse{}, 0, false
	}
	return c.value, c.status, true
}

func (c *healthCache) put(v HealthResponse, status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value, c.status, c.cachedAt = v, status, time.Now()
}

// handleHealth reports whether the service and its dependencies are usable.
//
// The status code is meaningful: 200 for healthy or degraded, 503 for
// unhealthy. Degraded returns 200 deliberately — a nearly-full disk is a
// warning for the administrator, not a reason for a load balancer to pull the
// instance out of service.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if cached, status, ok := s.health.get(); ok {
		WriteJSON(w, r, status, cached)
		return
	}

	// Bound the probe independently of the request: a hung database must
	// produce a fast "unhealthy", not a hung health check.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()

	resp := HealthResponse{
		Status:    health.Healthy,
		Version:   version.Version,
		UptimeSec: int64(time.Since(s.startedAt).Seconds()),
	}

	dbHealth := s.db.Check(ctx)
	resp.Database = dbHealth.Status
	resp.Checks = append(resp.Checks, HealthCheck{
		Name:      "database",
		Status:    dbHealth.Status,
		LatencyMs: dbHealth.LatencyMs,
		Detail:    dbHealth.Detail,
	})

	storageHealth := s.storage.Check()
	resp.Storage = storageHealth.Status
	resp.Checks = append(resp.Checks, HealthCheck{
		Name:   "storage",
		Status: storageHealth.Status,
		Detail: storageHealth.Detail,
	})

	resp.Status = health.Worst(dbHealth.Status, storageHealth.Status)

	httpStatus := http.StatusOK
	if !resp.Status.OK() {
		httpStatus = http.StatusServiceUnavailable
		logging.FromContext(r.Context()).Warn("health check reports unhealthy",
			"database", dbHealth.Status, "database_detail", dbHealth.Detail,
			"storage", storageHealth.Status, "storage_detail", storageHealth.Detail)
	}

	s.health.put(resp, httpStatus)
	WriteJSON(w, r, httpStatus, resp)
}

// handleLive answers whether the process is running at all. It touches no
// dependencies, so a database outage does not cause the container to be killed
// and restarted — restarting would not fix a database outage.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, r, http.StatusOK, map[string]any{
		"status":         health.Healthy,
		"uptime_seconds": int64(time.Since(s.startedAt).Seconds()),
	})
}

// handleReady answers whether the service can accept traffic. Unlike /health it
// is never cached, because startup ordering depends on an immediate answer.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	dbStatus := s.db.Check(ctx).Status
	storageStatus := s.storage.Check().Status
	overall := health.Worst(dbStatus, storageStatus)

	status := http.StatusOK
	if !overall.OK() {
		status = http.StatusServiceUnavailable
	}
	WriteJSON(w, r, status, map[string]any{
		"status":   overall,
		"database": dbStatus,
		"storage":  storageStatus,
	})
}

// handleVersion reports build metadata. Useful for verifying an upgrade landed.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, r, http.StatusOK, version.Current())
}
