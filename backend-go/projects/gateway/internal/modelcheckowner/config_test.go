package modelcheckowner

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

func TestDisabledNeedsNoOwnerOrStorage(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	if err != nil || cfg.Enabled {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestRejectsNonGatewayOwner(t *testing.T) {
	values := map[string]string{"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_J3B_OWNER": "jobs"}
	if _, err := LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("non-gateway J3b owner must fail closed")
	}
}

func TestRejectsSQLiteUntilHandoff(t *testing.T) {
	values := map[string]string{"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_J3B_OWNER": "gateway", "JUHE_AI_J3B_INSTANCE_ID": "gw-1", "JUHE_AI_J3B_STORE": "sqlite", "JUHE_AI_J3B_DATABASE_PATH": "j3b.db", "JUHE_AI_J3B_BUSINESS_DATABASE_PATH": "business.db", "JUHE_AI_J3B_CREDENTIAL_SECRET": "credential", "JUHE_AI_J3B_IDENTITY_SECRET": "identity"}
	if _, err := LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("SQLite must remain closed until owner handoff")
	}
}

func TestRejectsPostgresUntilRuntimeReadiness(t *testing.T) {
	values := map[string]string{"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_J3B_OWNER": "gateway", "JUHE_AI_J3B_INSTANCE_ID": "gw-1", "JUHE_AI_J3B_STORE": "postgres", "JUHE_AI_J3B_POSTGRES_URL": "postgres://j3b", "JUHE_AI_J3B_BUSINESS_POSTGRES_URL": "postgres://business", "JUHE_AI_J3B_CREDENTIAL_SECRET": "credential", "JUHE_AI_J3B_IDENTITY_SECRET": "identity"}
	if _, err := LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("Gateway J3b must remain closed until runtime readiness is wired")
	}
}

func TestRejectsConfirmedHandoffUntilNodeWriterStopped(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_J3B_ENABLED":                    "true",
		"JUHE_AI_J3B_OWNER":                      "gateway",
		"JUHE_AI_J3B_INSTANCE_ID":                "gw-1",
		"JUHE_AI_J3B_STORE":                      "postgres",
		"JUHE_AI_J3B_POSTGRES_URL":               "postgres://j3b",
		"JUHE_AI_J3B_BUSINESS_POSTGRES_URL":      "postgres://business",
		"JUHE_AI_J3B_CREDENTIAL_SECRET":          "credential",
		"JUHE_AI_J3B_IDENTITY_SECRET":            "identity",
		"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED": "true",
	}
	if _, err := LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("confirmed Business handoff must fail closed while Node writer is active")
	}
}

func TestAcceptsOnlyWhenAllOwnerGatesAreExplicit(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_J3B_ENABLED":                    "true",
		"JUHE_AI_J3B_OWNER":                      "gateway",
		"JUHE_AI_J3B_INSTANCE_ID":                "gw-1",
		"JUHE_AI_J3B_STORE":                      "postgres",
		"JUHE_AI_J3B_POSTGRES_URL":               "postgres://j3b",
		"JUHE_AI_J3B_BUSINESS_POSTGRES_URL":      "postgres://business",
		"JUHE_AI_J3B_CREDENTIAL_SECRET":          "credential",
		"JUHE_AI_J3B_IDENTITY_SECRET":            "identity",
		"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED": "true",
		"JUHE_AI_J3B_NODE_WRITER_STOPPED":        "true",
		"JUHE_AI_J3B_OWNER_EPOCH":                "epoch-1",
		"JUHE_AI_J3B_CUTOVER_EVIDENCE_PATH":      "evidence.json",
		"JUHE_AI_J3B_SCHEMA_READY":               "true",
		"JUHE_AI_J3B_HEALTH_BOUNDARY_READY":      "true",
		"JUHE_AI_J3B_RUNTIME_READY":              "true",
		"JUHE_AI_J3B_CIRCUIT_REDIS_URL":          "redis://127.0.0.1:6379/9",
		"JUHE_AI_J3B_CIRCUIT_REDIS_NAMESPACE":    "dev",
	}
	cfg, err := LoadConfig(func(key string) string { return values[key] })
	if err != nil || !cfg.Enabled || !cfg.BusinessHandoffConfirmed || !cfg.NodeWriterStopped || cfg.OwnerEpoch != "epoch-1" || !cfg.SchemaReady || !cfg.HealthBoundaryReady || !cfg.RuntimeReady || cfg.CircuitRuntimeCapacity != 100000 || cfg.CircuitRuntimeRetention <= 0 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestRejectsConfirmedHandoffWithoutOwnerEpoch(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_J3B_ENABLED":                    "true",
		"JUHE_AI_J3B_OWNER":                      "gateway",
		"JUHE_AI_J3B_INSTANCE_ID":                "gw-1",
		"JUHE_AI_J3B_STORE":                      "postgres",
		"JUHE_AI_J3B_POSTGRES_URL":               "postgres://j3b",
		"JUHE_AI_J3B_BUSINESS_POSTGRES_URL":      "postgres://business",
		"JUHE_AI_J3B_CREDENTIAL_SECRET":          "credential",
		"JUHE_AI_J3B_IDENTITY_SECRET":            "identity",
		"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED": "true",
		"JUHE_AI_J3B_NODE_WRITER_STOPPED":        "true",
	}
	if _, err := LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("confirmed Business handoff without owner epoch must fail closed")
	}
}

