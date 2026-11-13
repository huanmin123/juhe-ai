package accounthealth

// T207 C3 J1 投影消费水位回归（真实 PG，pgtestkit 隔离库）：
//   - 晚到旧行（observed_at 早于已消费行，物理写入在后）仍被消费——旧
//     observed_at 单调游标口径会永久跳过（BUG-0345 同族）；
//   - 消费幂等/重放安全：排空后重复 drain 零消费，receipt 唯一；
//   - 存量自举结算：水位列首次启用按旧游标一次性打标（不重投影历史），
//     游标之后的行正常消费；
//   - 水位打标与投影同事务（terminal receipt 提交路径），游标保留诊断推进。
//
// 夹具表按权威 DDL 形状简化：receipts 省略 accounts 外键（本用例只触达
// terminal ignored receipt + 水位打标路径，不触达 accounts 行锁链）。

import (
	"fmt"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

const t207ProjectionNow = "2026-11-13T12:00:00.000Z"

// newProjectionWatermarkPG 创建隔离库：juhe_jobs 表由生产 EnsureSchema 幂等
// 创建（含 projection_consumed_at 列与部分索引），业务侧只建游标/receipt 两表。
func newProjectionWatermarkPG(t *testing.T) (*sql.DB, *OutcomeProjector) {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		for _, schema := range []string{"juhe_jobs", "juhe_business"} {
			if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+schema); err != nil {
				return err
			}
		}
		_, err := db.ExecContext(ctx, `CREATE TABLE juhe_business.account_health_projection_cursors (
			consumer_key text PRIMARY KEY,
			observed_at text,
			outcome_id text,
			updated_at text NOT NULL,
			CHECK ((observed_at IS NULL AND outcome_id IS NULL) OR (observed_at IS NOT NULL AND outcome_id IS NOT NULL))
		)`)
		if err != nil {
			return err
		}
		_, err = db.ExecContext(ctx, `CREATE TABLE juhe_business.account_health_projection_receipts (
			outcome_id text PRIMARY KEY,
			account_id text NOT NULL,
			input_version integer NOT NULL CHECK (input_version >= 1),
			disposition text NOT NULL CHECK (disposition IN ('applied', 'stale', 'ignored', 'rejected')),
			reason text,
			applied_at text NOT NULL
		)`)
		return err
	})
	store := newStore(db, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("ensure jobs schema: %v", err)
	}
	business, err := NewProjectionBusinessDB(db)
	if err != nil {
		t.Fatalf("business handle: %v", err)
	}
	fixedNow, _ := time.Parse(time.RFC3339, t207ProjectionNow)
	projector, err := NewOutcomeProjector(store, OutcomeProjectorConfig{
		Business:         business,
		CredentialSecret: "t207-projection-secret",
		PollInterval:     time.Second,
		BatchSize:        100,
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:              func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("projector: %v", err)
	}
	return db, projector
}

// t207SeedOutcome 按 outcomes 表生产列集写入一行（payload 无 projection，
// drain 走 terminal ignored receipt 路径）。payload 按 AppendOutcome 生产形状
// 携带完整身份字段（解码后的 Outcome 身份同时是 receipt 键与消费水位键，
// 与行列逐字一致）。
func t207SeedOutcome(t *testing.T, db *sql.DB, outcomeID, requestID, observedAt string) {
	t.Helper()
	payload := fmt.Sprintf(`{"outcome_id":%q,"request_id":%q,"account_id":"t207-acct","observed_at":%q,"input_version":1,"config_revision":1,"dispatch_revision":1}`,
		outcomeID, requestID, observedAt)
	if _, err := db.Exec(`INSERT INTO juhe_jobs.account_health_outcomes
		(outcome_id, request_id, account_id, outcome, observed_at, input_version, config_revision, dispatch_revision, payload)
		VALUES ($1, $2, 't207-acct', 'health_success', $3::timestamptz, 1, 1, 1, $4)`,
		outcomeID, requestID, observedAt, payload); err != nil {
		t.Fatalf("seed outcome %s: %v", outcomeID, err)
	}
}

func t207ConsumedCount(t *testing.T, db *sql.DB, where string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_jobs.account_health_outcomes WHERE projection_consumed_at IS NOT NULL AND `+where, args...).Scan(&count); err != nil {
		t.Fatalf("count consumed: %v", err)
	}
	return count
}

func t207ReceiptCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_business.account_health_projection_receipts`).Scan(&count); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	return count
}

