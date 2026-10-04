package modelcheckowner

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestStaleRunSweepSQLConstruction 钉死 PG 方言收尾语句的构造：schema 限定表
// 名、status='running' 条件、两侧 ::timestamptz cast（updated_at 为 text 时间
// 索引列）以及终态字段写入顺序。
func TestStaleRunSweepSQLConstruction(t *testing.T) {
	query := staleRunSweepSQL("juhe_j3b.model_check_runs")
	for _, want := range []string{
		"UPDATE juhe_j3b.model_check_runs SET",
		"status='failed'",
		"error_code=?",
		"error_message=?",
		"updated_at=?",
		"WHERE status='running'",
		"updated_at::timestamptz < ?::timestamptz",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("sweep SQL missing %q: %s", want, query)
		}
	}
	// PG 模式下 bind 把占位符转换为 $1..$4，参数序：code、message、now、cutoff。
	bound := (&Store{mode: "postgres", schema: "juhe_j3b"}).bind(query)
	for _, want := range []string{"error_code=$1", "error_message=$2", "updated_at=$3", "updated_at::timestamptz < $4::timestamptz"} {
		if !strings.Contains(bound, want) {
			t.Fatalf("bound sweep SQL missing %q: %s", want, bound)
		}
	}
}

// TestStaleRunSweepSQLiteSQLConstruction 钉死 SQLite 方言收尾语句的构造：
// 与 PG 方言同字段序，比较为纯文本字典序（SQLite 无 ::timestamptz cast）。
func TestStaleRunSweepSQLiteSQLConstruction(t *testing.T) {
	query := staleRunSweepSQLiteSQL("model_check_runs")
	for _, want := range []string{
		"UPDATE model_check_runs SET",
		"status='failed'",
		"error_code=?",
		"error_message=?",
		"updated_at=?",
		"WHERE status='running'",
		"updated_at < ?",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("sqlite sweep SQL missing %q: %s", want, query)
		}
	}
	if strings.Contains(query, "::timestamptz") {
		t.Fatalf("sqlite sweep SQL must not use PG cast: %s", query)
	}
	// SQLite 模式下 bind 原样保留 ? 占位符。
	bound := (&Store{mode: "sqlite"}).bind(query)
	if bound != query {
		t.Fatalf("sqlite bind must keep ? placeholders: %s", bound)
	}
}

// TestSweepStaleRunsSQLiteMode 覆盖 SQLite 模式（standalone 正式部署模式）
// 的启动收尾：超过阈值的遗留 running run 被收尾为 failed/owner_lost，
// 阈值内的新鲜 run 不被波及。
func TestSweepStaleRunsSQLiteMode(t *testing.T) {
	store := newRuntimeTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	stale := RunRecord{ID: "stale-sqlite-run", SystemAccountID: "sys", ActorSystemAccountID: "sys", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", TriggerKind: "manual", ProbeSetVersion: "p1", StartedAt: now.Add(-2 * time.Hour)}
	if err := store.CreateRun(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	fresh := RunRecord{ID: "fresh-sqlite-run", SystemAccountID: "sys", ActorSystemAccountID: "sys", ProviderCode: "openai", TargetType: "account", TargetID: "acct", Model: "gpt-5.6-sol", Profile: "quick", TriggerKind: "manual", ProbeSetVersion: "p1", StartedAt: now}
	if err := store.CreateRun(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	changed, err := store.SweepStaleRuns(context.Background(), now)
	if err != nil || changed != 1 {
		t.Fatalf("sqlite sweep changed=%d err=%v, want 1/nil", changed, err)
	}
	var status, errorCode string
	if err := store.db.QueryRow(`SELECT status,error_code FROM model_check_runs WHERE id=?`, stale.ID).Scan(&status, &errorCode); err != nil || status != string(RunFailed) || errorCode != staleRunErrorCode {
		t.Fatalf("stale sqlite run status=%q errorCode=%q err=%v, want failed/%s", status, errorCode, err, staleRunErrorCode)
	}
	if err := store.db.QueryRow(`SELECT status FROM model_check_runs WHERE id=?`, fresh.ID).Scan(&status); err != nil || status != string(RunRunning) {
		t.Fatalf("fresh sqlite run status=%q err=%v, want untouched running", status, err)
	}
	// 幂等：重复执行自然空转。
	changed, err = store.SweepStaleRuns(context.Background(), now)
	if err != nil || changed != 0 {
		t.Fatalf("idempotent sqlite sweep changed=%d err=%v, want 0/nil", changed, err)
	}
}

// TestSweepStaleRunsInputGuards 覆盖未初始化 store 与零时间入参的拒绝路径。
func TestSweepStaleRunsInputGuards(t *testing.T) {
	var nilStore *Store
	if changed, err := nilStore.SweepStaleRuns(context.Background(), time.Now()); err == nil || changed != 0 {
		t.Fatalf("nil store sweep changed=%d err=%v, want error", changed, err)
	}
	store := newRuntimeTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SweepStaleRuns(context.Background(), time.Time{}); err == nil {
		t.Fatal("zero sweep time must be rejected")
	}
}

// TestStaleRunSweepThresholdCoversBoundedBudgets 钉死阈值依据（BUG-0265）：
// run 表 updated_at 在运行期不推进（仅在 CreateRun 与终态写点推进），阈值
// 必须超过全部代码内 run 预算——run-now 默认 10 分钟、计划/恢复执行
// ScheduleRunBudget(6 分钟 lease)=5.5 分钟——并对无预算的手动 SSE run 留出
// 余量，取 30 分钟。
func TestStaleRunSweepThresholdCoversBoundedBudgets(t *testing.T) {
	runNowDefaultBudget := 10 * time.Minute
	schedulerBudget := ScheduleRunBudget(6 * time.Minute)
	if schedulerBudget <= 0 || schedulerBudget >= runNowDefaultBudget {
		t.Fatalf("budget fixtures changed: scheduler=%v run-now=%v", schedulerBudget, runNowDefaultBudget)
	}
	if staleRunSweepThreshold <= runNowDefaultBudget {
		t.Fatalf("sweep threshold=%v must exceed the largest in-code run budget=%v", staleRunSweepThreshold, runNowDefaultBudget)
	}
	if staleRunSweepThreshold < 2*runNowDefaultBudget {
		t.Fatalf("sweep threshold=%v must keep >=2x headroom over the run-now budget=%v", staleRunSweepThreshold, runNowDefaultBudget)
	}
}
