package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"juhe-ai/backend-go/internal/store/port"
)

const managementModelAccountTrustResultSQL = `SELECT
  identity_status, mapping_status, usage_integrity_status, protocol_status, evidence_status,
  evidence_coverage, observation_count, round_count, independent_source_count,
  identity_observation_count, paired_probe_count, slope, intercept, intercept_baseline_median,
  intercept_baseline_mad, intercept_baseline_version, intercept_baseline_status,
  intercept_strong_gate_enabled, identity_distance, paired_distance, paired_baseline_median,
  paired_baseline_mad, baseline_version, baseline_version_status, feature_version,
  tokenizer_version, probe_set_version, reason_codes_json, last_observed_at
FROM juhe_stats.model_account_trust_results
WHERE system_account_id = $1::text
  AND account_id = $2::text
  AND requested_model = $3::text
LIMIT 1`

const managementModelCheckRunBaseFrom = `
FROM juhe_dataset.model_check_runs AS mcr
LEFT JOIN juhe_business.accounts AS acc
  ON acc.id = COALESCE(NULLIF(mcr.account_id, ''), mcr.target_id)`

type managementModelCheckRunRow struct {
	ID                         string
	SystemAccountID            string
	ActorSystemAccountID       string
	ProviderCode               string
	TargetType                 string
	TargetID                   string
	TargetName                 pgtype.Text
	TargetOwnerSystemAccountID pgtype.Text
	AccountID                  pgtype.Text
	GroupID                    pgtype.Text
	APIKeyID                   pgtype.Text
	Model                      string
	Profile                    string
	TrustedComparison          int32
	TrustedComparisonAvailable int32
	Level                      string
	Score                      int32
	MaxScore                   int32
	Status                     string
	Message                    string
	TraceID                    pgtype.Text
	ProbeSetVersion            string
	StartedAt                  string
	FinishedAt                 pgtype.Text
	DurationMs                 pgtype.Int4
	RequestSummaryJSON         string
	ResultSummaryJSON          string
	ErrorCode                  pgtype.Text
	ErrorMessage               pgtype.Text
	CreatedAt                  string
	UpdatedAt                  string
}

type managementModelCheckItemRow struct {
	ID                  string
	RunID               string
	ItemKey             string
	ItemType            string
	Status              string
	Score               int32
	MaxScore            int32
	DurationMs          pgtype.Int4
	TraceID             pgtype.Text
	EvidenceSummaryJSON string
	ErrorCode           pgtype.Text
	ErrorMessage        pgtype.Text
	CreatedAt           string
	UpdatedAt           string
}

func (s *Store) FindManagementModelCheckActive(ctx context.Context, actorSystemAccountID string) (port.ManagementModelCheckRun, bool, error) {
	var row managementModelCheckRunRow
	if err := scanManagementModelCheckRun(s.pool.QueryRow(ctx, managementModelCheckActiveQuery(), actorSystemAccountID), &row); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return port.ManagementModelCheckRun{}, false, nil
		}
		return port.ManagementModelCheckRun{}, false, fmt.Errorf("find active management model check: %w", err)
	}
	return managementModelCheckRunFact(row), true, nil
}

