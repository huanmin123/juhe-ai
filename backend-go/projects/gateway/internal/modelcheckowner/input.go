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

// InputRecord is the immutable, credential-free request snapshot persisted
// before any probe starts. Payload must contain only replayable request data.
type InputRecord struct {
	InputID, IdentityKey, TargetID, ConfigRevision, PolicyRevision, Trigger string
	InputVersion                                                            int64
	IssuedAt, ExpiresAt                                                     time.Time
	Payload                                                                 json.RawMessage
	InputDigest                                                             string
}

var (
	ErrInputConflict = errors.New("model check input ID is already bound to different immutable input")
	ErrInputTampered = errors.New("stored model check input failed integrity verification")
)

// IssueInput validates and durably records an immutable input. Repeating the
// exact input is idempotent; reusing an ID for different bytes fails closed.
func (s *Store) IssueInput(ctx context.Context, input InputRecord) (InputRecord, error) {
	if s == nil || s.db == nil {
		return InputRecord{}, errors.New("J3b store is not open")
	}
	if err := validateInput(input); err != nil {
		return InputRecord{}, err
	}
	canonicalPayload, err := canonicalJSON(input.Payload)
	if err != nil {
		return InputRecord{}, errors.New("J3b input payload must be valid JSON")
	}
	input.Payload = canonicalPayload
	digest, err := digestInput(input)
	if err != nil {
		return InputRecord{}, ErrInputTampered
	}
	if input.InputDigest != "" && input.InputDigest != digest {
		return InputRecord{}, ErrInputTampered
	}
	input.InputDigest = digest
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InputRecord{}, fmt.Errorf("begin J3b input transaction: %w", err)
	}
	defer tx.Rollback()
	table := s.table("model_check_inputs")
	var stored InputRecord
	var issuedRaw, expiresRaw any
	var storedPayload []byte
	err = tx.QueryRowContext(ctx, s.bind("SELECT input_id,identity_key,input_version,input_digest,target_id,config_revision,policy_revision,trigger,issued_at,expires_at,payload FROM "+table+" WHERE input_id=?"), input.InputID).Scan(&stored.InputID, &stored.IdentityKey, &stored.InputVersion, &stored.InputDigest, &stored.TargetID, &stored.ConfigRevision, &stored.PolicyRevision, &stored.Trigger, &issuedRaw, &expiresRaw, &storedPayload)
	if err == nil {
		stored.IssuedAt, err = parseDBTime(issuedRaw)
		if err != nil {
			return InputRecord{}, ErrInputTampered
		}
		stored.ExpiresAt, err = parseDBTime(expiresRaw)
		if err != nil {
			return InputRecord{}, ErrInputTampered
		}
		stored.Payload, err = canonicalJSON(storedPayload)
		if err != nil {
			return InputRecord{}, ErrInputTampered
		}
		storedDigest, digestErr := digestInput(stored)
		if digestErr != nil || stored.InputDigest != storedDigest || !sameInputSnapshot(stored, input) {
			return InputRecord{}, ErrInputConflict
		}
		if err := tx.Commit(); err != nil {
			return InputRecord{}, fmt.Errorf("commit idempotent J3b input: %w", err)
		}
		return stored, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return InputRecord{}, fmt.Errorf("read existing J3b input: %w", err)
	}
	var nextVersion int64
	err = tx.QueryRowContext(ctx, s.bind("INSERT INTO "+s.table("model_check_input_versions")+" (identity_key,next_version,updated_at) VALUES (?,?,?) ON CONFLICT(identity_key) DO UPDATE SET next_version="+s.table("model_check_input_versions")+".next_version+1,updated_at=excluded.updated_at RETURNING next_version"), input.IdentityKey, int64(2), input.IssuedAt.UTC().Format(time.RFC3339Nano)).Scan(&nextVersion)
	if err != nil || nextVersion < 2 {
		return InputRecord{}, fmt.Errorf("allocate J3b input version: %w", err)
	}
	input.InputVersion = nextVersion - 1
	input.InputDigest, err = digestInput(input)
	if err != nil {
		return InputRecord{}, ErrInputTampered
	}
	_, err = tx.ExecContext(ctx, s.bind("INSERT INTO "+table+" (input_id,identity_key,input_version,input_digest,target_id,config_revision,policy_revision,trigger,issued_at,expires_at,payload) VALUES (?,?,?,?,?,?,?,?,?,?,?)"), input.InputID, input.IdentityKey, input.InputVersion, input.InputDigest, input.TargetID, input.ConfigRevision, input.PolicyRevision, input.Trigger, input.IssuedAt.UTC().Format(time.RFC3339Nano), input.ExpiresAt.UTC().Format(time.RFC3339Nano), []byte(input.Payload))
	if err != nil {
		return InputRecord{}, fmt.Errorf("persist J3b input: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return InputRecord{}, fmt.Errorf("commit J3b input: %w", err)
	}
	return input, nil
}

