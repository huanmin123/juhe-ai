package proxylatency

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	pgSmokeJobsURLEnv      = "J3A_PG_SMOKE_JOBS_URL"
	pgSmokeInputURLEnv     = "J3A_PG_SMOKE_INPUT_URL"
	pgProjectorURLEnv      = "J3A_PG_PROJECTOR_URL"
	pgSmokeRequiredEnv     = "J3A_PG_SMOKE_REQUIRED"
	pgSmokePrefixEnv       = "J3A_PG_SMOKE_DB_PREFIX"
	pgSchemaContractURLEnv = "J3A_PG_SCHEMA_CONTRACT_SMOKE_URL"
	pgSmokeDefaultDBPrefix = "juhe_ai_sub2api_dev_j3a_"
)

// TestPostgresSchemaContractSmoke proves the production runtime preflight is
// not merely a table-name check. It is opt-in and only allows an isolated
// direct PostgreSQL scratch database prepared with an empty juhe_jobs schema.
func TestPostgresSchemaContractSmoke(t *testing.T) {
	rawURL := strings.TrimSpace(os.Getenv(pgSchemaContractURLEnv))
	if rawURL == "" {
		t.Skipf("J3a PostgreSQL schema-contract smoke skipped: set %s for an isolated run", pgSchemaContractURLEnv)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Port() != "5432" || parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" || !strings.HasPrefix(strings.Trim(parsed.Path, "/"), pgSmokeDefaultDBPrefix) {
		t.Fatalf("J3a schema-contract smoke only accepts explicit-role direct 5432 scratch PostgreSQL URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: rawURL})
	if err != nil {
		t.Fatalf("open J3a schema-contract PostgreSQL failed: %s", redactPGError(err, rawURL))
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close J3a schema-contract PostgreSQL failed: %s", redactPGError(err, rawURL))
		}
	})
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("explicit J3a schema-contract bootstrap failed: %s", redactPGError(err, rawURL))
	}
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("complete J3a schema-contract preflight failed: %s", redactPGError(err, rawURL))
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE juhe_jobs.proxy_latency_owner_leases ALTER COLUMN fence_token TYPE TEXT USING fence_token::TEXT`); err != nil {
		t.Fatalf("prepare J3a malformed schema-contract fixture failed: %s", redactPGError(err, rawURL))
	}
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "proxy_latency_owner_leases.fence_token") {
		t.Fatalf("J3a runtime schema preflight must reject malformed table, err=%s", redactPGError(err, rawURL))
	}
}

// TestPostgresResultProjectorSmoke is the explicit Go business-result gate.
// It is opt-in because it must use a disposable PgBouncer database and a
// result-role URL that can access juhe_business.  The generic Node CRUD smoke
// intentionally does not exercise this path after the Node writer removal.
func TestPostgresResultProjectorSmoke(t *testing.T) {
	jobsURL := strings.TrimSpace(os.Getenv(pgSmokeJobsURLEnv))
	resultURL := strings.TrimSpace(os.Getenv(pgProjectorURLEnv))
	required, err := parseSmokeRequired(os.Getenv(pgSmokeRequiredEnv))
	if err != nil {
		t.Fatalf("J3a projector PostgreSQL smoke 配置错误: %s", err)
	}
	if jobsURL == "" && resultURL == "" && !required {
		t.Skipf("J3a projector PostgreSQL smoke skipped: set %s, %s and %s=1 for an isolated required run", pgSmokeJobsURLEnv, pgProjectorURLEnv, pgSmokeRequiredEnv)
	}
	if jobsURL == "" || resultURL == "" {
		t.Fatalf("J3a projector PostgreSQL smoke requires both %s and %s", pgSmokeJobsURLEnv, pgProjectorURLEnv)
	}
	jobsDatabase, err := validateProjectorURL(jobsURL)
	if err != nil {
		t.Fatalf("J3a projector jobs URL preflight failed: %s", redactPGError(err, jobsURL))
	}
	resultDatabase, err := validateProjectorURL(resultURL)
	if err != nil {
		t.Fatalf("J3a projector result URL preflight failed: %s", redactPGError(err, resultURL))
	}
	if jobsDatabase != resultDatabase {
		t.Fatalf("J3a projector jobs/result URL 必须指向同一 scratch database: jobs=%q result=%q", jobsDatabase, resultDatabase)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: jobsURL})
	if err != nil {
		t.Fatalf("open J3a projector jobs PostgreSQL failed: %s", redactPGError(err, jobsURL))
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close J3a projector jobs PostgreSQL failed: %s", redactPGError(err, jobsURL))
		}
	})
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("J3a projector jobs EnsureSchema failed: %s", redactPGError(err, jobsURL))
	}
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("J3a projector jobs CheckSchema failed after explicit bootstrap: %s", redactPGError(err, jobsURL))
	}
	business, err := sql.Open("pgx", resultURL)
	if err != nil {
		t.Fatalf("open J3a projector result PostgreSQL failed: %s", redactPGError(err, resultURL))
	}
	t.Cleanup(func() {
		if err := business.Close(); err != nil {
			t.Errorf("close J3a projector result PostgreSQL failed: %s", redactPGError(err, resultURL))
		}
	})
	business.SetMaxOpenConns(2)
	business.SetMaxIdleConns(1)
	if err := business.PingContext(ctx); err != nil {
		t.Fatalf("ping J3a projector result PostgreSQL failed: %s", redactPGError(err, resultURL))
	}
	projector, err := NewResultProjector(store, business, ResultProjectorConfig{ConsumerKey: "j3a-pg-projector-smoke", PollInterval: time.Second, BatchSize: 10}, nil)
	if err != nil {
		t.Fatalf("create J3a result projector failed: %s", err)
	}
	if err := projector.CheckContract(ctx); err != nil {
		t.Fatalf("J3a result projector CheckContract failed: %s", redactPGError(err, resultURL))
	}

	var systemAccountID string
	if err := business.QueryRowContext(ctx, `SELECT id FROM juhe_business.system_accounts ORDER BY id LIMIT 1`).Scan(&systemAccountID); err != nil {
		t.Fatalf("J3a projector business fixture requires a system account: %s", redactPGError(err, resultURL))
	}
	ownerID := fmt.Sprintf("j3a-projector-%d", time.Now().UTC().UnixNano())
	proxyID := ownerID + "-proxy"
	noTargetProxyID := ownerID + "-no-target"
	outcomeIDs := make([]string, 0, 1)
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err = business.ExecContext(ctx, `INSERT INTO juhe_business.proxy_profiles (id,system_account_id,name,type,host,port,enabled,test_status,created_at,updated_at) VALUES ($1,$2,$3,'http','127.0.0.1',65535,TRUE,'unknown',$4,$4)`, proxyID, systemAccountID, ownerID, now)
	if err != nil {
		t.Fatalf("insert J3a projector business fixture failed: %s", redactPGError(err, resultURL))
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for _, outcomeID := range outcomeIDs {
			if _, err := business.ExecContext(cleanupCtx, `DELETE FROM juhe_business.proxy_latency_projection_receipts WHERE outcome_id=$1`, outcomeID); err != nil {
				t.Errorf("J3a projector cleanup receipt %q failed: %s", outcomeID, redactPGError(err, resultURL))
			}
		}
		if _, err := business.ExecContext(cleanupCtx, `DELETE FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1`, "j3a-pg-projector-smoke"); err != nil {
			t.Errorf("J3a projector cleanup cursor failed: %s", redactPGError(err, resultURL))
		}
		for _, fixtureID := range []string{proxyID, noTargetProxyID} {
			if _, err := business.ExecContext(cleanupCtx, `DELETE FROM juhe_business.proxy_profiles WHERE id=$1`, fixtureID); err != nil {
				t.Errorf("J3a projector cleanup proxy %q failed: %s", fixtureID, redactPGError(err, resultURL))
			}
		}
		for _, outcomeID := range outcomeIDs {
			var remaining int
			if err := business.QueryRowContext(cleanupCtx, `SELECT count(*) FROM juhe_business.proxy_latency_projection_receipts WHERE outcome_id=$1`, outcomeID).Scan(&remaining); err != nil {
				t.Errorf("J3a projector cleanup receipt %q verification failed: %s", outcomeID, redactPGError(err, resultURL))
			} else if remaining != 0 {
				t.Errorf("J3a projector cleanup receipt %q left %d rows", outcomeID, remaining)
			}
		}
		var cursorRemaining, proxyRemaining int
		if err := business.QueryRowContext(cleanupCtx, `SELECT count(*) FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1`, "j3a-pg-projector-smoke").Scan(&cursorRemaining); err != nil {
			t.Errorf("J3a projector cleanup cursor verification failed: %s", redactPGError(err, resultURL))
		} else if cursorRemaining != 0 {
			t.Errorf("J3a projector cleanup cursor left %d rows", cursorRemaining)
		}
		for _, fixtureID := range []string{proxyID, noTargetProxyID} {
			if err := business.QueryRowContext(cleanupCtx, `SELECT count(*) FROM juhe_business.proxy_profiles WHERE id=$1`, fixtureID).Scan(&proxyRemaining); err != nil {
				t.Errorf("J3a projector cleanup proxy %q verification failed: %s", fixtureID, redactPGError(err, resultURL))
			} else if proxyRemaining != 0 {
				t.Errorf("J3a projector cleanup proxy %q left %d rows", fixtureID, proxyRemaining)
			}
		}
	})

	owner, acquired, err := store.AcquireOwnerLease(ctx, ownerID, 45*time.Second)
	if err != nil || !acquired {
		t.Fatalf("acquire J3a projector owner lease failed: acquired=%t err=%s", acquired, redactPGError(err, jobsURL))
	}
	var proxyLease ProxyLease
	proxyLeaseAcquired := false
	cleanupProjectorSmokeRows(t, store, owner, &proxyLease, &proxyLeaseAcquired, proxyID, &outcomeIDs)
	draft := InputDraft{ProxyID: proxyID, ConfigRevision: now.Format(time.RFC3339Nano), Trigger: TriggerManual, IssuedAt: now, ExpiresAt: now.Add(time.Minute), PolicyVersion: proxyLatencyInputPolicyVersion, ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 65535, Targets: []Target{{Provider: "smoke", ProfileID: "scratch", URL: "https://example.invalid/"}}}
	issued, err := store.IssueInput(ctx, draft)
	if err != nil {
		t.Fatalf("issue J3a projector input failed: %s", redactPGError(err, jobsURL))
	}
	proxyLease, acquired, err = store.AcquireProxyLease(ctx, owner, proxyID, 30*time.Second)
	if err != nil || !acquired {
		t.Fatalf("acquire J3a projector proxy lease failed: acquired=%t err=%s", acquired, redactPGError(err, jobsURL))
	}
	proxyLeaseAcquired = true
	issuedSnapshot, claimToken, _, err := store.AdmitExecution(ctx, owner, proxyLease, issued)
	if err != nil || claimToken == "" {
		t.Fatalf("admit J3a projector execution failed: claim=%q err=%s", claimToken, redactPGError(err, jobsURL))
	}
	outcome := Outcome{OutcomeID: stableOutcomeID(issuedSnapshot.RequestID), RequestID: issuedSnapshot.RequestID, ProxyID: proxyID, ObservedAt: now.Add(time.Second), InputVersion: issuedSnapshot.InputVersion, ConfigRevision: issuedSnapshot.ConfigRevision, Trigger: TriggerManual, OwnerFenceToken: owner.FenceToken, ProxyFenceToken: proxyLease.FenceToken, OverallStatus: OverallPassed, Items: []ItemResult{{Provider: "smoke", ProfileID: "scratch", Status: ItemPassed, Outcome: OutcomeSuccess, HTTPStatus: 200}}, executionClaimToken: claimToken}
	inserted, err := store.AppendOutcome(ctx, owner, proxyLease, outcome)
	if err != nil || !inserted {
		t.Fatalf("append J3a projector outcome failed: inserted=%t err=%s", inserted, redactPGError(err, jobsURL))
	}
	outcomeIDs = append(outcomeIDs, outcome.OutcomeID)
	result, err := projector.ProjectOutcome(ctx, outcome)
	if err != nil || result.Disposition != ProjectionApplied || !result.Changed {
		t.Fatalf("J3a projector applied projection failed: disposition=%s changed=%t err=%s", result.Disposition, result.Changed, redactPGError(err, resultURL))
	}
	var status string
	var lastTested time.Time
	if err := business.QueryRowContext(ctx, `SELECT test_status,last_tested_at FROM juhe_business.proxy_profiles WHERE id=$1`, proxyID).Scan(&status, &lastTested); err != nil {
		t.Fatalf("read J3a projector applied proxy state failed: %s", redactPGError(err, resultURL))
	}
	if status != string(OverallPassed) || !lastTested.Equal(outcome.ObservedAt) {
		t.Fatalf("J3a projector applied proxy state mismatch: status=%q last_tested=%s", status, lastTested.UTC().Format(time.RFC3339Nano))
	}
	var disposition string
	if err := business.QueryRowContext(ctx, `SELECT disposition FROM juhe_business.proxy_latency_projection_receipts WHERE outcome_id=$1`, outcome.OutcomeID).Scan(&disposition); err != nil || disposition != string(ProjectionApplied) {
		t.Fatalf("J3a projector receipt mismatch: disposition=%q err=%s", disposition, redactPGError(err, resultURL))
	}
	if _, err := projector.Drain(ctx); err != nil {
		t.Fatalf("J3a projector cursor drain failed: %s", redactPGError(err, resultURL))
	}
	var cursorOutcome string
	if err := business.QueryRowContext(ctx, `SELECT outcome_id FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1`, "j3a-pg-projector-smoke").Scan(&cursorOutcome); err != nil || cursorOutcome != outcome.OutcomeID {
		t.Fatalf("J3a projector cursor mismatch: outcome=%q err=%s", cursorOutcome, redactPGError(err, resultURL))
	}
	if replay, err := projector.ProjectOutcome(ctx, outcome); err != nil || replay.Disposition != ProjectionApplied || replay.Changed {
		t.Fatalf("J3a projector receipt replay mismatch: disposition=%s changed=%t err=%s", replay.Disposition, replay.Changed, redactPGError(err, resultURL))
	}
	if err := projector.ProjectManualOutbound(ctx, outcome, "198.51.100.10", "smoke"); err != nil {
		t.Fatalf("J3a projector outbound CAS failed: %s", redactPGError(err, resultURL))
	}
	var outboundIP string
	if err := business.QueryRowContext(ctx, `SELECT outbound_ip FROM juhe_business.proxy_profiles WHERE id=$1`, proxyID).Scan(&outboundIP); err != nil || outboundIP != "198.51.100.10" {
		t.Fatalf("J3a projector outbound state mismatch: ip=%q err=%s", outboundIP, redactPGError(err, resultURL))
	}
	if _, err := business.ExecContext(ctx, `UPDATE juhe_business.proxy_profiles SET updated_at=updated_at + INTERVAL '1 microsecond' WHERE id=$1`, proxyID); err != nil {
		t.Fatalf("advance J3a projector config revision failed: %s", redactPGError(err, resultURL))
	}
	snapshotManualDurableState := func() (int, int, string, bool) {
		var receiptCount, cursorCount int
		if err := business.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.proxy_latency_projection_receipts`).Scan(&receiptCount); err != nil {
			t.Fatalf("snapshot J3a projector manual receipts failed: %s", redactPGError(err, resultURL))
		}
		if err := business.QueryRowContext(ctx, `SELECT count(*) FROM juhe_business.proxy_latency_projection_cursors`).Scan(&cursorCount); err != nil {
			t.Fatalf("snapshot J3a projector manual cursors failed: %s", redactPGError(err, resultURL))
		}
		var cursorOutcome sql.NullString
		err := business.QueryRowContext(ctx, `SELECT outcome_id FROM juhe_business.proxy_latency_projection_cursors WHERE consumer_key=$1`, "j3a-pg-projector-smoke").Scan(&cursorOutcome)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("snapshot J3a projector manual cursor value failed: %s", redactPGError(err, resultURL))
		}
		return receiptCount, cursorCount, cursorOutcome.String, cursorOutcome.Valid
	}
	projectManualWithoutDurableMutation := func(label string, invoke func() (ProjectionResult, error)) ProjectionResult {
		beforeReceipts, beforeCursors, beforeCursorOutcome, beforeCursorValid := snapshotManualDurableState()
		result, err := invoke()
		if err != nil {
			t.Fatalf("J3a projector %s manual projection failed: %s", label, redactPGError(err, resultURL))
		}
		afterReceipts, afterCursors, afterCursorOutcome, afterCursorValid := snapshotManualDurableState()
		if beforeReceipts != afterReceipts || beforeCursors != afterCursors || beforeCursorOutcome != afterCursorOutcome || beforeCursorValid != afterCursorValid {
			t.Fatalf("J3a projector %s manual projection changed durable receipt/cursor state: before=(receipts=%d cursors=%d cursor=%q valid=%t) after=(receipts=%d cursors=%d cursor=%q valid=%t)", label, beforeReceipts, beforeCursors, beforeCursorOutcome, beforeCursorValid, afterReceipts, afterCursors, afterCursorOutcome, afterCursorValid)
		}
		return result
	}
	staleRequest := ManualRequest{SchemaVersion: 1, ProxyID: proxyID, ProxyName: ownerID, ConfigRevision: now.Format(time.RFC3339Nano), ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 65535}
	stale := projectManualWithoutDurableMutation("stale", func() (ProjectionResult, error) {
		return projector.ProjectManualNoTargets(ctx, staleRequest, now.Add(2*time.Second))
	})
	if stale.Disposition != ProjectionStale {
		t.Fatalf("J3a projector stale fence failed: disposition=%s", stale.Disposition)
	}
	if _, err := business.ExecContext(ctx, `INSERT INTO juhe_business.proxy_profiles (id,system_account_id,name,type,host,port,enabled,test_status,created_at,updated_at) VALUES ($1,$2,$3,'http','127.0.0.1',65535,TRUE,'unknown',$4,$4)`, noTargetProxyID, systemAccountID, noTargetProxyID, now); err != nil {
		t.Fatalf("insert J3a no-target fixture failed: %s", redactPGError(err, resultURL))
	}
	noTargetRequest := ManualRequest{SchemaVersion: 1, ProxyID: noTargetProxyID, ProxyName: noTargetProxyID, ConfigRevision: now.Format(time.RFC3339Nano), ProxyType: "http", ProxyHost: "127.0.0.1", ProxyPort: 65535}
	noTarget := projectManualWithoutDurableMutation("no-target", func() (ProjectionResult, error) {
		return projector.ProjectManualNoTargets(ctx, noTargetRequest, now.Add(time.Second))
	})
	if noTarget.Disposition != ProjectionApplied || !noTarget.Changed {
		t.Fatalf("J3a projector no-target CAS failed: disposition=%s changed=%t", noTarget.Disposition, noTarget.Changed)
	}
	if _, err := business.ExecContext(ctx, `DELETE FROM juhe_business.proxy_profiles WHERE id=$1`, noTargetProxyID); err != nil {
		t.Fatalf("delete J3a no-target fixture failed: %s", redactPGError(err, resultURL))
	}
	deleted := projectManualWithoutDurableMutation("deleted", func() (ProjectionResult, error) {
		return projector.ProjectManualNoTargets(ctx, noTargetRequest, now.Add(2*time.Second))
	})
	if deleted.Disposition != ProjectionIgnored || deleted.Reason != "proxy_missing_or_deleted" {
		t.Fatalf("J3a projector deleted fence failed: disposition=%s reason=%q", deleted.Disposition, deleted.Reason)
	}
}

