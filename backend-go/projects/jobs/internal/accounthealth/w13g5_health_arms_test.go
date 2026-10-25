package accounthealth

// w13g5_health_arms_test.go 覆盖 store/executor/crypto/config/probe/
// direct_input/scheduler 的输入校验臂与深层 err 传播臂。
//
// 策略：
//  1. 关闭句柄批量错误臂——EnsureSchema 后关闭底层 DB，逐个调用公开方法，
//     所有语句级 err 臂在一次批量中断言传播（SQLite 句柄关闭后任何查询必错）；
//  2. 白盒直调纯函数校验臂；
//  3. 数据驱动扫描/解析错误臂。
//
// 不可达清单（覆盖率登记，证据）：
//   - executor.go newOutcomeID 的 rand.Read 失败回退分支：crypto/rand 无
//     注入点，失败即进程级故障；
//   - crypto.go EncryptV1Envelope 的 rand.Read 失败与 GCM 输出 <16 字节
//     防御臂：GCM Seal 对任意输入恒产出 16 字节 tag，不可构造；
//   - direct_input.go directProbeTarget 错误臂：依赖 accountprobe
//     ResolveHybridProbeTarget 的视图（候选源）上下文，单元级不可构造。

import (
	"context"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
	"strings"
	"testing"
	"time"

)

func w13g5HealthOutcome() Outcome {
	return Outcome{
		OutcomeID: "w13g5-outcome", RequestID: "w13g5-request", AccountID: "w13g5-acc",
		Outcome: "success", ObservedAt: time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC),
		InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1,
	}
}

