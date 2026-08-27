package modelcheckowner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type OutcomeCursor struct {
	StoredAt  time.Time
	OutcomeID string
}

type StoredOutcome struct {
	Outcome Outcome
	Input   InputRecord
}

// ListCommittedOutcomes returns immutable outcomes after a stable cursor.
// It is read-only and verifies the joined input identity and payload digest.
func (s *Store) ListCommittedOutcomes(ctx context.Context, cursor OutcomeCursor, limit int) ([]StoredOutcome, error) {
	if s == nil || s.db == nil || limit <= 0 || limit > 10000 {
		return nil, errors.New("J3b outcome cursor or limit is invalid")
	}
	query := `SELECT o.outcome_id,o.input_id,o.input_digest,o.fence_token,o.observed_at,o.stored_at,o.payload,o.payload_digest,i.input_id,i.identity_key,i.input_version,i.input_digest,i.target_id,i.config_revision,i.policy_revision,i.trigger,i.issued_at,i.expires_at,i.payload FROM ` + s.table("model_check_outcomes") + ` o JOIN ` + s.table("model_check_inputs") + ` i ON i.input_id=o.input_id WHERE o.committed=?`
	args := []any{true}
	if !cursor.StoredAt.IsZero() || cursor.OutcomeID != "" {
		if cursor.StoredAt.IsZero() || strings.TrimSpace(cursor.OutcomeID) == "" {
			return nil, errors.New("J3b outcome cursor is incomplete")
		}
		query += " AND (o.stored_at>? OR (o.stored_at=? AND o.outcome_id>?))"
		args = append(args, cursor.StoredAt.UTC().Format(time.RFC3339Nano), cursor.StoredAt.UTC().Format(time.RFC3339Nano), cursor.OutcomeID)
	}
	query += " ORDER BY o.stored_at ASC,o.outcome_id ASC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("list J3b committed outcomes: %w", err)
	}
	defer rows.Close()
	result := make([]StoredOutcome, 0)
	for rows.Next() {
		var outcome Outcome
		var input InputRecord
		var observedRaw, storedRaw, issuedRaw, expiresRaw any
		var outcomePayload, inputPayload []byte
		if err := rows.Scan(&outcome.OutcomeID, &outcome.InputID, &outcome.InputDigest, &outcome.FenceToken, &observedRaw, &storedRaw, &outcomePayload, &outcome.PayloadDigest, &input.InputID, &input.IdentityKey, &input.InputVersion, &input.InputDigest, &input.TargetID, &input.ConfigRevision, &input.PolicyRevision, &input.Trigger, &issuedRaw, &expiresRaw, &inputPayload); err != nil {
			return nil, fmt.Errorf("scan J3b committed outcome: %w", err)
		}
		outcome.ObservedAt, err = parseDBTime(observedRaw)
		if err != nil {
			return nil, err
		}
		outcome.StoredAt, err = parseDBTime(storedRaw)
		if err != nil {
			return nil, err
		}
		input.IssuedAt, err = parseDBTime(issuedRaw)
		if err != nil {
			return nil, err
		}
		input.ExpiresAt, err = parseDBTime(expiresRaw)
		if err != nil {
			return nil, err
		}
		input.Payload, err = canonicalJSON(inputPayload)
		computedInputDigest, digestErr := digestInput(input)
		if err != nil || digestErr != nil || computedInputDigest != input.InputDigest {
			return nil, ErrInputTampered
		}
		if digestPayload(outcomePayload) != outcome.PayloadDigest {
			return nil, errors.New("stored J3b outcome failed integrity verification")
		}
		outcome.Payload = outcomePayload
		result = append(result, StoredOutcome{Outcome: outcome, Input: input})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
