package auditlog

import (
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Windows may deny os.Symlink when Developer Mode/admin privileges are unavailable.
// The test remains mandatory on hosts that support symlink creation.
func symlinkCreationUnavailable(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.Errno(1314))
}

func TestOpenStoreDoesNotMutateBlobDirectory(t *testing.T) {
	_, _, cfg := openPostgresStoreConfig(t)
	if _, err := os.Stat(cfg.PayloadBlobDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test setup blob directory exists or stat failed: %v", err)
	}
	if _, err := os.Stat(cfg.PayloadBlobDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenStore must not create or clean blob directory: %v", err)
	}
	if err := os.MkdirAll(cfg.PayloadBlobDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(cfg.PayloadBlobDirectory, ".f3-audit-blob-stale-1-old.tmp")
	if err := os.WriteFile(stale, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	second, err := OpenStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("OpenStore must not clean a stale blob temp before lease: %v", err)
	}
}

func TestLoadConfigPrefersDedicatedPostgresURL(t *testing.T) {
	env := map[string]string{
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID":           "f3-postgres-config",
		"JUHE_AI_AUDIT_LOG_STORE":                 "postgres",
		"JUHE_AI_AUDIT_LOG_POSTGRES_URL":          "postgres://dedicated-owner@localhost/dedicated",
		"JUHE_AI_POSTGRES_URL":                    "postgres://shared-owner@localhost/shared",
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY":        t.TempDir(),
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL": "postgres://settings-owner@localhost/business",
	}
	cfg, err := LoadConfig(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PostgresURL != env["JUHE_AI_AUDIT_LOG_POSTGRES_URL"] {
		t.Fatalf("PostgresURL=%q want dedicated F3 URL", cfg.PostgresURL)
	}
	if cfg.PostgresMaxOpenConns != 20 || cfg.PostgresMaxIdleConns != 10 {
		t.Fatalf("default F3 PostgreSQL pool=%d/%d want 20/10", cfg.PostgresMaxOpenConns, cfg.PostgresMaxIdleConns)
	}
	// 2026-09-30 C4 收编：JUHE_AI_AUDIT_LOG_POSTGRES_MAX_OPEN_CONNS /
	// MAX_IDLE_CONNS 收编为常量，pool 覆盖断言删除（取值恒为默认）。
	// 2026-09-19：专职 URL 缺省回退主 JUHE_AI_POSTGRES_URL（显式仍优先）；
	// 两者皆空才保持 fail-fast。
	delete(env, "JUHE_AI_AUDIT_LOG_POSTGRES_URL")
	cfg, err = LoadConfig(func(name string) string { return env[name] })
	if err != nil || cfg.PostgresURL != env["JUHE_AI_POSTGRES_URL"] {
		t.Fatalf("fallback shared PostgresURL=%q err=%v", cfg.PostgresURL, err)
	}
	delete(env, "JUHE_AI_POSTGRES_URL")
	if _, err = LoadConfig(func(name string) string { return env[name] }); err == nil || !strings.Contains(err.Error(), "JUHE_AI_AUDIT_LOG_POSTGRES_URL") {
		t.Fatalf("missing dedicated PostgresURL must fail, got %v", err)
	}
}

func TestLoadConfigParsesNodeCompatibleRetentionPolicy(t *testing.T) {
	root := t.TempDir()
	env := loadConfigBaseEnv(root)
	cfg, err := LoadConfig(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RetentionInterval != time.Minute || cfg.RetentionBatchSize != 512 || cfg.SuccessHotRetentionHours != 1 || cfg.SuccessSampleRate != 1 || cfg.SuccessRetentionDays != 3 || cfg.ProblemRetentionDays != 7 {
		t.Fatalf("Node-compatible retention defaults mismatch: %+v", cfg)
	}
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	retention := cfg.RetentionConfigAt(now)
	if !retention.SuccessHotCutoff.Equal(now.Add(-time.Hour)) || !retention.SuccessCutoff.Equal(now.Add(-72*time.Hour)) || !retention.FailureCutoff.Equal(now.Add(-168*time.Hour)) || !retention.ErrorGroupCutoff.Equal(now.Add(-168*time.Hour)) || retention.SuccessSampleBucketThreshold != 10000 || retention.BatchSize != 512 {
		t.Fatalf("Node-compatible retention cutoffs mismatch: %+v", retention)
	}

	env["JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL"] = "2m"
	env["JUHE_AI_AUDIT_LOG_RETENTION_BATCH_SIZE"] = "42"
	env["JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS"] = "24"
	env["JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE"] = "0.1234"
	env["JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS"] = "2"
	env["JUHE_AI_AUDIT_LOG_PROBLEM_RETENTION_DAYS"] = "9"
	cfg, err = LoadConfig(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RetentionInterval != 2*time.Minute || cfg.RetentionBatchSize != 42 || cfg.SuccessHotRetentionHours != 24 || cfg.SuccessSampleRate != 0.1234 || cfg.SuccessRetentionDays != 2 || cfg.ProblemRetentionDays != 9 {
		t.Fatalf("configured retention policy mismatch: %+v", cfg)
	}
	for name, overrides := range map[string]map[string]string{
		"rate-days-mismatch": {"JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE": "0"},
		"zero-days-mismatch": {"JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS": "0"},
		"short-interval":     {"JUHE_AI_AUDIT_LOG_RETENTION_INTERVAL": "500ms"},
		"zero-batch":         {"JUHE_AI_AUDIT_LOG_RETENTION_BATCH_SIZE": "0"},
		"zero-problem-days":  {"JUHE_AI_AUDIT_LOG_PROBLEM_RETENTION_DAYS": "0"},
		"hot-hours-over-max": {"JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS": "169"},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := loadConfigBaseEnv(root)
			for key, value := range overrides {
				candidate[key] = value
			}
			if _, err := LoadConfig(func(key string) string { return candidate[key] }); err == nil {
				t.Fatalf("invalid retention settings %s must fail", name)
			}
		})
	}
}

func TestPersistStreamLifecycleAndIdempotency(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	lease := acquireLease(t, store)
	progress := fixture("audit-stream", LifecycleInProgress)
	progress.Payloads = nil
	progress.Attempts = nil
	if _, err := store.Persist(ctx, lease, progress); err != nil {
		t.Fatal(err)
	}
	final := fixture("audit-stream", LifecycleFinalized)
	final.AuditOutcome = AuditOutcomeSuccessAfterRetry
	final.Attempts = []AuditLogAttemptInput{{TempID: "attempt-1", AttemptIndex: 0, UpstreamMethod: "POST", UpstreamURL: "https://upstream.example/v1", StartedAt: final.StartedAt, EndedAt: final.EndedAt}}
	final.Payloads = []AuditLogPayloadInput{{AttemptTempID: "attempt-1", PartType: PayloadPartGatewayResponse, SequenceIndex: intPointer(0), ContentType: "application/json", Body: PayloadBody{Bytes: []byte(`{"ok":true}`), Present: true}}}
	if _, err := store.Persist(ctx, lease, final); err != nil {
		t.Fatal(err)
	}
	late := fixture("audit-stream", LifecycleInProgress)
	result, err := store.Persist(ctx, lease, late)
	if err != nil || !result.Ignored {
		t.Fatalf("late in_progress must be ignored: result=%+v err=%v", result, err)
	}
	retry, err := store.Persist(ctx, lease, final)
	if err != nil || !retry.Ignored {
		t.Fatalf("same finalized ID must be idempotent: result=%+v err=%v", retry, err)
	}
	var lifecycle, outcome string
	var attempts, payloads int
	if err := implementation.db.QueryRow(`SELECT lifecycle_status,audit_outcome,attempt_count,payload_count FROM juhe_dataset.audit_logs WHERE id=$1`, final.ID).Scan(&lifecycle, &outcome, &attempts, &payloads); err != nil {
		t.Fatal(err)
	}
	if lifecycle != string(LifecycleFinalized) || outcome != string(AuditOutcomeSuccessAfterRetry) || attempts != 1 || payloads != 1 {
		t.Fatalf("finalized state was not preserved: lifecycle=%s outcome=%s attempts=%d payloads=%d", lifecycle, outcome, attempts, payloads)
	}
}

func TestPersistRejectsInvalidAbsoluteTimesAndStoresCanonicalUTC(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	lease := acquireLease(t, store)

	for _, test := range []struct {
		name   string
		mutate func(*AuditLogInput)
	}{
		{name: "main startedAt without offset", mutate: func(input *AuditLogInput) { input.StartedAt = "2026-08-09T12:00:00" }},
		{name: "main createdAt malformed", mutate: func(input *AuditLogInput) { input.CreatedAt = "not-a-time" }},
		{name: "optional HTTP completed malformed", mutate: func(input *AuditLogInput) { input.HTTPCompletedAt = "2026-08-09 12:00:00Z" }},
		{name: "attempt endedAt without offset", mutate: func(input *AuditLogInput) {
			input.Attempts = []AuditLogAttemptInput{{AttemptIndex: 0, UpstreamMethod: "POST", UpstreamURL: "https://upstream.example", StartedAt: input.StartedAt, EndedAt: "2026-08-09T12:00:00"}}
		}},
		{name: "payload createdAt malformed", mutate: func(input *AuditLogInput) {
			input.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayResponse, CreatedAt: "bad"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := fixture("invalid-time-"+strings.ReplaceAll(test.name, " ", "-"), LifecycleFinalized)
			test.mutate(&input)
			if _, err := store.Persist(context.Background(), lease, input); err == nil {
				t.Fatal("invalid supplied absolute time must be rejected")
			}
		})
	}

	input := fixture("canonical-times", LifecycleFinalized)
	input.StartedAt = "2026-08-09T20:00:00+08:00"
	input.EndedAt = "2026-08-09T20:00:01+08:00"
	input.HTTPCompletedAt = "2026-08-09T20:00:00.500+08:00"
	input.CreatedAt = "2026-08-09T20:00:02+08:00"
	input.Attempts = []AuditLogAttemptInput{{ID: "canonical-attempt", AttemptIndex: 0, UpstreamMethod: "POST", UpstreamURL: "https://upstream.example", StartedAt: input.StartedAt, EndedAt: input.EndedAt}}
	input.Payloads = []AuditLogPayloadInput{{ID: "canonical-payload", PartType: PayloadPartGatewayResponse, CreatedAt: input.CreatedAt}}
	if _, err := store.Persist(context.Background(), lease, input); err != nil {
		t.Fatal(err)
	}
	// PG 臂 timestamptz 以 time.Time 回读；断言语义不变：存储值必须是规范化
	// UTC 时刻（12:00:00Z / 12:00:01Z / 12:00:00.5Z / 12:00:02Z）。
	var startedAt, endedAt, completedAt, createdAt time.Time
	if err := implementation.db.QueryRow(`SELECT started_at,ended_at,http_completed_at,created_at FROM juhe_dataset.audit_logs WHERE id=$1`, input.ID).Scan(&startedAt, &endedAt, &completedAt, &createdAt); err != nil {
		t.Fatal(err)
	}
	if !startedAt.Equal(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)) || !endedAt.Equal(time.Date(2026, 8, 9, 12, 0, 1, 0, time.UTC)) || !completedAt.Equal(time.Date(2026, 8, 9, 12, 0, 0, 500000000, time.UTC)) || !createdAt.Equal(time.Date(2026, 8, 9, 12, 0, 2, 0, time.UTC)) {
		t.Fatalf("main timestamps not canonical UTC: started=%v ended=%v completed=%v created=%v", startedAt, endedAt, completedAt, createdAt)
	}
	var attemptStartedAt, attemptEndedAt, payloadCreatedAt time.Time
	if err := implementation.db.QueryRow(`SELECT started_at,ended_at FROM juhe_dataset.audit_log_attempts WHERE id='canonical-attempt'`).Scan(&attemptStartedAt, &attemptEndedAt); err != nil {
		t.Fatal(err)
	}
	if err := implementation.db.QueryRow(`SELECT created_at FROM juhe_dataset.audit_payload_refs WHERE id='canonical-payload'`).Scan(&payloadCreatedAt); err != nil {
		t.Fatal(err)
	}
	if !attemptStartedAt.Equal(time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)) || !attemptEndedAt.Equal(time.Date(2026, 8, 9, 12, 0, 1, 0, time.UTC)) || !payloadCreatedAt.Equal(time.Date(2026, 8, 9, 12, 0, 2, 0, time.UTC)) {
		t.Fatalf("child timestamps not canonical UTC: attempt=%v/%v payload=%v", attemptStartedAt, attemptEndedAt, payloadCreatedAt)
	}
}

