package businesshandoff

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

func validJ3bCutoverEvidence(t *testing.T) (J3bCutoverEvidence, time.Time) {
	t.Helper()
	artifact := filepath.Join(t.TempDir(), "migration-backup.tar")
	data := []byte("immutable backup evidence")
	if err := os.WriteFile(artifact, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	readbackDigest := hex.EncodeToString(digest[:])
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	manifestPath, manifestHash := writeValidJ3bReadbackManifest(t, now)
	return J3bCutoverEvidence{
		OldOwner:       "node",
		NewOwner:       contracts.J3bGatewayCutoverOwner,
		OwnerEpoch:     "j3b-cutover-20260830-001",
		DrainCompleted: true,
		InFlight:       0,
		ActivePathZero: true,
		BackupArtifact: J3bBackupArtifact{
			Path: artifact,
			Hash: hex.EncodeToString(digest[:]),
		},
		RollbackReplayCursor: "receipt:00000042",
		Freshness: J3bEvidenceFreshness{
			CapturedAt:    now.Add(-2 * time.Minute).Format(time.RFC3339),
			MaxAgeSeconds: 300,
		},
		SourceDigest:     readbackDigest,
		TargetDigest:     readbackDigest,
		ReadbackManifest: J3bReadbackManifestReference{Path: manifestPath, Hash: manifestHash, FormatVersion: contracts.J3bReadbackManifestFormatVersion, Scope: contracts.J3bReadbackManifestScope, SourceSnapshotIdentity: "snapshot-1", SourceSchema: "legacy-sqlite-dataset+stats", TargetSchema: "juhe-j3b-sqlite"},
		BlockedFindings:  0,
	}, now
}

func writeValidJ3bReadbackManifest(t *testing.T, now time.Time) (string, string) {
	t.Helper()
	manifest := contracts.J3bReadbackManifest{
		FormatVersion: contracts.J3bReadbackManifestFormatVersion,
		Scope:         contracts.J3bReadbackManifestScope,
		Producer:      "test", SourceSnapshotIdentity: "snapshot-1", SourceSchema: "legacy-sqlite-dataset+stats", TargetSchema: "juhe-j3b-sqlite",
		ProjectionComplete: true, VerifiedAt: now.Format(time.RFC3339),
	}
	for _, name := range []string{"account_quality_health_hourly", "model_check_items", "model_check_observations", "model_check_runs", "model_token_intercept_baseline_versions"} {
		manifest.Tables = append(manifest.Tables, contracts.J3bReadbackTableDigest{Name: name, SourceRows: 1, TargetRows: 1, SourceDigest: strings.Repeat("a", 64), TargetDigest: strings.Repeat("a", 64)})
	}
	hash, err := contracts.ComputeJ3bReadbackManifestHash(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = hash
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "readback-manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	fileHash := sha256.Sum256(data)
	return path, hex.EncodeToString(fileHash[:])
}

func TestValidateJ3bCutoverEvidenceTable(t *testing.T) {
	base, now := validJ3bCutoverEvidence(t)
	tests := []struct {
		name   string
		mutate func(*J3bCutoverEvidence)
		ready  bool
	}{
		{name: "complete", ready: true},
		{name: "owner missing", mutate: func(e *J3bCutoverEvidence) { e.NewOwner = "" }},
		{name: "drain incomplete", mutate: func(e *J3bCutoverEvidence) { e.DrainCompleted = false }},
		{name: "in flight", mutate: func(e *J3bCutoverEvidence) { e.InFlight = 1 }},
		{name: "active path findings", mutate: func(e *J3bCutoverEvidence) { e.ActivePathZero = false }},
		{name: "blocked finding", mutate: func(e *J3bCutoverEvidence) { e.BlockedFindings = 1 }},
		{name: "hash mismatch", mutate: func(e *J3bCutoverEvidence) { e.BackupArtifact.Hash = "00" + e.BackupArtifact.Hash[2:] }},
		{name: "stale", mutate: func(e *J3bCutoverEvidence) { e.Freshness.CapturedAt = now.Add(-6 * time.Minute).Format(time.RFC3339) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := base
			if test.mutate != nil {
				test.mutate(&evidence)
			}
			report := ValidateJ3bCutoverEvidence(evidence, now)
			if report.Ready != test.ready {
				t.Fatalf("ready=%t, want %t; errors=%v", report.Ready, test.ready, report.Errors)
			}
		})
	}
}

func TestVerifyJ3bCutoverEvidenceRejectsManifestReferenceHashMismatch(t *testing.T) {
	evidence, now := validJ3bCutoverEvidence(t)
	evidence.ReadbackManifest.Hash = strings.Repeat("0", 64)
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := VerifyJ3bCutoverEvidence(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatalf("bad manifest file hash unexpectedly ready: %+v", report)
	}
}

func TestVerifyJ3bCutoverEvidenceRejectsManifestIdentityMismatch(t *testing.T) {
	evidence, now := validJ3bCutoverEvidence(t)
	evidence.ReadbackManifest.SourceSnapshotIdentity = "different-snapshot"
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := VerifyJ3bCutoverEvidence(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatalf("manifest identity mismatch unexpectedly ready: %+v", report)
	}
}

func TestVerifyJ3bCutoverEvidenceRejectsMissingRequiredJSONFields(t *testing.T) {
	evidence, now := validJ3bCutoverEvidence(t)
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "inFlight")
	delete(fields, "blockedFindings")
	path := filepath.Join(t.TempDir(), "evidence.json")
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := VerifyJ3bCutoverEvidence(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatalf("missing required fields unexpectedly ready: %+v", report)
	}
}

func TestVerifyJ3bCutoverEvidenceMalformedJSONIsUnreadyNotInputError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := VerifyJ3bCutoverEvidence(path, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || len(report.Errors) == 0 {
		t.Fatalf("malformed JSON report=%+v", report)
	}
}

func TestVerifyJ3bCutoverEvidenceRejectsUnknownFields(t *testing.T) {
	evidence, now := validJ3bCutoverEvidence(t)
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["unexpected"] = true
	path := filepath.Join(t.TempDir(), "evidence.json")
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := VerifyJ3bCutoverEvidence(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || len(report.Errors) == 0 {
		t.Fatalf("unknown field unexpectedly ready: %+v", report)
	}
}

func TestVerifyJ3bCutoverEvidenceUnreadableFileReturnsError(t *testing.T) {
	if _, err := VerifyJ3bCutoverEvidence(filepath.Join(t.TempDir(), "missing.json"), time.Now()); err == nil {
		t.Fatal("missing evidence file unexpectedly succeeded")
	}
}
