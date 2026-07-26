package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/modelquality"
	"juhe-ai/backend-go/internal/store/port"
)

func TestClaimFailedModelQualityHealthSyncsDefaultsAndBoundsBatch(t *testing.T) {
	for _, test := range []struct {
		name      string
		limit     int
		wantLimit int
	}{
		{name: "default", limit: 0, wantLimit: port.ModelQualityHealthSyncClaimDefaultLimit},
		{name: "maximum", limit: port.ModelQualityHealthSyncClaimMaximumLimit, wantLimit: port.ModelQualityHealthSyncClaimMaximumLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := &modelQualityScheduleTxStub{queryRows: &modelQualityScheduleRowsStub{}}
			batch, err := claimFailedModelQualityHealthSyncs(
				context.Background(),
				func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
				port.ModelQualityHealthSyncClaimInput{
					OwnerID: "health-sync-1", Limit: test.limit,
				},
				func() (string, error) { return "mqhs_claim_token_1", nil },
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(batch.Claims) != 0 || batch.Quarantined != 0 || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
				t.Fatalf("batch=%+v commit/rollback=%d/%d", batch, tx.commitCalls, tx.rollbackCalls)
			}
			if len(tx.queryCalls) != 1 {
				t.Fatalf("query calls=%d, want 1", len(tx.queryCalls))
			}
			wantArgs := []any{test.wantLimit, modelQualityHealthSyncMaximumDecisionBytes}
			if !reflect.DeepEqual(tx.queryCalls[0].args, wantArgs) {
				t.Fatalf("candidate args=%#v, want %#v", tx.queryCalls[0].args, wantArgs)
			}
		})
	}

	beginCalls := 0
	_, err := claimFailedModelQualityHealthSyncs(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
			beginCalls++
			return nil, errors.New("must not begin")
		},
		port.ModelQualityHealthSyncClaimInput{
			OwnerID: "health-sync-1",
			Limit:   port.ModelQualityHealthSyncClaimMaximumLimit + 1,
		},
		func() (string, error) { return "mqhs_claim_token_1", nil },
	)
	if err == nil || beginCalls != 0 {
		t.Fatalf("over-limit error=%v beginCalls=%d", err, beginCalls)
	}
}

