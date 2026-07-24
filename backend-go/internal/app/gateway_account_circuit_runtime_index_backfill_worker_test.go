package app

import (
	"strings"
	"testing"

	"juhe-ai/backend-go/internal/config"
)

func TestGatewayAccountCircuitRuntimeIndexBackfillWorkerIsDisabledByDefault(t *testing.T) {
	if err := RunGatewayAccountCircuitRuntimeIndexBackfillWorker(t.Context(), config.Config{}, nil, GatewayAccountCircuitRuntimeIndexBackfillWorkerOptions{}); err != nil {
		t.Fatalf("disabled backfill worker error = %v", err)
	}
}

func TestGatewayAccountCircuitRuntimeIndexBackfillWorkerRequiresOwnerAndDrain(t *testing.T) {
	err := RunGatewayAccountCircuitRuntimeIndexBackfillWorker(t.Context(), config.Config{}, nil, GatewayAccountCircuitRuntimeIndexBackfillWorkerOptions{Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "exclusive runtime-state") {
		t.Fatalf("backfill worker error = %v", err)
	}
	err = RunGatewayAccountCircuitRuntimeIndexBackfillWorker(t.Context(), config.Config{}, nil, GatewayAccountCircuitRuntimeIndexBackfillWorkerOptions{Enabled: true, GoExclusiveRuntimeStateOwner: true})
	if err == nil || !strings.Contains(err.Error(), "drained") {
		t.Fatalf("backfill worker error = %v", err)
	}
	err = RunGatewayAccountCircuitRuntimeIndexBackfillWorker(t.Context(), config.Config{}, nil, GatewayAccountCircuitRuntimeIndexBackfillWorkerOptions{Enabled: true, GoExclusiveRuntimeStateOwner: true, LegacyRuntimeWritersDrained: true})
	if err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("backfill worker error = %v", err)
	}
	err = RunGatewayAccountCircuitRuntimeIndexBackfillWorker(t.Context(), config.Config{}, nil, GatewayAccountCircuitRuntimeIndexBackfillWorkerOptions{Enabled: true, GoExclusiveRuntimeStateOwner: true, LegacyRuntimeWritersDrained: true, RuntimeStateWritesPaused: true})
	if err == nil || !strings.Contains(err.Error(), "control-plane") {
		t.Fatalf("backfill worker error = %v", err)
	}
}

func TestGatewayAccountCircuitRuntimeIndexBackfillWorkerValidatesConfigurationBeforeRedis(t *testing.T) {
	opts := GatewayAccountCircuitRuntimeIndexBackfillWorkerOptions{Enabled: true, GoExclusiveRuntimeStateOwner: true, LegacyRuntimeWritersDrained: true, RuntimeStateWritesPaused: true, ControlPlaneWritesPaused: true, MaxPages: -1}
	err := RunGatewayAccountCircuitRuntimeIndexBackfillWorker(t.Context(), config.Config{RedisStateURL: "://must-not-open"}, nil, opts)
	if err == nil || !strings.Contains(err.Error(), "bounds") {
		t.Fatalf("backfill worker error = %v", err)
	}
}