func TestRejectsConfirmedHandoffWithoutCutoverEvidencePath(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_J3B_ENABLED":                    "true",
		"JUHE_AI_J3B_OWNER":                      "gateway",
		"JUHE_AI_J3B_INSTANCE_ID":                "gw-1",
		"JUHE_AI_J3B_STORE":                      "postgres",
		"JUHE_AI_J3B_POSTGRES_URL":               "postgres://j3b",
		"JUHE_AI_J3B_BUSINESS_POSTGRES_URL":      "postgres://business",
		"JUHE_AI_J3B_CREDENTIAL_SECRET":          "credential",
		"JUHE_AI_J3B_IDENTITY_SECRET":            "identity",
		"JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED": "true",
		"JUHE_AI_J3B_NODE_WRITER_STOPPED":        "true",
		"JUHE_AI_J3B_OWNER_EPOCH":                "epoch-1",
	}
	if _, err := LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("confirmed Business handoff without cutover evidence path must fail closed")
	}
}

func TestVerifyConfiguredCutoverEvidenceBindsOwnerAndEpoch(t *testing.T) {
	dir := t.TempDir()
	backupPath := filepath.Join(dir, "backup.bin")
	backupData := []byte("backup")
	if err := os.WriteFile(backupPath, backupData, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(backupData)
	now := time.Now().UTC()
	manifestPath, manifestHash := writeValidConfiguredReadbackManifest(t, now)
	evidence := contracts.J3bCutoverEvidence{
		OldOwner: "node", NewOwner: contracts.J3bGatewayCutoverOwner, OwnerEpoch: "epoch-1", DrainCompleted: true,
		ActivePathZero: true, InFlight: 0, BlockedFindings: 0,
		BackupArtifact:       contracts.J3bBackupArtifact{Path: backupPath, Hash: hex.EncodeToString(digest[:])},
		RollbackReplayCursor: "cursor-1", SourceDigest: strings.Repeat("a", 64), TargetDigest: strings.Repeat("a", 64),
		ReadbackManifest: contracts.J3bReadbackManifestReference{
			Path: manifestPath, Hash: manifestHash, FormatVersion: contracts.J3bReadbackManifestFormatVersion,
			Scope: contracts.J3bReadbackManifestScope, SourceSnapshotIdentity: "snapshot-1",
			SourceSchema: "legacy-sqlite-dataset+stats", TargetSchema: "juhe-j3b-sqlite",
		},
		Freshness: contracts.J3bEvidenceFreshness{CapturedAt: now.Format(time.RFC3339), MaxAgeSeconds: 60},
	}
	path := filepath.Join(dir, "evidence.json")
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := VerifyConfiguredCutoverEvidence(path, "epoch-1", now)
	if err != nil || !report.Ready {
		t.Fatalf("valid configured evidence report=%+v err=%v", report, err)
	}
	report, err = VerifyConfiguredCutoverEvidence(path, "epoch-2", now)
	if err != nil || report.Ready || len(report.Errors) == 0 {
		t.Fatalf("epoch mismatch report=%+v err=%v", report, err)
	}
}

func writeValidConfiguredReadbackManifest(t *testing.T, now time.Time) (string, string) {
	t.Helper()
	manifest := contracts.J3bReadbackManifest{
		FormatVersion:          contracts.J3bReadbackManifestFormatVersion,
		Scope:                  contracts.J3bReadbackManifestScope,
		Producer:               "gateway-config-test",
		SourceSnapshotIdentity: "snapshot-1",
		SourceSchema:           "legacy-sqlite-dataset+stats",
		TargetSchema:           "juhe-j3b-sqlite",
		ProjectionComplete:     true,
		VerifiedAt:             now.Format(time.RFC3339),
	}
	for _, name := range []string{
		"account_quality_health_hourly",
		"model_check_items",
		"model_check_observations",
		"model_check_runs",
		"model_token_intercept_baseline_versions",
	} {
		manifest.Tables = append(manifest.Tables, contracts.J3bReadbackTableDigest{
			Name: name, SourceRows: 1, TargetRows: 1,
			SourceDigest: strings.Repeat("a", 64), TargetDigest: strings.Repeat("a", 64),
		})
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

func writeConfiguredEvidence(t *testing.T, evidence contracts.J3bCutoverEvidence) string {
	t.Helper()
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validConfiguredCutoverEvidence(t *testing.T) (contracts.J3bCutoverEvidence, time.Time) {
	t.Helper()
	dir := t.TempDir()
	backupData := []byte("backup")
	backupPath := filepath.Join(dir, "backup.bin")
	if err := os.WriteFile(backupPath, backupData, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(backupData)
	now := time.Now().UTC()
	manifestPath, manifestHash := writeValidConfiguredReadbackManifest(t, now)
	return contracts.J3bCutoverEvidence{
		OldOwner: "node", NewOwner: contracts.J3bGatewayCutoverOwner, OwnerEpoch: "epoch-1", DrainCompleted: true,
		ActivePathZero: true, InFlight: 0, BlockedFindings: 0,
		BackupArtifact:       contracts.J3bBackupArtifact{Path: backupPath, Hash: hex.EncodeToString(digest[:])},
		RollbackReplayCursor: "cursor-1", SourceDigest: strings.Repeat("a", 64), TargetDigest: strings.Repeat("a", 64),
		ReadbackManifest: contracts.J3bReadbackManifestReference{
			Path: manifestPath, Hash: manifestHash, FormatVersion: contracts.J3bReadbackManifestFormatVersion,
			Scope: contracts.J3bReadbackManifestScope, SourceSnapshotIdentity: "snapshot-1",
			SourceSchema: "legacy-sqlite-dataset+stats", TargetSchema: "juhe-j3b-sqlite",
		},
		Freshness: contracts.J3bEvidenceFreshness{CapturedAt: now.Format(time.RFC3339), MaxAgeSeconds: 60},
	}, now
}

func TestVerifyConfiguredCutoverEvidenceRejectsManifestFileHashMismatch(t *testing.T) {
	evidence, now := validConfiguredCutoverEvidence(t)
	evidence.ReadbackManifest.Hash = strings.Repeat("0", 64)
	report, err := VerifyConfiguredCutoverEvidence(writeConfiguredEvidence(t, evidence), "epoch-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatalf("bad manifest file hash unexpectedly ready: %+v", report)
	}
}

func TestVerifyConfiguredCutoverEvidenceRejectsManifestHashMismatch(t *testing.T) {
	evidence, now := validConfiguredCutoverEvidence(t)
	data, err := os.ReadFile(evidence.ReadbackManifest.Path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest contracts.J3bReadbackManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ManifestHash = strings.Repeat("0", 64)
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidence.ReadbackManifest.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	evidence.ReadbackManifest.Hash = hex.EncodeToString(digest[:])
	report, err := VerifyConfiguredCutoverEvidence(writeConfiguredEvidence(t, evidence), "epoch-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatalf("bad manifest hash unexpectedly ready: %+v", report)
	}
}

func TestVerifyConfiguredCutoverEvidenceRejectsManifestIdentityMismatch(t *testing.T) {
	evidence, now := validConfiguredCutoverEvidence(t)
	evidence.ReadbackManifest.SourceSnapshotIdentity = "different-snapshot"
	report, err := VerifyConfiguredCutoverEvidence(writeConfiguredEvidence(t, evidence), "epoch-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatalf("manifest identity mismatch unexpectedly ready: %+v", report)
	}
}

func TestRejectsCircuitRuntimeWithoutRedisOwnerConfig(t *testing.T) {
	values := map[string]string{
		"JUHE_AI_J3B_ENABLED": "true", "JUHE_AI_J3B_OWNER": "gateway", "JUHE_AI_J3B_INSTANCE_ID": "gw-1",
		"JUHE_AI_J3B_STORE": "postgres", "JUHE_AI_J3B_POSTGRES_URL": "postgres://j3b", "JUHE_AI_J3B_BUSINESS_POSTGRES_URL": "postgres://business",
		"JUHE_AI_J3B_CREDENTIAL_SECRET": "credential", "JUHE_AI_J3B_IDENTITY_SECRET": "identity", "JUHE_AI_J3B_BUSINESS_HANDOFF_CONFIRMED": "true",
		"JUHE_AI_J3B_NODE_WRITER_STOPPED": "true", "JUHE_AI_J3B_SCHEMA_READY": "true", "JUHE_AI_J3B_HEALTH_BOUNDARY_READY": "true", "JUHE_AI_J3B_RUNTIME_READY": "true",
	}
	if _, err := LoadConfig(func(key string) string { return values[key] }); err == nil {
		t.Fatal("circuit runtime without Redis owner config must fail closed")
	}
}
