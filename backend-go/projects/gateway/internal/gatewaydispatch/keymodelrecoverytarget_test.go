package gatewaydispatch

// keymodelrecoverytarget_test.go — PLAN-20261008T113056000Z 根治阶段：
// dispatch Prepare 的 RecoveryTarget 登记补缺单测。
//
// 空转缺口：dispatchsingle.go 的 Prepare 调用此前不传 RecoveryTarget，
// memory store 的 recoveryTargets 恒空，KeyModelMemoryRecoveryRunner Sweep
// 时 GetRecoveryTarget 返回 nil 直接跳过（keymodelrecovery.go:233-235）。
// 本文件断言两件事：
//  1. gatewayKeyModelRecoveryTarget 的字段语义（Node recoveryTarget 契约，
//     key-model-attempt.ts:230-237）：group/system 取请求上下文
//     （GatewayFailureUsageContext，而非 AccountCandidate.BoundGroupID /
//     SystemAccountID），account 取 route.AccountID；任一缺失 → nil。
//  2. 端到端登记链：Prepare 传 Target 后 ReportUpstreamNotComplete 使
//     store recoveryTargets 可被 GetRecoveryTarget 取回；nil Target 臂
//     保持不登记（normalizeRecoveryTarget 语义：nil 跳过不报错）。

import (
	"context"
	"testing"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayaccounteffects"
	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewaypreauth"
)

func keyModelRecoveryTargetTestRoute() gatewayaccounteffects.GatewayKeyModelCapability {
	return gatewayaccounteffects.GatewayKeyModelCapability{
		AccountID: "km-target-acc",
		Capability: gatewayaccounteffects.CapabilityKey{
			CredentialSourceAccountID: "km-target-source",
			KeyFingerprint:            "km-target-fp",
			ClientModel:               "gpt-test",
			ClientEndpointFamily:      "chat_completions",
			FinalUpstreamModel:        "gpt-upstream",
			UpstreamEndpointMode:      "chat_json",
			DispatchRevision:          3,
		},
	}
}

func keyModelRecoveryTargetTestUsage() gatewaypreauth.GatewayFailureUsageContext {
	return gatewaypreauth.GatewayFailureUsageContext{
		TraceID:         "km-target-trace",
		SystemAccountID: "km-target-sys",
		GroupID:         "km-target-group",
	}
}

// 请求上下文语义：group/system 来自 usage context，account 来自 route 载体。
// 委托账户场景（candidate.BoundGroupID/SystemAccountID 与请求上下文不同）下
// 登记的必须是请求上下文——恢复探针按请求所在分组重建候选。
func TestGatewayKeyModelRecoveryTargetUsesRequestContext(t *testing.T) {
	route := keyModelRecoveryTargetTestRoute()
	usage := keyModelRecoveryTargetTestUsage()
	target := gatewayKeyModelRecoveryTarget(&route, &usage)
	if target == nil {
		t.Fatal("三字段齐全必须产出 RecoveryTarget")
	}
	if target.AccountID != "km-target-acc" || target.GroupID != "km-target-group" || target.SystemAccountID != "km-target-sys" {
		t.Fatalf("target = %+v, want accountId=km-target-acc groupId=km-target-group systemAccountId=km-target-sys", target)
	}
}

func TestGatewayKeyModelRecoveryTargetMissingArms(t *testing.T) {
	route := keyModelRecoveryTargetTestRoute()
	usage := keyModelRecoveryTargetTestUsage()
	if got := gatewayKeyModelRecoveryTarget(nil, &usage); got != nil {
		t.Fatalf("route 缺席必须返回 nil, got %+v", got)
	}
	if got := gatewayKeyModelRecoveryTarget(&route, nil); got != nil {
		t.Fatalf("usage 缺席必须返回 nil, got %+v", got)
	}
	noGroup := usage
	noGroup.GroupID = "  "
	if got := gatewayKeyModelRecoveryTarget(&route, &noGroup); got != nil {
		t.Fatalf("groupId 空白必须返回 nil（Node undefined 语义）, got %+v", got)
	}
	noSystem := usage
	noSystem.SystemAccountID = ""
	if got := gatewayKeyModelRecoveryTarget(&route, &noSystem); got != nil {
		t.Fatalf("systemAccountId 空必须返回 nil, got %+v", got)
	}
	noAccount := route
	noAccount.AccountID = " "
	if got := gatewayKeyModelRecoveryTarget(&noAccount, &usage); got != nil {
		t.Fatalf("accountId 空白必须返回 nil, got %+v", got)
	}
}