func TestClaimFailedModelQualityHealthSyncsQuarantinesBadDecisionWithoutStarvingValidRun(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	leaseUntil := now.Add(2 * time.Minute)
	badDecision := `{"threshold":70,"threshold":71,"healthSyncResult":"failed","message":"bad","decidedAt":"2026-07-26T08:00:00.000Z"}`
	goodDecision := modelQualityHealthSyncDecisionJSON()
	tx := &modelQualityScheduleTxStub{
		queryRows: &modelQualityScheduleRowsStub{rows: [][]any{
			modelQualityHealthSyncCandidateValues("run-bad", badDecision, int64(len(badDecision)), 3, 4),
			modelQualityHealthSyncCandidateValues("run-good", goodDecision, int64(len(goodDecision)), 7, 9),
		}},
		queryRowQueue: []pgx.Row{modelQualityScheduleRowStub{values: []any{
			int64(8), modelQualityPolicyTimeText(leaseUntil), modelQualityPolicyTimeText(now),
		}}},
		execTags: []pgconn.CommandTag{pgconn.NewCommandTag("UPDATE 1")},
	}
	generatedTokens := []string{"mqhs_claim_token_bad", "mqhs_claim_token_good"}
	tokenIndex := 0
	batch, err := claimFailedModelQualityHealthSyncs(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		port.ModelQualityHealthSyncClaimInput{OwnerID: "health-sync-1", LeaseDuration: 2 * time.Minute, Limit: 2},
		func() (string, error) {
			value := generatedTokens[tokenIndex]
			tokenIndex++
			return value, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if batch.Quarantined != 1 || len(batch.Claims) != 1 || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("batch=%+v commit/rollback=%d/%d", batch, tx.commitCalls, tx.rollbackCalls)
	}
	claim := batch.Claims[0]
	if claim.RunID != "run-good" || claim.Failure.RunID != "run-good" || claim.Lease.OwnerID != "health-sync-1" ||
		claim.Lease.ClaimToken != "mqhs_claim_token_good" || claim.Lease.Epoch != 8 || !claim.Lease.Until.Equal(leaseUntil) {
		t.Fatalf("claim=%+v", claim)
	}
	if claim.Failure.ErrorCode != "model_quality_unavailable" || claim.Failure.Threshold != 70 ||
		!claim.Failure.ObservedAt.Equal(time.Date(2026, 7, 26, 8, 9, 10, 123000000, time.UTC)) || !claim.Failure.UpdatedAt.Equal(now) {
		t.Fatalf("failure=%+v", claim.Failure)
	}
	if len(tx.execCalls) != 1 || tx.execCalls[0].query != quarantineModelQualityHealthSyncRunSQL {
		t.Fatalf("quarantine calls=%+v", tx.execCalls)
	}
	quarantineArgs := tx.execCalls[0].args
	if len(quarantineArgs) != 6 || quarantineArgs[0] != int64(modelQualityHealthSyncQuarantineDelay/time.Millisecond) ||
		quarantineArgs[1] != "invalid_durable_fact" || !strings.Contains(quarantineArgs[2].(string), "duplicate top-level field") ||
		quarantineArgs[3] != "run-bad" || quarantineArgs[4] != int64(3) || quarantineArgs[5] != int64(4) {
		t.Fatalf("quarantine args=%#v", quarantineArgs)
	}
	if len(tx.queryCalls) != 2 || tx.queryCalls[1].query != claimModelQualityHealthSyncRunSQL {
		t.Fatalf("query calls=%+v", tx.queryCalls)
	}
	wantClaimArgs := []any{
		"health-sync-1", "mqhs_claim_token_good", int64((2 * time.Minute) / time.Millisecond),
		"run-good", int64(7), int64(9),
	}
	if !reflect.DeepEqual(tx.queryCalls[1].args, wantClaimArgs) {
		t.Fatalf("claim args=%#v, want %#v", tx.queryCalls[1].args, wantClaimArgs)
	}
	if rows := tx.queryRows.(*modelQualityScheduleRowsStub); !rows.closed {
		t.Fatal("candidate rows were not closed before quarantine and claim writes")
	}
}

func TestClaimFailedModelQualityHealthSyncsRejectsInvalidTokenOrEpoch(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	input := port.ModelQualityHealthSyncClaimInput{OwnerID: "health-sync-1", LeaseDuration: 2 * time.Minute, Limit: 1}

	beginCalls := 0
	_, err := claimFailedModelQualityHealthSyncs(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) {
			beginCalls++
			return nil, errors.New("must not begin")
		},
		input,
		func() (string, error) { return " bad-token", nil },
	)
	if err == nil || beginCalls != 0 {
		t.Fatalf("invalid token error=%v beginCalls=%d", err, beginCalls)
	}

	decision := modelQualityHealthSyncDecisionJSON()
	for _, test := range []struct {
		name           string
		candidateEpoch int64
		persistedEpoch int64
	}{
		{name: "non-monotonic", candidateEpoch: 7, persistedEpoch: 7},
		{name: "negative persisted", candidateEpoch: 0, persistedEpoch: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			tx := &modelQualityScheduleTxStub{
				queryRows: &modelQualityScheduleRowsStub{rows: [][]any{
					modelQualityHealthSyncCandidateValues("run-1", decision, int64(len(decision)), test.candidateEpoch, 0),
				}},
				queryRowQueue: []pgx.Row{modelQualityScheduleRowStub{values: []any{
					test.persistedEpoch, modelQualityPolicyTimeText(now.Add(2 * time.Minute)), modelQualityPolicyTimeText(now),
				}}},
			}
			_, err := claimFailedModelQualityHealthSyncs(
				context.Background(),
				func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
				input,
				func() (string, error) { return "mqhs_claim_token_1", nil },
			)
			if err == nil || tx.commitCalls != 0 || tx.rollbackCalls != 1 {
				t.Fatalf("error=%v commit/rollback=%d/%d", err, tx.commitCalls, tx.rollbackCalls)
			}
		})
	}
}

