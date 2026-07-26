package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/modelquality"
	"juhe-ai/backend-go/internal/store/port"
	"juhe-ai/backend-go/internal/timezonecompat"
)

const (
	modelQualityHealthSyncMaximumDecisionBytes = 64 << 10
	modelQualityHealthSyncMaximumErrorBytes    = 64 << 10
	modelQualityHealthSyncQuarantineDelay      = time.Hour
	modelQualityHealthSyncMaximumRetryDelay    = 24 * time.Hour
)

type modelQualityHealthSyncBeginTx func(context.Context, pgx.TxOptions) (pgx.Tx, error)
type modelQualityHealthSyncTokenGenerator func() (string, error)

type modelQualityHealthSyncExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type modelQualityHealthSyncCandidate struct {
	runID           string
	systemAccountID string
	providerCode    string
	accountID       string
	model           string
	profile         string
	score           int64
	level           string
	finishedAtRaw   pgtype.Text
	decisionJSON    pgtype.Text
	decisionBytes   int64
	updatedAtRaw    string
	claimEpoch      int64
	attemptCount    int64
}

type modelQualityHealthSyncDecision struct {
	threshold int
	message   string
	decidedAt time.Time
	fields    map[string]json.RawMessage
}

func (s *Store) ClaimFailedModelQualityHealthSyncs(
	ctx context.Context,
	input port.ModelQualityHealthSyncClaimInput,
) (port.ModelQualityHealthSyncClaimBatch, error) {
	return claimFailedModelQualityHealthSyncs(ctx, s.pool.BeginTx, input, func() (string, error) {
		value, err := uuid.NewRandom()
		if err != nil {
			return "", err
		}
		return "mqhs_claim_" + strings.ReplaceAll(value.String(), "-", ""), nil
	})
}

