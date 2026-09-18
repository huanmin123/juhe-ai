package gatewaydispatch

// w14e 覆盖率补强：usageheaders / capacity / shims / transport_urlpolicy /
// keymodelcapability / builtintools / errors 的剩余可达臂（第二批）。

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

func int64PtrW14E(value int64) *int64 { return &value }

func gatewayaccounteffectsCapabilityKeyForW14E() gatewayaccounteffects.CapabilityKey {
	return gatewayaccounteffects.CapabilityKey{
		CredentialSourceAccountID: "w14e-km",
		KeyFingerprint:            "fp",
		ClientModel:               "gpt-x",
		ClientEndpointFamily:      "chat_completions",
		FinalUpstreamModel:        "gpt-x-up",
		UpstreamEndpointMode:      "chat_json",
		DispatchRevision:          3,
	}
}

func TestW14EUsageHeadersNumberAndResetArms(t *testing.T) {
	if numberValueOf("not-a-number") != nil {
		t.Fatal("invalid number must return nil")
	}
	if got := numberValueOf(" 42.5 "); got == nil || *got != 42.5 {
		t.Fatalf("number value = %v", got)
	}
	if got := resetAtFromSeconds(time.UnixMilli(0), nil); got != "" {
		t.Fatalf("nil seconds reset = %q", got)
	}
	negative := int64(-5)
	if got := resetAtFromSeconds(time.UnixMilli(0), &negative); got == "" {
		t.Fatal("negative seconds must clamp to zero")
	}
	seconds := int64(120)
	if got := resetAtFromSeconds(time.UnixMilli(0), &seconds); got != "1970-01-01T00:02:00.000Z" {
		t.Fatalf("reset = %q", got)
	}
	if parseIsoDate("not-a-date") != nil {
		t.Fatal("invalid date must return nil")
	}
	if got := parseIsoDate("2026-01-02T03:04:05+0000"); got == nil {
		t.Fatal("loose ISO form must parse")
	}
	if got := parseIsoDate("2026-01-02T03:04:05Z"); got == nil {
		t.Fatal("strict ISO form must parse")
	}
	if got := ParseOpenAICodexUsageHeaders(nil); got != nil {
		t.Fatal("nil headers must return nil snapshot")
	}
	if got := ParseOpenAICodexUsageHeaders(http.Header{"X-Other": {"1"}}); got != nil {
		t.Fatal("headers without codex data must return nil")
	}
	snapshot := ParseOpenAICodexUsageHeaders(http.Header{
		"X-Codex-Primary-Used-Percent":          {"10.5"},
		"X-Codex-Primary-Reset-After-Seconds":   {"3600"},
		"X-Codex-Primary-Window-Minutes":        {"60"},
		"X-Codex-Secondary-Used-Percent":        {"20"},
		"X-Codex-Secondary-Reset-After-Seconds": {"60"},
		"X-Codex-Secondary-Window-Minutes":      {"5"},
		"X-Codex-Primary-Over-Secondary-Limit-Percent": {"1.5"},
	})
	if snapshot == nil || snapshot.PrimaryUsedPercent == nil || snapshot.SecondaryWindowMinutes == nil {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if PersistOpenAICodexUsageHeaders(nil, "acc", http.Header{}, "gateway") {
		t.Fatal("persist without data must return false")
	}
}

func TestW14EAssignNormalizedWindowArms(t *testing.T) {
	normalized := &NormalizedCodexLimits{}
	// 窗口分钟非法（<=0）直接跳过。
	zeroMinutes := codexWindowCandidate{windowMinutes: int64PtrW14E(0)}
	assignNormalizedWindow(normalized, "5h", zeroMinutes)
	// 全空候选跳过。
	empty := codexWindowCandidate{}
	assignNormalizedWindow(normalized, "5h", empty)
	// 5h 与 weekly 各自落位。
	used := 42.0
	seconds := int64(3600)
	minutes := int64(60)
	assignNormalizedWindow(normalized, "5h", codexWindowCandidate{usedPercent: &used, resetAfterSeconds: &seconds, windowMinutes: &minutes})
	assignNormalizedWindow(normalized, "7d", codexWindowCandidate{usedPercent: &used, resetAfterSeconds: &seconds})
	if normalized.Used5hPercent == nil || normalized.Used7dPercent == nil {
		t.Fatalf("normalized = %+v", normalized)
	}
}

func TestW14EShimsAndCapacityArms(t *testing.T) {
	// shims：nil policy 与带 policy 两个分支。
	if got := gatewayhotqualityEffectiveImageLaneConcurrencyLimit(5, nil); got != 5 {
		t.Fatalf("nil policy limit = %d", got)
	}
	policy := gatewayruntimecache.GroupSchedulingPolicy{"imageLaneMaxConcurrency": 2}
	if got := gatewayhotqualityEffectiveImageLaneConcurrencyLimit(9, &policy); got != 2 {
		t.Fatalf("policy limit = %d", got)
	}

	// capacity：空 runtime key 跳过 + 取更小上限的合并分支。
	accounts := []AccountCandidate{
		{ID: "w14e-cap", ConcurrencyLimit: 7},
		{ID: "w14e-cap", ConcurrencyLimit: 3},
	}
	limits := GatewayAccountConcurrencyLimitsByAccountID(accounts)
	if _, ok := limits[""]; ok {
		t.Fatal("empty account id must be skipped")
	}
	if got := limits["w14e-cap"]; got != 3 {
		t.Fatalf("merged limit = %d", got)
	}
}

func TestW14EBuiltinToolsJSONValueEqualArms(t *testing.T) {
	if jsonValueEqual(func() {}, nil) {
		t.Fatal("unmarshalable input must not be equal")
	}
	if !jsonValueEqual(map[string]any{"a": 1.0}, map[string]any{"a": 1.0}) {
		t.Fatal("equal structures must compare equal")
	}
	if jsonValueEqual(map[string]any{"a": 1.0}, map[string]any{"a": 2.0}) {
		t.Fatal("different structures must not compare equal")
	}
}

func TestW14EKeyModelCapabilityArms(t *testing.T) {
	// 缺 model/family/fingerprint/revision 时返回 nil。
	bare := AccountCandidate{ID: "w14e-km"}
	if got := gatewayKeyModelCapabilityForRoute(bare, "", "chat", false); got != nil {
		t.Fatalf("empty model capability = %+v", got)
	}
	// 主探针匹配：health check 模型不一致直接 false。
	capability := gatewayaccounteffectsCapabilityKeyForW14E()
	probeAccount := AccountCandidate{ID: "w14e-km", HealthCheckModel: "gpt-x", HealthCheckEndpointMode: "chat_json"}
	if gatewayKeyModelRouteMatchesMainProbe(bare, "other-model", "chat_completions", false, capability) {
		t.Fatal("model mismatch must not match main probe")
	}
	if gatewayKeyModelRouteMatchesMainProbe(probeAccount, "gpt-x", "responses", false, capability) {
		t.Fatal("endpoint family mismatch must not match main probe")
	}
	// MergePermitLostSignal：nil lost 通道返回父 context。
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	if MergePermitLostSignal(parent, nil) != parent {
		t.Fatal("nil lost channel must return parent")
	}
	// lost 通道关闭后派生 context 取消。
	lost := make(chan struct{})
	derived := MergePermitLostSignal(parent, lost)
	close(lost)
	<-derived.Done()
}

func TestW14ETransportURLPolicyNilArms(t *testing.T) {
	var nilPolicy *ResolvedUpstreamURLPolicy
	if nilPolicy.Guard() != nil {
		t.Fatal("nil policy guard must be nil")
	}
	if _, err := nilPolicy.PrepareSafeUpstreamRequestURL(context.Background(), "http://example.com"); err != nil {
		t.Fatalf("nil policy passthrough = %v", err)
	}
	guardless := &ResolvedUpstreamURLPolicy{}
	if _, err := guardless.PrepareSafeUpstreamRequestURL(context.Background(), "http://example.com"); err != nil {
		t.Fatalf("guard-less passthrough = %v", err)
	}
}

func TestW14EProvenTransportPipeError(t *testing.T) {
	cause := &StartedBodyTransportError{Err: errorsNewW14E("pipe boom")}
	pipe := &NonStreamUpstreamBodyPipeError{Message: "pipe broke", OriginalError: cause}
	if !IsProvenUpstreamBodyTransportError(pipe) {
		t.Fatal("pipe error wrapping a started transport error must be proven")
	}
	if pipe.Error() != "pipe broke" {
		t.Fatalf("pipe error text = %q", pipe.Error())
	}
}