func validateProjectorURL(raw string) (string, error) {
	return validatePgBouncerURL(raw)
}

// TestPostgresSmoke is deliberately opt-in. It must only be run against a
// disposable child database whose application URLs terminate at PgBouncer.
// The default local test command therefore reports an explicit SKIP rather
// than pretending SQLite coverage is PostgreSQL evidence.
func TestPostgresSmoke(t *testing.T) {
	jobsURL := strings.TrimSpace(os.Getenv(pgSmokeJobsURLEnv))
	inputURL := strings.TrimSpace(os.Getenv(pgSmokeInputURLEnv))
	required, requiredErr := parseSmokeRequired(os.Getenv(pgSmokeRequiredEnv))
	if requiredErr != nil {
		t.Fatalf("J3a PostgreSQL smoke 配置错误: %s", requiredErr)
	}
	if jobsURL == "" && inputURL == "" && !required {
		t.Skipf("J3a PostgreSQL smoke skipped: set %s, %s and %s=1 for an isolated required run", pgSmokeJobsURLEnv, pgSmokeInputURLEnv, pgSmokeRequiredEnv)
	}
	if jobsURL == "" || inputURL == "" {
		t.Fatalf("J3a PostgreSQL smoke requires both %s and %s when enabled", pgSmokeJobsURLEnv, pgSmokeInputEnvName())
	}

	dbPrefix := strings.TrimSpace(os.Getenv(pgSmokePrefixEnv))
	if dbPrefix == "" {
		dbPrefix = pgSmokeDefaultDBPrefix
	}
	if !strings.HasPrefix(dbPrefix, pgSmokeDefaultDBPrefix) {
		t.Fatalf("J3a PostgreSQL smoke database prefix 必须以固定 scratch 前缀 %q 开头", pgSmokeDefaultDBPrefix)
	}
	jobsDatabase, err := validatePgBouncerURL(jobsURL)
	if err != nil {
		t.Fatalf("J3a jobs URL preflight failed: %s", redactPGError(err, jobsURL))
	}
	inputDatabase, err := validatePgBouncerURL(inputURL)
	if err != nil {
		t.Fatalf("J3a input URL preflight failed: %s", redactPGError(err, inputURL))
	}
	if jobsDatabase != inputDatabase {
		t.Fatalf("J3a jobs/input URL 必须指向同一 scratch database: jobs=%q input=%q", jobsDatabase, inputDatabase)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := OpenStore(StoreConfig{Mode: StorePostgres, PostgresURL: jobsURL})
	if err != nil {
		t.Fatalf("open J3a jobs PostgreSQL failed: %s", redactPGError(err, jobsURL))
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close J3a jobs PostgreSQL failed: %s", redactPGError(err, jobsURL))
		}
	})
	if err := store.db.PingContext(ctx); err != nil {
		t.Fatalf("ping J3a jobs PostgreSQL failed: %s", redactPGError(err, jobsURL))
	}
	assertPGChildDatabase(t, ctx, store.db, dbPrefix, "jobs", jobsURL)
	assertJobsSchemaOwner(t, ctx, store.db, jobsURL)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("J3a jobs EnsureSchema failed: %s", redactPGError(err, jobsURL))
	}
	if err := store.CheckSchema(ctx); err != nil {
		t.Fatalf("J3a jobs CheckSchema failed after explicit bootstrap: %s", redactPGError(err, jobsURL))
	}
	assertJobsRoleCannotWriteBusiness(t, ctx, store.db, jobsURL)

	inputDB, err := sql.Open("pgx", inputURL)
	if err != nil {
		t.Fatalf("open J3a input PostgreSQL failed: %s", redactPGError(err, inputURL))
	}
	t.Cleanup(func() {
		if err := inputDB.Close(); err != nil {
			t.Errorf("close J3a input PostgreSQL failed: %s", redactPGError(err, inputURL))
		}
	})
	inputDB.SetMaxOpenConns(2)
	inputDB.SetMaxIdleConns(1)
	if err := inputDB.PingContext(ctx); err != nil {
		t.Fatalf("ping J3a input PostgreSQL failed: %s", redactPGError(err, inputURL))
	}
	assertPGChildDatabase(t, ctx, inputDB, dbPrefix, "input", inputURL)
	assertReaderCannotAccessJobs(t, ctx, inputDB, inputURL)
	assertReaderCannotWriteBusiness(t, ctx, inputDB, inputURL)
	reader, err := NewPostgresDirectInputReader(inputDB, time.Minute, time.Now)
	if err != nil {
		t.Fatalf("create J3a direct input reader failed: %s", redactPGError(err, inputURL))
	}
	if err := reader.CheckContract(ctx); err != nil {
		t.Fatalf("J3a direct input CheckContract failed: %s", redactPGError(err, inputURL))
	}
	assertReaderTransactionSettings(t, reader, ctx, inputURL)
	drafts, err := reader.LoadDue(ctx, 1)
	if err != nil {
		t.Fatalf("J3a direct input LoadDue failed: %s", redactPGError(err, inputURL))
	}
	if len(drafts) != 1 || drafts[0].ProxyID == "" || len(drafts[0].Targets) == 0 {
		t.Fatalf("J3a direct input fixture 未返回有效 enabled proxy/target")
	}
	var validTargets, invalidTargets int
	for _, target := range drafts[0].Targets {
		if target.ProbeError == targetProbeErrorInvalidURL {
			if target.URL != "" {
				t.Fatalf("J3a invalid target must not retain source URL: %+v", target)
			}
			invalidTargets++
			continue
		}
		if target.URL != "" {
			validTargets++
		}
	}
	if validTargets == 0 || invalidTargets == 0 {
		t.Fatalf("J3a direct input fixture must retain both valid and invalid enabled-provider targets: valid=%d invalid=%d targets=%+v", validTargets, invalidTargets, drafts[0].Targets)
	}

	ownerID := fmt.Sprintf("j3a-pg-smoke-%d", time.Now().UTC().UnixNano())
	owner, acquired, err := store.AcquireOwnerLease(ctx, ownerID, 45*time.Second)
	if err != nil || !acquired {
		t.Fatalf("acquire isolated J3a owner lease failed: acquired=%t err=%s", acquired, redactPGError(err, jobsURL))
	}
	proxyID := ownerID + "-proxy"
	cleanupSmokeRows(t, store, owner, proxyID)

	now := time.Now().UTC().Truncate(time.Microsecond)
	draft := InputDraft{
		ProxyID:        proxyID,
		ConfigRevision: now.Format(time.RFC3339Nano),
		Trigger:        TriggerManual,
		IssuedAt:       now,
		ExpiresAt:      now.Add(time.Minute),
		PolicyVersion:  proxyLatencyInputPolicyVersion,
		ProxyType:      "http",
		ProxyHost:      "127.0.0.1",
		ProxyPort:      65535,
		Targets:        []Target{{Provider: "smoke", ProfileID: "scratch", URL: "https://example.invalid/"}},
	}
	issued, err := store.IssueInput(ctx, draft)
	if err != nil {
		t.Fatalf("issue J3a PostgreSQL input failed: %s", redactPGError(err, jobsURL))
	}
	proxy, acquired, err := store.AcquireProxyLease(ctx, owner, proxyID, 30*time.Second)
	if err != nil || !acquired {
		t.Fatalf("acquire isolated J3a proxy lease failed: acquired=%t err=%s", acquired, redactPGError(err, jobsURL))
	}
	issuedSnapshot, claimToken, existing, err := store.AdmitExecution(ctx, owner, proxy, issued)
	if err != nil || existing != nil || claimToken == "" {
		t.Fatalf("admit J3a execution failed: claim=%q existing=%t err=%s", claimToken, existing != nil, redactPGError(err, jobsURL))
	}
	if issuedSnapshot.RequestID != issued.RequestID {
		t.Fatalf("admitted J3a snapshot request mismatch: got=%s want=%s", issuedSnapshot.RequestID, issued.RequestID)
	}

	outcome := Outcome{
		OutcomeID:           stableOutcomeID(issued.RequestID),
		RequestID:           issued.RequestID,
		ProxyID:             issued.ProxyID,
		ObservedAt:          now.Add(time.Second),
		InputVersion:        issued.InputVersion,
		ConfigRevision:      issued.ConfigRevision,
		Trigger:             issued.Trigger,
		OwnerFenceToken:     owner.FenceToken,
		ProxyFenceToken:     proxy.FenceToken,
		OverallStatus:       OverallPassed,
		Items:               []ItemResult{{Provider: "smoke", ProfileID: "scratch", Status: ItemPassed, Outcome: OutcomeSuccess, HTTPStatus: 200}},
		executionClaimToken: claimToken,
	}
	inserted, err := store.AppendOutcome(ctx, owner, proxy, outcome)
	if err != nil || !inserted {
		t.Fatalf("append J3a PostgreSQL outcome failed: inserted=%t err=%s", inserted, redactPGError(err, jobsURL))
	}
	loaded, found, err := store.LoadCommittedOutcome(ctx, issued)
	if err != nil || !found || loaded.RequestID != outcome.RequestID {
		t.Fatalf("load J3a PostgreSQL committed outcome failed: found=%t err=%s", found, redactPGError(err, jobsURL))
	}
	// Replay must be idempotent even after the first execution claim was
	// consumed. It must not require a second upstream attempt or a new claim.
	outcome.executionClaimToken = ""
	replayed, err := store.AppendOutcome(ctx, owner, proxy, outcome)
	if err != nil || replayed {
		t.Fatalf("replay J3a PostgreSQL outcome was not idempotent: replayed=%t err=%s", replayed, redactPGError(err, jobsURL))
	}
	poisoned := outcome
	poisoned.Items = append([]ItemResult(nil), outcome.Items...)
	poisoned.Items[0].HTTPStatus = 201
	if _, err := store.AppendOutcome(ctx, owner, proxy, poisoned); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("poisoned J3a PostgreSQL replay was not rejected: err=%s", redactPGError(err, jobsURL))
	}
	if err := store.ReleaseProxyLease(ctx, proxy); err != nil {
		t.Fatalf("release isolated J3a proxy lease failed: %s", redactPGError(err, jobsURL))
	}
	if err := store.ReleaseOwnerLease(ctx, owner); err != nil {
		t.Fatalf("release isolated J3a owner lease failed: %s", redactPGError(err, jobsURL))
	}
	successorID := ownerID + "-successor"
	successor, acquired, err := store.AcquireOwnerLease(ctx, successorID, 45*time.Second)
	if err != nil || !acquired {
		t.Fatalf("acquire successor J3a owner lease failed: acquired=%t err=%s", acquired, redactPGError(err, jobsURL))
	}
	successorProxy, acquired, err := store.AcquireProxyLease(ctx, successor, proxyID, 30*time.Second)
	if err != nil || !acquired {
		t.Fatalf("acquire successor J3a proxy lease failed: acquired=%t err=%s", acquired, redactPGError(err, jobsURL))
	}
	if replayed, err := store.AppendOutcome(ctx, successor, successorProxy, outcome); err != nil || replayed {
		t.Fatalf("successor J3a PostgreSQL replay was not idempotent: replayed=%t err=%s", replayed, redactPGError(err, jobsURL))
	}
	if err := store.ReleaseProxyLease(ctx, successorProxy); err != nil {
		t.Fatalf("release successor J3a proxy lease failed: %s", redactPGError(err, jobsURL))
	}
	if err := store.ReleaseOwnerLease(ctx, successor); err != nil {
		t.Fatalf("release successor J3a owner lease failed: %s", redactPGError(err, jobsURL))
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE juhe_jobs.proxy_latency_owner_leases ALTER COLUMN fence_token TYPE TEXT USING fence_token::TEXT`); err != nil {
		t.Fatalf("prepare malformed J3a PostgreSQL schema check failed: %s", redactPGError(err, jobsURL))
	}
	if err := store.CheckSchema(ctx); err == nil || !strings.Contains(err.Error(), "proxy_latency_owner_leases.fence_token") {
		t.Fatalf("J3a jobs runtime must fail closed on malformed PostgreSQL schema, err=%s", redactPGError(err, jobsURL))
	}
}

func pgSmokeInputEnvName() string { return pgSmokeInputURLEnv }

func parseSmokeRequired(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "required":
		return true, nil
	case "", "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s 只能是 1/true/yes/on/required 或 0/false/no/off", pgSmokeRequiredEnv)
	}
}

func validatePgBouncerURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return "", fmt.Errorf("必须是 postgres/postgresql URL")
	}
	if parsed.Hostname() == "" || parsed.Port() != "6432" {
		return "", fmt.Errorf("URL 必须通过 PgBouncer 6432，且包含 host")
	}
	if parsed.User == nil || strings.TrimSpace(parsed.User.Username()) == "" {
		return "", fmt.Errorf("URL 必须包含显式应用角色")
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	if database == "" || !strings.HasPrefix(database, pgSmokeDefaultDBPrefix) || database == pgSmokeDefaultDBPrefix {
		return "", fmt.Errorf("URL database 必须是固定 scratch 前缀 %q 加隔离后缀", pgSmokeDefaultDBPrefix)
	}
	return database, nil
}

func assertPGChildDatabase(t *testing.T, ctx context.Context, db *sql.DB, prefix, label, rawURL string) {
	t.Helper()
	var database, role string
	if err := db.QueryRowContext(ctx, `SELECT current_database(), current_user`).Scan(&database, &role); err != nil {
		t.Fatalf("J3a %s PG identity preflight failed: %s", label, redactPGError(err, rawURL))
	}
	if !strings.HasPrefix(database, prefix) {
		t.Fatalf("J3a %s PG database %q is outside required scratch prefix %q", label, database, prefix)
	}
	if strings.TrimSpace(role) == "" {
		t.Fatalf("J3a %s PG role is empty", label)
	}
}

func assertJobsSchemaOwner(t *testing.T, ctx context.Context, db *sql.DB, rawURL string) {
	t.Helper()
	var owner, current string
	err := db.QueryRowContext(ctx, `SELECT pg_get_userbyid(nspowner), current_user FROM pg_namespace WHERE nspname='juhe_jobs'`).Scan(&owner, &current)
	if err != nil {
		t.Fatalf("J3a jobs schema owner preflight failed: %s", redactPGError(err, rawURL))
	}
	if owner != current {
		t.Fatalf("J3a jobs schema owner mismatch: owner=%q current=%q", owner, current)
	}
}

func assertReaderCannotAccessJobs(t *testing.T, ctx context.Context, db *sql.DB, rawURL string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `SELECT 1 FROM juhe_jobs.proxy_latency_inputs LIMIT 1`); err == nil {
		t.Fatalf("J3a business reader unexpectedly can access juhe_jobs")
	}
}

func assertReaderCannotWriteBusiness(t *testing.T, ctx context.Context, db *sql.DB, rawURL string) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin reader permission probe failed: %s", redactPGError(err, rawURL))
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE juhe_business.proxy_profiles SET updated_at=updated_at WHERE FALSE`); err == nil {
		t.Fatal("J3a business reader unexpectedly can write proxy_profiles")
	}
}