func claimFailedModelQualityHealthSyncs(
	ctx context.Context,
	beginTx modelQualityHealthSyncBeginTx,
	input port.ModelQualityHealthSyncClaimInput,
	newToken modelQualityHealthSyncTokenGenerator,
) (port.ModelQualityHealthSyncClaimBatch, error) {
	input = normalizeModelQualityHealthSyncClaimInput(input)
	if err := validateModelQualityHealthSyncClaimInput(input); err != nil {
		return port.ModelQualityHealthSyncClaimBatch{}, err
	}
	if newToken == nil {
		return port.ModelQualityHealthSyncClaimBatch{}, fmt.Errorf("model quality health-sync token generator is required")
	}
	tokens := make([]port.ModelQualityHealthSyncClaimToken, input.Limit)
	for index := range tokens {
		tokenValue, err := newToken()
		if err != nil {
			return port.ModelQualityHealthSyncClaimBatch{}, fmt.Errorf("generate model quality health-sync claim token: %w", err)
		}
		if !validModelQualityScheduleText(tokenValue, 256) {
			return port.ModelQualityHealthSyncClaimBatch{}, fmt.Errorf("generated model quality health-sync claim token is invalid")
		}
		tokens[index] = port.ModelQualityHealthSyncClaimToken(tokenValue)
	}

	tx, err := beginModelQualityHealthSyncTx(ctx, beginTx, "claim")
	if err != nil {
		return port.ModelQualityHealthSyncClaimBatch{}, err
	}
	committed := false
	defer rollbackModelQualityHealthSyncTx(tx, &committed)()

	rows, err := tx.Query(ctx, claimModelQualityHealthSyncCandidatesSQL,
		input.Limit,
		modelQualityHealthSyncMaximumDecisionBytes,
	)
	if err != nil {
		return port.ModelQualityHealthSyncClaimBatch{}, fmt.Errorf("select model quality health-sync candidates: %w", err)
	}
	candidates := make([]modelQualityHealthSyncCandidate, 0, input.Limit)
	for rows.Next() {
		var candidate modelQualityHealthSyncCandidate
		if err := rows.Scan(
			&candidate.runID,
			&candidate.systemAccountID,
			&candidate.providerCode,
			&candidate.accountID,
			&candidate.model,
			&candidate.profile,
			&candidate.score,
			&candidate.level,
			&candidate.finishedAtRaw,
			&candidate.decisionJSON,
			&candidate.decisionBytes,
			&candidate.updatedAtRaw,
			&candidate.claimEpoch,
			&candidate.attemptCount,
		); err != nil {
			rows.Close()
			return port.ModelQualityHealthSyncClaimBatch{}, fmt.Errorf("scan model quality health-sync candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return port.ModelQualityHealthSyncClaimBatch{}, fmt.Errorf("read model quality health-sync candidates: %w", err)
	}
	rows.Close()

	result := port.ModelQualityHealthSyncClaimBatch{
		Claims: make([]port.ModelQualityHealthSyncClaim, 0, len(candidates)),
	}
	leaseMilliseconds := input.LeaseDuration / time.Millisecond
	for index, candidate := range candidates {
		claimToken := tokens[index]
		claim, prepareErr := prepareModelQualityHealthSyncClaim(candidate, input, claimToken)
		if prepareErr != nil {
			if err := quarantineModelQualityHealthSyncCandidate(ctx, tx, candidate, prepareErr); err != nil {
				return port.ModelQualityHealthSyncClaimBatch{}, err
			}
			result.Quarantined++
			continue
		}
		var persistedEpoch int64
		var leaseUntilRaw, claimedAtRaw string
		err := tx.QueryRow(ctx, claimModelQualityHealthSyncRunSQL,
			string(input.OwnerID),
			string(claimToken),
			int64(leaseMilliseconds),
			candidate.runID,
			candidate.claimEpoch,
			candidate.attemptCount,
		).Scan(&persistedEpoch, &leaseUntilRaw, &claimedAtRaw)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return port.ModelQualityHealthSyncClaimBatch{}, fmt.Errorf("claim model quality health-sync run %q: %w", candidate.runID, err)
		}
		if persistedEpoch != candidate.claimEpoch+1 || persistedEpoch < 1 {
			return port.ModelQualityHealthSyncClaimBatch{}, fmt.Errorf("claim model quality health-sync run %q returned invalid epoch", candidate.runID)
		}
		leaseUntil, err := modelQualityPolicyParseTime(leaseUntilRaw)
		if err != nil || leaseUntil.IsZero() {
			return port.ModelQualityHealthSyncClaimBatch{}, fmt.Errorf("claim model quality health-sync run %q returned invalid lease time", candidate.runID)
		}
		claimedAt, err := modelQualityPolicyParseTime(claimedAtRaw)
		if err != nil || claimedAt.IsZero() || !leaseUntil.After(claimedAt) {
			return port.ModelQualityHealthSyncClaimBatch{}, fmt.Errorf("claim model quality health-sync run %q returned invalid database time", candidate.runID)
		}
		claim.Lease.Epoch = uint64(persistedEpoch)
		claim.Lease.Until = leaseUntil
		claim.Failure.UpdatedAt = claimedAt
		result.Claims = append(result.Claims, claim)
	}

	if err := tx.Commit(ctx); err != nil {
		return port.ModelQualityHealthSyncClaimBatch{}, fmt.Errorf("commit model quality health-sync claim: %w", err)
	}
	committed = true
	return result, nil
}

func prepareModelQualityHealthSyncClaim(
	candidate modelQualityHealthSyncCandidate,
	input port.ModelQualityHealthSyncClaimInput,
	token port.ModelQualityHealthSyncClaimToken,
) (port.ModelQualityHealthSyncClaim, error) {
	if !validModelQualityScheduleText(candidate.runID, 256) ||
		!validModelQualityScheduleText(candidate.systemAccountID, 256) ||
		!validModelQualityScheduleText(candidate.providerCode, 256) ||
		!validModelQualityScheduleText(candidate.accountID, 256) ||
		!validModelQualityScheduleText(candidate.model, 512) ||
		candidate.score < 0 || candidate.score > 100 ||
		candidate.claimEpoch < 0 || candidate.claimEpoch >= math.MaxInt64 ||
		candidate.attemptCount < 0 || candidate.attemptCount >= math.MaxInt64 {
		return port.ModelQualityHealthSyncClaim{}, fmt.Errorf("persisted model quality health-sync fact is invalid")
	}
	profile := modelquality.Profile(candidate.profile)
	level := modelquality.Level(candidate.level)
	if (profile != modelquality.ProfileQuick && profile != modelquality.ProfileFull) || !validModelQualityHealthLevel(level) {
		return port.ModelQualityHealthSyncClaim{}, fmt.Errorf("persisted model quality health-sync profile or level is invalid")
	}
	updatedAt, err := modelQualityPolicyParseTime(candidate.updatedAtRaw)
	if err != nil || updatedAt.IsZero() {
		return port.ModelQualityHealthSyncClaim{}, fmt.Errorf("persisted model quality health-sync updated time is invalid")
	}
	if candidate.decisionBytes < 1 || candidate.decisionBytes > modelQualityHealthSyncMaximumDecisionBytes || !candidate.decisionJSON.Valid {
		return port.ModelQualityHealthSyncClaim{}, fmt.Errorf("persisted model quality health-sync decision exceeds its bounded contract")
	}
	decision, err := decodeModelQualityHealthSyncDecision(candidate.decisionJSON.String)
	if err != nil {
		return port.ModelQualityHealthSyncClaim{}, err
	}
	if _, err := encodeAppliedModelQualityHealthSyncDecision(decision, "0000-00-00T00"); err != nil {
		return port.ModelQualityHealthSyncClaim{}, err
	}
	observedAt := decision.decidedAt
	if candidate.finishedAtRaw.Valid {
		observedAt, err = modelQualityPolicyParseTime(candidate.finishedAtRaw.String)
		if err != nil || observedAt.IsZero() {
			return port.ModelQualityHealthSyncClaim{}, fmt.Errorf("persisted model quality health-sync finished time is invalid")
		}
	}
	errorCode := "model_quality_failed"
	if level == modelquality.LevelUnavailable {
		errorCode = "model_quality_unavailable"
	}
	return port.ModelQualityHealthSyncClaim{
		RunID: candidate.runID,
		Failure: port.ModelQualityHealthFailureInput{
			AccountID: candidate.accountID, SystemAccountID: candidate.systemAccountID,
			ProviderCode: candidate.providerCode, ObservedAt: observedAt,
			RunID: candidate.runID, Model: candidate.model, Profile: profile,
			Score: int(candidate.score), Threshold: decision.threshold, Level: level,
			ErrorCode: errorCode, ErrorMessage: truncateModelQualityTextRunes(decision.message, 1000),
		},
		DecisionFence: port.ModelQualityHealthSyncDecisionFence{
			RawJSON: candidate.decisionJSON.String, RawUpdatedAt: candidate.updatedAtRaw,
		},
		Lease: port.ModelQualityHealthSyncLease{
			OwnerID: input.OwnerID, ClaimToken: token,
		},
	}, nil
}

func quarantineModelQualityHealthSyncCandidate(
	ctx context.Context,
	execer modelQualityHealthSyncExecer,
	candidate modelQualityHealthSyncCandidate,
	reason error,
) error {
	message := truncateModelQualityTextRunes(reason.Error(), 1000)
	command, err := execer.Exec(ctx, quarantineModelQualityHealthSyncRunSQL,
		int64(modelQualityHealthSyncQuarantineDelay/time.Millisecond),
		"invalid_durable_fact",
		message,
		candidate.runID,
		candidate.claimEpoch,
		candidate.attemptCount,
	)
	if err != nil {
		return fmt.Errorf("quarantine model quality health-sync run %q: %w", candidate.runID, err)
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("quarantine model quality health-sync run %q lost its locked row", candidate.runID)
	}
	return nil
}

func (s *Store) CompleteModelQualityHealthSync(
	ctx context.Context,
	input port.ModelQualityHealthSyncCompleteInput,
) (port.ModelQualityHealthSyncCompleteResult, error) {
	timezone, found, err := s.GetManagementUsageStatsTimezone(ctx)
	if err != nil {
		return port.ModelQualityHealthSyncCompleteResult{}, fmt.Errorf("read model quality health-sync usageStatsTimezone: %w", err)
	}
	if !found {
		return port.ModelQualityHealthSyncCompleteResult{}, fmt.Errorf("model quality health-sync usageStatsTimezone is missing")
	}
	location, err := timezonecompat.LoadNodeLocation(timezone)
	if err != nil {
		return port.ModelQualityHealthSyncCompleteResult{}, fmt.Errorf("model quality health-sync usageStatsTimezone is invalid: %w", err)
	}
	return completeModelQualityHealthSync(ctx, s.pool.BeginTx, input, location)
}

func completeModelQualityHealthSync(
	ctx context.Context,
	beginTx modelQualityHealthSyncBeginTx,
	input port.ModelQualityHealthSyncCompleteInput,
	location *time.Location,
) (port.ModelQualityHealthSyncCompleteResult, error) {
	if err := validateModelQualityHealthSyncCompleteInput(input); err != nil {
		return port.ModelQualityHealthSyncCompleteResult{}, err
	}
	failure := input.Claim.Failure
	failure.UpdatedAt = input.CompletedAt
	prepared, err := prepareModelQualityHealthFailure(failure, location)
	if err != nil {
		return port.ModelQualityHealthSyncCompleteResult{}, err
	}
	appliedDecision, err := appliedModelQualityHealthSyncDecision(input.Claim.DecisionFence.RawJSON, prepared.statHour)
	if err != nil {
		return port.ModelQualityHealthSyncCompleteResult{}, err
	}

	tx, err := beginModelQualityHealthSyncTx(ctx, beginTx, "completion")
	if err != nil {
		return port.ModelQualityHealthSyncCompleteResult{}, err
	}
	committed := false
	defer rollbackModelQualityHealthSyncTx(tx, &committed)()

	var accountID string
	err = tx.QueryRow(ctx, lockModelQualityHealthSyncAccountSQL,
		input.Claim.Failure.AccountID,
		input.Claim.Failure.SystemAccountID,
	).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ModelQualityHealthSyncCompleteResult{}, nil
	}
	if err != nil {
		return port.ModelQualityHealthSyncCompleteResult{}, fmt.Errorf("lock model quality health-sync account: %w", err)
	}
	if _, err := recordModelQualityHealthFailure(ctx, tx, prepared); err != nil {
		return port.ModelQualityHealthSyncCompleteResult{}, err
	}
	command, err := tx.Exec(ctx, completeModelQualityHealthSyncRunSQL,
		appliedDecision,
		modelQualityPolicyTimeText(input.CompletedAt),
		input.Claim.RunID,
		input.Claim.Failure.AccountID,
		input.Claim.Failure.SystemAccountID,
		input.Claim.DecisionFence.RawJSON,
		input.Claim.DecisionFence.RawUpdatedAt,
		string(input.Claim.Lease.OwnerID),
		string(input.Claim.Lease.ClaimToken),
		int64(input.Claim.Lease.Epoch),
		modelQualityPolicyTimeText(input.Claim.Lease.Until),
	)
	if err != nil {
		return port.ModelQualityHealthSyncCompleteResult{}, fmt.Errorf("complete model quality health-sync run: %w", err)
	}
	if command.RowsAffected() != 1 {
		return port.ModelQualityHealthSyncCompleteResult{}, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return port.ModelQualityHealthSyncCompleteResult{}, fmt.Errorf("commit model quality health-sync completion: %w", err)
	}
	committed = true
	return port.ModelQualityHealthSyncCompleteResult{Applied: true, StatHour: prepared.statHour}, nil
}