func (s *Store) ListManagementModelCheckRuns(ctx context.Context, input port.ManagementModelCheckRunListInput) (port.ManagementModelCheckRunListResult, error) {
	query, args := managementModelCheckListQuery(input)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return port.ManagementModelCheckRunListResult{}, fmt.Errorf("list management model checks: %w", err)
	}
	defer rows.Close()

	limit := min(max(input.Limit, 1), 100)
	items := make([]port.ManagementModelCheckRun, 0, limit)
	for rows.Next() {
		var row managementModelCheckRunRow
		if err = scanManagementModelCheckRun(rows, &row); err != nil {
			return port.ManagementModelCheckRunListResult{}, fmt.Errorf("scan management model checks: %w", err)
		}
		items = append(items, managementModelCheckRunFact(row))
	}
	if err = rows.Err(); err != nil {
		return port.ManagementModelCheckRunListResult{}, fmt.Errorf("scan management model checks: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return port.ManagementModelCheckRunListResult{Items: items, HasMore: hasMore}, nil
}

func (s *Store) GetManagementModelCheckRun(ctx context.Context, id string, systemAccountID string) (port.ManagementModelCheckRun, []port.ManagementModelCheckItem, bool, error) {
	query, args := managementModelCheckDetailQuery(id, systemAccountID)
	var row managementModelCheckRunRow
	if err := scanManagementModelCheckRun(s.pool.QueryRow(ctx, query, args...), &row); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return port.ManagementModelCheckRun{}, nil, false, nil
		}
		return port.ManagementModelCheckRun{}, nil, false, fmt.Errorf("get management model check: %w", err)
	}

	rows, err := s.pool.Query(ctx, managementModelCheckItemsQuery(), id)
	if err != nil {
		return port.ManagementModelCheckRun{}, nil, true, fmt.Errorf("list management model check items: %w", err)
	}
	defer rows.Close()
	items := make([]port.ManagementModelCheckItem, 0)
	for rows.Next() {
		var item managementModelCheckItemRow
		if err = rows.Scan(
			&item.ID, &item.RunID, &item.ItemKey, &item.ItemType, &item.Status, &item.Score, &item.MaxScore,
			&item.DurationMs, &item.TraceID, &item.EvidenceSummaryJSON, &item.ErrorCode, &item.ErrorMessage,
			&item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return port.ManagementModelCheckRun{}, nil, true, fmt.Errorf("scan management model check items: %w", err)
		}
		items = append(items, managementModelCheckItemFact(item))
	}
	if err = rows.Err(); err != nil {
		return port.ManagementModelCheckRun{}, nil, true, fmt.Errorf("scan management model check items: %w", err)
	}
	return managementModelCheckRunFact(row), items, true, nil
}

func (s *Store) FindManagementModelAccountTrustResult(ctx context.Context, systemAccountID string, accountID string, requestedModel string) (port.ManagementModelAccountTrustResult, bool, error) {
	var result port.ManagementModelAccountTrustResult
	var slope, intercept, interceptBaselineMedian, interceptBaselineMAD, identityDistance pgtype.Float8
	var pairedDistance, pairedBaselineMedian, pairedBaselineMAD pgtype.Float8
	var interceptBaselineVersion, baselineVersion pgtype.Int4
	var interceptBaselineStatus, baselineVersionStatus, featureVersion, tokenizerVersion pgtype.Text
	var probeSetVersion, lastObservedAt pgtype.Text
	var evidenceCoverage float64
	var observationCount, roundCount, independentSourceCount, identityObservationCount, pairedProbeCount int32
	var interceptStrongGateEnabled int32
	var reasonCodesJSON string
	err := s.pool.QueryRow(ctx, managementModelAccountTrustResultSQL, systemAccountID, accountID, requestedModel).Scan(
		&result.IdentityStatus, &result.MappingStatus, &result.UsageIntegrityStatus, &result.ProtocolStatus, &result.EvidenceStatus,
		&evidenceCoverage, &observationCount, &roundCount, &independentSourceCount, &identityObservationCount,
		&pairedProbeCount, &slope, &intercept, &interceptBaselineMedian, &interceptBaselineMAD,
		&interceptBaselineVersion, &interceptBaselineStatus, &interceptStrongGateEnabled, &identityDistance,
		&pairedDistance, &pairedBaselineMedian, &pairedBaselineMAD, &baselineVersion, &baselineVersionStatus,
		&featureVersion, &tokenizerVersion, &probeSetVersion, &reasonCodesJSON, &lastObservedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ManagementModelAccountTrustResult{}, false, nil
	}
	if err != nil {
		return port.ManagementModelAccountTrustResult{}, false, fmt.Errorf("find management model trust result: %w", err)
	}
	result.EvidenceCoverage = evidenceCoverage
	result.ObservationCount = int(observationCount)
	result.RoundCount = int(roundCount)
	result.IndependentSourceCount = int(independentSourceCount)
	result.IdentityObservationCount = int(identityObservationCount)
	result.PairedProbeCount = int(pairedProbeCount)
	result.Slope = modelCheckFloatPtr(slope)
	result.Intercept = modelCheckFloatPtr(intercept)
	result.InterceptBaselineMedian = modelCheckFloatPtr(interceptBaselineMedian)
	result.InterceptBaselineMAD = modelCheckFloatPtr(interceptBaselineMAD)
	result.InterceptBaselineVersion = modelCheckIntPtr(interceptBaselineVersion)
	result.InterceptBaselineStatus = modelCheckTextPtr(interceptBaselineStatus)
	result.InterceptStrongGateEnabled = interceptStrongGateEnabled == 1
	result.IdentityDistance = modelCheckFloatPtr(identityDistance)
	result.PairedDistance = modelCheckFloatPtr(pairedDistance)
	result.PairedBaselineMedian = modelCheckFloatPtr(pairedBaselineMedian)
	result.PairedBaselineMAD = modelCheckFloatPtr(pairedBaselineMAD)
	result.BaselineVersion = modelCheckIntPtr(baselineVersion)
	result.BaselineVersionStatus = modelCheckTextPtr(baselineVersionStatus)
	result.FeatureVersion = modelCheckTextPtr(featureVersion)
	result.TokenizerVersion = modelCheckTextPtr(tokenizerVersion)
	if probeSetVersion.Valid {
		result.ProbeSetVersion = probeSetVersion.String
	}
	result.LastObservedAt = modelCheckTextPtr(lastObservedAt)
	if err = json.Unmarshal([]byte(reasonCodesJSON), &result.ReasonCodes); err != nil || result.ReasonCodes == nil {
		result.ReasonCodes = []string{}
	}
	return result, true, nil
}