func TestCompleteModelQualityHealthSyncCommitsStatsAndDecisionCASInOneTransaction(t *testing.T) {
	input := modelQualityHealthSyncCompleteInput()
	tx := &modelQualityScheduleTxStub{
		queryRowQueue: []pgx.Row{modelQualityScheduleRowStub{values: []any{"account-1"}}},
		execTags: []pgconn.CommandTag{
			pgconn.NewCommandTag("INSERT 0 1"),
			pgconn.NewCommandTag("UPDATE 1"),
		},
	}
	result, err := completeModelQualityHealthSync(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		input,
		time.UTC,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.StatHour != "2026-07-26T08" || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("result=%+v commit/rollback=%d/%d", result, tx.commitCalls, tx.rollbackCalls)
	}
	if len(tx.queryCalls) != 1 || tx.queryCalls[0].query != lockModelQualityHealthSyncAccountSQL ||
		!reflect.DeepEqual(tx.queryCalls[0].args, []any{"account-1", "system-1"}) {
		t.Fatalf("account lock calls=%+v", tx.queryCalls)
	}
	if len(tx.execCalls) != 2 || tx.execCalls[0].query != upsertModelQualityHealthFailureSQL || tx.execCalls[1].query != completeModelQualityHealthSyncRunSQL {
		t.Fatalf("exec calls=%+v", tx.execCalls)
	}
	completionArgs := tx.execCalls[1].args
	if len(completionArgs) != 11 {
		t.Fatalf("completion args=%#v", completionArgs)
	}
	wantFenceArgs := []any{
		"run-1", "account-1", "system-1", input.Claim.DecisionFence.RawJSON, input.Claim.DecisionFence.RawUpdatedAt,
		"health-sync-1", "mqhs_claim_token_1", int64(7), modelQualityPolicyTimeText(input.Claim.Lease.Until),
	}
	if !reflect.DeepEqual(completionArgs[2:], wantFenceArgs) {
		t.Fatalf("completion fence args=%#v, want %#v", completionArgs[2:], wantFenceArgs)
	}
	if completionArgs[1] != modelQualityPolicyTimeText(input.CompletedAt) {
		t.Fatalf("completion time=%#v", completionArgs[1])
	}
	var applied map[string]any
	if err := json.Unmarshal([]byte(completionArgs[0].(string)), &applied); err != nil {
		t.Fatalf("applied decision JSON=%q error=%v", completionArgs[0], err)
	}
	if applied["healthSyncResult"] != "applied" || applied["healthStatHour"] != "2026-07-26T08" {
		t.Fatalf("applied decision=%#v", applied)
	}
	wantFuture := map[string]any{"mode": "preserve", "version": float64(2)}
	if !reflect.DeepEqual(applied["futureField"], wantFuture) {
		t.Fatalf("unknown future field=%#v, want %#v", applied["futureField"], wantFuture)
	}
}

func TestCompleteModelQualityHealthSyncCanApplyWhenHourlyUpsertRowsAffectedIsZero(t *testing.T) {
	input := modelQualityHealthSyncCompleteInput()
	tx := &modelQualityScheduleTxStub{
		queryRowQueue: []pgx.Row{modelQualityScheduleRowStub{values: []any{"account-1"}}},
		execTags: []pgconn.CommandTag{
			pgconn.NewCommandTag("INSERT 0 0"),
			pgconn.NewCommandTag("UPDATE 1"),
		},
	}
	result, err := completeModelQualityHealthSync(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		input,
		time.UTC,
	)
	if err != nil || !result.Applied || tx.commitCalls != 1 || tx.rollbackCalls != 0 || len(tx.execCalls) != 2 {
		t.Fatalf("result=%+v error=%v commit/rollback=%d/%d exec=%d", result, err, tx.commitCalls, tx.rollbackCalls, len(tx.execCalls))
	}
}