func assertJobsRoleCannotWriteBusiness(t *testing.T, ctx context.Context, db *sql.DB, rawURL string) {
	t.Helper()
	var dbCreate, schemaCreate, schemaUsage bool
	if err := db.QueryRowContext(ctx, `SELECT has_database_privilege(current_user,current_database(),'CREATE'), has_schema_privilege(current_user,'juhe_business','CREATE'), has_schema_privilege(current_user,'juhe_business','USAGE')`).Scan(&dbCreate, &schemaCreate, &schemaUsage); err != nil {
		t.Fatalf("J3a jobs privilege preflight failed: %s", redactPGError(err, rawURL))
	}
	if dbCreate || schemaCreate || schemaUsage {
		t.Fatalf("J3a jobs role business/database privilege too broad: database_create=%t schema_create=%t schema_usage=%t", dbCreate, schemaCreate, schemaUsage)
	}
	if _, err := db.ExecContext(ctx, `UPDATE juhe_business.proxy_profiles SET updated_at=updated_at WHERE FALSE`); err == nil {
		t.Fatal("J3a jobs role unexpectedly can write business proxy_profiles")
	}
	if _, err := db.ExecContext(ctx, `SELECT 1 FROM juhe_business.proxy_profiles LIMIT 1`); err == nil {
		t.Fatal("J3a jobs role unexpectedly can read business proxy_profiles")
	}
}

