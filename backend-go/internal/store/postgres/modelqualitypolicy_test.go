package postgres

import (
	"context"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"juhe-ai/backend-go/internal/modelquality"
	"juhe-ai/backend-go/internal/store/port"
)

func TestModelQualityPolicyStoreImplementsPortsAndUsesCAS(t *testing.T) {
	var _ port.ModelQualityPolicyReader = (*Store)(nil)
	var _ port.ModelQualityPolicyWriter = (*Store)(nil)

	source := readModelQualityPolicyStoreSource(t)
	for _, required := range []string{
		"juhe_business.model_quality_policies",
		"ON CONFLICT (system_account_id) DO NOTHING",
		"WHERE system_account_id = $1 AND revision = $8",
		"SET revision = revision + 1",
		"modelquality.DefaultPolicy(systemAccountID)",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("store source missing %q", required)
		}
	}
	for _, forbidden := range []string{"database/sql", "sqlite", "http.", "worker"} {
		if strings.Contains(strings.ToLower(source), forbidden) {
			t.Errorf("store source contains forbidden %q", forbidden)
		}
	}
}

func TestReadModelQualityPolicyUsesEffectiveDefaultAndStrictPersistedMapping(t *testing.T) {
	ctx := context.Background()
	q := &modelQualityPolicyQueryStub{rows: []modelQualityPolicyTestRow{{err: pgx.ErrNoRows}}}
	record, err := readModelQualityPolicy(ctx, q, "sys_admin")
	if err != nil || record.Persisted || !reflect.DeepEqual(record.Policy, modelquality.DefaultPolicy("sys_admin")) {
		t.Fatalf("default record=%+v error=%v", record, err)
	}

	q = &modelQualityPolicyQueryStub{rows: []modelQualityPolicyTestRow{{values: modelQualityPolicyRowValues(7, "full", 0, 88, "quality_isolate", 35)}}}
	record, err = readModelQualityPolicy(ctx, q, "sys_admin")
	if err != nil || !record.Persisted || record.Policy.Revision != 7 || record.Policy.Profile != modelquality.ProfileFull || record.Policy.ManualEnforcementEnabled || record.Policy.PenaltyAction != modelquality.ActionQualityIsolate || record.CreatedAt == nil || record.UpdatedAt == nil {
		t.Fatalf("persisted record=%+v error=%v", record, err)
	}

	q = &modelQualityPolicyQueryStub{rows: []modelQualityPolicyTestRow{{values: modelQualityPolicyRowValues(1, "quick", 2, 70, "fallback", 10)}}}
	if _, err := readModelQualityPolicy(ctx, q, "sys_admin"); err == nil || !strings.Contains(err.Error(), "manual_enforcement_enabled") {
		t.Fatalf("invalid bool error=%v", err)
	}
}

func TestSaveModelQualityPolicyCreateUpdateAndConflict(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 26, 9, 45, 12, 345678900, time.FixedZone("UTC+8", 8*60*60))
	input := modelQualityPolicySaveInput(now)

	q := &modelQualityPolicyQueryStub{rows: []modelQualityPolicyTestRow{{values: modelQualityPolicyRowValues(1, "full", 1, 85, "fallback", 30)}}}
	result, err := saveModelQualityPolicy(ctx, q, input)
	if err != nil || result.Status != port.ModelQualityPolicySaved || result.Policy.Policy.Revision != 1 {
		t.Fatalf("create result=%+v error=%v", result, err)
	}
	if len(q.calls) != 1 || !strings.Contains(q.calls[0].sql, "INSERT INTO") || !reflect.DeepEqual(q.calls[0].args, []any{"sys_admin", "full", 1, 85, "fallback", 30, "2026-07-26T01:45:12.345Z"}) {
		t.Fatalf("create calls=%+v", q.calls)
	}

	input.ExpectedRevision = 1
	q = &modelQualityPolicyQueryStub{rows: []modelQualityPolicyTestRow{{values: modelQualityPolicyRowValues(2, "full", 1, 85, "fallback", 30)}}}
	result, err = saveModelQualityPolicy(ctx, q, input)
	if err != nil || result.Status != port.ModelQualityPolicySaved || result.Policy.Policy.Revision != 2 {
		t.Fatalf("update result=%+v error=%v", result, err)
	}
	if len(q.calls) != 1 || !strings.Contains(q.calls[0].sql, "UPDATE") || q.calls[0].args[7] != int64(1) {
		t.Fatalf("update calls=%+v", q.calls)
	}

	input.ExpectedRevision = 0
	q = &modelQualityPolicyQueryStub{rows: []modelQualityPolicyTestRow{
		{err: pgx.ErrNoRows},
		{values: modelQualityPolicyRowValues(3, "quick", 1, 70, "fallback", 10)},
	}}
	result, err = saveModelQualityPolicy(ctx, q, input)
	if err != nil || result.Status != port.ModelQualityPolicyConflict || !result.Policy.Persisted || result.Policy.Policy.Revision != 3 {
		t.Fatalf("conflict result=%+v error=%v", result, err)
	}
	if len(q.calls) != 2 || !strings.Contains(q.calls[0].sql, "INSERT") || !strings.Contains(q.calls[1].sql, "SELECT") {
		t.Fatalf("conflict calls=%+v", q.calls)
	}
}

