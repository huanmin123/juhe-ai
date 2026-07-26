package postgres

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/modelquality"
	"juhe-ai/backend-go/internal/store/port"
)

func TestModelQualityScheduleSQLUsesLockedRowsAndOpaqueCompletionFence(t *testing.T) {
	for _, fragment := range []string{
		"FOR UPDATE OF mqs SKIP LOCKED",
		"accounts.authorization_instance_authorization_id IS NULL",
		"accounts.status = 'active'",
		"ORDER BY mqs.next_run_at ASC, mqs.id ASC",
	} {
		if !strings.Contains(claimDueModelQualityScheduleCandidatesSQL, fragment) {
			t.Fatalf("claim candidate SQL missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"lease_token = $2",
		"revision = $5",
		"accounts.config_revision = $6",
		"accounts.status = 'active'",
		"next_run_at <= db_clock.now_text",
		"lease_until IS NULL OR lease_until <= db_clock.now_text",
		"RETURNING lease_until, updated_at",
	} {
		if !strings.Contains(claimModelQualityScheduleSQL, fragment) {
			t.Fatalf("claim update SQL missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"revision = $5",
		"interval_minutes = $3",
		"lease_owner = $6",
		"lease_token = $7",
		"lease_until = $8",
		"lease_until > db_clock.now_text",
		"next_run_at = to_char",
		"lease_token = NULL",
	} {
		if !strings.Contains(completeModelQualityScheduleSQL, fragment) {
			t.Fatalf("completion SQL missing %q", fragment)
		}
	}
	if !strings.Contains(updateModelQualityScheduleSQL, "lease_token = NULL") || !strings.Contains(updateModelQualityScheduleSQL, "revision = revision + 1") {
		t.Fatal("schedule edit must revoke an old token lease and advance config revision")
	}
	for name, sql := range map[string]string{
		"candidates": claimDueModelQualityScheduleCandidatesSQL,
		"claim":      claimModelQualityScheduleSQL,
		"complete":   completeModelQualityScheduleSQL,
	} {
		if strings.Count(sql, "clock_timestamp()") != 1 || !strings.Contains(sql, `'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'`) {
			t.Fatalf("%s SQL must fix one canonical database clock", name)
		}
	}
}

func TestClaimDueModelQualitySchedulesReturnsCommittedTokenizedFacts(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	leaseUntil := now.Add(5 * time.Minute)
	tx := &modelQualityScheduleTxStub{
		queryRows: &modelQualityScheduleRowsStub{rows: [][]any{
			modelQualityScheduleRowValues("mqs_1", 4, 60, 1, now.Add(-time.Minute), pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, now.Add(-time.Hour), now.Add(-time.Hour), int64(9)),
		}},
		queryRowsErr: nil,
		queryRowQueue: []pgx.Row{
			modelQualityScheduleRowStub{values: []any{modelQualityPolicyTimeText(leaseUntil), modelQualityPolicyTimeText(now)}},
			modelQualityScheduleRowStub{values: []any{"sys_admin", int64(7), "full", int64(1), int64(80), "quality_isolate", int64(30), modelQualityPolicyTimeText(now.Add(-time.Hour)), modelQualityPolicyTimeText(now.Add(-time.Hour))}},
		},
	}
	claims, err := claimDueModelQualitySchedules(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		port.ModelQualityScheduleClaimInput{OwnerID: "ops-1", LeaseDuration: 5 * time.Minute},
		func(string) (string, error) { return "claim_token_1", nil },
	)
	if err != nil {
		t.Fatalf("claimDueModelQualitySchedules() error = %v", err)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 0 || len(claims) != 1 {
		t.Fatalf("tx commit/rollback/claims = %d/%d/%d", tx.commitCalls, tx.rollbackCalls, len(claims))
	}
	claim := claims[0]
	if claim.Schedule.ID != "mqs_1" || claim.Schedule.Revision != 4 || claim.AccountConfigRevision != 9 || claim.Policy.Policy.Revision != 7 {
		t.Fatalf("claim facts = %+v", claim)
	}
	if claim.Lease.OwnerID != "ops-1" || claim.Lease.ClaimToken != "claim_token_1" || !claim.Lease.Until.Equal(leaseUntil) || claim.Schedule.Lease == nil || *claim.Schedule.Lease != claim.Lease {
		t.Fatalf("claim lease = %+v schedule lease = %+v", claim.Lease, claim.Schedule.Lease)
	}
	if len(tx.execCalls) != 0 || len(tx.queryCalls) != 3 || !reflect.DeepEqual(tx.queryCalls[1].args, []any{"ops-1", "claim_token_1", int64((5 * time.Minute) / time.Millisecond), "mqs_1", int64(4), int64(9)}) {
		t.Fatalf("claim update calls = %+v", tx.queryCalls)
	}
	if rows := tx.queryRows.(*modelQualityScheduleRowsStub); !rows.closed {
		t.Fatal("claim candidate rows were not closed before later transaction queries")
	}
}

func TestClaimDueModelQualitySchedulesSkipsAccountThatChangedAfterSelection(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	tx := &modelQualityScheduleTxStub{
		queryRows: &modelQualityScheduleRowsStub{rows: [][]any{
			modelQualityScheduleRowValues("mqs_1", 4, 60, 1, now.Add(-time.Minute), pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, now.Add(-time.Hour), now.Add(-time.Hour), int64(9)),
		}},
		queryRowQueue: []pgx.Row{modelQualityScheduleRowStub{err: pgx.ErrNoRows}},
	}
	claims, err := claimDueModelQualitySchedules(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		port.ModelQualityScheduleClaimInput{OwnerID: "ops-1", LeaseDuration: 5 * time.Minute, Limit: 1},
		func(string) (string, error) { return "claim_token_1", nil },
	)
	if err != nil || len(claims) != 0 || tx.commitCalls != 1 || tx.rollbackCalls != 0 || len(tx.queryRowQueue) != 0 {
		t.Fatalf("claims=%d error=%v commit/rollback=%d/%d policyQueries=%d", len(claims), err, tx.commitCalls, tx.rollbackCalls, len(tx.queryRowQueue))
	}
}

func TestClaimDueModelQualitySchedulesReturnsEntropyFailureAndRollsBack(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	tx := &modelQualityScheduleTxStub{
		queryRows: &modelQualityScheduleRowsStub{rows: [][]any{
			modelQualityScheduleRowValues("mqs_1", 4, 60, 1, now.Add(-time.Minute), pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, now.Add(-time.Hour), now.Add(-time.Hour), int64(9)),
		}},
	}
	wantErr := errors.New("entropy unavailable")
	_, err := claimDueModelQualitySchedules(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		port.ModelQualityScheduleClaimInput{OwnerID: "ops-1", LeaseDuration: 5 * time.Minute, Limit: 1},
		func(string) (string, error) { return "", wantErr },
	)
	if !errors.Is(err, wantErr) || tx.commitCalls != 0 || tx.rollbackCalls != 1 || len(tx.execCalls) != 0 {
		t.Fatalf("error=%v commit/rollback=%d/%d exec=%d", err, tx.commitCalls, tx.rollbackCalls, len(tx.execCalls))
	}
}

func TestCompleteModelQualityScheduleUsesAllFencesAndIgnoresCallerClock(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	input := port.ModelQualityScheduleCompleteInput{
		ScheduleID: "mqs_1", ExpectedRevision: 4,
		Lease: port.ModelQualityScheduleLease{OwnerID: "ops-1", ClaimToken: "claim_token_1", Until: now.Add(5 * time.Minute)},
		RunID: "run_1", Status: port.ModelQualityScheduleRunCompleted,
		CompletedAt: now.Add(24 * time.Hour), Interval: time.Hour,
	}
	execer := &modelQualityScheduleExecStub{tag: pgconn.NewCommandTag("UPDATE 1")}
	completed, err := completeModelQualitySchedule(context.Background(), execer, input)
	if err != nil || !completed {
		t.Fatalf("completed=%v error=%v", completed, err)
	}
	wantArgs := []any{"run_1", "completed", 60, "mqs_1", int64(4), "ops-1", "claim_token_1", modelQualityPolicyTimeText(now.Add(5 * time.Minute))}
	if !reflect.DeepEqual(execer.args, wantArgs) {
		t.Fatalf("completion args = %#v, want %#v", execer.args, wantArgs)
	}

	input.CompletedAt = time.Time{}
	execer.calls = 0
	if completed, err := completeModelQualitySchedule(context.Background(), execer, input); err != nil || !completed || execer.calls != 1 {
		t.Fatalf("database-authorized completion completed=%v error=%v calls=%d", completed, err, execer.calls)
	}
}

func TestUpsertModelQualityScheduleReturnsLockedConflictWithoutWriting(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	expected := modelquality.ScheduleRevision(3)
	tx := &modelQualityScheduleTxStub{queryRowQueue: []pgx.Row{
		modelQualityScheduleRowStub{values: []any{"account_1"}},
		modelQualityScheduleRowStub{values: modelQualityScheduleRowValues("mqs_1", 4, 60, 1, now.Add(time.Hour), pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, pgtype.Text{}, now.Add(-time.Hour), now.Add(-time.Hour))},
	}}
	result, err := upsertModelQualitySchedule(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		port.ModelQualityScheduleUpsertInput{SystemAccountID: "sys_admin", AccountID: "account_1", Model: "gpt-5", Interval: time.Hour, Enabled: true, ExpectedRevision: &expected, UpdatedAt: now},
		func(string) (string, error) { return "mqs_new", nil },
	)
	if err != nil || result.Status != port.ModelQualityScheduleConflict || result.Schedule == nil || result.Schedule.Revision != 4 || len(tx.execCalls) != 0 || tx.commitCalls != 1 {
		t.Fatalf("result=%+v error=%v exec=%d commit=%d", result, err, len(tx.execCalls), tx.commitCalls)
	}
}

func TestScanModelQualityScheduleAcceptsLegacyLeaseButRejectsPartialOrCorruptFacts(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	legacy := modelQualityScheduleRowStub{values: modelQualityScheduleRowValues(
		"mqs_1", 4, 60, 1, now.Add(time.Hour),
		pgtype.Text{}, pgtype.Text{}, pgtype.Text{},
		pgtype.Text{String: "node-owner", Valid: true}, pgtype.Text{}, pgtype.Text{String: modelQualityPolicyTimeText(now.Add(time.Minute)), Valid: true},
		now.Add(-time.Hour), now.Add(-time.Hour),
	)}
	schedule, err := scanModelQualitySchedule(legacy)
	if err != nil || schedule.Lease != nil {
		t.Fatalf("legacy schedule=%+v error=%v", schedule, err)
	}

	partial := legacy
	partial.values = append([]any(nil), legacy.values...)
	partial.values[13] = pgtype.Text{}
	if _, err := scanModelQualitySchedule(partial); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("partial legacy lease error=%v", err)
	}

	badLastRun := legacy
	badLastRun.values = append([]any(nil), legacy.values...)
	badLastRun.values[10] = pgtype.Text{String: "completed", Valid: true}
	if _, err := scanModelQualitySchedule(badLastRun); err == nil || !strings.Contains(err.Error(), "last run facts") {
		t.Fatalf("bad last run error=%v", err)
	}
}

func TestModelQualityScheduleValidationBoundsDatabaseAndLeaseInputs(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	validClaim := port.ModelQualityScheduleClaimInput{OwnerID: "ops-1", LeaseDuration: time.Minute, Limit: 1}
	if err := validateModelQualityScheduleClaimInput(validClaim); err != nil {
		t.Fatalf("valid claim error=%v", err)
	}
	for _, mutate := range []func(*port.ModelQualityScheduleClaimInput){
		func(v *port.ModelQualityScheduleClaimInput) { v.OwnerID = " owner" },
		func(v *port.ModelQualityScheduleClaimInput) { v.LeaseDuration = time.Minute - time.Nanosecond },
		func(v *port.ModelQualityScheduleClaimInput) {
			v.LeaseDuration = port.ModelQualityClaimMaximumLease + time.Nanosecond
		},
		func(v *port.ModelQualityScheduleClaimInput) { v.Limit = port.ModelQualityScheduleClaimMaximumLimit + 1 },
	} {
		input := validClaim
		mutate(&input)
		if err := validateModelQualityScheduleClaimInput(input); err == nil {
			t.Fatalf("invalid claim accepted: %+v", input)
		}
	}
	overflow := modelquality.ScheduleRevision(math.MaxInt32 + 1)
	if err := validateModelQualityScheduleUpsertInput(port.ModelQualityScheduleUpsertInput{SystemAccountID: "sys", AccountID: "account", Model: "gpt", Interval: time.Hour, UpdatedAt: now, ExpectedRevision: &overflow}); err == nil {
		t.Fatal("PostgreSQL INTEGER revision overflow was accepted")
	}
}

func modelQualityScheduleRowValues(
	id string,
	revision int64,
	intervalMinutes int64,
	enabled int64,
	nextRun time.Time,
	lastRunID, lastRunAt, lastRunStatus pgtype.Text,
	leaseOwner, leaseToken, leaseUntil pgtype.Text,
	createdAt, updatedAt time.Time,
	tail ...any,
) []any {
	values := []any{
		id, "sys_admin", "account_1", "gpt-5", intervalMinutes, enabled, revision,
		modelQualityPolicyTimeText(nextRun), lastRunID, lastRunAt, lastRunStatus,
		leaseOwner, leaseToken, leaseUntil,
		modelQualityPolicyTimeText(createdAt), modelQualityPolicyTimeText(updatedAt),
	}
	return append(values, tail...)
}

type modelQualityScheduleQueryCall struct {
	query string
	args  []any
}

type modelQualityScheduleTxStub struct {
	pgx.Tx
	queryRows     pgx.Rows
	queryRowsErr  error
	queryRowQueue []pgx.Row
	execTags      []pgconn.CommandTag
	execErrs      []error
	queryCalls    []modelQualityScheduleQueryCall
	execCalls     []modelQualityScheduleQueryCall
	commitCalls   int
	rollbackCalls int
}

func (s *modelQualityScheduleTxStub) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	s.queryCalls = append(s.queryCalls, modelQualityScheduleQueryCall{query: query, args: args})
	return s.queryRows, s.queryRowsErr
}

func (s *modelQualityScheduleTxStub) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	s.queryCalls = append(s.queryCalls, modelQualityScheduleQueryCall{query: query, args: args})
	if len(s.queryRowQueue) == 0 {
		return modelQualityScheduleRowStub{err: errors.New("unexpected QueryRow")}
	}
	row := s.queryRowQueue[0]
	s.queryRowQueue = s.queryRowQueue[1:]
	return row
}

func (s *modelQualityScheduleTxStub) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	s.execCalls = append(s.execCalls, modelQualityScheduleQueryCall{query: query, args: args})
	var tag pgconn.CommandTag
	if len(s.execTags) > 0 {
		tag = s.execTags[0]
		s.execTags = s.execTags[1:]
	}
	var err error
	if len(s.execErrs) > 0 {
		err = s.execErrs[0]
		s.execErrs = s.execErrs[1:]
	}
	return tag, err
}

func (s *modelQualityScheduleTxStub) Commit(context.Context) error {
	s.commitCalls++
	return nil
}

func (s *modelQualityScheduleTxStub) Rollback(context.Context) error {
	s.rollbackCalls++
	return nil
}

type modelQualityScheduleRowsStub struct {
	pgx.Rows
	rows   [][]any
	index  int
	closed bool
	err    error
}

func (s *modelQualityScheduleRowsStub) Next() bool {
	if s.index >= len(s.rows) {
		return false
	}
	s.index++
	return true
}

func (s *modelQualityScheduleRowsStub) Scan(dest ...any) error {
	if s.index == 0 || s.index > len(s.rows) {
		return errors.New("Scan called without current row")
	}
	return assignModelQualityScheduleScan(dest, s.rows[s.index-1])
}

func (s *modelQualityScheduleRowsStub) Err() error { return s.err }
func (s *modelQualityScheduleRowsStub) Close()     { s.closed = true }

type modelQualityScheduleRowStub struct {
	values []any
	err    error
}

func (s modelQualityScheduleRowStub) Scan(dest ...any) error {
	if s.err != nil {
		return s.err
	}
	return assignModelQualityScheduleScan(dest, s.values)
}

func assignModelQualityScheduleScan(dest []any, values []any) error {
	if len(dest) != len(values) {
		return errors.New("unexpected scan length")
	}
	for index, value := range values {
		switch target := dest[index].(type) {
		case *string:
			*target = value.(string)
		case *int64:
			*target = value.(int64)
		case *pgtype.Text:
			*target = value.(pgtype.Text)
		default:
			return errors.New("unexpected scan destination")
		}
	}
	return nil
}

type modelQualityScheduleExecStub struct {
	tag   pgconn.CommandTag
	err   error
	args  []any
	calls int
}

func (s *modelQualityScheduleExecStub) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	s.calls++
	s.args = args
	return s.tag, s.err
}

var _ pgx.Tx = (*modelQualityScheduleTxStub)(nil)
var _ pgx.Rows = (*modelQualityScheduleRowsStub)(nil)
