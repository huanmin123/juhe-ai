package modelcheckowner

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Claim struct {
	InputID, ClaimToken, OutcomeID, OwnerID string
	FenceToken                              int64
	ClaimUntil                              time.Time
}

type Outcome struct {
	OutcomeID, InputID, InputDigest string
	FenceToken                      int64
	ObservedAt, StoredAt            time.Time
	Payload                         json.RawMessage
	PayloadDigest                   string
}

var (
	ErrClaimBusy       = errors.New("J3b input is claimed by another owner")
	ErrClaimConflict   = errors.New("J3b claim conflicts with active claim")
	ErrStaleFence      = errors.New("J3b claim fence is stale")
	ErrOutcomeConflict = errors.New("J3b outcome conflicts with existing outcome")
)

// forUpdate serializes read/modify/write decisions on PostgreSQL. SQLite is
// opened with a single writer connection and does not accept this clause.
func (s *Store) forUpdate() string {
	if s != nil && s.mode == "postgres" {
		return " FOR UPDATE"
	}
	return ""
}

// ClaimInput reserves an immutable input with a monotonically increasing
// fence. Repeating the same live claim is idempotent; a different owner/token
// cannot overwrite it until the lease expires.
func (s *Store) ClaimInput(ctx context.Context, inputID, claimToken, outcomeID, ownerID string, lease time.Duration, now time.Time) (Claim, error) {
	if s == nil || s.db == nil || strings.TrimSpace(inputID) == "" || strings.TrimSpace(claimToken) == "" || strings.TrimSpace(outcomeID) == "" || strings.TrimSpace(ownerID) == "" || lease <= 0 || now.IsZero() {
		return Claim{}, errors.New("J3b claim input is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Claim{}, fmt.Errorf("begin J3b claim: %w", err)
	}
	defer tx.Rollback()
	var expiresRaw any
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT expires_at FROM `+s.table("model_check_inputs")+` WHERE input_id=?`+s.forUpdate()), inputID).Scan(&expiresRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Claim{}, errors.New("J3b input not found")
		}
		return Claim{}, err
	}
	expires, err := parseDBTime(expiresRaw)
	if err != nil || !now.Before(expires) {
		return Claim{}, errors.New("J3b input is expired")
	}
	var claim Claim
	var untilRaw any
	err = tx.QueryRowContext(ctx, s.bind(`SELECT claim_token,outcome_id,owner_id,fence_token,claim_until FROM `+s.table("model_check_execution_claims")+` WHERE input_id=?`+s.forUpdate()), inputID).Scan(&claim.ClaimToken, &claim.OutcomeID, &claim.OwnerID, &claim.FenceToken, &untilRaw)
	if err == nil {
		claim.InputID = inputID
		claim.ClaimUntil, err = parseDBTime(untilRaw)
		if err != nil {
			return Claim{}, err
		}
		if now.Before(claim.ClaimUntil) {
			if claim.ClaimToken == claimToken && claim.OwnerID == ownerID && claim.OutcomeID == outcomeID {
				return claim, tx.Commit()
			}
			if claim.OwnerID != ownerID || claim.ClaimToken != claimToken {
				return Claim{}, ErrClaimBusy
			}
			return Claim{}, ErrClaimConflict
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Claim{}, err
	}
	fence := int64(1)
	if claim.FenceToken > 0 {
		fence = claim.FenceToken + 1
	}
	claim = Claim{InputID: inputID, ClaimToken: claimToken, OutcomeID: outcomeID, OwnerID: ownerID, FenceToken: fence, ClaimUntil: now.Add(lease)}
	result, err := tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("model_check_execution_claims")+` (input_id,claim_token,outcome_id,owner_id,fence_token,claim_until,updated_at) VALUES (?,?,?,?,?,?,?) ON CONFLICT(input_id) DO UPDATE SET claim_token=excluded.claim_token,outcome_id=excluded.outcome_id,owner_id=excluded.owner_id,fence_token=excluded.fence_token,claim_until=excluded.claim_until,updated_at=excluded.updated_at`), claim.InputID, claim.ClaimToken, claim.OutcomeID, claim.OwnerID, claim.FenceToken, claim.ClaimUntil.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Claim{}, fmt.Errorf("persist J3b claim: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return Claim{}, fmt.Errorf("persist J3b claim affected %d rows: %w", changed, err)
	}
	return claim, tx.Commit()
}

// RenewClaim extends a live execution lease without changing its fence. The
// update is conditional on the exact claim identity and an unexpired lease so
// a worker that lost ownership can never revive its claim.
func (s *Store) RenewClaim(ctx context.Context, claim Claim, lease time.Duration, now time.Time) error {
	if s == nil || s.db == nil || claim.InputID == "" || claim.ClaimToken == "" || claim.OwnerID == "" || claim.OutcomeID == "" || claim.FenceToken < 1 || lease <= 0 || now.IsZero() {
		return errors.New("J3b claim renewal input is invalid")
	}
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("model_check_execution_claims")+` SET claim_until=?,updated_at=? WHERE input_id=? AND claim_token=? AND owner_id=? AND outcome_id=? AND fence_token=? AND claim_until>?`), now.Add(lease).UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), claim.InputID, claim.ClaimToken, claim.OwnerID, claim.OutcomeID, claim.FenceToken, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("renew J3b claim: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("renew J3b claim affected rows: %w", err)
	}
	if changed != 1 {
		return ErrStaleFence
	}
	return nil
}

func (s *Store) ReleaseClaim(ctx context.Context, claim Claim, now time.Time) error {
	if s == nil || s.db == nil || claim.InputID == "" || claim.ClaimToken == "" || claim.OwnerID == "" || claim.OutcomeID == "" || claim.FenceToken < 1 || now.IsZero() {
		return errors.New("J3b claim release input is invalid")
	}
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE `+s.table("model_check_execution_claims")+` SET claim_until=?,updated_at=? WHERE input_id=? AND claim_token=? AND owner_id=? AND outcome_id=? AND fence_token=?`), now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), claim.InputID, claim.ClaimToken, claim.OwnerID, claim.OutcomeID, claim.FenceToken)
	if err != nil {
		return fmt.Errorf("release J3b claim: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrStaleFence
	}
	return nil
}