func (s *Store) ReleaseModelQualityHealthSync(
	ctx context.Context,
	input port.ModelQualityHealthSyncReleaseInput,
) (bool, error) {
	return releaseModelQualityHealthSync(ctx, s.pool, input)
}

func releaseModelQualityHealthSync(
	ctx context.Context,
	execer modelQualityHealthSyncExecer,
	input port.ModelQualityHealthSyncReleaseInput,
) (bool, error) {
	if err := validateModelQualityHealthSyncReleaseInput(input); err != nil {
		return false, err
	}
	message := truncateModelQualityTextRunes(input.ErrorMessage, 1000)
	retryDelayMilliseconds := input.RetryDelay / time.Millisecond
	command, err := execer.Exec(ctx, releaseModelQualityHealthSyncRunSQL,
		int64(retryDelayMilliseconds),
		input.ErrorClass,
		message,
		input.RunID,
		string(input.Lease.OwnerID),
		string(input.Lease.ClaimToken),
		int64(input.Lease.Epoch),
		modelQualityPolicyTimeText(input.Lease.Until),
	)
	if err != nil {
		return false, fmt.Errorf("release model quality health-sync run: %w", err)
	}
	return command.RowsAffected() == 1, nil
}

func decodeModelQualityHealthSyncDecision(raw string) (modelQualityHealthSyncDecision, error) {
	if len(raw) == 0 || len(raw) > modelQualityHealthSyncMaximumDecisionBytes || !utf8.ValidString(raw) {
		return modelQualityHealthSyncDecision{}, fmt.Errorf("persisted model quality health-sync decision is outside its bounded contract")
	}
	fields, err := decodeUniqueModelQualityHealthSyncObject(raw)
	if err != nil {
		return modelQualityHealthSyncDecision{}, fmt.Errorf("persisted model quality health-sync decision is invalid: %w", err)
	}
	var threshold int
	if value, ok := fields["threshold"]; !ok || json.Unmarshal(value, &threshold) != nil || threshold < 40 || threshold > 100 {
		return modelQualityHealthSyncDecision{}, fmt.Errorf("persisted model quality health-sync threshold is invalid")
	}
	var result string
	if value, ok := fields["healthSyncResult"]; !ok || json.Unmarshal(value, &result) != nil || result != "failed" {
		return modelQualityHealthSyncDecision{}, fmt.Errorf("persisted model quality health-sync result is not failed")
	}
	var message string
	if value, ok := fields["message"]; !ok || json.Unmarshal(value, &message) != nil ||
		!utf8.ValidString(message) || len(message) > modelQualityHealthSyncMaximumErrorBytes || strings.IndexByte(message, 0) >= 0 {
		return modelQualityHealthSyncDecision{}, fmt.Errorf("persisted model quality health-sync message is invalid")
	}
	var decidedAtRaw string
	if value, ok := fields["decidedAt"]; !ok || json.Unmarshal(value, &decidedAtRaw) != nil {
		return modelQualityHealthSyncDecision{}, fmt.Errorf("persisted model quality health-sync decided time is invalid")
	}
	decidedAt, err := modelQualityPolicyParseTime(decidedAtRaw)
	if err != nil || decidedAt.IsZero() {
		return modelQualityHealthSyncDecision{}, fmt.Errorf("persisted model quality health-sync decided time is invalid")
	}
	return modelQualityHealthSyncDecision{
		threshold: threshold, message: message, decidedAt: decidedAt, fields: fields,
	}, nil
}