func TestCompleteModelQualityHealthSyncRollsBackHourlyFactWhenDecisionFenceIsStale(t *testing.T) {
	input := modelQualityHealthSyncCompleteInput()
	input.CompletedAt = input.Claim.Lease.Until.Add(time.Minute)
	tx := &modelQualityScheduleTxStub{
		queryRowQueue: []pgx.Row{modelQualityScheduleRowStub{values: []any{"account-1"}}},
		execTags: []pgconn.CommandTag{
			pgconn.NewCommandTag("INSERT 0 1"),
			pgconn.NewCommandTag("UPDATE 0"),
		},
	}
	result, err := completeModelQualityHealthSync(
		context.Background(),
		func(context.Context, pgx.TxOptions) (pgx.Tx, error) { return tx, nil },
		input,
		time.UTC,
	)
	if err != nil || result.Applied || result.StatHour != "" || tx.commitCalls != 0 || tx.rollbackCalls != 1 || len(tx.execCalls) != 2 {
		t.Fatalf("result=%+v error=%v commit/rollback=%d/%d exec=%d", result, err, tx.commitCalls, tx.rollbackCalls, len(tx.execCalls))
	}
}

func TestReleaseModelQualityHealthSyncUsesLeaseFenceAndBoundsErrors(t *testing.T) {
	input := modelQualityHealthSyncReleaseInput()
	stale := &modelQualityHealthExecStub{tag: pgconn.NewCommandTag("UPDATE 0")}
	released, err := releaseModelQualityHealthSync(context.Background(), stale, input)
	if err != nil || released || stale.calls != 1 || stale.query != releaseModelQualityHealthSyncRunSQL {
		t.Fatalf("released=%v error=%v calls=%d query=%q", released, err, stale.calls, stale.query)
	}
	wantArgs := []any{
		int64(input.RetryDelay / time.Millisecond), input.ErrorClass, input.ErrorMessage, input.RunID,
		"health-sync-1", "mqhs_claim_token_1", int64(7), modelQualityPolicyTimeText(input.Lease.Until),
	}
	if !reflect.DeepEqual(stale.args, wantArgs) {
		t.Fatalf("release args=%#v, want %#v", stale.args, wantArgs)
	}

	for _, test := range []struct {
		name   string
		mutate func(*port.ModelQualityHealthSyncReleaseInput)
	}{
		{name: "missing lease expiry", mutate: func(v *port.ModelQualityHealthSyncReleaseInput) { v.Lease.Until = time.Time{} }},
		{name: "retry too short", mutate: func(v *port.ModelQualityHealthSyncReleaseInput) { v.RetryDelay = 999 * time.Millisecond }},
		{name: "retry too long", mutate: func(v *port.ModelQualityHealthSyncReleaseInput) {
			v.RetryDelay = modelQualityHealthSyncMaximumRetryDelay + time.Millisecond
		}},
		{name: "missing epoch", mutate: func(v *port.ModelQualityHealthSyncReleaseInput) { v.Lease.Epoch = 0 }},
		{name: "epoch over postgres bigint", mutate: func(v *port.ModelQualityHealthSyncReleaseInput) { v.Lease.Epoch = uint64(math.MaxInt64) + 1 }},
		{name: "invalid error class", mutate: func(v *port.ModelQualityHealthSyncReleaseInput) { v.ErrorClass = "transient\nerror" }},
		{name: "NUL error message", mutate: func(v *port.ModelQualityHealthSyncReleaseInput) { v.ErrorMessage = "bad\x00message" }},
		{name: "oversized error message", mutate: func(v *port.ModelQualityHealthSyncReleaseInput) {
			v.ErrorMessage = strings.Repeat("x", modelQualityHealthSyncMaximumErrorBytes+1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := input
			test.mutate(&candidate)
			execer := &modelQualityHealthExecStub{}
			if _, err := releaseModelQualityHealthSync(context.Background(), execer, candidate); err == nil || execer.calls != 0 {
				t.Fatalf("error=%v calls=%d", err, execer.calls)
			}
		})
	}

	wantErr := errors.New("database unavailable")
	execer := &modelQualityHealthExecStub{err: wantErr}
	if _, err := releaseModelQualityHealthSync(context.Background(), execer, input); !errors.Is(err, wantErr) {
		t.Fatalf("error=%v, want wrapped %v", err, wantErr)
	}
}

func TestModelQualityHealthSyncDecisionRejectsDuplicatesPreservesUnknownFieldsAndBoundsBytes(t *testing.T) {
	duplicate := `{"threshold":70,"healthSyncResult":"failed","message":"one","message":"two","decidedAt":"2026-07-26T08:00:00.000Z"}`
	if _, err := decodeModelQualityHealthSyncDecision(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate top-level field") {
		t.Fatalf("duplicate error=%v", err)
	}

	raw := modelQualityHealthSyncDecisionJSON()
	appliedRaw, err := appliedModelQualityHealthSyncDecision(raw, "2026-07-26T08")
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err := json.Unmarshal([]byte(raw), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(appliedRaw), &after); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"threshold", "message", "decidedAt", "futureField"} {
		if !reflect.DeepEqual(after[key], before[key]) {
			t.Fatalf("field %q changed: before=%#v after=%#v", key, before[key], after[key])
		}
	}
	if after["healthSyncResult"] != "applied" || after["healthStatHour"] != "2026-07-26T08" {
		t.Fatalf("applied decision=%#v", after)
	}

	prefix := `{"threshold":70,"healthSyncResult":"failed","message":"ok","decidedAt":"2026-07-26T08:00:00.000Z","futurePadding":"`
	suffix := `"}`
	paddingLength := modelQualityHealthSyncMaximumDecisionBytes - len(prefix) - len(suffix)
	if paddingLength < 0 {
		t.Fatal("invalid test fixture length")
	}
	exact := prefix + strings.Repeat("x", paddingLength) + suffix
	if len(exact) != modelQualityHealthSyncMaximumDecisionBytes {
		t.Fatalf("exact decision bytes=%d", len(exact))
	}
	if _, err := decodeModelQualityHealthSyncDecision(exact); err != nil {
		t.Fatalf("exact 64KiB decision rejected: %v", err)
	}
	if _, err := decodeModelQualityHealthSyncDecision(exact + " "); err == nil {
		t.Fatal("decision above 64KiB was accepted")
	}
	if _, err := appliedModelQualityHealthSyncDecision(exact, "2026-07-26T08"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expanded 64KiB decision error=%v", err)
	}
	if !utf8.ValidString(exact) {
		t.Fatal("exact fixture must remain valid UTF-8")
	}
}