func assertReaderTransactionSettings(t *testing.T, reader *PostgresDirectInputReader, ctx context.Context, rawURL string) {
	t.Helper()
	tx, err := reader.beginReadOnly(ctx)
	if err != nil {
		t.Fatalf("J3a reader transaction setup failed: %s", redactPGError(err, rawURL))
	}
	defer tx.Rollback()
	var isolation, readOnly, statementTimeout, lockTimeout string
	if err := tx.QueryRowContext(ctx, `SELECT current_setting('transaction_isolation'), current_setting('transaction_read_only'), current_setting('statement_timeout'), current_setting('lock_timeout')`).Scan(&isolation, &readOnly, &statementTimeout, &lockTimeout); err != nil {
		t.Fatalf("J3a reader transaction settings query failed: %s", redactPGError(err, rawURL))
	}
	if isolation != "repeatable read" || readOnly != "on" || statementTimeout != proxyLatencyStatementTimeout || lockTimeout != proxyLatencyLockTimeout {
		t.Fatalf("J3a reader transaction settings mismatch: isolation=%q read_only=%q statement_timeout=%q lock_timeout=%q", isolation, readOnly, statementTimeout, lockTimeout)
	}
}

func cleanupSmokeRows(t *testing.T, store *Store, owner OwnerLease, proxyID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := store.db.ExecContext(ctx, `DELETE FROM juhe_jobs.proxy_latency_proxy_leases WHERE proxy_id=$1`, proxyID); err != nil {
			t.Errorf("J3a smoke cleanup proxy lease failed: %s", redactPGError(err, ""))
		}
		if _, err := store.db.ExecContext(ctx, `DELETE FROM juhe_jobs.proxy_latency_execution_claims WHERE proxy_id=$1`, proxyID); err != nil {
			t.Errorf("J3a smoke cleanup execution claims failed: %s", redactPGError(err, ""))
		}
		if _, err := store.db.ExecContext(ctx, `DELETE FROM juhe_jobs.proxy_latency_outcomes WHERE proxy_id=$1`, proxyID); err != nil {
			t.Errorf("J3a smoke cleanup outcomes failed: %s", redactPGError(err, ""))
		}
		if _, err := store.db.ExecContext(ctx, `DELETE FROM juhe_jobs.proxy_latency_inputs WHERE proxy_id=$1`, proxyID); err != nil {
			t.Errorf("J3a smoke cleanup inputs failed: %s", redactPGError(err, ""))
		}
		if _, err := store.db.ExecContext(ctx, `DELETE FROM juhe_jobs.proxy_latency_input_versions WHERE proxy_id=$1`, proxyID); err != nil {
			t.Errorf("J3a smoke cleanup input versions failed: %s", redactPGError(err, ""))
		}
		if err := store.ReleaseOwnerLease(ctx, owner); err != nil && !errors.Is(err, ErrOwnerLeaseLost) {
			t.Errorf("J3a smoke cleanup owner release failed: %s", redactPGError(err, ""))
		}
		if _, err := store.db.ExecContext(ctx, `DELETE FROM juhe_jobs.proxy_latency_owner_leases WHERE lease_key='proxy-latency-owner' AND owner_id=$1`, owner.OwnerID); err != nil {
			t.Errorf("J3a smoke cleanup owner row failed: %s", redactPGError(err, ""))
		}
	})
}