func TestFinalizedReplacesAllNodeLifecycleFields(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	lease := acquireLease(t, store)
	progress := fixture("audit-full-replace", LifecycleInProgress)
	progress.SystemAccountID, progress.QueryString, progress.ClientIP, progress.SampleReason = "before-account", "before=true", "127.0.0.1", "in_progress"
	if _, err := store.Persist(context.Background(), lease, progress); err != nil {
		t.Fatal(err)
	}
	final := fixture("audit-full-replace", LifecycleFinalized)
	final.SystemAccountID, final.QueryString, final.ClientIP, final.SampleReason = "after-account", "after=true", "10.0.0.2", "finalized"
	if _, err := store.Persist(context.Background(), lease, final); err != nil {
		t.Fatal(err)
	}
	var accountID, query, ip, reason, lifecycle string
	if err := implementation.db.QueryRow(`SELECT system_account_id,query_string,client_ip,sample_reason,lifecycle_status FROM juhe_dataset.audit_logs WHERE id=$1`, final.ID).Scan(&accountID, &query, &ip, &reason, &lifecycle); err != nil {
		t.Fatal(err)
	}
	if accountID != final.SystemAccountID || query != final.QueryString || ip != final.ClientIP || reason != final.SampleReason || lifecycle != string(LifecycleFinalized) {
		t.Fatalf("finalized update must replace Node lifecycle fields: account=%q query=%q ip=%q reason=%q lifecycle=%q", accountID, query, ip, reason, lifecycle)
	}
}

func TestPersistWritesAndAssociatesErrorGroup(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	input := fixture("audit-error-group", LifecycleFinalized)
	input.Success = false
	input.AuditOutcome = AuditOutcomeUpstreamFailed
	input.ErrorCode = "upstream_unavailable"
	input.ErrorMessage = "upstream unavailable 123456789"
	lease := acquireLease(t, store)
	if _, err := store.Persist(context.Background(), lease, input); err != nil {
		t.Fatal(err)
	}
	var groupID string
	if err := implementation.db.QueryRow(`SELECT error_group_id FROM juhe_dataset.audit_logs WHERE id=$1`, input.ID).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if groupID == "" {
		t.Fatal("audit log did not retain its error group")
	}
	var count int
	if err := implementation.db.QueryRow(`SELECT count FROM juhe_dataset.audit_error_groups WHERE id=$1`, groupID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("error group count=%d, want 1", count)
	}
	second := fixture("audit-error-group-2", LifecycleFinalized)
	second.Success, second.AuditOutcome, second.ErrorCode, second.ErrorMessage = false, AuditOutcomeUpstreamFailed, input.ErrorCode, "upstream unavailable 999999999"
	if _, err := store.Persist(context.Background(), lease, second); err != nil {
		t.Fatal(err)
	}
	var secondID string
	if err := implementation.db.QueryRow(`SELECT error_group_id FROM juhe_dataset.audit_logs WHERE id=$1`, second.ID).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	if secondID != groupID {
		t.Fatalf("equivalent Node-style errors must share a group: first=%q second=%q", groupID, secondID)
	}
	if err := implementation.db.QueryRow(`SELECT count FROM juhe_dataset.audit_error_groups WHERE id=$1`, groupID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("automatic error group count=%d, want 2", count)
	}
}

func TestPayloadRefsAllowDifferentPartTypesAtSameSequence(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	input := fixture("audit-same-sequence", LifecycleFinalized)
	input.Payloads = []AuditLogPayloadInput{
		{PartType: PayloadPartClientRequest, SequenceIndex: intPointer(0), Body: PayloadBody{Bytes: []byte("request"), Present: true}},
		{PartType: PayloadPartGatewayResponse, SequenceIndex: intPointer(0), Body: PayloadBody{Bytes: []byte("response"), Present: true}},
	}
	if _, err := store.Persist(context.Background(), acquireLease(t, store), input); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1`, input.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("different part types sharing sequence_index must both persist, got %d", count)
	}
}

func TestPersistRejectsStaleOwnerFence(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	ctx := context.Background()
	first := acquireLease(t, store)
	if err := store.ReleaseOwnerLease(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := acquireLease(t, store)
	if second.FenceToken <= first.FenceToken {
		t.Fatalf("fence did not increase: first=%d second=%d", first.FenceToken, second.FenceToken)
	}
	if _, err := store.Persist(ctx, first, fixture("audit-stale", LifecycleFinalized)); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("stale owner must not write: %v", err)
	}
	var total int
	if err := implementation.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_logs`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("stale owner wrote %d audit rows", total)
	}
}

