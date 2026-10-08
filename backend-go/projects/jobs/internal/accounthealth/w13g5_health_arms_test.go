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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// w13g5HealthOpenStore 打开一个真实 SQLite store 并完成建表。
func w13g5HealthOpenStore(t *testing.T) *Store {
	t.Helper()
	_ = sqlite.Driver{}
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "w13g5-health.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

// w13g5HealthClosedStore 打开、建表并关闭底层句柄的 store，用于批量错误臂。
func w13g5HealthClosedStore(t *testing.T) *Store {
	t.Helper()
	store := w13g5HealthOpenStore(t)
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	return store
}

func w13g5HealthOutcome() Outcome {
	return Outcome{
		OutcomeID: "w13g5-outcome", RequestID: "w13g5-request", AccountID: "w13g5-acc",
		Outcome: "success", ObservedAt: time.Date(2026, 9, 18, 8, 0, 0, 0, time.UTC),
		InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1,
	}
}

func TestW13g5HealthClosedStoreWriteArms(t *testing.T) {
	store := w13g5HealthClosedStore(t)
	ctx := context.Background()
	lease := OwnerLease{OwnerID: "w13g5-owner", FenceToken: 1}
	if _, _, err := store.AcquireOwnerLease(ctx, "w13g5-owner", time.Minute); err == nil {
		t.Fatal("关闭句柄后租约获取必须报错")
	}
	if _, err := store.RenewOwnerLease(ctx, lease, time.Minute); err == nil {
		t.Fatal("关闭句柄后租约续约必须报错")
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err == nil {
		t.Fatal("关闭句柄后租约释放必须报错")
	}
	if _, err := store.AppendOutcome(ctx, lease, w13g5HealthOutcome()); err == nil {
		t.Fatal("关闭句柄后追加 outcome 必须报错")
	}
	if _, _, err := store.LoadCurrentState(ctx, "w13g5-acc"); err == nil {
		t.Fatal("关闭句柄后读取状态必须报错")
	}
	if _, err := store.LoadDirectInputSuppressions(ctx, time.Now()); err == nil {
		t.Fatal("关闭句柄后读取抑制必须报错")
	}
	if _, err := store.HasRequest(ctx, "w13g5-request"); err == nil {
		t.Fatal("关闭句柄后 HasRequest 必须报错")
	}
	if _, _, err := store.LoadKeyCursor(ctx, "w13g5-acc", "w13g5-purpose", "w13g5-fp"); err == nil {
		t.Fatal("关闭句柄后读取 cursor 必须报错")
	}
	if err := store.SaveKeyCursor(ctx, lease, "w13g5-acc", "w13g5-purpose", "w13g5-fp", 1); err == nil {
		t.Fatal("关闭句柄后保存 cursor 必须报错")
	}
	if err := store.EnsureSchema(ctx); err == nil {
		t.Fatal("关闭句柄后建表必须报错")
	}
}

func TestW13g5HealthStoreValidationArms(t *testing.T) {
	store := w13g5HealthOpenStore(t)
	ctx := context.Background()
	// 租约参数校验。
	if _, _, err := store.AcquireOwnerLease(ctx, "", time.Minute); err == nil {
		t.Fatal("空 owner 必须报错")
	}
	if _, _, err := store.AcquireOwnerLease(ctx, "w13g5-owner", 0); err == nil {
		t.Fatal("零 duration 必须报错")
	}
	if _, err := store.RenewOwnerLease(ctx, OwnerLease{OwnerID: "o"}, 0); err == nil {
		t.Fatal("零续约 duration 必须报错")
	}
	if err := store.ReleaseOwnerLease(ctx, OwnerLease{FenceToken: 0}); err == nil {
		t.Fatal("无效释放参数必须报错")
	}
	// AppendOutcome 幂等字段校验（在 DB 调用之前）。
	bad := w13g5HealthOutcome()
	bad.OutcomeID = ""
	if _, err := store.AppendOutcome(ctx, OwnerLease{OwnerID: "w13g5-owner", FenceToken: 1}, bad); err == nil {
		t.Fatal("缺 outcomeID 必须报错")
	}
	noRequest := w13g5HealthOutcome()
	noRequest.RequestID = ""
	if _, err := store.AppendOutcome(ctx, OwnerLease{}, noRequest); err == nil {
		t.Fatal("缺 requestID 必须报错")
	}
	noAccount := w13g5HealthOutcome()
	noAccount.AccountID = ""
	if _, err := store.AppendOutcome(ctx, OwnerLease{}, noAccount); err == nil {
		t.Fatal("缺 accountID 必须报错")
	}
	zeroObserved := w13g5HealthOutcome()
	zeroObserved.ObservedAt = time.Time{}
	if _, err := store.AppendOutcome(ctx, OwnerLease{}, zeroObserved); err == nil {
		t.Fatal("零 observedAt 必须报错")
	}
	lowVersion := w13g5HealthOutcome()
	lowVersion.InputVersion = 0
	if _, err := store.AppendOutcome(ctx, OwnerLease{}, lowVersion); err == nil {
		t.Fatal("inputVersion<1 必须报错")
	}
	lowConfig := w13g5HealthOutcome()
	lowConfig.ConfigRevision = 0
	if _, err := store.AppendOutcome(ctx, OwnerLease{}, lowConfig); err == nil {
		t.Fatal("configRevision<1 必须报错")
	}
	lowDispatch := w13g5HealthOutcome()
	lowDispatch.DispatchRevision = 0
	if _, err := store.AppendOutcome(ctx, OwnerLease{}, lowDispatch); err == nil {
		t.Fatal("dispatchRevision<1 必须报错")
	}
}

func TestW13g5HealthStoreSQLiteFlows(t *testing.T) {
	store := w13g5HealthOpenStore(t)
	ctx := context.Background()
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w13g5-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("租约必须获取: %v %v", acquired, err)
	}
	// 未到期重复获取 → false。
	if _, acquiredAgain, err := store.AcquireOwnerLease(ctx, "w13g5-owner-2", time.Minute); err != nil || acquiredAgain {
		t.Fatalf("未到期租约不得被夺取: %v %v", acquiredAgain, err)
	}
	if ok, err := store.RenewOwnerLease(ctx, lease, time.Minute); err != nil || !ok {
		t.Fatalf("续约必须成功: %v %v", ok, err)
	}
	// 错误 owner 续约 → false。
	if ok, err := store.RenewOwnerLease(ctx, OwnerLease{OwnerID: "w13g5-other", FenceToken: lease.FenceToken}, time.Minute); err != nil || ok {
		t.Fatalf("错误 owner 续约必须未命中: %v %v", ok, err)
	}
	written, err := store.AppendOutcome(ctx, lease, w13g5HealthOutcome())
	if err != nil || !written {
		t.Fatalf("outcome 必须写入: %v %v", written, err)
	}
	// 重复 request 幂等 → false。
	duplicate, err := store.AppendOutcome(ctx, lease, w13g5HealthOutcome())
	if err != nil || duplicate {
		t.Fatalf("重复 request 必须幂等: %v %v", duplicate, err)
	}
	state, found, err := store.LoadCurrentState(ctx, "w13g5-acc")
	if err != nil || !found || state.AccountID != "w13g5-acc" {
		t.Fatalf("状态必须可读: %+v %v %v", state, found, err)
	}
	has, err := store.HasRequest(ctx, "w13g5-request")
	if err != nil || !has {
		t.Fatalf("request 必须存在: %v %v", has, err)
	}
	if err := store.SaveKeyCursor(ctx, lease, "w13g5-acc", "w13g5-purpose", "w13g5-fp", 7); err != nil {
		t.Fatal(err)
	}
	index, found, err := store.LoadKeyCursor(ctx, "w13g5-acc", "w13g5-purpose", "w13g5-fp")
	if err != nil || !found || index != 7 {
		t.Fatalf("cursor 必须可读: %d %v %v", index, found, err)
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err != nil {
		t.Fatal(err)
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

func TestW13g5HealthConfigIsolationArms(t *testing.T) {
	getenv := func(name string) string {
		if name == "JUHE_AI_DATABASE_PATH" {
			return `F:\w13g5-conflict\stats.sqlite3`
		}
		return ""
	}
	// 与其他库共用文件 → 报错。
	if err := validateSQLiteIsolation(`F:\w13g5-conflict\STATS.sqlite3`, `F:\w13g5-input`, getenv); err == nil {
		t.Fatal("共用 SQLite 文件必须报错")
	}
	// store 放入 input 目录 → 报错。
	if err := validateSQLiteIsolation(`F:\w13g5-input\health.sqlite3`, `F:\w13g5-input`, func(string) string { return "" }); err == nil {
		t.Fatal("store 放入 input 目录必须报错")
	}
	// 正常隔离 → 通过。
	if err := validateSQLiteIsolation(`F:\w13g5-health\health.sqlite3`, `F:\w13g5-input`, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	if !equalPath(`F:\A\a.db`, `f:\a\A.DB`) {
		t.Fatal("Windows 大小写不敏感比较失败")
	}
	if err := validateSQLiteIsolation(`F:\w13g5-health\health.sqlite3`, `F:\w13g5-input`, func(name string) string {
		if name == "JUHE_AI_DATASET_DATABASE_PATH" {
			return "relative\\..\\path"
		}
		return ""
	}); err != nil {
		t.Fatalf("可解析的相对路径不得报错: %v", err)
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