func cleanupProjectorSmokeRows(t *testing.T, store *Store, owner OwnerLease, proxyLease *ProxyLease, proxyLeaseAcquired *bool, proxyID string, outcomeIDs *[]string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if proxyLeaseAcquired != nil && *proxyLeaseAcquired {
			if err := store.ReleaseProxyLease(ctx, *proxyLease); err != nil {
				t.Errorf("J3a projector smoke cleanup proxy lease release failed: %s", redactPGError(err, ""))
			}
		}
		if err := store.ReleaseOwnerLease(ctx, owner); err != nil {
			t.Errorf("J3a projector smoke cleanup owner lease release failed: %s", redactPGError(err, ""))
		}
		cleanupStatements := []string{
			`DELETE FROM juhe_jobs.proxy_latency_execution_claims WHERE proxy_id=$1`,
			`DELETE FROM juhe_jobs.proxy_latency_outcomes WHERE proxy_id=$1`,
			`DELETE FROM juhe_jobs.proxy_latency_inputs WHERE proxy_id=$1`,
			`DELETE FROM juhe_jobs.proxy_latency_input_versions WHERE proxy_id=$1`,
			`DELETE FROM juhe_jobs.proxy_latency_proxy_leases WHERE proxy_id=$1`,
			`DELETE FROM juhe_jobs.proxy_latency_owner_leases WHERE lease_key='proxy-latency-owner' AND owner_id=$1`,
		}
		for _, statement := range cleanupStatements {
			argument := any(proxyID)
			if strings.Contains(statement, "owner_leases") {
				argument = owner.OwnerID
			}
			if _, err := store.db.ExecContext(ctx, statement, argument); err != nil {
				t.Errorf("J3a projector smoke cleanup jobs rows failed: statement=%q err=%s", statement, redactPGError(err, ""))
			}
		}
		if outcomeIDs != nil && len(*outcomeIDs) > 0 {
			for _, outcomeID := range *outcomeIDs {
				if _, err := store.db.ExecContext(ctx, `DELETE FROM juhe_jobs.proxy_latency_outcomes WHERE outcome_id=$1`, outcomeID); err != nil {
					t.Errorf("J3a projector smoke cleanup outcome %q failed: %s", outcomeID, redactPGError(err, ""))
				}
			}
		}
		jobsChecks := []struct {
			name  string
			query string
			arg   string
		}{
			{name: "execution claims", query: `SELECT count(*) FROM juhe_jobs.proxy_latency_execution_claims WHERE proxy_id=$1`, arg: proxyID},
			{name: "outcomes", query: `SELECT count(*) FROM juhe_jobs.proxy_latency_outcomes WHERE proxy_id=$1`, arg: proxyID},
			{name: "inputs", query: `SELECT count(*) FROM juhe_jobs.proxy_latency_inputs WHERE proxy_id=$1`, arg: proxyID},
			{name: "input versions", query: `SELECT count(*) FROM juhe_jobs.proxy_latency_input_versions WHERE proxy_id=$1`, arg: proxyID},
			{name: "proxy leases", query: `SELECT count(*) FROM juhe_jobs.proxy_latency_proxy_leases WHERE proxy_id=$1`, arg: proxyID},
			{name: "owner leases", query: `SELECT count(*) FROM juhe_jobs.proxy_latency_owner_leases WHERE lease_key='proxy-latency-owner' AND owner_id=$1`, arg: owner.OwnerID},
		}
		for _, check := range jobsChecks {
			var remaining int
			if err := store.db.QueryRowContext(ctx, check.query, check.arg).Scan(&remaining); err != nil {
				t.Errorf("J3a projector smoke cleanup %s verification failed: %s", check.name, redactPGError(err, ""))
			} else if remaining != 0 {
				t.Errorf("J3a projector smoke cleanup %s left %d rows", check.name, remaining)
			}
		}
	})
}

func redactPGError(err error, rawURL string) string {
	if err == nil {
		return "<nil>"
	}
	message := err.Error()
	if rawURL != "" {
		message = strings.ReplaceAll(message, rawURL, "<redacted-pg-url>")
		if parsed, parseErr := url.Parse(rawURL); parseErr == nil {
			if parsed.User != nil {
				message = strings.ReplaceAll(message, parsed.User.String(), "<redacted-userinfo>")
				message = strings.ReplaceAll(message, parsed.User.Username(), "<redacted-user>")
				if password, ok := parsed.User.Password(); ok {
					message = strings.ReplaceAll(message, password, "<redacted-password>")
				}
			}
		}
	}
	message = strings.ReplaceAll(message, "password=", "password=<redacted>")
	return message
}