// LoadInput replays the exact immutable snapshot and verifies the stored
// payload digest before a caller can execute probes.
func (s *Store) LoadInput(ctx context.Context, inputID string, now time.Time) (InputRecord, error) {
	if s == nil || s.db == nil || strings.TrimSpace(inputID) == "" {
		return InputRecord{}, errors.New("J3b input ID is required")
	}
	var input InputRecord
	var issuedRaw, expiresRaw any
	var payload []byte
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT input_id,identity_key,input_version,input_digest,target_id,config_revision,policy_revision,trigger,issued_at,expires_at,payload FROM `+s.table("model_check_inputs")+` WHERE input_id=?`), inputID).Scan(&input.InputID, &input.IdentityKey, &input.InputVersion, &input.InputDigest, &input.TargetID, &input.ConfigRevision, &input.PolicyRevision, &input.Trigger, &issuedRaw, &expiresRaw, &payload)
	if err != nil {
		return InputRecord{}, err
	}
	input.IssuedAt, err = parseDBTime(issuedRaw)
	if err != nil {
		return InputRecord{}, fmt.Errorf("parse J3b input issued_at: %w", err)
	}
	input.ExpiresAt, err = parseDBTime(expiresRaw)
	if err != nil {
		return InputRecord{}, fmt.Errorf("parse J3b input expires_at: %w", err)
	}
	canonical, err := canonicalJSON(payload)
	input.Payload = canonical
	computedDigest, digestErr := digestInput(input)
	if err != nil || digestErr != nil || computedDigest != input.InputDigest {
		return InputRecord{}, ErrInputTampered
	}
	if now.IsZero() || !now.Before(input.ExpiresAt) {
		return InputRecord{}, errors.New("J3b input is expired")
	}
	return input, nil
}

func validateInput(input InputRecord) error {
	for name, value := range map[string]string{"input_id": input.InputID, "identity_key": input.IdentityKey, "target_id": input.TargetID, "config_revision": input.ConfigRevision, "policy_revision": input.PolicyRevision, "trigger": input.Trigger} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("J3b input %s is required", name)
		}
	}
	if input.IssuedAt.IsZero() || input.ExpiresAt.IsZero() || !input.ExpiresAt.After(input.IssuedAt) {
		return errors.New("J3b input expiry must be after issued_at")
	}
	if !json.Valid(input.Payload) {
		return errors.New("J3b input payload must be valid JSON")
	}
	return nil
}

func digestPayload(payload []byte) string {
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func digestInput(input InputRecord) (string, error) {
	payload, err := canonicalJSON(input.Payload)
	if err != nil {
		return "", err
	}
	immutable := struct {
		InputID, IdentityKey, TargetID, ConfigRevision, PolicyRevision, Trigger string
		IssuedAt, ExpiresAt                                                     string
		Payload                                                                 json.RawMessage
	}{
		InputID: input.InputID, IdentityKey: input.IdentityKey, TargetID: input.TargetID,
		ConfigRevision: input.ConfigRevision, PolicyRevision: input.PolicyRevision, Trigger: input.Trigger,
		IssuedAt: input.IssuedAt.UTC().Format(time.RFC3339Nano), ExpiresAt: input.ExpiresAt.UTC().Format(time.RFC3339Nano), Payload: payload,
	}
	encoded, err := json.Marshal(immutable)
	if err != nil {
		return "", err
	}
	return digestPayload(encoded), nil
}

func sameInputSnapshot(left, right InputRecord) bool {
	return left.InputID == right.InputID && left.IdentityKey == right.IdentityKey && left.TargetID == right.TargetID && left.ConfigRevision == right.ConfigRevision && left.PolicyRevision == right.PolicyRevision && left.Trigger == right.Trigger && left.IssuedAt.UTC().Equal(right.IssuedAt.UTC()) && left.ExpiresAt.UTC().Equal(right.ExpiresAt.UTC()) && jsonEqual(left.Payload, right.Payload)
}

func canonicalJSON(payload []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func jsonEqual(left, right []byte) bool {
	var a, b any
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(a)
	rightCanonical, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && string(leftCanonical) == string(rightCanonical)
}

func (s *Store) table(name string) string {
	if s.mode == "postgres" {
		return s.schema + "." + name
	}
	return name
}
