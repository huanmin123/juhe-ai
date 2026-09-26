package accounthealth

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

type hourlyFakeSink struct {
	observations []AccountHealthHourlyObservation
	err          error
}

func (s *hourlyFakeSink) RecordAccountHealthHourly(_ context.Context, observation AccountHealthHourlyObservation) error {
	if s.err != nil {
		return s.err
	}
	s.observations = append(s.observations, observation)
	return nil
}

func newHourlyBusinessFixture(t *testing.T) *ProjectionBusinessDB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE accounts (
		id text PRIMARY KEY, provider_code text, system_account_id text)`); err != nil {
		t.Fatalf("create accounts: %v", err)
	}
	seed := []struct{ id, provider, owner string }{
		{"acc-1", "openai", "sysacc-owner"},
		{"acc-no-provider", "", "sysacc-owner"},
		{"acc-no-owner", "openai", ""},
	}
	for _, row := range seed {
		if _, err := db.ExecContext(context.Background(), `INSERT INTO accounts (id, provider_code, system_account_id)
			VALUES ('`+row.id+`', '`+row.provider+`', '`+row.owner+`')`); err != nil {
			t.Fatalf("seed account %s: %v", row.id, err)
		}
	}
	business, err := NewProjectionBusinessDB(db, false)
	if err != nil {
		t.Fatalf("new business handle: %v", err)
	}
	return business
}

// TestRecordAccountHealthHourlyObservation 锚定 BUG-0194 第二层的观测转换：
// outcome 值域映射、归属补齐、stale/无主账户跳过、sink 失败透传。
func TestRecordAccountHealthHourlyObservation(t *testing.T) {
	business := newHourlyBusinessFixture(t)
	observedAt := time.Date(2026, 9, 26, 15, 26, 19, 0, time.UTC)

	cases := []struct {
		name       string
		outcome    Outcome
		wantCalled bool
		wantStatus bool
		wantProv   string
	}{
		{"success", Outcome{OutcomeID: "o1", AccountID: "acc-1", Outcome: OutcomeSuccess, ObservedAt: observedAt, StatusCode: 200}, true, true, "openai"},
		{"upstream failure maps to failure", Outcome{OutcomeID: "o2", AccountID: "acc-1", Outcome: OutcomeUpstreamFailed, ObservedAt: observedAt, ErrorCode: "upstream_protocol_failure"}, true, false, "openai"},
		{"neutral maps to failure", Outcome{OutcomeID: "o3", AccountID: "acc-1", Outcome: OutcomeNeutral, ObservedAt: observedAt}, true, false, "openai"},
		{"missing provider falls to unknown", Outcome{OutcomeID: "o4", AccountID: "acc-no-provider", Outcome: OutcomeUpstreamFailed, ObservedAt: observedAt}, true, false, "unknown"},
		{"stale skipped", Outcome{OutcomeID: "o5", AccountID: "acc-1", Outcome: OutcomeStale, ObservedAt: observedAt}, false, false, ""},
		{"deleted account skipped", Outcome{OutcomeID: "o6", AccountID: "acc-gone", Outcome: OutcomeUpstreamFailed, ObservedAt: observedAt}, false, false, ""},
		{"no owner skipped", Outcome{OutcomeID: "o7", AccountID: "acc-no-owner", Outcome: OutcomeUpstreamFailed, ObservedAt: observedAt}, false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &hourlyFakeSink{}
			if err := recordAccountHealthHourlyObservation(context.Background(), sink, business, tc.outcome); err != nil {
				t.Fatalf("record: %v", err)
			}
			if tc.wantCalled != (len(sink.observations) == 1) {
				t.Fatalf("called = %v, want %v", len(sink.observations) == 1, tc.wantCalled)
			}
			if !tc.wantCalled {
				return
			}
			got := sink.observations[0]
			if got.Success != tc.wantStatus {
				t.Fatalf("success = %v, want %v", got.Success, tc.wantStatus)
			}
			if got.ProviderCode != tc.wantProv {
				t.Fatalf("provider = %q, want %q", got.ProviderCode, tc.wantProv)
			}
			if got.SystemAccountID != "sysacc-owner" {
				t.Fatalf("systemAccount = %q, want sysacc-owner", got.SystemAccountID)
			}
			if got.ObservedAt != observedAt {
				t.Fatalf("observedAt = %v, want %v", got.ObservedAt, observedAt)
			}
		})
	}
}

// TestRecordAccountHealthHourlyObservationSinkError 确保 sink 失败透传（调用方
// 据此 warn，不静默吞掉）。
func TestRecordAccountHealthHourlyObservationSinkError(t *testing.T) {
	business := newHourlyBusinessFixture(t)
	sink := &hourlyFakeSink{err: context.DeadlineExceeded}
	err := recordAccountHealthHourlyObservation(context.Background(), sink, business, Outcome{
		OutcomeID: "o-err", AccountID: "acc-1", Outcome: OutcomeUpstreamFailed,
		ObservedAt: time.Date(2026, 9, 26, 15, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("want sink error to propagate")
	}
}