func (s *Store) CommitOutcome(ctx context.Context, outcome Outcome, claim Claim, now time.Time) error {
	if s == nil || s.db == nil || outcome.InputID == "" || outcome.OutcomeID == "" || claim.InputID != outcome.InputID || claim.ClaimToken == "" || outcome.InputDigest == "" || len(outcome.Payload) == 0 || !json.Valid(outcome.Payload) || now.IsZero() {
		return errors.New("J3b outcome input is invalid")
	}
	sum := sha256.Sum256(outcome.Payload)
	digest := hex.EncodeToString(sum[:])
	if outcome.PayloadDigest != "" && outcome.PayloadDigest != digest {
		return errors.New("J3b outcome payload digest mismatch")
	}
	outcome.PayloadDigest = digest
	if outcome.ObservedAt.IsZero() {
		outcome.ObservedAt = now
	}
	if outcome.StoredAt.IsZero() {
		outcome.StoredAt = now
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin J3b outcome: %w", err)
	}
	defer tx.Rollback()
	var inputDigest string
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT input_digest FROM `+s.table("model_check_inputs")+` WHERE input_id=?`), outcome.InputID).Scan(&inputDigest); err != nil {
		return err
	}
	if inputDigest != outcome.InputDigest {
		return errors.New("J3b outcome input digest mismatch")
	}
	var token, owner, outcomeID string
	var fence int64
	var untilRaw any
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT claim_token,owner_id,outcome_id,fence_token,claim_until FROM `+s.table("model_check_execution_claims")+` WHERE input_id=?`+s.forUpdate()), outcome.InputID).Scan(&token, &owner, &outcomeID, &fence, &untilRaw); err != nil {
		return err
	}
	until, err := parseDBTime(untilRaw)
	if err != nil {
		return err
	}
	if token != claim.ClaimToken || owner != claim.OwnerID || outcomeID != claim.OutcomeID || fence != claim.FenceToken || !now.Before(until) {
		return ErrStaleFence
	}
	var existingID, existingDigest string
	err = tx.QueryRowContext(ctx, s.bind(`SELECT outcome_id,payload_digest FROM `+s.table("model_check_outcomes")+` WHERE input_id=?`+s.forUpdate()), outcome.InputID).Scan(&existingID, &existingDigest)
	if err == nil {
		if existingID != outcome.OutcomeID || existingDigest != outcome.PayloadDigest {
			return ErrOutcomeConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, s.bind(`INSERT INTO `+s.table("model_check_outcomes")+` (outcome_id,input_id,input_digest,fence_token,observed_at,stored_at,payload,payload_digest,committed) VALUES (?,?,?,?,?,?,?,?,?)`), outcome.OutcomeID, outcome.InputID, outcome.InputDigest, fence, outcome.ObservedAt.UTC().Format(time.RFC3339Nano), outcome.StoredAt.UTC().Format(time.RFC3339Nano), []byte(outcome.Payload), outcome.PayloadDigest, true)
	if err != nil {
		return fmt.Errorf("persist J3b outcome: %w", err)
	}
	return tx.Commit()
}

func parseDBTime(value any) (time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		return typed, nil
	case string:
		return time.Parse(time.RFC3339Nano, typed)
	case []byte:
		return time.Parse(time.RFC3339Nano, string(typed))
	default:
		return time.Time{}, fmt.Errorf("unsupported J3b timestamp %T", value)
	}
}