func TestBlobTempsArePrivateUntilLeaseAndStaleTempsAreSafeToClean(t *testing.T) {
	store, _, cfg := openPostgresStoreConfig(t)
	defer store.Close()
	input := fixture("audit-no-lease", LifecycleFinalized)
	input.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayResponse, Body: PayloadBody{Bytes: []byte("private"), Present: true}}}
	if _, err := store.Persist(context.Background(), OwnerLease{}, input); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("no lease must fail before file publication: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.PayloadBlobDirectory, "sha256")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("lease failure published a blob directory")
	}
	lease := acquireLease(t, store)
	if err := os.MkdirAll(cfg.PayloadBlobDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(cfg.PayloadBlobDirectory, ".f3-audit-blob-"+blobTempOwnerKey(lease)+"-stale.tmp")
	foreign := filepath.Join(cfg.PayloadBlobDirectory, ".f3-audit-blob-foreign-1-stale.tmp")
	published := filepath.Join(cfg.PayloadBlobDirectory, "published.blob")
	if err := os.WriteFile(temp, []byte("temp"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(published, []byte("published"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(temp, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(foreign, old, old); err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupOwnedBlobTemps(context.Background(), lease, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temp); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned stale private temp was not removed")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("cleanup removed another owner/fence temp: %v", err)
	}
	if _, err := os.Stat(published); err != nil {
		t.Fatalf("cleanup removed a published blob: %v", err)
	}
	// A later acquired fence can safely reclaim an abandoned older-fence temp,
	// but never a current/future fence or a published content-addressed file.
	if err := store.ReleaseOwnerLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	newLease := acquireLease(t, store)
	if err := store.CleanupOrphanedBlobTemps(context.Background(), newLease, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new fenced owner did not reclaim a safe old orphan temp: %v", err)
	}
	if _, err := os.Stat(published); err != nil {
		t.Fatalf("orphan cleanup removed published blob: %v", err)
	}
}

func TestHashOnlyRetainsInputHashAndNonNegativeSize(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	size := int64(-42)
	input := fixture("audit-hash-only", LifecycleFinalized)
	input.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayError, BodySHA256: "source-hash", RawBodySizeBytes: &size, CaptureStatus: PayloadCaptureHashOnly}}
	if _, err := store.Persist(context.Background(), acquireLease(t, store), input); err != nil {
		t.Fatal(err)
	}
	var hash string
	var raw int64
	if err := implementation.db.QueryRow(`SELECT body_sha256,raw_size_bytes FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1`, input.ID).Scan(&hash, &raw); err != nil {
		t.Fatal(err)
	}
	if hash != "source-hash" || raw != 0 {
		t.Fatalf("hash-only metadata mismatch: hash=%q raw=%d", hash, raw)
	}
}

func TestPayloadMatchesNodeExplicitMetadataAndGzipSemantics(t *testing.T) {
	store, impl := openPostgresStore(t)
	defer store.Close()
	explicitSize := int64(17)
	largeJSON := []byte(`{"message":"` + strings.Repeat("compressible-content-", 800) + `"}`)
	input := fixture("audit-gzip-explicit", LifecycleFinalized)
	input.Payloads = []AuditLogPayloadInput{
		{
			PartType:         PayloadPartClientRequest,
			ContentType:      "application/json",
			Headers:          map[string]HeaderValues{}, // Node's truthy {} must persist.
			Body:             PayloadBody{Bytes: largeJSON, Present: true},
			BodySHA256:       "upstream-explicit-hash",
			RawBodySizeBytes: &explicitSize,
		},
	}
	lease := acquireLease(t, store)
	if _, err := store.Persist(context.Background(), lease, input); err != nil {
		t.Fatal(err)
	}
	var headersID, bodyID sql.NullString
	var bodyHash string
	var raw, compressed int64
	if err := impl.db.QueryRow(`SELECT headers_blob_id,body_blob_id,body_sha256,raw_size_bytes,compressed_size_bytes FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1`, input.ID).Scan(&headersID, &bodyID, &bodyHash, &raw, &compressed); err != nil {
		t.Fatal(err)
	}
	if !headersID.Valid || !bodyID.Valid {
		t.Fatalf("explicit empty headers and body must have blobs: headers=%+v body=%+v", headersID, bodyID)
	}
	if bodyHash != "upstream-explicit-hash" || raw != int64(len(`{}`))+explicitSize {
		t.Fatalf("explicit Node metadata must win: hash=%q raw=%d", bodyHash, raw)
	}
	var saved int64
	if err := impl.db.QueryRow(`SELECT compression_saved_bytes FROM juhe_dataset.audit_logs WHERE id=$1`, input.ID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	if saved != 0 {
		t.Fatalf("compressionSaved must clamp an explicit raw-size underflow to zero, got %d", saved)
	}
	var compression, storageKey string
	var storedSize int64
	if err := impl.db.QueryRow(`SELECT compression,storage_key,compressed_size_bytes FROM juhe_dataset.audit_payload_blobs WHERE id=$1`, bodyID.String).Scan(&compression, &storageKey, &storedSize); err != nil {
		t.Fatal(err)
	}
	if compression != "gzip" || storedSize >= int64(len(largeJSON)) || compressed != int64(len(`{}`))+storedSize {
		t.Fatalf("gzip metadata/totals drift: compression=%q stored=%d ref=%d", compression, storedSize, compressed)
	}
	file, err := os.Open(filepath.Join(impl.blobDir, filepath.FromSlash(storageKey)))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	_ = reader.Close()
	_ = file.Close()
	if err != nil || string(decoded) != string(largeJSON) {
		t.Fatalf("stored gzip payload mismatch: err=%v bytes=%d", err, len(decoded))
	}

	// Same raw bytes must reuse existing canonical compression and size, not a
	// freshly selected representation.
	second := fixture("audit-gzip-canonical", LifecycleFinalized)
	second.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartClientRequest, ContentType: "application/json", Body: PayloadBody{Bytes: largeJSON, Present: true}}}
	if _, err := store.Persist(context.Background(), lease, second); err != nil {
		t.Fatal(err)
	}
	var secondID string
	var secondCompressed int64
	if err := impl.db.QueryRow(`SELECT body_blob_id,compressed_size_bytes FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1`, second.ID).Scan(&secondID, &secondCompressed); err != nil {
		t.Fatal(err)
	}
	if secondID != bodyID.String || secondCompressed != storedSize {
		t.Fatalf("existing canonical blob metadata was not retained: id=%q compressed=%d", secondID, secondCompressed)
	}

	notEligible, storedBytes, err := newBlobRecord(largeJSON, "application/octet-stream", "gzip")
	if err != nil || notEligible.compression != "none" || len(storedBytes) != len(largeJSON) {
		t.Fatalf("pre-encoded/non-compressible body must remain raw: compression=%q err=%v", notEligible.compression, err)
	}
}

func TestCanonicalBlobMissingFileFailsWithoutRecompression(t *testing.T) {
	store, impl, cfg := openPostgresStoreConfig(t)
	defer store.Close()
	lease := acquireLease(t, store)
	body := []byte(`{"message":"` + strings.Repeat("node-gzip-canonical-", 600) + `"}`)
	first := fixture("audit-canonical-source", LifecycleFinalized)
	first.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayResponse, ContentType: "application/json", Body: PayloadBody{Bytes: body, Present: true}}}
	if _, err := store.Persist(context.Background(), lease, first); err != nil {
		t.Fatal(err)
	}
	var storageKey string
	if err := impl.db.QueryRow(`SELECT storage_key FROM juhe_dataset.audit_payload_blobs`).Scan(&storageKey); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(cfg.PayloadBlobDirectory, filepath.FromSlash(storageKey))); err != nil {
		t.Fatal(err)
	}
	second := fixture("audit-canonical-missing", LifecycleFinalized)
	second.Payloads = first.Payloads
	// 换 Path 拉开错误指纹：本测试只验证 canonical 文件缺失检测，不得被
	// 错误组窗口内去重（出生即降级、不落盘）提前短路。
	second.Path = "/v1/canonical-missing"
	if _, err := store.Persist(context.Background(), lease, second); err == nil || !strings.Contains(err.Error(), "缺少物理文件") {
		t.Fatalf("missing canonical gzip file must fail instead of Go recompression: %v", err)
	}
	var rows int
	if err := impl.db.QueryRow(`SELECT COUNT(*) FROM juhe_dataset.audit_logs WHERE id=$1`, second.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("failed canonical recovery must roll back parent row, got %d", rows)
	}
}

