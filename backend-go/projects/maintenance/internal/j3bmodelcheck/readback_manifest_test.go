package j3bmodelcheck

import (
	"strings"
	"testing"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

func completeReadbackEvidence() (map[string]string, map[string]int64, map[string]string) {
	tables := map[string]string{}
	rows := map[string]int64{}
	digests := map[string]string{}
	for _, name := range j3bReadbackRequiredTables() {
		tables[name] = "match"
		rows[name] = 1
		digests[name] = strings.Repeat("a", 64)
	}
	return tables, rows, digests
}

func TestNewSQLiteJ3bReadbackManifestRequiresCompleteLosslessReport(t *testing.T) {
	tables, rows, digests := completeReadbackEvidence()
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	report := BackfillVerificationReport{Complete: true, ProjectionComplete: true, Tables: tables, SourceRows: rows, TargetRows: rows, SourceDigest: digests, TargetDigest: digests}
	manifest, err := NewSQLiteJ3bReadbackManifest(report, J3bReadbackManifestOptions{SourceSnapshotIdentity: "sqlite-snapshot-1", VerifiedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if errors := contracts.ValidateJ3bReadbackManifest(manifest, now, 60); len(errors) != 0 {
		t.Fatalf("manifest errors=%v", errors)
	}
	report.Complete = false
	if _, err := NewSQLiteJ3bReadbackManifest(report, J3bReadbackManifestOptions{SourceSnapshotIdentity: "sqlite-snapshot-1", VerifiedAt: now}); err == nil {
		t.Fatal("incomplete SQLite readback unexpectedly converted")
	}
}

func TestNewPostgresJ3bReadbackManifestRejectsBoundedOrMissingEvidence(t *testing.T) {
	tables, rows, digests := completeReadbackEvidence()
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	report := PostgresBackfillVerificationReport{Ready: true, TransactionReadOnly: true, Tables: tables, SourceRows: rows, TargetRows: rows, SourceDigest: digests, TargetDigest: digests, SourceExceededRowLimit: map[string]bool{}, TargetExceededRowLimit: map[string]bool{}}
	manifest, err := NewPostgresJ3bReadbackManifest(report, J3bReadbackManifestOptions{SourceSnapshotIdentity: "postgres-snapshot-1", VerifiedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SourceSchema != "juhe_dataset+juhe_stats" || manifest.TargetSchema != SchemaName {
		t.Fatalf("unexpected schema binding: %+v", manifest)
	}
	report.SourceExceededRowLimit["model_check_runs"] = true
	if _, err := NewPostgresJ3bReadbackManifest(report, J3bReadbackManifestOptions{SourceSnapshotIdentity: "postgres-snapshot-1", VerifiedAt: now}); err == nil {
		t.Fatal("row-limited PostgreSQL report unexpectedly converted")
	}
}
