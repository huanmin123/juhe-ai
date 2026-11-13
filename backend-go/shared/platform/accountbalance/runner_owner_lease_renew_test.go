package accountbalance

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// renewSlowBalanceHTTP 是带首次延迟的固定响应 HTTPDoer：首次调用执行
// onFirst 回调（测试借上游探测窗口篡改租约行/制造批次时长），其余调用
// 立即返回固定余额 JSON。
type renewSlowBalanceHTTP struct {
	body       string
	delay      time.Duration
	onFirst    func()
	callCount  atomic.Int64
	firstFired sync.Once
}

func (c *renewSlowBalanceHTTP) Do(*http.Request) (*http.Response, error) {
	if c.callCount.Add(1) == 1 && c.onFirst != nil {
		c.firstFired.Do(func() {
			if c.onFirst != nil {
				c.onFirst()
			}
		})
	}
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return &http.Response{StatusCode: http.StatusOK, Body: ioNopCloser{Reader: strings.NewReader(c.body)}}, nil
}

// renewManualInputs 构造 count 个互不冲突的手动刷新输入（直调 runInputs 的
// 测试 helper 形态，多 input manual 保持全局 owner lease key）。
func renewManualInputs(t *testing.T, count int, issuedAt time.Time) []Input {
	t.Helper()
	secret := "owner-lease-renew-secret"
	credential, err := NewCredentialEnvelope(secret, "api_key", map[string]string{"api_key": "sk-renew"})
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]Input, 0, count)
	for index := 0; index < count; index++ {
		accountID := "acct-renew-" + strings.Repeat("x", index+1)
		inputs = append(inputs, Input{
			AccountID: accountID, SystemAccountID: "sys-renew", InputVersion: 1, ConfigRevision: 1,
			Provider: "openai", Type: "api_key", Status: "active", Schedulable: true,
			BaseURL: "https://example.test", Config: QueryConfig{Adapter: Adapter("builtin"), IntervalMinutes: 5},
			APIKey: credential, Trigger: TriggerManual, IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(time.Minute),
		})
	}
	return inputs
}