func TestAttemptTempIDNeverCrossesAuditLogAndBlobConflictUsesCanonicalID(t *testing.T) {
	store, impl := openPostgresStore(t)
	defer store.Close()
	lease := acquireLease(t, store)
	first := fixture("audit-first", LifecycleFinalized)
	first.Attempts = []AuditLogAttemptInput{{ID: "shared-attempt", TempID: "first", AttemptIndex: 0, UpstreamMethod: "POST", UpstreamURL: "https://one", StartedAt: first.StartedAt}}
	first.Payloads = []AuditLogPayloadInput{{AttemptTempID: "first", PartType: PayloadPartUpstreamResponse, Body: PayloadBody{Bytes: []byte("same"), Present: true}}}
	if _, err := store.Persist(context.Background(), lease, first); err != nil {
		t.Fatal(err)
	}
	var canonical string
	if err := impl.db.QueryRow(`SELECT id FROM juhe_dataset.audit_payload_blobs`).Scan(&canonical); err != nil {
		t.Fatal(err)
	}
	second := fixture("audit-second", LifecycleFinalized)
	second.Attempts = []AuditLogAttemptInput{{ID: "shared-attempt", TempID: "second", AttemptIndex: 0, UpstreamMethod: "POST", UpstreamURL: "https://two", StartedAt: second.StartedAt}}
	second.Payloads = []AuditLogPayloadInput{{AttemptTempID: "second", PartType: PayloadPartUpstreamResponse, Body: PayloadBody{Bytes: []byte("same"), Present: true}}}
	// 换 Path 拉开错误指纹，避开错误组窗口内去重（否则第二条不落盘）。
	second.Path = "/v1/attempt-conflict"
	if _, err := store.Persist(context.Background(), lease, second); err != nil {
		t.Fatal(err)
	}
	var attemptRef, bodyID sql.NullString
	if err := impl.db.QueryRow(`SELECT attempt_id,body_blob_id FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1`, second.ID).Scan(&attemptRef, &bodyID); err != nil {
		t.Fatal(err)
	}
	if attemptRef.Valid {
		t.Fatalf("conflicting attempt ID must not cross-link another audit: %q", attemptRef.String)
	}
	if !bodyID.Valid || bodyID.String != canonical {
		t.Fatalf("expected canonical existing blob ID %q, got %+v", canonical, bodyID)
	}
}

func TestHeaderJSONPreservesScalarAndArray(t *testing.T) {
	var headers map[string]HeaderValues
	if err := json.Unmarshal([]byte(`{"one":"value","many":["a","b"]}`), &headers); err != nil {
		t.Fatal(err)
	}
	bytes, err := marshalNodeHeaders(headers)
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes) != `{"many":["a","b"],"one":"value"}` {
		t.Fatalf("header JSON semantic drift: %s", bytes)
	}
}

func TestErrorMessageNormalizationUsesJavaScriptUTF16Slice(t *testing.T) {
	value := strings.Repeat("😀", 250) + "tail"
	normalized := normalizeErrorMessage(value)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	var decoded string
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != strings.Repeat("😀", 250) {
		t.Fatalf("Node UTF-16 slice must retain exactly 500 code units, got runes=%d", len([]rune(decoded)))
	}
	// A 499th code unit followed by an emoji is sliced between its surrogate
	// pair in JS. The JSON representation must retain that lone high surrogate
	// rather than silently replacing it before hashing.
	lone := normalizeErrorMessage(strings.Repeat("a", 499) + "😀")
	loneJSON, err := json.Marshal(lone)
	if err != nil || !strings.Contains(string(loneJSON), `\ud83d`) {
		t.Fatalf("Node UTF-16 half-surrogate must be preserved in JSON hashing: %s err=%v", loneJSON, err)
	}
}

func TestErrorGroupUsesEventTimeForFirstAndLast(t *testing.T) {
	store, implementation := openPostgresStore(t)
	defer store.Close()
	lease := acquireLease(t, store)
	late := fixture("audit-event-late", LifecycleFinalized)
	late.Success = false
	late.AuditOutcome = AuditOutcomeUpstreamFailed
	late.ErrorCode = "x"
	late.CreatedAt = "2026-08-09T12:04:00.000Z"
	early := fixture("audit-event-early", LifecycleFinalized)
	early.Success = false
	early.AuditOutcome = AuditOutcomeUpstreamFailed
	early.ErrorCode = "x"
	early.CreatedAt = "2026-08-09T12:01:00.000Z"
	if _, err := store.Persist(context.Background(), lease, late); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Persist(context.Background(), lease, early); err != nil {
		t.Fatal(err)
	}
	var first, last string
	if err := implementation.db.QueryRow(`SELECT first_event_id,last_event_id FROM juhe_dataset.audit_error_groups`).Scan(&first, &last); err != nil {
		t.Fatal(err)
	}
	if first != early.ID || last != late.ID {
		t.Fatalf("event timestamp order mismatch: first=%q last=%q", first, last)
	}
}

func tempCount(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".f3-audit-blob-") {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPersistRejectsNodeExcludedTrafficSource(t *testing.T) {
	store, _ := openPostgresStore(t)
	defer store.Close()
	input := fixture("audit-excluded-source", LifecycleFinalized)
	input.TrafficSource = TrafficSourceAccountHealthCheck
	if _, err := store.Persist(context.Background(), acquireLease(t, store), input); err == nil || !strings.Contains(err.Error(), "不属于当前原始审计持久化范围") {
		t.Fatalf("Node excluded source must fail explicitly, got %v", err)
	}
}

func TestInputDTOAcceptsNodeBufferAndPostgresBinding(t *testing.T) {
	var input AuditLogInput
	encoded := []byte(`{
  "id":"stable-id","lifecycleStatus":"finalized","traceId":"trace","trafficSource":"gateway",
  "method":"POST","path":"/v1/responses","auditOutcome":"success","success":true,
  "sampleBucket":1,"sampleReason":"test","startedAt":"2026-08-09T12:00:00Z","endedAt":"2026-08-09T12:00:01Z",
  "attempts":[{"tempId":"attempt","attemptIndex":0,"upstreamMethod":"POST","upstreamUrl":"https://example.test","startedAt":"2026-08-09T12:00:00Z"}],
  "payloads":[{"attemptTempId":"attempt","partType":"upstream_response","headers":{"x-test":"one"},"body":{"type":"Buffer","data":[104,105]}}]
}`)
	if err := json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Payloads) != 1 || !input.Payloads[0].Body.Present || string(input.Payloads[0].Body.Bytes) != "hi" || len(input.Payloads[0].Headers["x-test"].Values) != 1 {
		t.Fatalf("Node DTO JSON mapping failed: %+v", input.Payloads)
	}
	store := &sqlStore{}
	bound := store.bind(`INSERT INTO juhe_dataset.audit_logs (id, trace_id) VALUES (?, ?)`)
	if bound != `INSERT INTO juhe_dataset.audit_logs (id, trace_id) VALUES ($1, $2)` {
		t.Fatalf("PostgreSQL bind conversion mismatch: %q", bound)
	}
	if !strings.Contains(postgresSchema, "juhe_dataset.audit_log_owner_leases") || strings.Contains(postgresSchema, "?") {
		t.Fatal("PostgreSQL schema must own F3 tables without SQLite placeholders")
	}
	if !strings.Contains(postgresSchema, "audit_payload_blobs") || !strings.Contains(postgresSchema, "audit_error_groups") {
		t.Fatal("PostgreSQL schema must include the full F3 persistence set")
	}
	for _, index := range []string{"idx_audit_logs_persisted_created", "idx_audit_logs_system_persisted_created", "idx_audit_logs_system_trace_created", "idx_audit_logs_session_created", "idx_audit_payload_refs_log_part", "idx_audit_error_groups_updated", "idx_audit_error_groups_api_key_account"} {
		if !strings.Contains(postgresSchema, index) {
			t.Fatalf("PostgreSQL fresh schema must include Node F3 read/retention index %s", index)
		}
	}
	if !strings.Contains(postgresSchema, `trace_id COLLATE "C"`) || !strings.Contains(postgresSchema, `client_ip COLLATE "C"`) {
		t.Fatalf("PostgreSQL trace/client_ip prefix indexes must use Node-compatible COLLATE C: %s", postgresSchema)
	}
}

