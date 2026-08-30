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

func ValidateJ3bCutoverEvidence(evidence J3bCutoverEvidence, now time.Time) J3bCutoverEvidenceReport {
	return contracts.ValidateJ3bCutoverEvidence(evidence, now, verifyJ3bBackupArtifact)
}

func ValidateJ3bBackfillEvidence(evidence J3bCutoverEvidence, now time.Time) J3bCutoverEvidenceReport {
	return contracts.ValidateJ3bBackfillEvidence(evidence, now, verifyJ3bBackupArtifact)
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

func equalJ3bDigest(actual, expected string) bool {
	return normalizeJ3bDigest(actual) == normalizeJ3bDigest(expected)
}

func normalizeJ3bDigest(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.TrimPrefix(value, "sha256:")
}
