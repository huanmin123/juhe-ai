package main

import (
	"context"
	"testing"
	"time"

	gatewaydispatch "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/gateway_dispatch"
	keymodelruntime "github.com/huanminabc/juhe-ai/backend-go-gateway/internal/business/key_model_runtime"
)

// 编译期确认零配置 memory 回退满足 Dispatcher 的 key-model 准入端口。
var _ gatewaydispatch.KeyModelGate = (*j3bMemoryKeyModelGate)(nil)

func TestJ3bMemoryKeyModelGateAdmitSettleLifecycle(t *testing.T) {
	gate := newJ3bMemoryKeyModelGate()
	capability := keymodelruntime.Capability{
		CredentialSourceAccountID: "acc-1",
		KeyFingerprint:            "fp-1",
		ClientModel:               "gpt-5.6",
		ClientEndpointFamily:      "responses",
		FinalUpstreamModel:        "gpt-5.6",
		UpstreamEndpointMode:      "responses",
		DispatchRevision:          1,
	}
	decision, permit, _, err := gate.AdmitForeground(context.Background(), capability, "attempt-1")
	if err != nil || decision != keymodelruntime.ForegroundAdmitted {
		t.Fatalf("admit=%v decision=%v err=%v", permit, decision, err)
	}
	renewed, ok, err := gate.RenewForeground(context.Background(), permit)
	if err != nil || !ok || renewed.AttemptID != permit.AttemptID {
		t.Fatalf("renew ok=%v err=%v", ok, err)
	}
	status, _, err := gate.RecordFailureIntent(context.Background(), keymodelruntime.FailureIntent{
		IntentID: "attempt-1", RequestID: "attempt-1", AttemptID: "attempt-1",
		Capability: capability, ObservedAt: time.Now().UTC(), Permit: &permit,
	})
	if err != nil {
		t.Fatalf("record failure intent: %v", err)
	}
	switch status {
	case keymodelruntime.StatusApplied, keymodelruntime.StatusIdempotent, keymodelruntime.StatusNotDue:
	default:
		t.Fatalf("unexpected mutation status: %v", status)
	}
	if released, err := gate.ReleaseForeground(context.Background(), permit); err != nil || !released {
		t.Fatalf("release released=%v err=%v", released, err)
	}
	// 失败记账后同 capability 再次准入仍应成功（memory 阈值未触顶）。
	if _, _, _, err := gate.AdmitForeground(context.Background(), capability, "attempt-2"); err != nil {
		t.Fatalf("re-admit: %v", err)
	}
}