func TestPostgresLeaseSQLUsesRealtimeCommitFenceWithoutLongRowLock(t *testing.T) {
	for name, statement := range map[string]string{
		"acquire": postgresAcquireLeaseSQL,
		"renew":   postgresRenewLeaseSQL,
		"initial": postgresInitialFenceSQL,
		"commit":  postgresCommitFenceSQL,
	} {
		if !strings.Contains(statement, "clock_timestamp()") {
			t.Fatalf("PostgreSQL %s lease query must use database realtime clock: %s", name, statement)
		}
	}
	if strings.Contains(strings.ToUpper(postgresInitialFenceSQL), "FOR UPDATE") {
		t.Fatalf("initial PostgreSQL fence must not lock the lease row across Persist: %s", postgresInitialFenceSQL)
	}
	if !strings.Contains(strings.ToUpper(postgresCommitFenceSQL), "UPDATE") || !strings.Contains(strings.ToUpper(postgresCommitFenceSQL), "RETURNING") {
		t.Fatalf("commit fence must atomically lock/check the lease immediately before Commit: %s", postgresCommitFenceSQL)
	}
}

func TestBlobParentSyncPlatformContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no portable directory fsync in os.File. The implementation
		// must deliberately avoid claiming one or touching an arbitrary path.
		if err := syncBlobParent(filepath.Join(t.TempDir(), "does-not-exist")); err != nil {
			t.Fatalf("Windows parent sync must be an explicit no-op: %v", err)
		}
		return
	}
	if err := syncBlobParent(t.TempDir()); err != nil {
		t.Fatalf("Unix parent directory fsync failed: %v", err)
	}
}

