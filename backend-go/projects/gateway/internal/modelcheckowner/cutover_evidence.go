package modelcheckowner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// VerifyConfiguredCutoverEvidence reads the externally generated post-cutover
// proof before Gateway opens Business storage or any management listener.
// It never mutates the evidence artifact or owner state.
func VerifyConfiguredCutoverEvidence(path, ownerEpoch string, now time.Time) (contracts.J3bCutoverEvidenceReport, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return contracts.J3bCutoverEvidenceReport{}, fmt.Errorf("J3b cutover evidence path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return contracts.J3bCutoverEvidenceReport{}, fmt.Errorf("read J3b cutover evidence file: %w", err)
	}
	evidence, err := contracts.DecodeJ3bCutoverEvidence(data)
	if err != nil {
		return contracts.J3bCutoverEvidenceReport{Errors: []string{fmt.Sprintf("decode J3b cutover evidence: %v", err)}}, nil
	}
	report := contracts.ValidateJ3bCutoverEvidenceForOwnerWithReadback(evidence, contracts.J3bGatewayCutoverOwner, ownerEpoch, now, verifyConfiguredBackupArtifact, verifyConfiguredReadbackManifest)
	return report, nil
}

func verifyConfiguredBackupArtifact(artifact contracts.J3bBackupArtifact) error {
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
	if !equalConfiguredDigest(actual, artifact.Hash) {
		return fmt.Errorf("hash does not match file SHA-256")
	}
	return nil
}

// verifyConfiguredReadbackManifest is intentionally file-only. It verifies
// the evidence reference against the exact manifest bytes, then delegates
// schema, freshness, scope, identity and table checks to shared contracts.
func verifyConfiguredReadbackManifest(reference contracts.J3bReadbackManifestReference, evidence contracts.J3bCutoverEvidence, now time.Time) error {
	data, err := os.ReadFile(reference.Path)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	digest := sha256.Sum256(data)
	if !equalConfiguredDigest(hex.EncodeToString(digest[:]), reference.Hash) {
		return fmt.Errorf("manifest file SHA-256 does not match evidence reference")
	}
	manifest, err := contracts.DecodeJ3bReadbackManifest(data)
	if err != nil {
		return fmt.Errorf("decode manifest: %w", err)
	}
	if reference.FormatVersion != manifest.FormatVersion ||
		reference.Scope != manifest.Scope ||
		reference.SourceSnapshotIdentity != manifest.SourceSnapshotIdentity ||
		reference.SourceSchema != manifest.SourceSchema ||
		reference.TargetSchema != manifest.TargetSchema {
		return fmt.Errorf("manifest identity does not match evidence reference")
	}
	if errors := contracts.ValidateJ3bReadbackManifest(manifest, now, evidence.Freshness.MaxAgeSeconds); len(errors) > 0 {
		return fmt.Errorf("manifest is not cutover-ready: %s", strings.Join(errors, "; "))
	}
	return nil
}

func equalConfiguredDigest(actual, expected string) bool {
	return normalizeConfiguredDigest(actual) == normalizeConfiguredDigest(expected)
}

func normalizeConfiguredDigest(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.TrimPrefix(value, "sha256:")
}