func decodeUniqueModelQualityHealthSyncObject(raw string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("decision must be a JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("decision field name must be a string")
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, fmt.Errorf("decision contains a duplicate top-level field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decision contains trailing data")
	}
	return fields, nil
}

func appliedModelQualityHealthSyncDecision(raw, statHour string) (string, error) {
	decision, err := decodeModelQualityHealthSyncDecision(raw)
	if err != nil {
		return "", err
	}
	return encodeAppliedModelQualityHealthSyncDecision(decision, statHour)
}

func encodeAppliedModelQualityHealthSyncDecision(
	decision modelQualityHealthSyncDecision,
	statHour string,
) (string, error) {
	decision.fields["healthSyncResult"] = json.RawMessage(`"applied"`)
	statHourJSON, err := json.Marshal(statHour)
	if err != nil {
		return "", fmt.Errorf("encode model quality health-sync stat hour: %w", err)
	}
	decision.fields["healthStatHour"] = statHourJSON
	encoded, err := json.Marshal(decision.fields)
	if err != nil {
		return "", fmt.Errorf("encode applied model quality health-sync decision: %w", err)
	}
	if len(encoded) > modelQualityHealthSyncMaximumDecisionBytes {
		return "", fmt.Errorf("applied model quality health-sync decision exceeds its bounded contract")
	}
	return string(encoded), nil
}

