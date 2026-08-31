package j3bmodelcheck

import (
	"fmt"
	"sort"
	"strings"
	"time"

	contracts "github.com/huanminabc/juhe-ai/backend-go-contracts"
)

// J3bReadbackManifestOptions binds an observed report to the source snapshot
// identifier recorded by the maintenance operator. This is a file evidence
// binding, not an attestation that the named snapshot is recoverable.
type J3bReadbackManifestOptions struct {
	SourceSnapshotIdentity string
	VerifiedAt             time.Time
}

func NewSQLiteJ3bReadbackManifest(report BackfillVerificationReport, options J3bReadbackManifestOptions) (contracts.J3bReadbackManifest, error) {
	if !report.Complete || !report.ProjectionComplete {
		return contracts.J3bReadbackManifest{}, fmt.Errorf("SQLite readback report is not complete")
	}
	return newJ3bReadbackManifest(
		"j3bmodelcheck/sqlite-readback-v2",
		"legacy-sqlite-dataset+stats",
		"juhe-j3b-sqlite",
		report.Tables,
		report.SourceRows,
		report.TargetRows,
		report.SourceDigest,
		report.TargetDigest,
		options,
	)
}

func NewPostgresJ3bReadbackManifest(report PostgresBackfillVerificationReport, options J3bReadbackManifestOptions) (contracts.J3bReadbackManifest, error) {
	if !report.Ready || !report.TransactionReadOnly {
		return contracts.J3bReadbackManifest{}, fmt.Errorf("PostgreSQL readback report is not complete")
	}
	for _, table := range j3bReadbackRequiredTables() {
		if report.SourceExceededRowLimit[table] || report.TargetExceededRowLimit[table] {
			return contracts.J3bReadbackManifest{}, fmt.Errorf("PostgreSQL readback table %s exceeded row limit", table)
		}
	}
	return newJ3bReadbackManifest(
		"j3bmodelcheck/postgres-readback-v2",
		"juhe_dataset+juhe_stats",
		SchemaName,
		report.Tables,
		report.SourceRows,
		report.TargetRows,
		report.SourceDigest,
		report.TargetDigest,
		options,
	)
}

func newJ3bReadbackManifest(producer, sourceSchema, targetSchema string, statuses map[string]string, sourceRows, targetRows map[string]int64, sourceDigest, targetDigest map[string]string, options J3bReadbackManifestOptions) (contracts.J3bReadbackManifest, error) {
	if strings.TrimSpace(options.SourceSnapshotIdentity) == "" {
		return contracts.J3bReadbackManifest{}, fmt.Errorf("J3b readback manifest source snapshot identity is required")
	}
	if options.VerifiedAt.IsZero() {
		return contracts.J3bReadbackManifest{}, fmt.Errorf("J3b readback manifest verified time is required")
	}
	manifest := contracts.J3bReadbackManifest{
		FormatVersion:          contracts.J3bReadbackManifestFormatVersion,
		Scope:                  contracts.J3bReadbackManifestScope,
		Producer:               producer,
		SourceSnapshotIdentity: strings.TrimSpace(options.SourceSnapshotIdentity),
		SourceSchema:           sourceSchema,
		TargetSchema:           targetSchema,
		ProjectionComplete:     true,
		VerifiedAt:             options.VerifiedAt.UTC().Format(time.RFC3339),
		Tables:                 make([]contracts.J3bReadbackTableDigest, 0, len(j3bReadbackRequiredTables())),
	}
	for _, name := range j3bReadbackRequiredTables() {
		if statuses[name] != "match" {
			return contracts.J3bReadbackManifest{}, fmt.Errorf("J3b readback table %s is not a match", name)
		}
		sourceCount, sourceOK := sourceRows[name]
		targetCount, targetOK := targetRows[name]
		sourceHash, sourceDigestOK := sourceDigest[name]
		targetHash, targetDigestOK := targetDigest[name]
		if !sourceOK || !targetOK || !sourceDigestOK || !targetDigestOK {
			return contracts.J3bReadbackManifest{}, fmt.Errorf("J3b readback table %s has incomplete evidence", name)
		}
		manifest.Tables = append(manifest.Tables, contracts.J3bReadbackTableDigest{
			Name: name, SourceRows: sourceCount, TargetRows: targetCount, SourceDigest: sourceHash, TargetDigest: targetHash,
		})
	}
	sort.Slice(manifest.Tables, func(i, j int) bool { return manifest.Tables[i].Name < manifest.Tables[j].Name })
	hash, err := contracts.ComputeJ3bReadbackManifestHash(manifest)
	if err != nil {
		return contracts.J3bReadbackManifest{}, fmt.Errorf("hash J3b readback manifest: %w", err)
	}
	manifest.ManifestHash = hash
	if errors := contracts.ValidateJ3bReadbackManifest(manifest, options.VerifiedAt.UTC(), 1); len(errors) > 0 {
		return contracts.J3bReadbackManifest{}, fmt.Errorf("J3b readback manifest is invalid: %s", strings.Join(errors, "; "))
	}
	return manifest, nil
}

func j3bReadbackRequiredTables() []string {
	return []string{
		"account_quality_health_hourly",
		"model_check_items",
		"model_check_observations",
		"model_check_runs",
		"model_account_trust_results",
		"model_token_intercept_baseline_versions",
		"model_trust_aggregation_state",
		"model_trust_latest_dirty_accounts",
		"model_trust_observation_receipts",
	}
}
