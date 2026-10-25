package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
)

func newHourlyWriterFixture(t *testing.T) (*healthHourlyWriter, func(t *testing.T) (string, string, string, sql.NullString)) {
	t.Helper()
	// PG 唯一形态：独立 PG 测试库 + sqldialect 改写（`:memory:` sqlite 夹具
	// 随 sqlite 形态退役删除；`?` 由 sqldialect 保留测试内写法）。
	db, _ := wgOpenPGDialectDB(t, func(ctx context.Context, db *sql.DB) error {
		_, err := db.Exec(`CREATE SCHEMA IF NOT EXISTS juhe_stats;
CREATE TABLE juhe_stats.account_health_hourly (
		account_id text NOT NULL,
		system_account_id text NOT NULL,
		provider_code text NOT NULL,
		stat_hour text NOT NULL,
		status text NOT NULL CHECK (status IN ('success', 'failure')),
		last_observed_at text NOT NULL,
		last_record_id text NOT NULL,
		status_code integer,
		error_code text,
		error_message text,
		updated_at text NOT NULL,
		PRIMARY KEY (account_id, stat_hour))`)
		return err
	})
	location := time.FixedZone("cjz", 8*3600)
	writer := &healthHourlyWriter{
		db:       db,
		timezone: func(context.Context) (*time.Location, error) { return location, nil },
		now:      func() time.Time { return time.Date(2026, 9, 26, 15, 0, 0, 0, time.UTC) },
	}
	inspect := func(t *testing.T) (string, string, string, sql.NullString) {
		t.Helper()
		var status, statHour, lastObserved string
		var statusCode sql.NullString
		if err := db.QueryRowContext(context.Background(), `SELECT status, stat_hour, last_observed_at, status_code
			FROM juhe_stats.account_health_hourly WHERE account_id = 'acc-1'`).Scan(&status, &statHour, &lastObserved, &statusCode); err != nil {
			t.Fatalf("read strip row: %v", err)
		}
		return status, statHour, lastObserved, statusCode
	}
	return writer, inspect
}

// TestHealthHourlyWriter 时区桶 + newest-wins：观测时刻按统计时区落桶；
// 更新观测覆盖旧值，旧观测不回退新值（文本比较即时间序）。
func TestHealthHourlyWriter(t *testing.T) {
	writer, inspect := newHourlyWriterFixture(t)
	ctx := context.Background()

	// 23:30 UTC → +08 时区次日 07 时桶。
	first := accounthealth.AccountHealthHourlyObservation{
		AccountID: "acc-1", ProviderCode: "openai", SystemAccountID: "sysacc-owner",
		ObservedAt: time.Date(2026, 9, 26, 23, 30, 0, 0, time.UTC),
		OutcomeID:  "outcome-1", Success: false, ErrorCode: "upstream_failure",
	}
	if err := writer.RecordAccountHealthHourly(ctx, first); err != nil {
		t.Fatalf("first write: %v", err)
	}
	status, statHour, lastObserved, statusCode := inspect(t)
	if status != "failure" || statHour != "2026-09-27T07" {
		t.Fatalf("status/hour = %s/%s, want failure/2026-09-27T07", status, statHour)
	}
	if lastObserved != "2026-09-26T23:30:00.000Z" || statusCode.Valid {
		t.Fatalf("lastObserved/statusCode = %s/%v (status_code 缺省应为 NULL)", lastObserved, statusCode)
	}

	// 更新观测（同一桶，更晚，成功）覆盖。
	second := first
	second.OutcomeID = "outcome-2"
	second.Success = true
	second.ObservedAt = time.Date(2026, 9, 26, 23, 45, 0, 0, time.UTC)
	second.StatusCode = 200
	if err := writer.RecordAccountHealthHourly(ctx, second); err != nil {
		t.Fatalf("newer write: %v", err)
	}
	status, _, lastObserved, statusCode = inspect(t)
	if status != "success" || lastObserved != "2026-09-26T23:45:00.000Z" || statusCode.String != "200" {
		t.Fatalf("newer observation not applied: %s/%s/%v", status, lastObserved, statusCode)
	}

	// 旧观测不回退新值。
	older := first
	older.OutcomeID = "outcome-0"
	older.ObservedAt = time.Date(2026, 9, 26, 23, 10, 0, 0, time.UTC)
	if err := writer.RecordAccountHealthHourly(ctx, older); err != nil {
		t.Fatalf("older write: %v", err)
	}
	status, _, lastObserved, _ = inspect(t)
	if status != "success" || lastObserved != "2026-09-26T23:45:00.000Z" {
		t.Fatalf("older observation downgraded strip: %s/%s", status, lastObserved)
	}
}
