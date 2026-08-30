package contracts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// J3bGatewayCutoverOwner is the canonical target owner declared by the
// Business owner manifest. It is deliberately distinct from the process mode
// value "gateway" used by Gateway configuration.
const J3bGatewayCutoverOwner = "go-gateway"

// J3bCutoverEvidence is an externally produced handoff observation. Shared
// contracts only decode and validate it; callers own file access and artifact
// hashing so this package remains free of I/O.
type J3bCutoverEvidence struct {
	OldOwner             string               `json:"oldOwner"`
	NewOwner             string               `json:"newOwner"`
	OwnerEpoch           string               `json:"ownerEpoch"`
	DrainCompleted       bool                 `json:"drainCompleted"`
	InFlight             int64                `json:"inFlight"`
	ActivePathZero       bool                 `json:"activePathZero"`
	BackupArtifact       J3bBackupArtifact    `json:"backupArtifact"`
	RollbackReplayCursor string               `json:"rollbackReplayCursor"`
	Freshness            J3bEvidenceFreshness `json:"freshness"`
	SourceDigest         string               `json:"sourceDigest"`
	TargetDigest         string               `json:"targetDigest"`
	BlockedFindings      int64                `json:"blockedFindings"`
	inFlightSet          bool
	blockedFindingsSet   bool
	parsedFromJSON       bool
}

type J3bBackupArtifact struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

type J3bEvidenceFreshness struct {
	CapturedAt    string `json:"capturedAt"`
	MaxAgeSeconds int64  `json:"maxAgeSeconds"`
}

type J3bCutoverEvidenceReport struct {
	Ready  bool     `json:"ready"`
	Errors []string `json:"errors,omitempty"`
}

// J3bCutoverArtifactVerifier performs caller-owned artifact verification. It
// must not mutate the artifact or external owner state.
type J3bCutoverArtifactVerifier func(J3bBackupArtifact) error

// UnmarshalJSON rejects unknown fields and records required zero-valued
// counters so missing inFlight/blockedFindings cannot be mistaken for zero.
func (e *J3bCutoverEvidence) UnmarshalJSON(data []byte) error {
	type alias J3bCutoverEvidence
	var decoded alias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*e = J3bCutoverEvidence(decoded)
	if raw, ok := fields["inFlight"]; ok && string(bytes.TrimSpace(raw)) != "null" {
		e.inFlightSet = true
	}
	if raw, ok := fields["blockedFindings"]; ok && string(bytes.TrimSpace(raw)) != "null" {
		e.blockedFindingsSet = true
	}
	e.parsedFromJSON = true
	return nil
}