func TestPostgresLeaseSmokeWhenExplicitlyConfigured(t *testing.T) {
	postgresURL := strings.TrimSpace(os.Getenv("JUHE_AI_AUDIT_LOG_TEST_POSTGRES_URL"))
	if postgresURL == "" {
		t.Skip("未设置 JUHE_AI_AUDIT_LOG_TEST_POSTGRES_URL；跳过真实 PostgreSQL lease smoke")
	}
	assertDestructivePostgresSmokeTarget(t, postgresURL)
	blobRoot := t.TempDir()
	store, err := OpenStore(Config{PostgresURL: postgresURL, PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2, PayloadBlobDirectory: blobRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	impl := store.(*sqlStore)
	assertEmptyPostgresF3Tables(t, ctx, impl.db)
	defer func() {
		cleanupPostgresSmokeRows(t, ctx, impl.db)
		if err := os.RemoveAll(blobRoot); err != nil {
			t.Errorf("清理 PostgreSQL smoke 测试 blob 目录失败: %v", err)
		}
		if _, err := os.Stat(blobRoot); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("PostgreSQL smoke 测试 blob 文件未清空: %v", err)
		}
	}()
	runID := "f3pgsmoke" + strconv.FormatInt(time.Now().UnixNano(), 10)
	owner := runID + "-owner"
	lease, acquired, err := store.AcquireOwnerLease(ctx, owner, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("真实 PostgreSQL acquire 失败: acquired=%v err=%v", acquired, err)
	}
	assertPostgresCollatedPrefixIndexes(t, ctx, impl.db)
	// Same stable ID has one winner: only it may create all child/blob/group
	// side effects.
	input := fixture(runID+"-same-id", LifecycleFinalized)
	input.Success, input.AuditOutcome, input.ErrorCode = false, AuditOutcomeUpstreamFailed, "pg-same-id"
	input.Attempts = []AuditLogAttemptInput{{TempID: "attempt", AttemptIndex: 0, UpstreamMethod: "POST", UpstreamURL: "https://example.test", StartedAt: input.StartedAt}}
	input.Payloads = []AuditLogPayloadInput{{AttemptTempID: "attempt", PartType: PayloadPartGatewayError, ContentType: "application/json", Body: PayloadBody{Bytes: []byte("postgres one winner"), Present: true}}}
	results := make(chan PersistResult, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() { result, err := store.Persist(ctx, lease, input); results <- result; errs <- err }()
	}
	ignored := 0
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("真实 PostgreSQL 同 ID 并发 Persist 失败: %v", err)
		}
		if (<-results).Ignored {
			ignored++
		}
	}
	if ignored != 1 {
		t.Fatalf("真实 PostgreSQL 同 ID 并发必须恰有一个 loser，got %d", ignored)
	}
	for table, want := range map[string]int{"audit_logs": 1, "audit_log_attempts": 1, "audit_payload_refs": 1, "audit_error_groups": 1, "audit_payload_blobs": 1} {
		var count int
		if err := impl.db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.`+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("真实 PostgreSQL 同 ID loser 产生 %s 副作用: got=%d want=%d", table, count, want)
		}
	}
	if err := store.ReleaseOwnerLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	newLease, acquired, err := store.AcquireOwnerLease(ctx, owner+"-next", time.Minute)
	if err != nil || !acquired || newLease.FenceToken <= lease.FenceToken {
		t.Fatalf("真实 PostgreSQL fence handoff 失败: old=%+v new=%+v acquired=%v err=%v", lease, newLease, acquired, err)
	}
	if _, err := store.Persist(ctx, lease, fixture(runID+"-stale", LifecycleFinalized)); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("真实 PostgreSQL late lease 必须被 fence 拒绝: %v", err)
	}
	// Real write transaction: initial fence -> write -> lease expiry/takeover ->
	// commit fence rejection -> rollback must remove its write.
	oldTx, err := impl.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = oldTx.Rollback() }()
	if err := impl.verifyLeaseTx(ctx, oldTx, newLease); err != nil {
		t.Fatalf("真实 PostgreSQL old transaction initial fence 失败: %v", err)
	}
	oldInput := fixture(runID+"-rollback-after-takeover", LifecycleFinalized)
	if wrote, err := impl.upsertLog(ctx, oldTx, oldInput, nil); err != nil || !wrote {
		t.Fatalf("真实 PostgreSQL old transaction 审计写入失败: wrote=%v err=%v", wrote, err)
	}
	if _, err := impl.db.ExecContext(ctx, `UPDATE juhe_dataset.audit_log_owner_leases SET lease_until=clock_timestamp() - INTERVAL '1 millisecond' WHERE lease_key=$1 AND owner_id=$2 AND fence_token=$3`, "f3-audit-log-persistence", newLease.OwnerID, newLease.FenceToken); err != nil {
		t.Fatal(err)
	}
	successor, acquired, err := store.AcquireOwnerLease(ctx, owner+"-takeover", time.Minute)
	if err != nil || !acquired || successor.FenceToken <= newLease.FenceToken {
		t.Fatalf("真实 PostgreSQL expiry takeover 失败: prior=%+v successor=%+v acquired=%v err=%v", newLease, successor, acquired, err)
	}
	if err := impl.verifyLeaseBeforeCommit(ctx, oldTx, newLease); !errors.Is(err, ErrOwnerLeaseLost) {
		t.Fatalf("真实 PostgreSQL old transaction commit fence 必须拒绝已 takeover 的 lease: %v", err)
	}
	if err := oldTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var rolledBackRows int
	if err := impl.db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.audit_logs WHERE id=$1`, oldInput.ID).Scan(&rolledBackRows); err != nil {
		t.Fatal(err)
	}
	if rolledBackRows != 0 {
		t.Fatalf("真实 PostgreSQL takeover 后旧事务写入未回滚: %d", rolledBackRows)
	}
	refInput := fixture(runID+"-ref-count", LifecycleFinalized)
	refInput.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayResponse, Body: PayloadBody{Bytes: []byte("postgres references are truth"), Present: true}}}
	if _, err := store.Persist(ctx, successor, refInput); err != nil {
		t.Fatalf("真实 PostgreSQL ref-count audit 写入失败: %v", err)
	}
	var refCount int64
	if err := impl.db.QueryRowContext(ctx, `SELECT ref_count FROM juhe_dataset.audit_payload_blobs b JOIN juhe_dataset.audit_payload_refs r ON r.body_blob_id=b.id WHERE r.audit_log_id=$1`, refInput.ID).Scan(&refCount); err != nil {
		t.Fatal(err)
	}
	if refCount != 0 {
		t.Fatalf("PostgreSQL ref_count must remain reference-derived, got %d", refCount)
	}

	// Commit a real retention GC intent, then interleave its physical-delete
	// phase with a new Persist of the same canonical content. Either operation
	// may win the per-blob lock; the end state must retain the new reference,
	// metadata and physical file.
	gcOld := fixture(runID+"-gc-old", LifecycleFinalized)
	gcOld.CreatedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	gcOld.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayResponse, ContentType: "text/plain", Body: PayloadBody{Bytes: []byte("postgres gc interleave"), Present: true}}}
	if _, err := store.Persist(ctx, successor, gcOld); err != nil {
		t.Fatalf("真实 PostgreSQL GC 旧引用写入失败: %v", err)
	}
	var gcBlobID, gcStorageKey string
	if err := impl.db.QueryRowContext(ctx, `SELECT b.id,b.storage_key FROM juhe_dataset.audit_payload_blobs b JOIN juhe_dataset.audit_payload_refs r ON r.body_blob_id=b.id WHERE r.audit_log_id=$1`, gcOld.ID).Scan(&gcBlobID, &gcStorageKey); err != nil {
		t.Fatalf("读取真实 PostgreSQL GC blob 失败: %v", err)
	}
	gcScheduleTx, err := impl.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := impl.verifyLeaseTx(ctx, gcScheduleTx, successor); err != nil {
		t.Fatal(err)
	}
	_, gcCandidates, err := impl.deleteRetentionChildren(ctx, gcScheduleTx, []string{gcOld.ID}, true)
	if err != nil {
		t.Fatalf("真实 PostgreSQL GC 删除旧子行失败: %v", err)
	}
	if _, err := impl.deleteAuditLogRows(ctx, gcScheduleTx, []string{gcOld.ID}); err != nil {
		t.Fatalf("真实 PostgreSQL GC 删除旧日志失败: %v", err)
	}
	gcRows, err := impl.unreferencedBlobRows(ctx, gcScheduleTx, gcCandidates, 10)
	if err != nil {
		t.Fatalf("真实 PostgreSQL GC 查询候选失败: %v", err)
	}
	if len(gcRows) != 1 || gcRows[0].id != gcBlobID {
		t.Fatalf("真实 PostgreSQL GC 候选=%+v, want blob=%s", gcRows, gcBlobID)
	}
	// Retention may only inspect candidates before it takes the per-blob
	// advisory lock. Persist takes that advisory lock before it subsequently
	// writes the canonical blob row. This ordered, real-PostgreSQL interleave
	// fails by deadline if a candidate scan regresses to FOR UPDATE.
	orderCtx, cancelOrder := context.WithTimeout(ctx, 5*time.Second)
	defer cancelOrder()
	persistOrderTx, err := impl.db.BeginTx(orderCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = persistOrderTx.Rollback() }()
	if err := impl.lockBlobLifecycleTx(orderCtx, persistOrderTx, gcBlobID); err != nil {
		t.Fatalf("真实 PostgreSQL Persist advisory blob lock 失败: %v", err)
	}
	var lockedBlobID string
	if err := persistOrderTx.QueryRowContext(orderCtx, `UPDATE juhe_dataset.audit_payload_blobs SET last_seen_at=last_seen_at WHERE id=$1 RETURNING id`, gcBlobID).Scan(&lockedBlobID); err != nil {
		t.Fatalf("retention 候选扫描不得阻塞 Persist advisory 后的 blob 元数据更新: %v", err)
	}
	if lockedBlobID != gcBlobID {
		t.Fatalf("真实 PostgreSQL Persist 锁定错误 blob: got=%s want=%s", lockedBlobID, gcBlobID)
	}
	if err := persistOrderTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := impl.scheduleUnreferencedBlobGC(ctx, gcScheduleTx, gcRows); err != nil {
		t.Fatalf("真实 PostgreSQL GC 排程失败: %v", err)
	}
	if err := impl.verifyLeaseBeforeCommit(ctx, gcScheduleTx, successor); err != nil {
		t.Fatal(err)
	}
	if err := gcScheduleTx.Commit(); err != nil {
		t.Fatalf("真实 PostgreSQL GC 排程提交失败: %v", err)
	}
	var scheduled int
	if err := impl.db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.audit_payload_blob_gc WHERE blob_id=$1 AND storage_key=$2`, gcBlobID, gcStorageKey).Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if scheduled != 1 {
		t.Fatalf("真实 PostgreSQL GC locator 未持久化: %d", scheduled)
	}

	gcNew := fixture(runID+"-gc-new", LifecycleFinalized)
	gcNew.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayResponse, ContentType: "text/plain", Body: PayloadBody{Bytes: []byte("postgres gc interleave"), Present: true}}}
	// 换 Path 拉开错误指纹：同指纹同窗口会被错误组去重"出生即降级"（正文不
	// 落盘、无新引用），本段的 GC 交错断言依赖新引用真实物化。
	gcNew.Path = "/v1/gc-interleave-new"
	interleaveCtx, cancelInterleave := context.WithTimeout(ctx, 15*time.Second)
	defer cancelInterleave()
	startInterleave := make(chan struct{})
	persistInterleave := make(chan error, 1)
	cleanupInterleave := make(chan error, 1)
	go func() {
		<-startInterleave
		_, err := store.Persist(interleaveCtx, successor, gcNew)
		persistInterleave <- err
	}()
	go func() {
		<-startInterleave
		_, err := impl.cleanupScheduledBlobFile(interleaveCtx, successor, pendingBlobGCRow{blobID: gcBlobID, storageKey: gcStorageKey})
		cleanupInterleave <- err
	}()
	close(startInterleave)
	if err := <-persistInterleave; err != nil {
		t.Fatalf("真实 PostgreSQL GC 交错 Persist 失败: %v", err)
	}
	if err := <-cleanupInterleave; err != nil {
		t.Fatalf("真实 PostgreSQL GC 交错 cleanup 失败: %v", err)
	}
	gcPath := filepath.Join(blobRoot, filepath.FromSlash(gcStorageKey))
	if _, err := os.Stat(gcPath); err != nil {
		t.Fatalf("真实 PostgreSQL GC 交错后新引用缺少物理文件: %v", err)
	}
	var survivingMetadata, survivingRefs, remainingLocator int
	if err := impl.db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.audit_payload_blobs b JOIN juhe_dataset.audit_payload_refs r ON r.body_blob_id=b.id WHERE b.id=$1 AND r.audit_log_id=$2`, gcBlobID, gcNew.ID).Scan(&survivingMetadata); err != nil {
		t.Fatal(err)
	}
	if err := impl.db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1 AND body_blob_id=$2`, gcNew.ID, gcBlobID).Scan(&survivingRefs); err != nil {
		t.Fatal(err)
	}
	if err := impl.db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.audit_payload_blob_gc WHERE blob_id=$1`, gcBlobID).Scan(&remainingLocator); err != nil {
		t.Fatal(err)
	}
	if survivingMetadata != 1 || survivingRefs != 1 || remainingLocator != 0 {
		t.Fatalf("真实 PostgreSQL GC 交错后状态 metadata=%d refs=%d locator=%d", survivingMetadata, survivingRefs, remainingLocator)
	}

	// A physical delete error must not make the orphan untraceable: the blob
	// metadata and the durable GC locator stay available for a later pass.
	failureBlobID := runID + "-gc-delete-failure"
	failureStorageKey := "smoke-delete-failure.blob"
	failurePath := filepath.Join(blobRoot, failureStorageKey)
	if err := os.MkdirAll(failurePath, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(failurePath, "held"), []byte("block deletion"), 0o640); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := impl.db.ExecContext(ctx, `INSERT INTO juhe_dataset.audit_payload_blobs (id,sha256,raw_size_bytes,compressed_size_bytes,content_type,storage_key,first_seen_at,last_seen_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$7,$7)`, failureBlobID, runID+"-gc-delete-failure-hash", 1, 1, "text/plain", failureStorageKey, now); err != nil {
		t.Fatalf("写入真实 PostgreSQL 失败删除 blob metadata 失败: %v", err)
	}
	failureScheduleTx, err := impl.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := impl.verifyLeaseTx(ctx, failureScheduleTx, successor); err != nil {
		t.Fatal(err)
	}
	if err := impl.scheduleUnreferencedBlobGC(ctx, failureScheduleTx, []retentionBlobRow{{id: failureBlobID, storageKey: failureStorageKey}}); err != nil {
		t.Fatalf("排程真实 PostgreSQL 失败删除 blob 失败: %v", err)
	}
	if err := impl.verifyLeaseBeforeCommit(ctx, failureScheduleTx, successor); err != nil {
		t.Fatal(err)
	}
	if err := failureScheduleTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := impl.cleanupScheduledBlobFile(ctx, successor, pendingBlobGCRow{blobID: failureBlobID, storageKey: failureStorageKey}); err == nil || !strings.Contains(err.Error(), "删除 F3 audit blob 文件失败") {
		t.Fatalf("真实 PostgreSQL GC 文件删除失败错误=%v", err)
	}
	var retainedMetadata, retainedLocator int
	if err := impl.db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.audit_payload_blobs WHERE id=$1`, failureBlobID).Scan(&retainedMetadata); err != nil {
		t.Fatal(err)
	}
	if err := impl.db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.audit_payload_blob_gc WHERE blob_id=$1 AND storage_key=$2`, failureBlobID, failureStorageKey).Scan(&retainedLocator); err != nil {
		t.Fatal(err)
	}
	if retainedMetadata != 1 || retainedLocator != 1 {
		t.Fatalf("真实 PostgreSQL GC 删除失败未保留可重试状态 metadata=%d locator=%d", retainedMetadata, retainedLocator)
	}
}