func normalizeModelQualityHealthSyncClaimInput(input port.ModelQualityHealthSyncClaimInput) port.ModelQualityHealthSyncClaimInput {
	if input.Limit == 0 {
		input.Limit = port.ModelQualityHealthSyncClaimDefaultLimit
	}
	if input.LeaseDuration == 0 {
		input.LeaseDuration = port.ModelQualityHealthSyncClaimDefaultLease
	}
	return input
}

func validateModelQualityHealthSyncClaimInput(input port.ModelQualityHealthSyncClaimInput) error {
	if !validModelQualityScheduleText(string(input.OwnerID), 128) ||
		input.LeaseDuration < port.ModelQualityClaimMinimumLease || input.LeaseDuration > port.ModelQualityClaimMaximumLease ||
		input.LeaseDuration%time.Millisecond != 0 ||
		input.Limit < 1 || input.Limit > port.ModelQualityHealthSyncClaimMaximumLimit {
		return fmt.Errorf("model quality health-sync claim is invalid")
	}
	return nil
}

func validateModelQualityHealthSyncCompleteInput(input port.ModelQualityHealthSyncCompleteInput) error {
	claim := input.Claim
	if !validModelQualityScheduleText(claim.RunID, 256) || claim.RunID != claim.Failure.RunID ||
		!validModelQualityScheduleText(string(claim.Lease.OwnerID), 128) ||
		!validModelQualityScheduleText(string(claim.Lease.ClaimToken), 256) ||
		claim.Lease.Epoch == 0 || claim.Lease.Epoch > math.MaxInt64 ||
		claim.Lease.Until.IsZero() || input.CompletedAt.IsZero() ||
		!validModelQualityHealthYear(claim.Lease.Until.UTC().Year()) || !validModelQualityHealthYear(input.CompletedAt.UTC().Year()) ||
		!validModelQualityHealthSyncFenceTime(claim.DecisionFence.RawUpdatedAt) || len(claim.DecisionFence.RawJSON) == 0 ||
		len(claim.DecisionFence.RawJSON) > modelQualityHealthSyncMaximumDecisionBytes {
		return fmt.Errorf("model quality health-sync completion is invalid")
	}
	return nil
}