func DecodeJ3bCutoverEvidence(data []byte) (J3bCutoverEvidence, error) {
	var evidence J3bCutoverEvidence
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&evidence); err != nil {
		return J3bCutoverEvidence{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != nil {
		if errors.Is(err, io.EOF) {
			return evidence, nil
		}
		return J3bCutoverEvidence{}, err
	}
	return J3bCutoverEvidence{}, fmt.Errorf("trailing JSON data")
}

func ValidateJ3bCutoverEvidence(evidence J3bCutoverEvidence, now time.Time, verifyArtifact J3bCutoverArtifactVerifier) J3bCutoverEvidenceReport {
	return validateJ3bCutoverEvidence(evidence, now, verifyArtifact, true)
}

// ValidateJ3bBackfillEvidence validates the pre-backfill handoff proof. The
// target digest is optional because the backfill itself creates that target.
func ValidateJ3bBackfillEvidence(evidence J3bCutoverEvidence, now time.Time, verifyArtifact J3bCutoverArtifactVerifier) J3bCutoverEvidenceReport {
	return validateJ3bCutoverEvidence(evidence, now, verifyArtifact, false)
}

// ValidateJ3bCutoverEvidenceForOwner additionally binds the proof to the
// process owner and epoch that are about to be started.
func ValidateJ3bCutoverEvidenceForOwner(evidence J3bCutoverEvidence, owner, epoch string, now time.Time, verifyArtifact J3bCutoverArtifactVerifier) J3bCutoverEvidenceReport {
	report := validateJ3bCutoverEvidence(evidence, now, verifyArtifact, true)
	if strings.TrimSpace(owner) != "" && strings.TrimSpace(evidence.NewOwner) != strings.TrimSpace(owner) {
		report.Errors = append(report.Errors, "newOwner does not match configured owner")
	}
	if strings.TrimSpace(epoch) != "" && strings.TrimSpace(evidence.OwnerEpoch) != strings.TrimSpace(epoch) {
		report.Errors = append(report.Errors, "ownerEpoch does not match configured epoch")
	}
	report.Ready = len(report.Errors) == 0
	return report
}

func validateJ3bCutoverEvidence(evidence J3bCutoverEvidence, now time.Time, verifyArtifact J3bCutoverArtifactVerifier, requireTargetDigest bool) J3bCutoverEvidenceReport {
	report := J3bCutoverEvidenceReport{}
	add := func(message string) { report.Errors = append(report.Errors, message) }
	if strings.TrimSpace(evidence.OldOwner) == "" {
		add("oldOwner is required")
	}
	if strings.TrimSpace(evidence.NewOwner) == "" {
		add("newOwner is required")
	} else if strings.TrimSpace(evidence.NewOwner) != J3bGatewayCutoverOwner {
		add("newOwner must be go-gateway")
	}
	if strings.TrimSpace(evidence.OldOwner) != "" && strings.TrimSpace(evidence.OldOwner) == strings.TrimSpace(evidence.NewOwner) {
		add("oldOwner and newOwner must differ")
	}
	if strings.TrimSpace(evidence.OwnerEpoch) == "" {
		add("ownerEpoch is required")
	}
	if !evidence.DrainCompleted {
		add("drainCompleted must be true")
	}
	if evidence.parsedFromJSON && !evidence.inFlightSet {
		add("inFlight is required")
	} else if evidence.InFlight != 0 {
		add("inFlight must be zero")
	}
	if !evidence.ActivePathZero {
		add("activePathZero must be true")
	}
	if evidence.parsedFromJSON && !evidence.blockedFindingsSet {
		add("blockedFindings is required")
	} else if evidence.BlockedFindings != 0 {
		add("blockedFindings must be zero")
	}
	if strings.TrimSpace(evidence.RollbackReplayCursor) == "" {
		add("rollbackReplayCursor is required")
	}
	if !validJ3bEvidenceDigest(evidence.SourceDigest) {
		add("sourceDigest must be a SHA-256 digest")
	}
	if requireTargetDigest {
		if !validJ3bEvidenceDigest(evidence.TargetDigest) {
			add("targetDigest must be a SHA-256 digest")
		} else if validJ3bEvidenceDigest(evidence.SourceDigest) && !equalJ3bEvidenceDigest(evidence.SourceDigest, evidence.TargetDigest) {
			add("sourceDigest and targetDigest must match")
		}
	} else if strings.TrimSpace(evidence.TargetDigest) != "" && !validJ3bEvidenceDigest(evidence.TargetDigest) {
		add("targetDigest must be a SHA-256 digest when provided")
	}
	if !validJ3bEvidenceDigest(evidence.BackupArtifact.Hash) {
		add("backupArtifact.hash must be a SHA-256 digest")
	}
	if strings.TrimSpace(evidence.BackupArtifact.Path) == "" {
		add("backupArtifact.path is required")
	} else if verifyArtifact == nil {
		add("backupArtifact verifier is required")
	} else if err := verifyArtifact(evidence.BackupArtifact); err != nil {
		add(fmt.Sprintf("backupArtifact verification failed: %v", err))
	}
	if evidence.Freshness.MaxAgeSeconds <= 0 {
		add("freshness.maxAgeSeconds must be positive")
	} else if evidence.Freshness.MaxAgeSeconds > int64((time.Duration(1<<63-1))/time.Second) {
		add("freshness.maxAgeSeconds is out of range")
	} else if captured, err := time.Parse(time.RFC3339, strings.TrimSpace(evidence.Freshness.CapturedAt)); err != nil {
		add("freshness.capturedAt must be RFC3339")
	} else if now.IsZero() {
		add("validation time is required")
	} else if captured.After(now) || now.Sub(captured) > time.Duration(evidence.Freshness.MaxAgeSeconds)*time.Second {
		add("freshness evidence is expired or from the future")
	}
	report.Ready = len(report.Errors) == 0
	return report
}

func validJ3bEvidenceDigest(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "sha256:") {
		value = value[len("sha256:"):]
	}
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func equalJ3bEvidenceDigest(left, right string) bool {
	return normalizeJ3bEvidenceDigest(left) == normalizeJ3bEvidenceDigest(right)
}

func normalizeJ3bEvidenceDigest(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(value, "sha256:") {
		return value[len("sha256:"):]
	}
	return value
}
