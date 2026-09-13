package main

// w1: compose.go / chain_compose.go 的组合根小适配器直测（K5 失效总线桥、
// 设置源、日志桥、spool 溢出、body 拒绝记录器的门控路径）。

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaybody"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaydispatch"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayhybrid"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayusage"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/inval"
)

func TestW1SettingsAndUsageAdapters(t *testing.T) {
	// settingsTimezone：委托读取键名。
	read := func(key string) (string, error) {
		if key != "usageStatsTimezone" {
			t.Fatalf("timezone 键 = %q", key)
		}
		return "Asia/Shanghai", nil
	}
	zone, err := settingsTimezone(read)(context.Background())
	if err != nil || zone != "Asia/Shanghai" {
		t.Fatalf("timezone = %q, %v", zone, err)
	}
	// delegatedSettingsAdapter。
	adapter := delegatedSettingsAdapter{read: read}
	if value, err := adapter.SettingValue("usageStatsTimezone"); err != nil || value != "Asia/Shanghai" {
		t.Fatalf("adapter = %q, %v", value, err)
	}
	// unavailableUsageReader：降级契约。
	if value, err := (unavailableUsageReader{}).RequestLimitTotal(context.Background(), "k"); err == nil || value != "" {
		t.Fatalf("degraded = %q, %v", value, err)
	}
	// producerLogger：不 panic 即可。
	(producerLogger{}).Warn("w", "k", "v")
	(producerLogger{}).Error("e", "k", "v")
	// devAutoLoginResolver：未配置用户名 → nil。
	if devAutoLoginResolver(nil, "") != nil {
		t.Fatal("空用户名必须 nil")
	}
}

func TestW1BusInvalidatorAdapters(t *testing.T) {
	// nil bus：错误契约（未接线）。
	authzNil := authsysBusInvalidator{}
	authzNil.InvalidateRuntime("reason")
	if err := authzNil.InvalidateAPIKeyValidation("reason"); err == nil {
		t.Fatal("nil bus 必须报未接线错误")
	}
	accountsNil := accountsBusInvalidator{}
	if err := accountsNil.InvalidateAccountLookup("acc"); err != nil {
		t.Fatalf("lookup no-op 必须成功: %v", err)
	}
	if err := accountsNil.ClearResourceAuthorizationLookupCaches(); err != nil {
		t.Fatalf("clear no-op 必须成功: %v", err)
	}
	for name, call := range map[string]func() error{
		"gateway runtime":  func() error { return accountsNil.InvalidateGatewayRuntime("r") },
		"group account":    func() error { return accountsNil.InvalidateGroupAccountIds() },
		"authz quota":      func() error { return accountsNil.InvalidateAuthorizationQuota("r") },
		"runtime relay":    func() error { return accountsNil.InvalidateRuntime("r") },
		"key validation":   func() error { return accountsNil.InvalidateAPIKeyValidation("r") },
		"authz validation": func() error { return authzNil.InvalidateAPIKeyValidation("r") },
	} {
		if err := call(); err == nil {
			t.Fatalf("%s nil bus 必须报错", name)
		}
	}
	// 已接线 bus：全部成功。
	bus := inval.New(nil)
	wiredAuthz := authsysBusInvalidator{bus: bus}
	if err := wiredAuthz.InvalidateAPIKeyValidation("reason"); err != nil {
		t.Fatalf("wired authz: %v", err)
	}
	wiredAccounts := accountsBusInvalidator{bus: bus}
	for name, call := range map[string]func() error{
		"gateway runtime": func() error { return wiredAccounts.InvalidateGatewayRuntime("r") },
		"group account":   func() error { return wiredAccounts.InvalidateGroupAccountIds() },
		"authz quota":     func() error { return wiredAccounts.InvalidateAuthorizationQuota("r") },
		"runtime relay":   func() error { return wiredAccounts.InvalidateRuntime("r") },
		"key validation":  func() error { return wiredAccounts.InvalidateAPIKeyValidation("r") },
	} {
		if err := call(); err != nil {
			t.Fatalf("%s wired 必须成功: %v", name, err)
		}
	}
}

