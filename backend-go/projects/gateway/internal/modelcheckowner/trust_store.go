package modelcheckowner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const trustAggregationScope = "model-trust-observation-aggregation"

// TrustProjection is the durable, credential-free trust summary for one
// account/model run. It intentionally carries only the Go evaluator's
// formed/partial facts; historical Node token and identity windows remain
// explicit maintenance facts and are never inferred here.
type TrustProjection struct {
	RunID, SystemAccountID, AccountID, RequestedModel string
	Report                                            TrustReport
}

type trustObservation struct {
	id, createdAt, mappingStatus, protocolStatus string
}

// ProjectTrust records receipt de-duplication before marking observations
// consumed, advances the durable cursor monotonically, and updates the latest
// read model only for a newer observation. A replay with different facts at
// the same cursor fails closed rather than rewriting trust history.
func (s *Store) ProjectTrust(ctx context.Context, projection TrustProjection) error {
	if s == nil || s.db == nil || strings.TrimSpace(projection.RunID) == "" || strings.TrimSpace(projection.SystemAccountID) == "" || strings.TrimSpace(projection.AccountID) == "" || strings.TrimSpace(projection.RequestedModel) == "" || !validTrustReport(projection.Report) {
		return errors.New("J3b trust projection input is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin J3b trust projection: %w", err)
	}
	defer tx.Rollback()
	observations, err := s.trustObservations(ctx, tx, projection)
	if err != nil {
		return err
	}
	if len(observations) == 0 {
		return errors.New("J3b trust projection has no durable observations")
	}
	processedAt := time.Now().UTC().Format(time.RFC3339Nano)
	for _, observation := range observations {
		if err := s.recordTrustReceipt(ctx, tx, observation, processedAt); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("model_check_observations")+` SET aggregation_completed_at=COALESCE(aggregation_completed_at,?) WHERE run_id=? AND system_account_id=? AND account_id=? AND requested_model=?`), processedAt, projection.RunID, projection.SystemAccountID, projection.AccountID, projection.RequestedModel); err != nil {
		return fmt.Errorf("mark J3b trust observations consumed: %w", err)
	}
	last := observations[len(observations)-1]
	if err := s.upsertTrustLatest(ctx, tx, projection, observations, last, processedAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM `+s.table("model_trust_latest_dirty_accounts")+` WHERE system_account_id=? AND account_id=? AND requested_model=?`), projection.SystemAccountID, projection.AccountID, projection.RequestedModel); err != nil {
		return fmt.Errorf("clear J3b trust latest dirty result: %w", err)
	}
	if err := s.advanceTrustCursor(ctx, tx, last, processedAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit J3b trust projection: %w", err)
	}
	return nil
}

func validTrustReport(report TrustReport) bool {
	return strings.TrimSpace(report.IdentityStatus) != "" && report.TrustScore >= 0 && report.TrustScore <= 1
}

func (s *Store) trustObservations(ctx context.Context, tx *sql.Tx, projection TrustProjection) ([]trustObservation, error) {
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT id,created_at,mapping_status,protocol_status FROM `+s.table("model_check_observations")+` WHERE run_id=? AND system_account_id=? AND account_id=? AND requested_model=? ORDER BY created_at,id`), projection.RunID, projection.SystemAccountID, projection.AccountID, projection.RequestedModel)
	if err != nil {
		return nil, fmt.Errorf("read J3b trust observations: %w", err)
	}
	defer rows.Close()
	result := make([]trustObservation, 0)
	for rows.Next() {
		var observation trustObservation
		if err := rows.Scan(&observation.id, &observation.createdAt, &observation.mappingStatus, &observation.protocolStatus); err != nil {
			return nil, fmt.Errorf("scan J3b trust observation: %w", err)
		}
		if strings.TrimSpace(observation.id) == "" || !validTrustInstant(observation.createdAt) {
			return nil, errors.New("J3b trust observation receipt is malformed")
		}
		result = append(result, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate J3b trust observations: %w", err)
	}
	return result, nil
}

