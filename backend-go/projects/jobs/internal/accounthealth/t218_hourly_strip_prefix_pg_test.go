package accounthealth

// T218 小时条带写入前置回归（真实 PG，pgtestkit 隔离库）：
//   - recordHourly 失败 → DrainOnce 返回错误且该 outcome 未打标消费、无
//     receipt（下轮重放重试，不再 warn 后永久丢失）；
//   - sink 恢复后重放：条带写入一次成功，投影/receipt/消费水位推进；
//   - 重复 drain 零消费零重写（幂等）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/pgtestkit"
)

// t218FlakySink 先失败 failCount 次再成功，记录全部成功写入。
type t218FlakySink struct {
	failCount    int
	observations []AccountHealthHourlyObservation
}

func (s *t218FlakySink) RecordAccountHealthHourly(ctx context.Context, observation AccountHealthHourlyObservation) error {
	if s.failCount > 0 {
		s.failCount--
		return errors.New("t218: stats 库暂不可用")
	}
	s.observations = append(s.observations, observation)
	return nil
}

func newT218PrefixProjector(t *testing.T, sink AccountHealthHourlySink) (*sql.DB, *OutcomeProjector) {
	t.Helper()
	db := pgtestkit.New(t, func(ctx context.Context, db *sql.DB) error {
		for _, schema := range []string{"juhe_jobs", "juhe_business"} {
			if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS `+schema); err != nil {
				return err
			}
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE juhe_business.account_health_projection_cursors (
			consumer_key text PRIMARY KEY,
			observed_at text,
			outcome_id text,
			updated_at text NOT NULL,
			CHECK ((observed_at IS NULL AND outcome_id IS NULL) OR (observed_at IS NOT NULL AND outcome_id IS NOT NULL))
		)`); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `CREATE TABLE juhe_business.account_health_projection_receipts (
			outcome_id text PRIMARY KEY,
			account_id text NOT NULL,
			input_version integer NOT NULL CHECK (input_version >= 1),
			disposition text NOT NULL CHECK (disposition IN ('applied', 'stale', 'ignored', 'rejected')),
			reason text,
			applied_at text NOT NULL
		)`); err != nil {
			return err
		}
		_, err := db.ExecContext(ctx, `CREATE TABLE juhe_business.accounts (
			id text PRIMARY KEY,
			vendor_code text,
			system_account_id text NOT NULL
		)`)
		if err != nil {
			return err
		}
		_, err = db.ExecContext(ctx, `INSERT INTO juhe_business.accounts (id, vendor_code, system_account_id)
			VALUES ('t218-acct', 'gpt', 'sys-t218')`)
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
		CredentialSecret: "t218-projection-secret",
		PollInterval:     time.Second,
		BatchSize:        100,
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:              func() time.Time { return fixedNow },
		Hourly:           sink,
	})
	if err != nil {
		t.Fatalf("projector: %v", err)
	}
	return db, projector
}

func t218SeedOutcome(t *testing.T, db *sql.DB, outcomeID, observedAt string) {
	t.Helper()
	payload := fmt.Sprintf(`{"outcome_id":%q,"request_id":%q,"account_id":"t218-acct","outcome":"complete_success","observed_at":%q,"input_version":1,"config_revision":1,"dispatch_revision":1}`,
		outcomeID, "req-"+outcomeID, observedAt)
	if _, err := db.Exec(`INSERT INTO juhe_jobs.account_health_outcomes
		(outcome_id, request_id, account_id, outcome, observed_at, input_version, config_revision, dispatch_revision, payload)
		VALUES ($1, $2, 't218-acct', 'complete_success', $3::timestamptz, 1, 1, 1, $4)`,
		outcomeID, "req-"+outcomeID, observedAt, payload); err != nil {
		t.Fatalf("seed outcome %s: %v", outcomeID, err)
	}
}

func TestT218HourlyWriteFailureKeepsOutcomeUnconsumed(t *testing.T) {
	sink := &t218FlakySink{failCount: 1}
	db, projector := newT218PrefixProjector(t, sink)
	ctx := context.Background()
	t218SeedOutcome(t, db, "t218-out-a", "2026-11-21T10:00:00Z")

	// 第一次 drain：小时条写失败 → 停轮，行未消费、无 receipt、条带零写入。
	if _, err := projector.DrainOnce(ctx); err == nil {
		t.Fatal("小时条写失败应上抛停轮")
	}
	var consumed int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_jobs.account_health_outcomes WHERE projection_consumed_at IS NOT NULL`).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != 0 {
		t.Fatalf("消费水位不应打标: %d", consumed)
	}
	var receipts int
	if err := db.QueryRow(`SELECT count(*) FROM juhe_business.account_health_projection_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 {
		t.Fatalf("失败轮不应产生 receipt: %d", receipts)
	}

	// 重放（sink 恢复）：条带写入一次成功，投影与消费水位推进。
	result, err := projector.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("重放 drain: %v", err)
	}
	if result.Processed != 1 {
		t.Fatalf("重放 processed = %d want 1", result.Processed)
	}
	if err := db.QueryRow(`SELECT count(*) FROM juhe_jobs.account_health_outcomes WHERE projection_consumed_at IS NOT NULL`).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != 1 {
		t.Fatalf("重放后应打标消费: %d", consumed)
	}
	if len(sink.observations) != 1 {
		t.Fatalf("条带应恰好写入一次: %#v", sink.observations)
	}
	got := sink.observations[0]
	if got.AccountID != "t218-acct" || got.SystemAccountID != "sys-t218" || got.ProviderCode != "gpt" || got.OutcomeID != "t218-out-a" || !got.Success {
		t.Fatalf("条带字段错误: %#v", got)
	}

	// 重复 drain：幂等，零消费零重写。
	again, err := projector.DrainOnce(ctx)
	if err != nil {
		t.Fatalf("重复 drain: %v", err)
	}
	if again.Processed != 0 || len(sink.observations) != 1 {
		t.Fatalf("重复 drain 应零消费零重写: processed=%d sink=%#v", again.Processed, sink.observations)
	}
}