func TestModelQualityHealthSyncSQLCarriesConcurrencyAndCASFences(t *testing.T) {
	for _, fragment := range []string{
		"FOR UPDATE OF runs SKIP LOCKED",
		"clock_timestamp() AT TIME ZONE 'UTC'",
		"quality_health_sync_claim_epoch < 9223372036854775807",
		"quality_health_sync_attempt_count < 9223372036854775807",
	} {
		if !strings.Contains(claimModelQualityHealthSyncCandidatesSQL, fragment) {
			t.Fatalf("candidate SQL missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"quality_health_sync_claim_epoch = quality_health_sync_claim_epoch + 1",
		"quality_health_sync_claim_until = db_clock.lease_until_text",
		"quality_health_sync_next_attempt_at = db_clock.lease_until_text",
		"quality_health_sync_claim_epoch = $5",
		"quality_health_sync_attempt_count = $6",
		"RETURNING\n  quality_health_sync_claim_epoch",
	} {
		if !strings.Contains(claimModelQualityHealthSyncRunSQL, fragment) {
			t.Fatalf("claim SQL missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"quality_decision_json = $6",
		"updated_at = $7",
		"quality_health_sync_claim_owner = $8",
		"quality_health_sync_claim_token = $9",
		"quality_health_sync_claim_epoch = $10",
		"quality_health_sync_claim_until = $11",
		"quality_health_sync_claim_until > " + modelQualityHealthSyncDatabaseNowExpression,
	} {
		if !strings.Contains(completeModelQualityHealthSyncRunSQL, fragment) {
			t.Fatalf("completion SQL missing %q", fragment)
		}
	}
	if strings.Contains(completeModelQualityHealthSyncRunSQL, "quality_health_sync_claim_until > $2") ||
		!strings.Contains(releaseModelQualityHealthSyncRunSQL, "quality_health_sync_claim_until > "+modelQualityHealthSyncDatabaseNowExpression) {
		t.Fatal("completion/release lease expiry must use the PostgreSQL clock")
	}
}

func modelQualityHealthSyncCandidateValues(runID, decision string, decisionBytes, claimEpoch, attemptCount int64) []any {
	return []any{
		runID,
		"system-1",
		"openai",
		"account-1",
		"gpt-5",
		"quick",
		int64(0),
		"unavailable",
		pgtype.Text{String: "2026-07-26T08:09:10.123Z", Valid: true},
		pgtype.Text{String: decision, Valid: true},
		decisionBytes,
		"2026-07-26T08:10:00.000Z",
		claimEpoch,
		attemptCount,
	}
}

func modelQualityHealthSyncDecisionJSON() string {
	return `{"threshold":70,"healthSyncResult":"failed","message":"upstream unavailable","decidedAt":"2026-07-26T08:09:10.123Z","futureField":{"mode":"preserve","version":2}}`
}

func modelQualityHealthSyncCompleteInput() port.ModelQualityHealthSyncCompleteInput {
	now := time.Date(2026, 7, 26, 8, 10, 0, 0, time.UTC)
	raw := modelQualityHealthSyncDecisionJSON()
	return port.ModelQualityHealthSyncCompleteInput{
		Claim: port.ModelQualityHealthSyncClaim{
			RunID: "run-1",
			Failure: port.ModelQualityHealthFailureInput{
				AccountID: "account-1", SystemAccountID: "system-1", ProviderCode: "openai",
				ObservedAt: time.Date(2026, 7, 26, 8, 9, 10, 123000000, time.UTC), RunID: "run-1",
				Model: "gpt-5", Profile: modelquality.ProfileQuick, Score: 0, Threshold: 70,
				Level: modelquality.LevelUnavailable, ErrorCode: "model_quality_unavailable", ErrorMessage: "upstream unavailable",
			},
			DecisionFence: port.ModelQualityHealthSyncDecisionFence{
				RawJSON: raw, RawUpdatedAt: "2026-07-26T08:10:00.000Z",
			},
			Lease: port.ModelQualityHealthSyncLease{
				OwnerID: "health-sync-1", ClaimToken: "mqhs_claim_token_1", Epoch: 7, Until: now.Add(2 * time.Minute),
			},
		},
		CompletedAt: now,
	}
}

func modelQualityHealthSyncReleaseInput() port.ModelQualityHealthSyncReleaseInput {
	failedAt := time.Date(2026, 7, 26, 8, 10, 0, 0, time.UTC)
	return port.ModelQualityHealthSyncReleaseInput{
		RunID: "run-1",
		Lease: port.ModelQualityHealthSyncLease{
			OwnerID: "health-sync-1", ClaimToken: "mqhs_claim_token_1", Epoch: 7, Until: failedAt.Add(2 * time.Minute),
		},
		RetryDelay: time.Minute, ErrorClass: "transient_storage", ErrorMessage: "database unavailable",
	}
}