// hijackOwnerLeaseLeaseRow 把指定 key 空间的 owner lease 行移交他人
// （owner_id 改写 + fence+1）：RenewOwnerLease 的 owner/fence 校验从此
// 不命中，模拟 T207 C4 的失租形态（租约被接管）。
func hijackOwnerLeaseLeaseRow(t *testing.T, store *Store, leaseKey string) {
	t.Helper()
	result, err := store.db.Exec(`UPDATE juhe_jobs.account_balance_owner_leases
SET owner_id='lease-hijacker', fence_token=fence_token+1, lease_until=now()+'1 hour', updated_at=now()
WHERE lease_key=$1`, leaseKey)
	if err != nil {
		t.Fatalf("hijack owner lease row: %v", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		t.Fatalf("hijack owner lease row: changed=%d, want 1", changed)
	}
}

// TestRunInputsRenewsOwnerLeaseAcrossBatch 锁定 T207 C4 核心行为：整批处理
// 时长超过 ownerLeaseTTL 时，周期续租保证批内每个 input 的 outcome 仍以本
// owner 写入（无失租错误）。无续租时第二个 input 起的 AppendOutcome 会被
// verifyOwnerTx 以 owner lease 过期拒绝（TTL 300ms < 3×200ms 串行探测）。
func TestRunInputsRenewsOwnerLeaseAcrossBatch(t *testing.T) {
	store := openKitPostgresStore(t)
	ctx := context.Background()
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: "renew-owner", CredentialSecret: "owner-lease-renew-secret", IOConcurrency: 1, OwnerLeaseTTL: 300 * time.Millisecond, HTTPClient: &renewSlowBalanceHTTP{body: `{"unit":"USD","remaining":"12.5"}`, delay: 200 * time.Millisecond}, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	// 测试注入短续租周期（生产默认 TTL/3 下限 5s，由 ownerLeaseRenewInterval
	// 推导，不另设配置面）。
	runner.ownerLeaseRenewIntervalFixed = 50 * time.Millisecond
	now := time.Now().UTC()
	report, err := runner.runInputs(ctx, TriggerManual, renewManualInputs(t, 3, now))
	if err != nil {
		t.Fatal(err)
	}
	if report.Executed != 3 || report.Skipped != 0 || report.Stale != 0 || len(report.Errors) != 0 {
		t.Fatalf("续租必须让整批 outcome 正常写入: %#v", report)
	}
}

// TestRunInputsStopsRemainingInputsWhenOwnerLeaseLost 锁定失租路径：owner
// 行被他人接管后（owner/fence 校验不命中），续租协程置失租标志并停止，
// 剩余 inputs 不再分派（无新上游调用）、计入 Skipped。已分派 in-flight 的
// input 按 fence 既有语义落 Errors（input1 的 outcome 写入与 input2 的账户
// 租约获取都被 owner/fence 校验拒绝）——fence 是失租的最终防线，本测试
// 锁定的是「不再浪费上游调用」与「剩余 inputs 计入 Skipped」两条新增契约
//（5 inputs 串行：失租置位后 index=2 的检查点 break，remaining=2 计入
// Skipped；input2 恰在置位前越过检查点、走 skipped 计数 + Errors，1+2=3）。
func TestRunInputsStopsRemainingInputsWhenOwnerLeaseLost(t *testing.T) {
	store := openKitPostgresStore(t)
	ctx := context.Background()
	client := &renewSlowBalanceHTTP{body: `{"unit":"USD","remaining":"9"}`, delay: 300 * time.Millisecond, onFirst: func() {
		// 首个探测进行中移交 owner 行：下一次续租 tick 即失租。
		hijackOwnerLeaseLeaseRow(t, store, OwnerLeaseKeyPeriodic)
	}}
	runner, err := NewRunner(RunnerConfig{Store: store, OwnerID: "lost-owner", CredentialSecret: "owner-lease-renew-secret", IOConcurrency: 1, OwnerLeaseTTL: time.Minute, HTTPClient: client, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner.ownerLeaseRenewIntervalFixed = 20 * time.Millisecond
	now := time.Now().UTC()
	report, err := runner.runInputs(ctx, TriggerManual, renewManualInputs(t, 5, now))
	if err != nil {
		t.Fatal(err)
	}
	if report.Executed != 1 || report.Skipped != 3 {
		t.Fatalf("失租后剩余 inputs 必须计入 Skipped: %#v", report)
	}
	if len(report.Errors) != 2 {
		t.Fatalf("在途 input 必须按 fence 语义落 Errors（恰 2 项）: %#v", report.Errors)
	}
	if _, ok := report.Errors["acct-renew-x"]; !ok {
		t.Fatalf("input1 outcome 写入必须被失租 fence 拒绝: %#v", report.Errors)
	}
	if _, ok := report.Errors["acct-renew-xx"]; !ok {
		t.Fatalf("input2 账户租约必须被失租 fence 拒绝: %#v", report.Errors)
	}
	if calls := client.callCount.Load(); calls != 1 {
		t.Fatalf("失租后上游调用次数 = %d, want 1（剩余 input 不再探测）", calls)
	}
}

// TestOwnerLeaseRenewIntervalDefaults 锁定默认续租周期推导：TTL/3 且下限
// 5s；测试注入值优先。
func TestOwnerLeaseRenewIntervalDefaults(t *testing.T) {
	runner := &Runner{ownerLeaseTTL: time.Minute}
	if got, want := runner.ownerLeaseRenewInterval(), 20*time.Second; got != want {
		t.Fatalf("TTL=1m 续租周期 = %v, want %v", got, want)
	}
	runner = &Runner{ownerLeaseTTL: 9 * time.Second}
	if got, want := runner.ownerLeaseRenewInterval(), 5*time.Second; got != want {
		t.Fatalf("TTL=9s 续租周期 = %v, want 下限 %v", got, want)
	}
	runner = &Runner{ownerLeaseTTL: time.Minute, ownerLeaseRenewIntervalFixed: 10 * time.Millisecond}
	if got, want := runner.ownerLeaseRenewInterval(), 10*time.Millisecond; got != want {
		t.Fatalf("注入续租周期 = %v, want %v", got, want)
	}
}
