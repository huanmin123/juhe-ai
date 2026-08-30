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
	report := contracts.ValidateJ3bCutoverEvidenceForOwner(evidence, contracts.J3bGatewayCutoverOwner, ownerEpoch, now, verifyConfiguredBackupArtifact)
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
	expected := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(artifact.Hash)), "sha256:")
	if actual != expected {
		return fmt.Errorf("hash does not match file SHA-256")
	}
	return nil
}