func TestPostgresRetentionSameIDPersistLockOrderSmokeWhenExplicitlyConfigured(t *testing.T) {
	postgresURL := strings.TrimSpace(os.Getenv("JUHE_AI_AUDIT_LOG_TEST_POSTGRES_URL"))
	if postgresURL == "" {
		t.Skip("未设置 JUHE_AI_AUDIT_LOG_TEST_POSTGRES_URL；跳过真实 PostgreSQL same-ID retention/Persist 锁序 smoke")
	}
	assertDestructivePostgresSmokeTarget(t, postgresURL)
	blobRoot := t.TempDir()
	store, err := OpenStore(Config{PostgresURL: postgresURL, PostgresMaxOpenConns: 4, PostgresMaxIdleConns: 2, PayloadBlobDirectory: blobRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	impl := store.(*sqlStore)
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	assertEmptyPostgresF3Tables(t, ctx, impl.db)
	defer func() {
		cleanupPostgresSmokeRows(t, ctx, impl.db)
		if err := os.RemoveAll(blobRoot); err != nil {
			t.Errorf("清理 PostgreSQL same-ID smoke 测试 blob 目录失败: %v", err)
		}
	}()

	runID := "f3pgsameid" + strconv.FormatInt(time.Now().UnixNano(), 10)
	lease, acquired, err := store.AcquireOwnerLease(ctx, runID+"-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("真实 PostgreSQL same-ID smoke 获取 lease 失败: acquired=%v err=%v", acquired, err)
	}
	old := fixture(runID+"-audit", LifecycleFinalized)
	old.CreatedAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	old.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayResponse, ContentType: "text/plain", Body: PayloadBody{Bytes: []byte("same audit lifecycle blob"), Present: true}}}
	if _, err := store.Persist(ctx, lease, old); err != nil {
		t.Fatalf("真实 PostgreSQL same-ID 旧审计写入失败: %v", err)
	}
	var blobID, storageKey string
	if err := impl.db.QueryRowContext(ctx, `SELECT b.id,b.storage_key FROM juhe_dataset.audit_payload_blobs b JOIN juhe_dataset.audit_payload_refs r ON r.body_blob_id=b.id WHERE r.audit_log_id=$1`, old.ID).Scan(&blobID, &storageKey); err != nil {
		t.Fatalf("读取真实 PostgreSQL same-ID blob 失败: %v", err)
	}

	retentionTx, err := impl.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := impl.verifyLeaseTx(ctx, retentionTx, lease); err != nil {
		_ = retentionTx.Rollback()
		t.Fatal(err)
	}
	_, candidates, err := impl.deleteRetentionChildren(ctx, retentionTx, []string{old.ID}, true)
	if err != nil {
		_ = retentionTx.Rollback()
		t.Fatalf("真实 PostgreSQL same-ID retention 删除子行失败: %v", err)
	}
	if _, err := impl.deleteAuditLogRows(ctx, retentionTx, []string{old.ID}); err != nil {
		_ = retentionTx.Rollback()
		t.Fatalf("真实 PostgreSQL same-ID retention 删除父行失败: %v", err)
	}
	rows, err := impl.unreferencedBlobRows(ctx, retentionTx, candidates, 10)
	if err != nil {
		_ = retentionTx.Rollback()
		t.Fatalf("真实 PostgreSQL same-ID retention 查询候选失败: %v", err)
	}
	if len(rows) != 1 || rows[0].id != blobID {
		_ = retentionTx.Rollback()
		t.Fatalf("真实 PostgreSQL same-ID retention 候选=%+v，want blob=%s", rows, blobID)
	}

	// Retention now owns the audit-ID gate and its row locks, but intentionally
	// has not taken the blob lock. Persist must be waiting on that audit gate,
	// leaving the blob advisory lock available for retention's GC scheduling.
	interleaveCtx, cancelInterleave := context.WithTimeout(ctx, 15*time.Second)
	defer cancelInterleave()
	newInput := fixture(old.ID, LifecycleFinalized)
	newInput.Payloads = []AuditLogPayloadInput{{PartType: PayloadPartGatewayResponse, ContentType: "text/plain", Body: PayloadBody{Bytes: []byte("same audit lifecycle blob"), Present: true}}}
	type persistOutcome struct {
		result PersistResult
		err    error
	}
	persisted := make(chan persistOutcome, 1)
	go func() {
		result, err := store.Persist(interleaveCtx, lease, newInput)
		persisted <- persistOutcome{result: result, err: err}
	}()
	if err := waitForPostgresBlockedPersist(interleaveCtx, impl.db); err != nil {
		_ = retentionTx.Rollback()
		outcome := <-persisted
		t.Fatalf("真实 PostgreSQL same-ID Persist 未进入锁等待: wait=%v persist=%+v", err, outcome)
	}
	probeTx, err := impl.db.BeginTx(interleaveCtx, nil)
	if err != nil {
		_ = retentionTx.Rollback()
		outcome := <-persisted
		t.Fatalf("开始 PostgreSQL blob 锁探测事务失败: %v; Persist=%+v", err, outcome)
	}
	var blobLockAvailable bool
	err = probeTx.QueryRowContext(interleaveCtx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1, $2::bigint))`, blobID, postgresBlobLifecycleLockSeed).Scan(&blobLockAvailable)
	rollbackProbeErr := probeTx.Rollback()
	if err != nil || rollbackProbeErr != nil {
		_ = retentionTx.Rollback()
		outcome := <-persisted
		t.Fatalf("探测 PostgreSQL same-ID blob 锁失败: query=%v rollback=%v persist=%+v", err, rollbackProbeErr, outcome)
	}
	if !blobLockAvailable {
		_ = retentionTx.Rollback()
		outcome := <-persisted
		t.Fatalf("same-ID Persist 在等待 retention audit 行时已持有 blob advisory，锁序回归为 blob->audit: %+v", outcome)
	}
	if err := impl.scheduleUnreferencedBlobGC(interleaveCtx, retentionTx, rows); err != nil {
		_ = retentionTx.Rollback()
		outcome := <-persisted
		t.Fatalf("真实 PostgreSQL same-ID retention GC 排程失败: %v; Persist=%+v", err, outcome)
	}
	if err := impl.verifyLeaseBeforeCommit(interleaveCtx, retentionTx, lease); err != nil {
		_ = retentionTx.Rollback()
		outcome := <-persisted
		t.Fatalf("真实 PostgreSQL same-ID retention 提交 fence 失败: %v; Persist=%+v", err, outcome)
	}
	if err := retentionTx.Commit(); err != nil {
		outcome := <-persisted
		t.Fatalf("真实 PostgreSQL same-ID retention 提交失败: %v; Persist=%+v", err, outcome)
	}
	outcome := <-persisted
	if outcome.err != nil || outcome.result.Ignored {
		t.Fatalf("真实 PostgreSQL delayed same-ID Persist 丢失输入: result=%+v err=%v", outcome.result, outcome.err)
	}
	replay, err := store.Persist(ctx, lease, newInput)
	if err != nil || !replay.Ignored {
		t.Fatalf("真实 PostgreSQL duplicate same-ID Persist 结果=%+v err=%v", replay, err)
	}
	var auditRows, refs, locators int
	if err := impl.db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.audit_logs WHERE id=$1`, newInput.ID).Scan(&auditRows); err != nil {
		t.Fatal(err)
	}
	if err := impl.db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.audit_payload_refs WHERE audit_log_id=$1 AND body_blob_id=$2`, newInput.ID, blobID).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if err := impl.db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.audit_payload_blob_gc WHERE blob_id=$1`, blobID).Scan(&locators); err != nil {
		t.Fatal(err)
	}
	if auditRows != 1 || refs != 1 || locators != 0 {
		t.Fatalf("真实 PostgreSQL same-ID 最终状态 audit=%d refs=%d gc=%d", auditRows, refs, locators)
	}
	if _, err := os.Stat(filepath.Join(blobRoot, filepath.FromSlash(storageKey))); err != nil {
		t.Fatalf("真实 PostgreSQL same-ID 交错后 canonical blob 丢失: %v", err)
	}
}

