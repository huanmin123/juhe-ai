package modelcheckruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckstore"
)

func TestW9HLoadConfigArms(t *testing.T) {
	// nil getenv 回落 os.Getenv（这里保证 disabled 默认，不依赖环境启用）。
	if _, err := LoadConfig(nil); err != nil {
		t.Fatalf("nil getenv err=%v", err)
	}
	disabled, err := LoadConfig(func(string) string { return "" })
	if err != nil || disabled.Enabled {
		t.Fatalf("disabled cfg=%+v err=%v", disabled, err)
	}
	_, enabled := LoadConfig(func(key string) string {
		if key == "JUHE_AI_MODEL_CHECK_ENABLED" {
			return "true"
		}
		return ""
	})
	if enabled == nil {
		t.Fatal("enabled config must fail closed in jobs runtime")
	}
	if !strings.Contains(enabled.Error(), "J3b") {
		t.Fatalf("enabled err=%v", enabled)
	}
}

func TestW9HDurationAndIntegerHelpers(t *testing.T) {
	if got := duration("", 5*time.Second); got != 5*time.Second {
		t.Fatalf("fallback duration=%v", got)
	}
	if got := duration("2m", time.Second); got != 2*time.Minute {
		t.Fatalf("parsed duration=%v", got)
	}
	if got := duration("bogus", time.Second); got != 0 {
		t.Fatalf("invalid duration=%v", got)
	}
	if got := integer("", 7); got != 7 {
		t.Fatalf("fallback integer=%d", got)
	}
	if got := integer("42", 7); got != 42 {
		t.Fatalf("parsed integer=%d", got)
	}
	if got := integer("bogus", 7); got != -1 {
		t.Fatalf("invalid integer=%d", got)
	}
}

func w9hRunRequest() RunRequest {
	policy, err := modelcheckinput.NewPolicySnapshot("w9h-policy", "quick", true, 70, "fallback", 10)
	if err != nil {
		panic(err)
	}
	return RunRequest{
		SystemAccountID: "sys_admin", ActorSystemAccountID: "actor",
		Target: modelcheckinput.AccountSnapshot{
			ID: "acc-1", ConfigRevision: "1", ProviderCode: "openai", ProtocolProfileID: "profile",
			ProtocolProfileRevision: "2", EndpointFingerprint: "fp", MappedUpstreamModel: "model",
			CredentialEnvelopeRef: "env", ProxyConfigurationVersion: "1",
		},
		Model: "model", Profile: "quick", ProbeSetVersion: "probe-set",
		Policy: policy, Trigger: modelcheckinput.TriggerManual,
		StartedAt:  time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
		DeadlineAt: time.Date(2026, 9, 6, 12, 1, 0, 0, time.UTC),
	}
}

func TestW9HRunNotInitializedAndInvalidRequest(t *testing.T) {
	// 空 service：Durable/Dataset/Resolver 缺失 → ErrNotInitialized。
	var nilService *Service
	if _, err := nilService.RunWithProgress(context.Background(), w9hRunRequest(), nil); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("nil service err=%v", err)
	}
	empty := &Service{}
	if _, err := empty.RunWithProgress(context.Background(), w9hRunRequest(), nil); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("empty service err=%v", err)
	}
	// validateRequest 直接表驱动（service 未初始化时 run 先返回
	// ErrNotInitialized，校验器需直测）。
	if err := validateRequest(w9hRunRequest()); err != nil {
		t.Fatalf("valid request err=%v", err)
	}
	broken := w9hRunRequest()
	broken.Model = ""
	if err := validateRequest(broken); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid request err=%v", err)
	}
	inconsistent := w9hRunRequest()
	inconsistent.TrustedComparison = true
	if err := validateRequest(inconsistent); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("inconsistent err=%v", err)
	}
	deadline := w9hRunRequest()
	deadline.DeadlineAt = deadline.StartedAt
	if err := validateRequest(deadline); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("deadline err=%v", err)
	}
}

func TestW9HStoreMappingHelpers(t *testing.T) {
	if triggerToStore(modelcheckinput.TriggerScheduled) != modelcheckstore.TriggerScheduled {
		t.Fatal("scheduled mapping")
	}
	if triggerToStore(modelcheckinput.TriggerQualityRecovery) != modelcheckstore.TriggerQualityRecovery {
		t.Fatal("quality recovery mapping")
	}
	if triggerToStore(modelcheckinput.TriggerManual) != modelcheckstore.TriggerManual {
		t.Fatal("manual mapping")
	}
	if itemStatus("passed") != modelcheckstore.ItemPassed {
		t.Fatal("passed mapping")
	}
	if itemStatus("warning") != modelcheckstore.ItemWarning {
		t.Fatal("warning mapping")
	}
	if itemStatus("skipped") != modelcheckstore.ItemSkipped {
		t.Fatal("skipped mapping")
	}
	if itemStatus("bogus") != modelcheckstore.ItemFailed {
		t.Fatal("default mapping")
	}
	if string(marshalEvidence(nil)) != `{}` {
		t.Fatal("empty evidence")
	}
	if string(marshalEvidence(map[string]any{"ok": true})) != `{"ok":true}` {
		t.Fatalf("evidence=%s", marshalEvidence(map[string]any{"ok": true}))
	}
	if string(marshalRequestSummary("input-1")) != `{"inputId":"input-1"}` {
		t.Fatalf("summary=%s", marshalRequestSummary("input-1"))
	}
}
