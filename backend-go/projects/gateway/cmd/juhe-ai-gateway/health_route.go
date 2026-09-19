package main

import (
	"errors"
	"net/http"
	"sync/atomic"

	"github.com/huanminabc/juhe-ai/backend-go-platform/ownermode"
)

// gatewayOwnerHealth aggregates this process' resident owner readiness for
// both health faces: the loopback /health listener (main.go) and the main
// port's GET /__aisys__/health route (compose.go). Single readiness source —
// the hybrid-era exec healthcheck and the post-cutover HTTP liveness probe
// must report identical owner facts.
type gatewayOwnerHealth struct {
	ownerMode             ownermode.Mode
	auditRunning          *atomic.Bool
	operationEnabled      bool
	operationRunning      *atomic.Bool
	j3bWired              bool
	j3bRunning            *atomic.Bool
	retentionEnabled      bool
	retentionRunning      *atomic.Bool
	circuitRuntimeEnabled bool
	circuitRuntimeRunning *atomic.Bool
}

// validate rejects a partially-wired health state: every owner family the
// payload reports must carry a real atomic, mirroring the main() wiring —
// a nil atomic here would panic at request time instead of failing fast at
// composition time.
func (h *gatewayOwnerHealth) validate() error {
	if h.auditRunning == nil || h.operationRunning == nil || h.j3bRunning == nil || h.retentionRunning == nil || h.circuitRuntimeRunning == nil {
		return errors.New("owner 健康聚合状态必须为每个 owner 家族接线真实 atomic（main 构造契约）")
	}
	return nil
}

// readiness mirrors the loopback /health contract: 200 when every enabled
// owner component reports running, 503 otherwise.
func (h *gatewayOwnerHealth) readiness() (int, map[string]any) {
	ready := h.auditRunning.Load() &&
		(!h.operationEnabled || h.operationRunning.Load()) &&
		(!h.j3bWired || h.j3bRunning.Load()) &&
		(!h.retentionEnabled || h.retentionRunning.Load()) &&
		(!h.circuitRuntimeEnabled || h.circuitRuntimeRunning.Load())
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	return status, map[string]any{
		"ready":                      ready,
		"ownerReady":                 ready,
		"ownerMode":                  h.ownerMode,
		"auditLogReady":              h.auditRunning.Load(),
		"operationLogReady":          h.operationRunning.Load(),
		"j3bReady":                   h.j3bRunning.Load(),
		"sessionRetentionReady":      h.retentionRunning.Load(),
		"accountCircuitRuntimeReady": h.circuitRuntimeRunning.Load(),
	}
}
