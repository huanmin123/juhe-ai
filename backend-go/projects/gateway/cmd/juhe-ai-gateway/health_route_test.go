package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/auditlog"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/pgpool"
	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

// composeTestOwnerHealth provides a ready-state owner health for compose
// tests: audit running, optional owner families disabled, active mode. All
// pointers are wired — readiness()/payload mirror the production contract
// where main() passes a real atomic for every field.
func composeTestOwnerHealth() *gatewayOwnerHealth {
	auditRunning := &atomic.Bool{}
	auditRunning.Store(true)
	operationRunning := &atomic.Bool{}
	j3bRunning := &atomic.Bool{}
	retentionRunning := &atomic.Bool{}
	circuitRuntimeRunning := &atomic.Bool{}
	return &gatewayOwnerHealth{
		ownerMode:             ownermode.Active,
		auditRunning:          auditRunning,
		operationEnabled:      false,
		operationRunning:      operationRunning,
		j3bWired:              false,
		j3bRunning:            j3bRunning,
		retentionEnabled:      false,
		retentionRunning:      retentionRunning,
		circuitRuntimeEnabled: false,
		circuitRuntimeRunning: circuitRuntimeRunning,
	}
}

func TestGatewayOwnerHealthReadiness(t *testing.T) {
	health := composeTestOwnerHealth()
	status, payload := health.readiness()
	if status != http.StatusOK || payload["ready"] != true {
		t.Fatalf("ready state must be 200/true, got %d/%v", status, payload["ready"])
	}
	health.auditRunning.Store(false)
	status, payload = health.readiness()
	if status != http.StatusServiceUnavailable || payload["ready"] != false {
		t.Fatalf("audit down must be 503/false, got %d/%v", status, payload["ready"])
	}
}

func TestGatewayOwnerHealthDisabledComponentsIgnored(t *testing.T) {
	health := composeTestOwnerHealth()
	if _, payload := health.readiness(); payload["ready"] != true {
		t.Fatalf("disabled optional families must not degrade readiness: %v", payload)
	}
}

func TestComposeSystemAPIRequiresOwnerHealthWiring(t *testing.T) {
	if _, err := composeSystemAPI(composeTestConfig(t), pgpool.NewRegistry(), nil, nil, nil, auditlog.Config{}, nil); err == nil {
		t.Fatal("compose must fail fast when owner health state is not wired")
	}
}

func TestComposeSystemAPIServesAISysHealth(t *testing.T) {
	cfg := composeTestConfig(t)
	store := openComposeOperationStore(t)
	createRuntimeLogDataset(t, cfg.RuntimeLogDatabasePath)
	auditConfig, auditProducer, closeAudit := openComposeAuditSources(t, filepath.Dir(cfg.DatasetDatabasePath))
	defer closeAudit()
	ownerHealth := composeTestOwnerHealth()
	composed, err := composeSystemAPI(cfg, pgpool.NewRegistry(), store, openComposeOperationLease(t, store), auditProducer, auditConfig, ownerHealth)
	if err != nil {
		t.Fatalf("compose system api: %v", err)
	}
	defer composed.Shutdown()
	seedSystemSettings(t, composed.DB)

	server := httptest.NewServer(composed.Kernel)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	get := func(path string) *http.Response {
		response, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		return response
	}

	// Liveness face: same aggregated owner readiness as loopback /health.
	health := get("/__aisys__/health")
	if health.StatusCode != http.StatusOK {
		t.Fatalf("/__aisys__/health status=%d", health.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(health.Body).Decode(&payload); err != nil {
		t.Fatalf("decode /__aisys__/health body: %v", err)
	}
	if payload["ready"] != true || payload["ownerMode"] != string(ownermode.Active) {
		t.Fatalf("/__aisys__/health payload=%#v", payload)
	}

	// Liveness failure face: audit owner down flips the route to 503 with the
	// same JSON contract (route precedence over the SPA catch-all is enforced
	// by ServeMux exact-pattern matching and covered by the helpweb suite).
	ownerHealth.auditRunning.Store(false)
	degraded := get("/__aisys__/health")
	if degraded.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/__aisys__/health degraded status=%d", degraded.StatusCode)
	}
}