func TestW13g5HealthCryptoArms(t *testing.T) {
	if _, err := exactkeyprobe.EncryptV1Envelope("  ", []byte("data")); err == nil {
		t.Fatal("空 secret 必须报错")
	}
	envelope, err := exactkeyprobe.EncryptV1Envelope("w13g5-secret", []byte("w13g5-plaintext"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := exactkeyprobe.DecryptV1Envelope("w13g5-secret", envelope)
	if err != nil || string(plaintext) != "w13g5-plaintext" {
		t.Fatalf("回环失败: %s %v", plaintext, err)
	}
	// 错 secret → 认证失败。
	if _, err := exactkeyprobe.DecryptV1Envelope("w13g5-other", envelope); err == nil {
		t.Fatal("错 secret 必须认证失败")
	}
	// 坏 envelope：部分缺失 / 非法 base64 / 非法长度。
	if _, err := exactkeyprobe.DecryptV1Envelope("w13g5-secret", "v1"); err == nil {
		t.Fatal("不完整 envelope 必须报错")
	}
	parts := strings.Split(envelope, ":")
	if _, err := exactkeyprobe.DecryptV1Envelope("w13g5-secret", parts[0]+":"+parts[1]+":!!!:!!!"); err == nil {
		t.Fatal("非法 base64 必须报错")
	}
	if _, err := exactkeyprobe.DecryptV1Envelope("w13g5-secret", parts[0]+":"+parts[1]+":AQ:AQ"); err == nil {
		t.Fatal("非法 IV/tag 长度必须报错")
	}
}

// TestW13g5HealthDirectHelpers 的 openAIProfileMode/mustJSON/directTime 臂随
// 探针执行器闭包下沉 exactkeyprobe（同名测试），此处保留 directInputScanCap/
// directSettingInt 留守臂。
func TestW13g5HealthDirectHelpers(t *testing.T) {
	// directInputScanCap：16x 准入窗口。
	if got := directInputScanCap(64); got != 64*16 {
		t.Fatalf("16 倍窗口: %d", got)
	}
	if got := directInputScanCap(0); got != 0 {
		t.Fatalf("零 limit 原样: %d", got)
	}
	// directSettingInt 边界（设置值为 JSON 编码数字）。
	got, err := directSettingInt(map[string]string{"w13g5-key": "5"}, "w13g5", "w13g5-key", 1, 10)
	if err != nil || got != 5 {
		t.Fatalf("合法设置读取: %d %v", got, err)
	}
	if _, err := directSettingInt(map[string]string{"w13g5-key": "not-int"}, "w13g5", "w13g5-key", 1, 10); err == nil {
		t.Fatal("非法数字必须报错")
	}
	if _, err := directSettingInt(map[string]string{"w13g5-key": "99"}, "w13g5", "w13g5-key", 1, 10); err == nil {
		t.Fatal("越界必须报错")
	}
}

func TestW13g5HealthSchedulerPureArms(t *testing.T) {
	// sourceFenceHealthMutationAllowed。
	if sourceFenceHealthMutationAllowed(exactkeyprobe.Input{}, CurrentState{}, false) {
		t.Fatal("无 fence 无 prior 时不得允许健康突变")
	}
	// preserveStateForSourceOnlyOutcome：fence 一致 → 保留 prior 状态。
	prior := CurrentState{AccountID: "w13g5-acc", InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1, FailureCount: 3}
	outcome := w13g5HealthOutcome()
	input := exactkeyprobe.Input{InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1}
	preserveStateForSourceOnlyOutcome(&outcome, input, prior, true)
	if outcome.FailureCount != 3 {
		t.Fatalf("fence 一致必须保留 prior 状态: %+v", outcome)
	}
	// fence 不一致 → 不保留（走 Eligibility 分支）。
	outcome2 := w13g5HealthOutcome()
	preserveStateForSourceOnlyOutcome(&outcome2, exactkeyprobe.Input{Eligibility: exactkeyprobe.Eligibility{AccountStatus: "temporary_unavailable"}}, prior, true)
	if outcome2.FailureCount == 3 {
		t.Fatal("fence 不一致不得保留 prior 状态")
	}
	// cooldownDeferGrowthStep / cooldownDefer / boundedCooldownRemaining。
	fence := &exactkeyprobe.CooldownFence{Generation: "w13g5-gen"}
	if step := cooldownDeferGrowthStep(fence, time.Now(), time.Minute); step < 0 {
		t.Fatalf("增长步必须非负: %d", step)
	}
	if got := cooldownDefer(0, time.Minute, time.Hour); got <= 0 {
		t.Fatalf("defer 必须为正: %v", got)
	}
	d, ok := boundedCooldownRemaining(exactkeyprobe.Input{}, "failure", fence, time.Now())
	if ok && d < 0 {
		t.Fatalf("剩余冷却不得为负: %v", d)
	}
}

func TestW13g5HealthExecutorRequestFenceArms(t *testing.T) {
	input := exactkeyprobe.Input{AccountID: "w13g5-acc", InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1, Type: "api_key"}
	request := ProbeRequest{RequestID: "w13g5-req", AccountID: "w13g5-other", InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1, Deadline: time.Now().Add(time.Hour)}
	// fence 不匹配臂。
	outcome, err := ExecuteInputProbe(context.Background(), nil, OwnerLease{}, input, request, exactkeyprobe.ProbeOptions{})
	if err != nil || outcome.ErrorCode != "request_fence_invalid" {
		t.Fatalf("fence 不匹配必须失败: %+v %v", outcome, err)
	}
	// 过期臂。
	request.AccountID = "w13g5-acc"
	request.Deadline = time.Now().Add(-time.Hour)
	outcome, err = ExecuteInputProbe(context.Background(), nil, OwnerLease{}, input, request, exactkeyprobe.ProbeOptions{})
	if err != nil || outcome.ErrorCode != "request_deadline_elapsed" {
		t.Fatalf("过期必须失败: %+v %v", outcome, err)
	}
	// oauth 缺 access 臂。
	request.Deadline = time.Now().Add(time.Hour)
	oauthInput := input
	oauthInput.Type = "oauth"
	outcome, err = ExecuteInputProbe(context.Background(), nil, OwnerLease{}, oauthInput, request, exactkeyprobe.ProbeOptions{})
	if err != nil || outcome.ErrorCode != "oauth_access_missing" {
		t.Fatalf("OAuth access 缺失必须失败: %+v %v", outcome, err)
	}
	// api key pool 缺失臂。
	outcome, err = ExecuteInputProbe(context.Background(), nil, OwnerLease{}, input, request, exactkeyprobe.ProbeOptions{})
	if err != nil || outcome.ErrorCode != "api_key_pool_missing" {
		t.Fatalf("API Key pool 缺失必须失败: %+v %v", outcome, err)
	}
}