func managementModelCheckActiveQuery() string {
	return managementModelCheckRunSelect(false) + managementModelCheckRunBaseFrom + `
WHERE mcr.actor_system_account_id = $1::text
  AND mcr.status = 'running'
ORDER BY mcr.created_at DESC, mcr.id DESC
LIMIT 1`
}

func managementModelCheckListQuery(input port.ManagementModelCheckRunListInput) (string, []any) {
	clauses := make([]string, 0, 8)
	args := make([]any, 0, 10)
	add := func(column string, value string, operator string) {
		if value == "" {
			return
		}
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf("%s %s $%d::text", column, operator, len(args)))
	}
	add("mcr.system_account_id", input.SystemAccountID, "=")
	add("mcr.target_type", input.TargetType, "=")
	add("mcr.target_id", input.TargetID, "=")
	add("mcr.model", input.Model, "=")
	add("mcr.level", input.Level, "=")
	add("mcr.status", input.Status, "=")
	add("mcr.created_at", input.StartAt, ">=")
	add("mcr.created_at", input.EndAt, "<=")
	query := managementModelCheckRunSelect(false) + managementModelCheckRunBaseFrom
	if len(clauses) > 0 {
		query += "\nWHERE " + strings.Join(clauses, "\n  AND ")
	}
	limit := min(max(input.Limit, 1), 100)
	offset := min(max(input.Offset, 0), 1000-limit)
	args = append(args, limit+1, offset)
	query += fmt.Sprintf("\nORDER BY mcr.created_at DESC, mcr.id DESC\nLIMIT $%d OFFSET $%d", len(args)-1, len(args))
	return query, args
}

func managementModelCheckDetailQuery(id string, systemAccountID string) (string, []any) {
	query := managementModelCheckRunSelect(true) + managementModelCheckRunBaseFrom + "\nWHERE mcr.id = $1::text"
	args := []any{id}
	if systemAccountID != "" {
		query += "\n  AND mcr.system_account_id = $2::text"
		args = append(args, systemAccountID)
	}
	return query + "\nLIMIT 1", args
}

func managementModelCheckItemsQuery() string {
	return `SELECT
  mci.id, mci.run_id, mci.item_key, mci.item_type, mci.status, mci.score, mci.max_score,
  mci.duration_ms, mci.trace_id, mci.evidence_summary_json, mci.error_code, mci.error_message,
  mci.created_at, mci.updated_at
FROM juhe_dataset.model_check_items AS mci
WHERE mci.run_id = $1::text
ORDER BY mci.created_at ASC, mci.id ASC`
}

