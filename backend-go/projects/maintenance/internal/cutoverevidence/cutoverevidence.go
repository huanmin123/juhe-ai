// Package cutoverevidence assembles the post-cutover J3b handoff evidence
// JSON that go-gateway requires in PostgreSQL mode
// (JUHE_AI_BUSINESS_CUTOVER_EVIDENCE_PATH must pass
// modelcheckowner.VerifyConfiguredCutoverEvidence). Assembly is mechanical:
// every attested field comes from the options or from the referenced
// artifacts, the backup SHA-256 and the manifest file hash are read from the
// real files, and the written file is re-verified with the exact contract
// chain the gateway runs (businesshandoff.VerifyJ3bCutoverEvidence). Evidence
// that does not verify ready is deleted again: the tool never leaves a
// half-valid evidence file behind.
package cutoverevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
	"github.com/huanminabc/juhe-ai/backend-go-maintenance/internal/businesshandoff"
)

const (
	// DefaultOldOwner is the pre-cutover owner recorded when the CLI flag is
	// left at its default.
	DefaultOldOwner = "node-runtime"

	// DefaultMaxAgeSeconds is the evidence freshness window: 90 days.
	DefaultMaxAgeSeconds int64 = 7776000
)

// Options binds one evidence assembly invocation. Every field is required
// except OldOwner and MaxAgeSeconds, which the CLI defaults to
// DefaultOldOwner / DefaultMaxAgeSeconds; the package still refuses empty or
// non-positive values so direct callers cannot skip the attested inputs.
type Options struct {
	// OldOwner is the pre-cutover owner (must differ from go-gateway, which
	// is enforced by the contract self-check).
	OldOwner string
	// OwnerEpoch binds the evidence to one owner epoch; go-gateway compares
	// it against its configured epoch.
	OwnerEpoch string
	// RollbackReplayCursor names the replay position a rollback would resume
	// from (for example a timestamped batch id).
	RollbackReplayCursor string
	// MaxAgeSeconds is freshness.maxAgeSeconds of the assembled evidence.
	MaxAgeSeconds int64
	// BackupArtifactPath is a real, readable, regular file whose SHA-256 is
	// read by this tool and recorded in the evidence.
	BackupArtifactPath string
	// ReadbackManifestPath is a v2 J3b readback manifest file; its raw bytes
	// are hashed into the evidence reference.
	ReadbackManifestPath string
}

// Report is the JSON outcome of one AssembleCutoverEvidence call. Ready
// requires the written evidence to pass businesshandoff.VerifyJ3bCutoverEvidence;
// a not-ready report always describes a removed file.
type Report struct {
	Ready                bool     `json:"ready"`
	EvidencePath         string   `json:"evidencePath"`
	OldOwner             string   `json:"oldOwner"`
	NewOwner             string   `json:"newOwner"`
	OwnerEpoch           string   `json:"ownerEpoch"`
	RollbackReplayCursor string   `json:"rollbackReplayCursor"`
	BackupArtifactPath   string   `json:"backupArtifactPath"`
	BackupSha256         string   `json:"backupSha256"`
	ReadbackManifestPath string   `json:"readbackManifestPath"`
	FreshnessCapturedAt  string   `json:"freshnessCapturedAt"`
	MaxAgeSeconds        int64    `json:"maxAgeSeconds"`
	Errors               []string `json:"errors,omitempty"`
}

// InputError marks a usage-level assembly failure: a missing required option
// value, a non-positive freshness window, or an input file that cannot be
// read or decoded. The CLI maps it to exit 2, while runtime failures
// (writing the evidence) stay exit 1 and contract not-ready results stay
// exit 3.
type InputError struct {
	err error
}

func (e *InputError) Error() string { return e.err.Error() }

func (e *InputError) Unwrap() error { return e.err }

func inputErrorf(format string, args ...any) *InputError {
	return &InputError{err: fmt.Errorf(format, args...)}
}

// verifyAssembled is the self-check seam: production always uses the
// gateway-identical file chain. Tests may swap it to inject verification
// failures that cannot happen with real inputs (I/O errors after the write).
var verifyAssembled = businesshandoff.VerifyJ3bCutoverEvidence

