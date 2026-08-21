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
	pgSmokeRequiredEnv     = "J3A_PG_SMOKE_REQUIRED"
	pgSmokePrefixEnv       = "J3A_PG_SMOKE_DB_PREFIX"
	pgSmokeDefaultDBPrefix = "juhe_ai_sub2api_dev_j3a_"
)

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