func (s *Store) recordTrustReceipt(ctx context.Context, tx *sql.Tx, observation trustObservation, processedAt string) error {
	result, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("model_trust_observation_receipts")+` (observation_id,observation_created_at,processed_at) VALUES (?,?,?) ON CONFLICT(observation_id) DO NOTHING`), observation.id, observation.createdAt, processedAt)
	if err != nil {
		return fmt.Errorf("record J3b trust observation receipt: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read J3b trust observation receipt result: %w", err)
	}
	if changed == 1 {
		return nil
	}
	var original string
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT observation_created_at FROM `+s.table("model_trust_observation_receipts")+` WHERE observation_id=?`), observation.id).Scan(&original); err != nil {
		return fmt.Errorf("read existing J3b trust observation receipt: %w", err)
	}
	if original != observation.createdAt {
		return errors.New("J3b trust observation receipt replay conflicts with original cursor")
	}
	return nil
}

func (s *Store) upsertTrustLatest(ctx context.Context, tx *sql.Tx, projection TrustProjection, observations []trustObservation, last trustObservation, updatedAt string) error {
	reasons := append([]string(nil), projection.Report.ReasonCodes...)
	sort.Strings(reasons)
	reasons = compactTrustReasons(reasons)
	reasonJSON, err := json.Marshal(reasons)
	if err != nil {
		return fmt.Errorf("marshal J3b trust reason codes: %w", err)
	}
	mappingStatus := trustMappingStatus(observations)
	protocolStatus := trustProtocolStatus(observations)
	evidenceStatus := "insufficient"
	if projection.Report.TrustFormed {
		evidenceStatus = "stable"
	}
	evidenceCoverage := int(projection.Report.TrustScore * 100)
	if evidenceCoverage < 0 {
		evidenceCoverage = 0
	}
	if evidenceCoverage > 100 {
		evidenceCoverage = 100
	}
	var currentLast, currentLastID sql.NullString
	var currentIdentity, currentMapping, currentProtocol, currentEvidence, currentReason string
	var currentCoverage, currentCount int
	err = tx.QueryRowContext(ctx, s.bind(`SELECT identity_status,mapping_status,protocol_status,evidence_status,evidence_coverage,observation_count,reason_codes_json,last_observed_id,last_observed_at FROM `+s.table("model_account_trust_results")+` WHERE system_account_id=? AND account_id=? AND requested_model=?`), projection.SystemAccountID, projection.AccountID, projection.RequestedModel).Scan(&currentIdentity, &currentMapping, &currentProtocol, &currentEvidence, &currentCoverage, &currentCount, &currentReason, &currentLastID, &currentLast)
	if err == nil {
		comparison := compareTrustCursor(currentLast.String, currentLastID.String, last.createdAt, last.id)
		if comparison > 0 {
			return nil
		}
		if comparison == 0 && (currentIdentity != projection.Report.IdentityStatus || currentMapping != mappingStatus || currentProtocol != protocolStatus || currentEvidence != evidenceStatus || currentCoverage != evidenceCoverage || currentCount != len(observations) || !jsonEqual([]byte(currentReason), reasonJSON)) {
			return errors.New("J3b trust latest result replay conflicts with original projection")
		}
		if comparison == 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("model_account_trust_results")+` SET identity_status=?,mapping_status=?,usage_integrity_status='insufficient_evidence',protocol_status=?,evidence_status=?,evidence_coverage=?,observation_count=?,reason_codes_json=?,last_observed_id=?,last_observed_at=?,updated_at=? WHERE system_account_id=? AND account_id=? AND requested_model=?`), projection.Report.IdentityStatus, mappingStatus, protocolStatus, evidenceStatus, evidenceCoverage, len(observations), string(reasonJSON), last.id, last.createdAt, updatedAt, projection.SystemAccountID, projection.AccountID, projection.RequestedModel)
		if err != nil {
			return fmt.Errorf("update J3b trust latest result: %w", err)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read J3b trust latest result: %w", err)
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("model_account_trust_results")+` (system_account_id,account_id,requested_model,identity_status,mapping_status,usage_integrity_status,protocol_status,evidence_status,evidence_coverage,observation_count,reason_codes_json,last_observed_id,last_observed_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), projection.SystemAccountID, projection.AccountID, projection.RequestedModel, projection.Report.IdentityStatus, mappingStatus, "insufficient_evidence", protocolStatus, evidenceStatus, evidenceCoverage, len(observations), string(reasonJSON), last.id, last.createdAt, updatedAt)
	if err != nil {
		return fmt.Errorf("insert J3b trust latest result: %w", err)
	}
	return nil
}

func (s *Store) advanceTrustCursor(ctx context.Context, tx *sql.Tx, last trustObservation, updatedAt string) error {
	var currentCreated, currentID sql.NullString
	err := tx.QueryRowContext(ctx, s.bind(`SELECT cursor_created_at,cursor_id FROM `+s.table("model_trust_aggregation_state")+` WHERE scope_key=?`), trustAggregationScope).Scan(&currentCreated, &currentID)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("model_trust_aggregation_state")+` (scope_key,cursor_created_at,cursor_id,last_success_at,updated_at) VALUES (?,?,?,?,?)`), trustAggregationScope, last.createdAt, last.id, updatedAt, updatedAt)
		if err != nil {
			return fmt.Errorf("insert J3b trust cursor: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read J3b trust cursor: %w", err)
	}
	if compareTrustCursor(currentCreated.String, currentID.String, last.createdAt, last.id) >= 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, s.bind(`UPDATE `+s.table("model_trust_aggregation_state")+` SET cursor_created_at=?,cursor_id=?,last_success_at=?,updated_at=? WHERE scope_key=?`), last.createdAt, last.id, updatedAt, updatedAt, trustAggregationScope); err != nil {
		return fmt.Errorf("advance J3b trust cursor: %w", err)
	}
	return nil
}

func validTrustInstant(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func compareTrustCursor(leftCreated, leftID, rightCreated, rightID string) int {
	if leftCreated == "" {
		if rightCreated == "" {
			return strings.Compare(leftID, rightID)
		}
		return -1
	}
	if rightCreated == "" {
		return 1
	}
	if leftCreated != rightCreated {
		return strings.Compare(leftCreated, rightCreated)
	}
	return strings.Compare(leftID, rightID)
}

func compactTrustReasons(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || (len(result) > 0 && result[len(result)-1] == value) {
			continue
		}
		result = append(result, value)
	}
	return result
}

func trustMappingStatus(observations []trustObservation) string {
	if len(observations) == 0 {
		return "unknown"
	}
	first := observations[0].mappingStatus
	for _, observation := range observations[1:] {
		if observation.mappingStatus != first {
			return "mixed"
		}
	}
	if strings.TrimSpace(first) == "" {
		return "unknown"
	}
	return first
}

func trustProtocolStatus(observations []trustObservation) string {
	if len(observations) == 0 {
		return "insufficient_evidence"
	}
	allPassed := true
	for _, observation := range observations {
		if observation.protocolStatus == "failed" {
			return "failed"
		}
		if observation.protocolStatus != "passed" {
			allPassed = false
		}
	}
	if allPassed {
		return "passed"
	}
	return "insufficient_evidence"
}