func validateModelQualityHealthSyncReleaseInput(input port.ModelQualityHealthSyncReleaseInput) error {
	if !validModelQualityScheduleText(input.RunID, 256) ||
		!validModelQualityScheduleText(string(input.Lease.OwnerID), 128) ||
		!validModelQualityScheduleText(string(input.Lease.ClaimToken), 256) ||
		input.Lease.Epoch == 0 || input.Lease.Epoch > math.MaxInt64 ||
		input.Lease.Until.IsZero() || !validModelQualityHealthYear(input.Lease.Until.UTC().Year()) ||
		input.RetryDelay < time.Second || input.RetryDelay > modelQualityHealthSyncMaximumRetryDelay ||
		input.RetryDelay%time.Millisecond != 0 ||
		!validModelQualityScheduleText(input.ErrorClass, 64) ||
		!utf8.ValidString(input.ErrorMessage) || len(input.ErrorMessage) > modelQualityHealthSyncMaximumErrorBytes ||
		strings.IndexByte(input.ErrorMessage, 0) >= 0 {
		return fmt.Errorf("model quality health-sync release is invalid")
	}
	return nil
}

func validModelQualityHealthSyncFenceTime(value string) bool {
	parsed, err := modelQualityPolicyParseTime(value)
	return err == nil && !parsed.IsZero() && validModelQualityHealthYear(parsed.UTC().Year())
}

func beginModelQualityHealthSyncTx(
	ctx context.Context,
	beginTx modelQualityHealthSyncBeginTx,
	operation string,
) (pgx.Tx, error) {
	if beginTx == nil {
		return nil, fmt.Errorf("model quality health-sync %s transaction starter is required", operation)
	}
	tx, err := beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin model quality health-sync %s transaction: %w", operation, err)
	}
	return tx, nil
}

func rollbackModelQualityHealthSyncTx(tx pgx.Tx, committed *bool) func() {
	return func() {
		if *committed {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}
}

var _ port.ModelQualityHealthSyncClaimer = (*Store)(nil)
var _ port.ModelQualityHealthSyncCompleter = (*Store)(nil)
var _ port.ModelQualityHealthSyncReleaser = (*Store)(nil)