func managementModelCheckRunSelect(includeSummaries bool) string {
	summaries := "'{}'::text, '{}'::text"
	if includeSummaries {
		summaries = "mcr.request_summary_json, mcr.result_summary_json"
	}
	return `SELECT
  mcr.id, mcr.system_account_id, mcr.actor_system_account_id, mcr.provider_code,
  mcr.target_type, mcr.target_id, COALESCE(NULLIF(BTRIM(mcr.target_name), ''), acc.name),
  mcr.target_owner_system_account_id, mcr.account_id, mcr.group_id, mcr.api_key_id,
  mcr.model, mcr.profile, mcr.trusted_comparison_enabled, mcr.trusted_comparison_available,
  mcr.level, mcr.score, mcr.max_score, mcr.status, mcr.message, mcr.trace_id, mcr.probe_set_version,
  mcr.started_at, mcr.finished_at, mcr.duration_ms, ` + summaries + `,
  mcr.error_code, mcr.error_message, mcr.created_at, mcr.updated_at`
}

type managementModelCheckRunScanner interface {
	Scan(...any) error
}

func scanManagementModelCheckRun(scanner managementModelCheckRunScanner, row *managementModelCheckRunRow) error {
	return scanner.Scan(
		&row.ID, &row.SystemAccountID, &row.ActorSystemAccountID, &row.ProviderCode,
		&row.TargetType, &row.TargetID, &row.TargetName, &row.TargetOwnerSystemAccountID,
		&row.AccountID, &row.GroupID, &row.APIKeyID, &row.Model, &row.Profile,
		&row.TrustedComparison, &row.TrustedComparisonAvailable, &row.Level, &row.Score,
		&row.MaxScore, &row.Status, &row.Message, &row.TraceID, &row.ProbeSetVersion,
		&row.StartedAt, &row.FinishedAt, &row.DurationMs, &row.RequestSummaryJSON,
		&row.ResultSummaryJSON, &row.ErrorCode, &row.ErrorMessage, &row.CreatedAt, &row.UpdatedAt,
	)
}

func managementModelCheckRunFact(row managementModelCheckRunRow) port.ManagementModelCheckRun {
	return port.ManagementModelCheckRun{
		ID: row.ID, SystemAccountID: row.SystemAccountID, ActorSystemAccountID: row.ActorSystemAccountID,
		ProviderCode: row.ProviderCode, TargetType: row.TargetType, TargetID: row.TargetID,
		TargetName: modelCheckTextPtr(row.TargetName), TargetOwnerSystemAccountID: modelCheckTextPtr(row.TargetOwnerSystemAccountID),
		AccountID: modelCheckTextPtr(row.AccountID), GroupID: modelCheckTextPtr(row.GroupID), APIKeyID: modelCheckTextPtr(row.APIKeyID),
		Model: row.Model, Profile: row.Profile, TrustedComparison: row.TrustedComparison == 1,
		TrustedComparisonAvailable: row.TrustedComparisonAvailable == 1, Level: row.Level,
		Score: int(row.Score), MaxScore: int(row.MaxScore), Status: row.Status, Message: row.Message,
		TraceID: modelCheckTextPtr(row.TraceID), ProbeSetVersion: row.ProbeSetVersion, StartedAt: row.StartedAt,
		FinishedAt: modelCheckTextPtr(row.FinishedAt), DurationMs: modelCheckIntPtr(row.DurationMs),
		RequestSummaryJSON: row.RequestSummaryJSON, ResultSummaryJSON: row.ResultSummaryJSON,
		ErrorCode: modelCheckTextPtr(row.ErrorCode), ErrorMessage: modelCheckTextPtr(row.ErrorMessage),
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func managementModelCheckItemFact(row managementModelCheckItemRow) port.ManagementModelCheckItem {
	return port.ManagementModelCheckItem{
		ID: row.ID, RunID: row.RunID, ItemKey: row.ItemKey, ItemType: row.ItemType, Status: row.Status,
		Score: int(row.Score), MaxScore: int(row.MaxScore), DurationMs: modelCheckIntPtr(row.DurationMs),
		TraceID: modelCheckTextPtr(row.TraceID), EvidenceSummaryJSON: row.EvidenceSummaryJSON,
		ErrorCode: modelCheckTextPtr(row.ErrorCode), ErrorMessage: modelCheckTextPtr(row.ErrorMessage),
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func modelCheckTextPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func modelCheckIntPtr(value pgtype.Int4) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int32)
	return &result
}

func modelCheckFloatPtr(value pgtype.Float8) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}

var _ port.ManagementModelCheckReader = (*Store)(nil)
