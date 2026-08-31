package businesshandoff

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// The evidence schema and pure validation are shared so Gateway and
// maintenance use identical contract rules. This package owns only the
// maintenance file I/O wrapper.
type J3bCutoverEvidence = contracts.J3bCutoverEvidence
type J3bBackupArtifact = contracts.J3bBackupArtifact
type J3bEvidenceFreshness = contracts.J3bEvidenceFreshness
type J3bCutoverEvidenceReport = contracts.J3bCutoverEvidenceReport
type J3bReadbackManifestReference = contracts.J3bReadbackManifestReference
type J3bReadbackManifest = contracts.J3bReadbackManifest

func ValidateJ3bCutoverEvidence(evidence J3bCutoverEvidence, now time.Time) J3bCutoverEvidenceReport {
	return contracts.ValidateJ3bCutoverEvidenceWithReadback(evidence, now, verifyJ3bBackupArtifact, verifyJ3bReadbackManifest)
}

func ValidateJ3bCutoverEvidenceForOwner(evidence J3bCutoverEvidence, owner, epoch string, now time.Time) J3bCutoverEvidenceReport {
	return contracts.ValidateJ3bCutoverEvidenceForOwnerWithReadback(evidence, owner, epoch, now, verifyJ3bBackupArtifact, verifyJ3bReadbackManifest)
}

func ValidateJ3bBackfillEvidence(evidence J3bCutoverEvidence, now time.Time) J3bCutoverEvidenceReport {
	return contracts.ValidateJ3bBackfillEvidenceWithReadback(evidence, now, verifyJ3bBackupArtifact, verifyJ3bReadbackManifest)
}

func VerifyJ3bCutoverEvidence(path string, now time.Time) (J3bCutoverEvidenceReport, error) {
	return verifyJ3bEvidence(path, now, true)
}

func VerifyJ3bBackfillEvidence(path string, now time.Time) (J3bCutoverEvidenceReport, error) {
	return verifyJ3bEvidence(path, now, false)
}

func verifyJ3bEvidence(path string, now time.Time, requireTargetDigest bool) (J3bCutoverEvidenceReport, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return J3bCutoverEvidenceReport{}, fmt.Errorf("J3b cutover evidence path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return J3bCutoverEvidenceReport{}, fmt.Errorf("read J3b cutover evidence file: %w", err)
	}
	evidence, err := contracts.DecodeJ3bCutoverEvidence(data)
	if err != nil {
		return J3bCutoverEvidenceReport{Errors: []string{fmt.Sprintf("decode J3b cutover evidence: %v", err)}}, nil
	}
	if requireTargetDigest {
		return ValidateJ3bCutoverEvidence(evidence, now), nil
	}
	return ValidateJ3bBackfillEvidence(evidence, now), nil
}

func verifyJ3bBackupArtifact(artifact J3bBackupArtifact) error {
	info, err := os.Stat(artifact.Path)
	if err != nil {
		return fmt.Errorf("path is unreadable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path must be a regular file")
	}
	file, err := os.Open(artifact.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(digest.Sum(nil))
	if !equalJ3bDigest(actual, artifact.Hash) {
		return fmt.Errorf("hash does not match file SHA-256")
	}
	return nil
}

// verifyJ3bReadbackManifest is intentionally file-only. It verifies that the
// evidence reference hashes the exact manifest bytes, then delegates all
// version, scope, snapshot, table, projection and freshness checks to the
// shared pure contract. It does not open SQLite/PostgreSQL or imply recovery.
func verifyJ3bReadbackManifest(reference J3bReadbackManifestReference, evidence J3bCutoverEvidence, now time.Time) error {
	data, err := os.ReadFile(reference.Path)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	digest := sha256.Sum256(data)
	if !equalJ3bDigest(hex.EncodeToString(digest[:]), reference.Hash) {
		return fmt.Errorf("manifest file SHA-256 does not match evidence reference")
	}
	manifest, err := contracts.DecodeJ3bReadbackManifest(data)
	if err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	if reference.FormatVersion != manifest.FormatVersion || reference.Scope != manifest.Scope || reference.SourceSnapshotIdentity != manifest.SourceSnapshotIdentity || reference.SourceSchema != manifest.SourceSchema || reference.TargetSchema != manifest.TargetSchema {
		return fmt.Errorf("manifest identity does not match evidence reference")
	}
	if errors := contracts.ValidateJ3bReadbackManifest(manifest, now, evidence.Freshness.MaxAgeSeconds); len(errors) > 0 {
		return fmt.Errorf("manifest is not cutover-ready: %s", strings.Join(errors, "; "))
	}
	return nil
}

func equalJ3bDigest(actual, expected string) bool {
	return normalizeJ3bDigest(actual) == normalizeJ3bDigest(expected)
}

func normalizeJ3bDigest(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.TrimPrefix(value, "sha256:")
}