// AssembleCutoverEvidence builds the cutover evidence JSON at evidenceOutPath
// and self-verifies it. newOwner is always the literal go-gateway
// (contracts.J3bGatewayCutoverOwner), drainCompleted/activePathZero are true,
// inFlight/blockedFindings are the explicit zero, sourceDigest/targetDigest
// stay empty and freshness.capturedAt is now (RFC3339). The written file is
// re-verified with businesshandoff.VerifyJ3bCutoverEvidence against the same
// now; a not-ready verdict or a verification error removes the written file
// before returning.
func AssembleCutoverEvidence(opts Options, evidenceOutPath string, now time.Time) (Report, error) {
	oldOwner := strings.TrimSpace(opts.OldOwner)
	ownerEpoch := strings.TrimSpace(opts.OwnerEpoch)
	replayCursor := strings.TrimSpace(opts.RollbackReplayCursor)
	backupPath := strings.TrimSpace(opts.BackupArtifactPath)
	manifestPath := strings.TrimSpace(opts.ReadbackManifestPath)
	outPath := strings.TrimSpace(evidenceOutPath)
	report := Report{
		EvidencePath:         outPath,
		OldOwner:             oldOwner,
		NewOwner:             contracts.J3bGatewayCutoverOwner,
		OwnerEpoch:           ownerEpoch,
		RollbackReplayCursor: replayCursor,
		BackupArtifactPath:   backupPath,
		ReadbackManifestPath: manifestPath,
		MaxAgeSeconds:        opts.MaxAgeSeconds,
	}
	if now.IsZero() {
		return report, inputErrorf("assembly clock is required")
	}
	if oldOwner == "" {
		return report, inputErrorf("old owner is required")
	}
	if ownerEpoch == "" {
		return report, inputErrorf("owner epoch is required")
	}
	if replayCursor == "" {
		return report, inputErrorf("rollback replay cursor is required")
	}
	if opts.MaxAgeSeconds <= 0 {
		return report, inputErrorf("max age seconds must be positive")
	}
	if backupPath == "" {
		return report, inputErrorf("backup artifact path is required")
	}
	if manifestPath == "" {
		return report, inputErrorf("readback manifest path is required")
	}
	if outPath == "" {
		return report, inputErrorf("evidence output path is required")
	}
	backupDigest, err := sha256FileHex(backupPath)
	if err != nil {
		return report, inputErrorf("read backup artifact: %v", err)
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return report, inputErrorf("read readback manifest: %v", err)
	}
	manifest, err := contracts.DecodeJ3bReadbackManifest(manifestData)
	if err != nil {
		return report, inputErrorf("decode readback manifest: %v", err)
	}

	evidence := contracts.J3bCutoverEvidence{
		OldOwner:             oldOwner,
		NewOwner:             contracts.J3bGatewayCutoverOwner,
		OwnerEpoch:           ownerEpoch,
		DrainCompleted:       true,
		InFlight:             0,
		ActivePathZero:       true,
		BackupArtifact:       contracts.J3bBackupArtifact{Path: backupPath, Hash: backupDigest},
		RollbackReplayCursor: replayCursor,
		Freshness: contracts.J3bEvidenceFreshness{
			CapturedAt:    now.UTC().Format(time.RFC3339),
			MaxAgeSeconds: opts.MaxAgeSeconds,
		},
		SourceDigest: "",
		TargetDigest: "",
		ReadbackManifest: contracts.J3bReadbackManifestReference{
			Path:                   manifestPath,
			Hash:                   sha256BytesHex(manifestData),
			FormatVersion:          manifest.FormatVersion,
			Scope:                  manifest.Scope,
			SourceSnapshotIdentity: manifest.SourceSnapshotIdentity,
			SourceSchema:           manifest.SourceSchema,
			TargetSchema:           manifest.TargetSchema,
		},
		BlockedFindings: 0,
	}
	report.BackupSha256 = backupDigest
	report.FreshnessCapturedAt = evidence.Freshness.CapturedAt

	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return report, fmt.Errorf("marshal cutover evidence: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(outPath, data, 0o600); err != nil {
		return report, fmt.Errorf("write cutover evidence: %w", err)
	}
	verifyReport, err := verifyAssembled(outPath, now)
	if err != nil {
		_ = os.Remove(outPath)
		report.Errors = []string{err.Error()}
		return report, fmt.Errorf("self-verify written cutover evidence: %w", err)
	}
	if !verifyReport.Ready {
		_ = os.Remove(outPath)
		report.Ready = false
		report.Errors = verifyReport.Errors
		return report, nil
	}
	report.Ready = true
	return report, nil
}

// sha256FileHex hashes one real, regular file byte by byte.
func sha256FileHex(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("backup artifact must be a regular file")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func sha256BytesHex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