func TestSaveModelQualityPolicyRejectsInvalidInputBeforeQuery(t *testing.T) {
	now := time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*port.ModelQualityPolicySaveInput)
	}{
		{"blank system account", func(v *port.ModelQualityPolicySaveInput) { v.SystemAccountID = " sys_admin" }},
		{"revision overflow", func(v *port.ModelQualityPolicySaveInput) {
			v.ExpectedRevision = modelquality.PolicyRevision(math.MaxInt32)
		}},
		{"not whole minute", func(v *port.ModelQualityPolicySaveInput) { v.RecoveryInterval = 10*time.Minute + time.Second }},
		{"invalid action", func(v *port.ModelQualityPolicySaveInput) { v.PenaltyAction = "erase" }},
		{"missing time", func(v *port.ModelQualityPolicySaveInput) { v.UpdatedAt = time.Time{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := modelQualityPolicySaveInput(now)
			test.mutate(&input)
			q := &modelQualityPolicyQueryStub{}
			if _, err := saveModelQualityPolicy(context.Background(), q, input); err == nil || len(q.calls) != 0 {
				t.Fatalf("error=%v calls=%+v", err, q.calls)
			}
		})
	}
}

func TestModelQualityPolicyTimeParsingAndNoRowErrors(t *testing.T) {
	if _, err := modelQualityPolicyParseTime("bad"); err == nil {
		t.Fatal("expected invalid time error")
	}
	q := &modelQualityPolicyQueryStub{rows: []modelQualityPolicyTestRow{{err: errors.New("postgres unavailable")}}}
	if _, err := readModelQualityPolicy(context.Background(), q, "sys_admin"); err == nil || !strings.Contains(err.Error(), "read model quality policy") {
		t.Fatalf("read error=%v", err)
	}
}

func modelQualityPolicySaveInput(now time.Time) port.ModelQualityPolicySaveInput {
	return port.ModelQualityPolicySaveInput{
		SystemAccountID:          "sys_admin",
		ExpectedRevision:         0,
		Profile:                  modelquality.ProfileFull,
		ManualEnforcementEnabled: true,
		PenaltyThreshold:         85,
		PenaltyAction:            modelquality.ActionFallback,
		RecoveryInterval:         30 * time.Minute,
		UpdatedAt:                now,
	}
}

func modelQualityPolicyRowValues(revision int64, profile string, enabled int64, threshold int64, action string, recovery int64) []any {
	return []any{"sys_admin", revision, profile, enabled, threshold, action, recovery, "2026-07-26T01:45:12.345Z", "2026-07-26T01:45:12.345Z"}
}

type modelQualityPolicyQueryCall struct {
	sql  string
	args []any
}
type modelQualityPolicyQueryStub struct {
	rows  []modelQualityPolicyTestRow
	calls []modelQualityPolicyQueryCall
}
type modelQualityPolicyTestRow struct {
	values []any
	err    error
}

func (s *modelQualityPolicyQueryStub) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	s.calls = append(s.calls, modelQualityPolicyQueryCall{sql: sql, args: args})
	if len(s.rows) == 0 {
		return modelQualityPolicyTestRow{err: errors.New("unexpected query")}
	}
	row := s.rows[0]
	s.rows = s.rows[1:]
	return row
}

func (r modelQualityPolicyTestRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("unexpected scan length")
	}
	for index, value := range r.values {
		switch target := dest[index].(type) {
		case *string:
			*target = value.(string)
		case *int64:
			*target = value.(int64)
		default:
			return errors.New("unexpected scan destination")
		}
	}
	return nil
}

func readModelQualityPolicyStoreSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("modelqualitypolicy.go")
	if err != nil {
		t.Fatalf("read store source: %v", err)
	}
	return string(data)
}