func waitForPostgresBlockedPersist(ctx context.Context, db *sql.DB) error {
	delay := 10 * time.Millisecond
	for {
		var blocked bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname = current_database()
				AND pid <> pg_backend_pid()
				AND wait_event_type = 'Lock'
				AND (query LIKE '%pg_advisory_xact_lock%' OR query LIKE '%audit_logs%')
		)`).Scan(&blocked)
		if err != nil {
			return err
		}
		if blocked {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
			if delay < 250*time.Millisecond {
				delay *= 2
			}
		}
	}
}

const postgresSmokeDestructiveToken = "I_UNDERSTAND_THIS_EMPTY_DB_IS_DISPOSABLE"

var postgresSmokeDatabaseName = regexp.MustCompile(`^juhe_ai_sub2api_dev_f3_smoke_[a-z0-9_]+$`)

func TestPostgresSmokeDatabaseWhitelist(t *testing.T) {
	for databaseName, allowed := range map[string]bool{
		"juhe_ai_sub2api_dev_f3_smoke_contract_01": true,
		"juhe_ai_sub2api_dev_f3_smoke_a":           true,
		"juhe_ai_sub2api_dev_f3_smoke_":            false,
		"juhe_ai_f3_smoke_contract_01":             false,
		"juhe_ai_sub2api_dev_f3_smoke_Upper":       false,
		"juhe_ai_sub2api_dev_f3_smoke_dot.name":    false,
		"juhe_ai_sub2api_dev_f3_smoke_name-extra":  false,
	} {
		if got := postgresSmokeDatabaseName.MatchString(databaseName); got != allowed {
			t.Fatalf("PostgreSQL smoke database whitelist mismatch: name=%q got=%v want=%v", databaseName, got, allowed)
		}
	}
}

func assertDestructivePostgresSmokeTarget(t *testing.T, postgresURL string) {
	t.Helper()
	if os.Getenv("JUHE_AI_AUDIT_LOG_TEST_POSTGRES_DESTRUCTIVE") != postgresSmokeDestructiveToken {
		t.Fatalf("真实 PostgreSQL smoke 需要 JUHE_AI_AUDIT_LOG_TEST_POSTGRES_DESTRUCTIVE=%q；未获明确可销毁空库授权，不写入", postgresSmokeDestructiveToken)
	}
	parsed, err := url.Parse(postgresURL)
	if err != nil {
		t.Fatalf("真实 PostgreSQL smoke URL 非法: %v", err)
	}
	databaseName := strings.TrimPrefix(strings.TrimSpace(parsed.Path), "/")
	if !postgresSmokeDatabaseName.MatchString(databaseName) {
		t.Fatalf("真实 PostgreSQL smoke 仅允许可销毁库名 juhe_ai_sub2api_dev_f3_smoke_<name>（<name> 仅小写字母、数字或下划线且非空），当前为 %q", databaseName)
	}
}

var postgresF3Tables = []string{"audit_log_owner_leases", "audit_logs", "audit_log_attempts", "audit_payload_blobs", "audit_payload_refs", "audit_error_groups", "audit_payload_blob_gc"}

func assertEmptyPostgresF3Tables(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, table := range postgresF3Tables {
		var name sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT to_regclass($1)::text`, "juhe_dataset."+table).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !name.Valid {
			continue
		}
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.`+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("真实 PostgreSQL smoke 目标库的 F3 表 %s 非空（%d 行）；拒绝写入", table, count)
		}
	}
}

func assertPostgresCollatedPrefixIndexes(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for index, column := range map[string]string{
		"idx_audit_logs_system_trace_created":     "trace_id",
		"idx_audit_logs_system_client_ip_created": "client_ip",
	} {
		var definition string
		if err := db.QueryRowContext(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname='juhe_dataset' AND indexname=$1`, index).Scan(&definition); err != nil {
			t.Fatalf("读取 PostgreSQL fresh schema 索引 %s 失败: %v", index, err)
		}
		if !strings.Contains(definition, column+` COLLATE "C"`) {
			t.Fatalf("PostgreSQL fresh schema 索引 %s 缺少 %s COLLATE C: %s", index, column, definition)
		}
	}
}

func cleanupPostgresSmokeRows(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"audit_payload_blob_gc", "audit_logs", "audit_error_groups", "audit_payload_blobs", "audit_log_owner_leases"} {
		if _, err := db.ExecContext(ctx, `DELETE FROM juhe_dataset.`+table); err != nil {
			t.Errorf("清理 PostgreSQL smoke F3 表 %s 失败: %v", table, err)
		}
	}
	for _, table := range postgresF3Tables {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM juhe_dataset.`+table).Scan(&count); err != nil {
			t.Errorf("核验 PostgreSQL smoke F3 表 %s 残留失败: %v", table, err)
			continue
		}
		if count != 0 {
			t.Errorf("PostgreSQL smoke 清理后 F3 表 %s 仍有 %d 行", table, count)
		}
	}
}

// loadConfigBaseEnv 提供 LoadConfig 全链测试的最小 env 基座（存储形态收口后
// 仅含 PG 连接串、实例 ID 与 blob 目录文件面）。
func loadConfigBaseEnv(root string) map[string]string {
	return map[string]string{
		"JUHE_AI_POSTGRES_URL":                    "postgres://f3-test-main",
		"JUHE_AI_AUDIT_LOG_INSTANCE_ID":           "f3-test-instance",
		"JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY":        filepath.Join(root, "audit-blobs"),
		"JUHE_AI_AUDIT_LOG_BUSINESS_SETTINGS_URL": "postgres://f3-test-main",
	}
}

func acquireLease(t *testing.T, store Store) OwnerLease {
	t.Helper()
	lease, acquired, err := store.AcquireOwnerLease(context.Background(), "test-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire owner lease: acquired=%v err=%v", acquired, err)
	}
	return lease
}

func fixture(id string, lifecycle LifecycleStatus) AuditLogInput {
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	return AuditLogInput{ID: id, LifecycleStatus: lifecycle, TraceID: "trace-" + id, TrafficSource: TrafficSourceGateway, Method: "POST", Path: "/v1/responses", AuditOutcome: AuditOutcomeGatewaySucceeded, Success: true, SampleBucket: 1, SampleReason: "test", StartedAt: now, EndedAt: now, CreatedAt: now}
}

func intPointer(value int) *int { return &value }

var _ = sql.ErrNoRows