// 登记链端到端：Prepare(RecoveryTarget) → attempt ReportUpstreamNotComplete
// → memory store recoveryTargets 可被 GetRecoveryTarget 取回（值经
// normalizeRecoveryTarget 归一）。非 main-probe 臂（failure budget 可认领）。
func TestPreparedFailureRegistersRecoveryTarget(t *testing.T) {
	store := gatewayaccounteffects.NewInMemoryKeyModelRuntimeStore(gatewayaccounteffects.SystemClock{})
	route := keyModelRecoveryTargetTestRoute()
	usage := keyModelRecoveryTargetTestUsage()
	preparation, err := gatewayaccounteffects.PrepareGatewayKeyModelAttempt(context.Background(), store,
		gatewayaccounteffects.PrepareGatewayKeyModelAttemptInput{
			Route:          route,
			RequestID:      "km-target-req",
			AttemptID:      "km-target-attempt-1",
			FailureBudget:  gatewayaccounteffects.NewGatewayKeyModelFailureBudget(),
			RecoveryTarget: gatewayKeyModelRecoveryTarget(&route, &usage),
			Scheduler:      gatewayaccounteffects.NewManualScheduler(),
			Logger:         gatewayaccounteffects.NopLogger{},
		})
	if err != nil {
		t.Fatalf("PrepareGatewayKeyModelAttempt: %v", err)
	}
	if preparation.Status != gatewayaccounteffects.AttemptPreparationAdmitted || preparation.Attempt == nil {
		t.Fatalf("preparation = %s, want admitted+attempt", preparation.Status)
	}
	if err := preparation.Attempt.ReportUpstreamNotComplete(context.Background()); err != nil {
		t.Fatalf("ReportUpstreamNotComplete: %v", err)
	}
	select {
	case <-preparation.Attempt.WaitTerminal():
	default:
		t.Fatal("失败上报必须结算（WaitTerminal 未关闭）")
	}
	registered := store.GetRecoveryTarget(route.Capability)
	if registered == nil {
		t.Fatal("失败登记后 GetRecoveryTarget 必须返回 target（空转缺口回归断言）")
	}
	if registered.AccountID != "km-target-acc" || registered.GroupID != "km-target-group" || registered.SystemAccountID != "km-target-sys" {
		t.Fatalf("registered = %+v, want 与登记 target 一致", registered)
	}
}

// nil Target 臂：不传 RecoveryTarget 的失败不登记（store 查询返回 nil），
// 与既有行为一致（normalizeRecoveryTarget 仅对非 nil intent 生效）。
func TestPreparedFailureWithoutTargetSkipsRegistration(t *testing.T) {
	store := gatewayaccounteffects.NewInMemoryKeyModelRuntimeStore(gatewayaccounteffects.SystemClock{})
	route := keyModelRecoveryTargetTestRoute()
	preparation, err := gatewayaccounteffects.PrepareGatewayKeyModelAttempt(context.Background(), store,
		gatewayaccounteffects.PrepareGatewayKeyModelAttemptInput{
			Route:         route,
			RequestID:     "km-target-req-2",
			AttemptID:     "km-target-attempt-2",
			FailureBudget: gatewayaccounteffects.NewGatewayKeyModelFailureBudget(),
			Scheduler:     gatewayaccounteffects.NewManualScheduler(),
			Logger:        gatewayaccounteffects.NopLogger{},
		})
	if err != nil {
		t.Fatalf("PrepareGatewayKeyModelAttempt: %v", err)
	}
	if err := preparation.Attempt.ReportUpstreamNotComplete(context.Background()); err != nil {
		t.Fatalf("ReportUpstreamNotComplete: %v", err)
	}
	if registered := store.GetRecoveryTarget(route.Capability); registered != nil {
		t.Fatalf("nil RecoveryTarget 不登记, got %+v", registered)
	}
}
