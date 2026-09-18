package main

// w14a（单元层）：尾部错误臂收敛——chain_account_locks 闭库/空行臂、
// sampleLockDelayMs 钳制边界、compose_prewarm 失败告警臂。
//
// 本文件登记的进程内不可达语句：
//   - chain_account_locks.go 766-768（chainLockRandomToken 的 crand.Read
//     错误回落）：crypto/rand.Read 进程内恒成功。
//   - chain_account_locks.go 390-392（CompleteSuccessAsync 的 UPDATE 错误臂）：
//     需要 ENGAGED 行 + 启动后中途故障注入的 DB 写失败，测试夹具无缝
//     （闭库臂已在 findState 处先行返回）。
//   - chain_account_locks.go 579-581（sampleLockDelayMs 低钳制）：间隔
//     [5,30] 钳制下 delay ≥ 3000 恒不触 2000 下限（见测试注释）。

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/gatewayruntimecache"
)

// w14aClosedLocksFixture 构建一套锁桥夹具后关闭底层 DB，用于收割「执行期
// DB 故障」错误臂（sql: database is closed）。
func w14aClosedLocksFixture(t *testing.T) *lockFixture {
	t.Helper()
	fixture := newAccountLocksFixture(t)
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}
	return fixture
}

func TestW14aLocksClosedDatabaseArms(t *testing.T) {
	fixture := w14aClosedLocksFixture(t)
	ctx := context.Background()

	// CompleteSuccessAsync：findState 错误臂。
	if err := fixture.locks.CompleteSuccessAsync(ctx, "w14a-acc", "lease-1", nil); err == nil {
		t.Fatal("闭库后 CompleteSuccessAsync 必须报错")
	}
	// AcquireRetryLeaseAsync：执行期错误臂。
	if _, err := fixture.locks.AcquireRetryLeaseAsync(ctx, "w14a-acc", 5000); err == nil {
		t.Fatal("闭库后 AcquireRetryLeaseAsync 必须报错")
	}
}

// TestW14aLocksCompleteSuccessUnknownAccount 收割 current == nil 空操作臂
// （无锁行的账户）。
func TestW14aLocksCompleteSuccessUnknownAccount(t *testing.T) {
	fixture := newAccountLocksFixture(t)
	if err := fixture.locks.CompleteSuccessAsync(context.Background(), "w14a-unknown-acc", "lease-1", nil); err != nil {
		t.Fatalf("无锁行账户必须空操作: %v", err)
	}
}

// TestW14aSampleLockDelayMsClamps 收割高钳制边界（>30000）。低钳制
// （delay < 2000，chain_account_locks.go 579-581）不可达：
// NormalizeLockRetryIntervalSeconds 把间隔钳制在 [5,30]，base ≥ 5000、
// offset ≥ -2000，delay ≥ 3000 恒不触下限（Node 移植镜像代码，保留不删）。
func TestW14aSampleLockDelayMsClamps(t *testing.T) {
	// 30s 间隔（区间上限）+ 正偏移种子 → 高于 30000 上限。
	seed := w14aSeedWithPositiveOffset()
	high := sampleLockDelayMs(30, seed)
	if high != 30_000 {
		t.Fatalf("高钳制期望 30000, got %d", high)
	}
}

// w14aSeedWithPositiveOffset 找一个 hash 偏移 > 0 的种子（offset =
// |hash|%4001-2000 > 0 即 |hash|%4001 > 2000）。
func w14aSeedWithPositiveOffset() string {
	for i := 0; i < 64; i++ {
		seed := "w14a-seed-" + string(rune('a'+i))
		if sampleLockDelayMs(30, seed) > 30_000 {
			return seed
		}
	}
	return "w14a-seed-a"
}

// w14aFailingPrewarmModels 通过 GatewayAPIKeyPrewarmer 接缝注入预热失败
// （compose_prewarm.go 的失败告警臂）。
type w14aFailingPrewarmModels struct {
	gatewayruntimecache.ReadModels
}

func (w14aFailingPrewarmModels) ListActiveGatewayAPIKeyHashes(context.Context) ([]string, error) {
	return nil, errors.New("w14a prewarm list 失败")
}

func TestW14aPrewarmFailureWarnArm(t *testing.T) {
	cache, err := gatewayruntimecache.New(w14aFailingPrewarmModels{}, gatewayruntimecache.Options{})
	if err != nil {
		t.Fatal(err)
	}
	startGatewayAPIKeyCachePrewarm(cache, slog.Default())
	// fire-and-forget goroutine：留出执行窗口；断言只收敛于不 panic。
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// TestW14aChatImageDecodeErrorArm 收割 chat 图片解码失败归一臂。
// 同文件 70-72（isChatSupportedFormat 否定臂）不可达：进程内注册的解码器
// （jpeg/png/gif/webp）与白名单一一对应，可解码即受支持。
func TestW14aChatImageDecodeErrorArm(t *testing.T) {
	processor := newChatImageProcessor()
	_, err := processor.ProcessUpload([]byte("w14a: not an image at all"), "")
	if err == nil {
		t.Fatal("垃圾字节必须被拒绝")
	}
}