// TestT207ConsumesLateArrivingOutcome：晚到旧行（observed_at 早于已消费行）
// 落库后仍被消费；重复 drain 零消费（幂等）；游标保留诊断推进。
func TestT207ConsumesLateArrivingOutcome(t *testing.T) {
	db, projector := newProjectionWatermarkPG(t)
	ctx := context.Background()
	t207SeedOutcome(t, db, "t207-out-a", "req-a", "2026-11-13T10:00:00Z")
	t207SeedOutcome(t, db, "t207-out-b", "req-b", "2026-11-13T10:05:00Z")

	result, err := projector.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("drain 1: %v", err)
	}
	if result.Processed != 2 {
		t.Fatalf("drain 1 processed = %d want 2", result.Processed)
	}
	if got := t207ConsumedCount(t, db, `true`); got != 2 {
		t.Fatalf("已消费行数 = %d want 2", got)
	}
	if got := t207ReceiptCount(t, db); got != 2 {
		t.Fatalf("receipt 行数 = %d want 2（打标与投影同批落库）", got)
	}

	// 晚到旧行：observed_at 早于已消费行，物理写入在 drain 之后。
	t207SeedOutcome(t, db, "t207-out-late", "req-late", "2026-11-13T09:00:00Z")
	result, err = projector.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("drain 2: %v", err)
	}
	if result.Processed != 1 {
		t.Fatalf("晚到行 processed = %d want 1（旧单调游标口径会永久跳过）", result.Processed)
	}
	if got := t207ConsumedCount(t, db, `outcome_id = 't207-out-late'`); got != 1 {
		t.Fatalf("晚到行未被消费, count = %d", got)
	}

	// 排空后重复 drain 零消费：水位口径下无重复重放。
	result, err = projector.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("drain 3: %v", err)
	}
	if result.Processed != 0 {
		t.Fatalf("重复 drain processed = %d want 0", result.Processed)
	}
	if got := t207ReceiptCount(t, db); got != 3 {
		t.Fatalf("重复 drain 后 receipt 行数 = %d want 3（不重放）", got)
	}
	// 游标诊断面保留：推进到已消费的最晚一行。
	var observedAt, outcomeID sql.NullString
	if err := db.QueryRow(`SELECT observed_at, outcome_id FROM juhe_business.account_health_projection_cursors WHERE consumer_key = $1`, DefaultProjectionConsumerKey).Scan(&observedAt, &outcomeID); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if outcomeID.String != "t207-out-b" {
		t.Fatalf("诊断游标 outcome_id = %q want t207-out-b（晚到行不回退游标）", outcomeID.String)
	}
}

// TestT207BootstrapSettlesByCursor：水位列首次启用时按旧游标结算存量（不重
// 投影历史），游标之后的行正常消费。
func TestT207BootstrapSettlesByCursor(t *testing.T) {
	db, projector := newProjectionWatermarkPG(t)
	ctx := context.Background()
	t207SeedOutcome(t, db, "t207-boot-a", "req-a", "2026-11-13T10:00:00Z")
	t207SeedOutcome(t, db, "t207-boot-b", "req-b", "2026-11-13T10:05:00Z")
	t207SeedOutcome(t, db, "t207-boot-c", "req-c", "2026-11-13T10:10:00Z")
	// 模拟改造前的库：旧游标已推进到 boot-b（boot-a/boot-b 按旧口径已消费）。
	if _, err := db.Exec(`INSERT INTO juhe_business.account_health_projection_cursors
		(consumer_key, observed_at, outcome_id, updated_at) VALUES ($1, $2, 't207-boot-b', $3)`,
		DefaultProjectionConsumerKey, "2026-11-13T10:05:00Z", t207ProjectionNow); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	result, err := projector.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("bootstrap drain: %v", err)
	}
	if result.Processed != 0 {
		t.Fatalf("存量结算轮 processed = %d want 0", result.Processed)
	}
	if got := t207ConsumedCount(t, db, `outcome_id in ('t207-boot-a','t207-boot-b')`); got != 2 {
		t.Fatalf("游标及之前的存量应被结算为已消费, count = %d want 2", got)
	}
	if got := t207ReceiptCount(t, db); got != 0 {
		t.Fatalf("存量结算不得重投影历史, receipt = %d want 0", got)
	}
	if got := t207ConsumedCount(t, db, `outcome_id = 't207-boot-c'`); got != 0 {
		t.Fatalf("游标之后的行应保持未消费, count = %d want 0", got)
	}

	// 结算完成后的下一轮正常消费游标之后的行。
	result, err = projector.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("normal drain: %v", err)
	}
	if result.Processed != 1 {
		t.Fatalf("结算后 processed = %d want 1", result.Processed)
	}
	if got := t207ConsumedCount(t, db, `outcome_id = 't207-boot-c'`); got != 1 {
		t.Fatalf("游标后的行未被消费, count = %d", got)
	}
}

// TestT207BootstrapIdempotentAfterEnable：水位启用后（已有打标行）自举不再
// 触发，重复 drain 直接走正常消费且零重放。
func TestT207BootstrapIdempotentAfterEnable(t *testing.T) {
	db, projector := newProjectionWatermarkPG(t)
	ctx := context.Background()
	t207SeedOutcome(t, db, "t207-idem-a", "req-a", "2026-11-13T10:00:00Z")
	if _, err := projector.DrainOnce(ctx); err != nil {
		t.Fatalf("drain 1: %v", err)
	}
	// 新增晚到行 + 无游标库形态：已有打标行 ⇒ 不进入自举，直接消费晚到行。
	t207SeedOutcome(t, db, "t207-idem-late", "req-late", "2026-11-13T09:00:00Z")
	result, err := projector.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("drain 2: %v", err)
	}
	if result.Processed != 1 {
		t.Fatalf("水位已启用时晚到行 processed = %d want 1", result.Processed)
	}
	if got := t207ConsumedCount(t, db, `true`); got != 2 {
		t.Fatalf("已消费行数 = %d want 2", got)
	}
}