func TestW1GatewaybodyLoggerAndSpoolOverflow(t *testing.T) {
	logger := gatewaybodyLogger{inner: slog.Default()}
	logger.Debug("d", map[string]any{"k": "v"})
	logger.Info("i", nil)
	logger.Warn("w", map[string]any{})
	logger.Error("e", map[string]any{"k": 1})
	// spoolOverflow：nil spool 吞掉；有 spool 透传。
	if err := (spoolOverflow{}).PersistOverflow(gatewayusage.Ctx(context.Background()), gatewayusage.UsageRecordInput{}); err != nil {
		t.Fatalf("nil spool 必须成功: %v", err)
	}
}

func TestW1AuxiliaryDispatchHelpers(t *testing.T) {
	input := gatewayhybrid.AuxiliaryDispatchInput{TraceID: "trace_aux", TargetModel: "m", Endpoint: "/v1/chat/completions"}
	// auxiliaryDispatchFailure：字段投影。
	_, failure := auxiliaryDispatchFailure(input, "code_1", "消息", nil, "grp_1", true, 502, true, true)
	if failure.ErrorCode != "code_1" || failure.ErrorMessage != "消息" || !failure.HasGroupID || failure.GroupID != "grp_1" {
		t.Fatalf("failure = %+v", failure)
	}
	if !failure.HasStatusCode || failure.StatusCode != 502 || !failure.ShouldRecordUsage {
		t.Fatalf("failure 状态 = %+v", failure)
	}
	// wireChainHybridAuxiliaryTransport：非具体类型安全。
	wireChainHybridAuxiliaryTransport(nil, gatewaydispatch.TransportDeps{})
	var dispatcher hybridAuxiliaryDispatcher = &chainHybridAuxiliaryDispatcher{}
	wireChainHybridAuxiliaryTransport(dispatcher, gatewaydispatch.TransportDeps{})
	// 具体类型：注入后 transport 落位。
	concrete := &chainHybridAuxiliaryDispatcher{}
	wireChainHybridAuxiliaryTransport(concrete, gatewaydispatch.TransportDeps{})
	_ = concrete
	// chainHybridAuxiliaryDispatcher nil / 无 cache：失败臂。
	if _, failure := concrete.DispatchHybridAuxiliaryChatCompletion(context.Background(), input); failure == nil {
		t.Fatal("无 cache 必须走失败臂")
	}
	var nilDispatcher *chainHybridAuxiliaryDispatcher
	if _, failure := nilDispatcher.DispatchHybridAuxiliaryChatCompletion(context.Background(), input); failure == nil {
		t.Fatal("nil dispatcher 必须走失败臂")
	}
}

func TestW1ChainBodyRejectionRecorderGated(t *testing.T) {
	recorder := &chainBodyRejectionRecorder{}
	request := httptest.NewRequest("POST", "/v1/chat/completions?q=1", nil)
	payload := gatewaybody.GatewayErrorPayload("请求过大", "request_too_large", "")
	// audit / usage 全 nil：走门控早退，不 panic。
	recorder.RecordGatewayBodyRejection(request, nil, gatewaybody.RejectionInput{
		StatusCode:      413,
		ResponsePayload: payload,
		Reason:          gatewaybody.RejectReasonGatewayBodyAdmission,
		ErrorCode:       "request_body_too_large",
		ErrorMessage:    "",
	})
	// nil auditEnabled：同样早退。
	recorder2 := &chainBodyRejectionRecorder{audit: nopAuditDispatcher{}}
	recorder2.RecordGatewayBodyRejection(request, nil, gatewaybody.RejectionInput{
		StatusCode: 413, ResponsePayload: payload,
	})
	// auditEnabled false：早退。
	recorder3 := &chainBodyRejectionRecorder{audit: nopAuditDispatcher{}, auditEnabled: func() bool { return false }}
	recorder3.RecordGatewayBodyRejection(request, nil, gatewaybody.RejectionInput{StatusCode: 413, ResponsePayload: payload})
	// auditEnabled true：dispatchDroppedAudit 走完（audit sink 是 no-op）。
	recorder4 := &chainBodyRejectionRecorder{audit: nopAuditDispatcher{}, auditEnabled: func() bool { return true }, clock: gatewaypreauth.SystemClock{}}
	recorder4.RecordGatewayBodyRejection(request, nil, gatewaybody.RejectionInput{StatusCode: 413, ResponsePayload: payload})
	_ = errors.New
}

// nopAuditDispatcher 是 gatewaypreauth.AuditDispatcher 的 no-op 实现。
type nopAuditDispatcher struct{}

func (nopAuditDispatcher) Dispatch(input gatewaypreauth.DispatchedAuditLogInput) {}
