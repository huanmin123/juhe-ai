package accounthealth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Credential envelope crypto arms
// ---------------------------------------------------------------------------

func TestW9HCredentialEnvelopeArms(t *testing.T) {
	if _, err := EncryptV1Envelope("  ", []byte("x")); err == nil || !strings.Contains(err.Error(), "secret 不能为空") {
		t.Fatalf("empty secret err=%v", err)
	}
	if _, err := DecryptV1Envelope("s", "v1:only:three"); err == nil || !strings.Contains(err.Error(), "不支持的凭据 envelope 格式") {
		t.Fatalf("bad shape err=%v", err)
	}
	if _, err := DecryptV1Envelope("s", "v2:a:b:c"); err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("bad version err=%v", err)
	}
	if _, err := DecryptV1Envelope("s", "v1:!!!:!!!:!!!"); err == nil {
		t.Fatal("bad base64 must fail")
	}
	// Round trip proves the secret/key derivation; a wrong secret must fail.
	sealed, err := EncryptV1Envelope("secret-w9h", []byte(`{"k":"v"}`))
	if err != nil {
		t.Fatalf("seal err=%v", err)
	}
	plaintext, err := DecryptV1Envelope("secret-w9h", sealed)
	if err != nil || string(plaintext) != `{"k":"v"}` {
		t.Fatalf("round trip=%q err=%v", plaintext, err)
	}
	if _, err := DecryptV1Envelope("other-secret", sealed); err == nil {
		t.Fatal("wrong secret must fail")
	}
}

// ---------------------------------------------------------------------------
// Quota limits
// ---------------------------------------------------------------------------

func TestW9HParseDirectQuotaLimitsArms(t *testing.T) {
	if _, err := ParseDirectQuotaLimits("{bad"); err == nil || !strings.Contains(err.Error(), "解析 authorization limits 失败") {
		t.Fatalf("bad json err=%v", err)
	}
	if _, err := ParseDirectQuotaLimits(`{"daily":{"enabled":true,"limit":-1}}`); err == nil || !strings.Contains(err.Error(), "quota limit 无效") {
		t.Fatalf("negative limit err=%v", err)
	}
	if _, err := ParseDirectQuotaLimits(`{"hourly":{"enabled":true,"limit":5,"hours":0}}`); err == nil || !strings.Contains(err.Error(), "hourly quota limit 无效") {
		t.Fatalf("hourly hours err=%v", err)
	}
	limits, err := ParseDirectQuotaLimits(`{"daily":{"enabled":true,"limit":10},"weekly":{"enabled":false,"limit":0},"total":{"enabled":true,"limit":100}}`)
	if err != nil || limits.Daily == nil || limits.Total == nil || limits.Weekly == nil {
		t.Fatalf("limits=%+v err=%v", limits, err)
	}
	if DirectQuotaExceeded(limits, DirectQuotaCosts{Daily: 9.9, Total: 99.9}) {
		t.Fatal("below-limit costs must not exceed")
	}
	if !DirectQuotaExceeded(limits, DirectQuotaCosts{Daily: 1, Total: 100}) {
		t.Fatal("total at limit must exceed")
	}
	if DirectQuotaExceeded(DirectQuotaLimits{}, DirectQuotaCosts{Total: 9999}) {
		t.Fatal("no limits never exceeds")
	}
}

// ---------------------------------------------------------------------------
// Probe request file validation
// ---------------------------------------------------------------------------

const w9hCapabilityHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func validW9HProbeRequest() ProbeRequest {
	return ProbeRequest{
		RequestID: "req-w9h", AccountID: "acc-w9h", Reason: "manual",
		InputVersion: 1, ConfigRevision: 1, DispatchRevision: 1, Deadline: time.Now().Add(time.Hour),
	}
}

func TestW9HValidateProbeRequestArms(t *testing.T) {
	if err := validateProbeRequest(validW9HProbeRequest()); err != nil {
		t.Fatalf("valid request err=%v", err)
	}
	broken := validW9HProbeRequest()
	broken.Reason = " "
	if err := validateProbeRequest(broken); err == nil || !strings.Contains(err.Error(), "缺少 ID、account 或 reason") {
		t.Fatalf("missing reason err=%v", err)
	}
	broken = validW9HProbeRequest()
	broken.InputVersion = 0
	if err := validateProbeRequest(broken); err == nil || !strings.Contains(err.Error(), "fence 或 deadline 无效") {
		t.Fatalf("bad fence err=%v", err)
	}
	withSource := validW9HProbeRequest()
	withSource.SourceFence = &SourceFence{StateKey: "", SourceFenceID: "f", RuntimeKey: "r", AccountID: "acc-w9h", ConfigRevision: 1, SourceGeneration: 1, ProbeGeneration: 1}
	if err := validateProbeRequest(withSource); err == nil || !strings.Contains(err.Error(), "source fence 无效") {
		t.Fatalf("bad source fence err=%v", err)
	}
	withKeyModel := validW9HProbeRequest()
	withKeyModel.KeyModelFence = &KeyModelFence{
		CapabilityHash: "SHORT", KeyFingerprint: "fp", OwnerID: "owner", DispatchRevision: 1,
	}
	if err := validateProbeRequest(withKeyModel); err == nil || !strings.Contains(err.Error(), "key-model fence 无效") {
		t.Fatalf("bad key-model fence err=%v", err)
	}
	withKeyModel.KeyModelFence.CapabilityHash = strings.ToUpper(w9hCapabilityHash)
	if err := validateProbeRequest(withKeyModel); err == nil {
		t.Fatal("uppercase capability hash must fail")
	}
	if isCapabilityHash(w9hCapabilityHash) != true || isCapabilityHash("") || isCapabilityHash(w9hCapabilityHash[:63]) {
		t.Fatal("isCapabilityHash bounds")
	}
	if isCapabilityHash(w9hCapabilityHash[:63] + "g") {
		t.Fatal("non-hex tail must fail")
	}
}

// ---------------------------------------------------------------------------
// Signed input/request files
// ---------------------------------------------------------------------------

func w9hSign(t *testing.T, key []byte, keyID string, payload any) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(signedInputAlgorithm + "\n" + keyID + "\n"))
	_, _ = mac.Write(raw)
	envelope := SignedInputEnvelope{
		Algorithm: signedInputAlgorithm, KeyID: keyID,
		Payload:   base64.RawURLEncoding.EncodeToString(raw),
		Signature: base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestW9HVerifySignedPayloadArms(t *testing.T) {
	keys := map[string][]byte{"k1": []byte("key-w9h-00000000000000000000000000")}
	payload := map[string]string{"hello": "world"}
	signed := w9hSign(t, keys["k1"], "k1", payload)
	if _, err := VerifySignedPayload(signed, keys); err != nil {
		t.Fatalf("valid payload err=%v", err)
	}
	if _, err := VerifySignedPayload([]byte("not json"), keys); err == nil || !strings.Contains(err.Error(), "解析签名 envelope 失败") {
		t.Fatalf("bad envelope err=%v", err)
	}
	if _, err := VerifySignedPayload(signed, nil); err == nil || !strings.Contains(err.Error(), "key 不可用") {
		t.Fatalf("missing key err=%v", err)
	}
	// Tampered payload breaks the MAC.
	var envelope SignedInputEnvelope
	if err := json.Unmarshal(signed, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Payload = base64.RawURLEncoding.EncodeToString([]byte(`{"tampered":true}`))
	tampered, _ := json.Marshal(envelope)
	if _, err := VerifySignedPayload(tampered, keys); err == nil || !strings.Contains(err.Error(), "签名校验失败") {
		t.Fatalf("tampered err=%v", err)
	}
	envelope.Payload = "!!!"
	badPayload, _ := json.Marshal(envelope)
	if _, err := VerifySignedPayload(badPayload, keys); err == nil || !strings.Contains(err.Error(), "payload 编码无效") {
		t.Fatalf("bad payload encoding err=%v", err)
	}
	envelope.Payload = base64.RawURLEncoding.EncodeToString([]byte(`{}`))
	envelope.Signature = "!!!"
	badSignature, _ := json.Marshal(envelope)
	if _, err := VerifySignedPayload(badSignature, keys); err == nil || !strings.Contains(err.Error(), "signature 编码无效") {
		t.Fatalf("bad signature encoding err=%v", err)
	}
	// VerifySignedInput decodes the payload into the Input contract.
	input := Input{AccountID: "acc-w9h", InputVersion: 2}
	signedInput := w9hSign(t, keys["k1"], "k1", input)
	decoded, err := VerifySignedInput(signedInput, keys)
	if err != nil || decoded.AccountID != "acc-w9h" || decoded.InputVersion != 2 {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	if _, err := VerifySignedInput(w9hSign(t, keys["k1"], "k1", "not-an-object"), keys); err == nil || !strings.Contains(err.Error(), "解析 input payload 失败") {
		t.Fatalf("bad input payload err=%v", err)
	}
}

func TestW9HLoadSignedInputFilesArms(t *testing.T) {
	if _, err := LoadSignedInputFiles("  ", nil); err == nil || !strings.Contains(err.Error(), "目录缺失") {
		t.Fatalf("missing directory err=%v", err)
	}
	if _, err := LoadSignedInputFiles(filepath.Join(t.TempDir(), "absent"), nil); err == nil || !strings.Contains(err.Error(), "读取 account-health input 目录失败") {
		t.Fatalf("absent directory err=%v", err)
	}
	keys := map[string][]byte{"k1": []byte("key-w9h-00000000000000000000000000")}
	directory := t.TempDir()
	input := Input{AccountID: "acc-file", InputVersion: 7}
	w9hWrite(t, filepath.Join(directory, "acc.account-health-input.json"), w9hSign(t, keys["k1"], "k1", input))
	// An unrelated file is ignored.
	w9hWrite(t, filepath.Join(directory, "notes.txt"), []byte("ignore me"))
	inputs, err := LoadSignedInputFiles(directory, keys)
	if err != nil || len(inputs) != 1 || inputs[0].AccountID != "acc-file" {
		t.Fatalf("inputs=%+v err=%v", inputs, err)
	}
	// A corrupt signed file surfaces as a per-file error.
	w9hWrite(t, filepath.Join(directory, "bad.account-health-input.json"), []byte("{"))
	if _, err := LoadSignedInputFiles(directory, keys); err == nil {
		t.Fatal("corrupt file must fail")
	}
}

func w9hWrite(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Executor early-return arms
// ---------------------------------------------------------------------------

func TestW9HExecuteInputProbeEarlyArms(t *testing.T) {
	store, lease := openSQLiteStoreWithLease(t)
	ctx := context.Background()
	base := Input{AccountID: "w9h-exec-acc", InputVersion: 3, ConfigRevision: 2, DispatchRevision: 5, Type: "api_key"}
	request := ProbeRequest{RequestID: "req-w9h-exec", AccountID: "w9h-exec-acc", Reason: "cycle", InputVersion: 3, ConfigRevision: 2, DispatchRevision: 5, Deadline: time.Now().Add(time.Hour)}

	// Fence mismatch.
	mismatch := request
	mismatch.ConfigRevision = 9
	outcome, err := ExecuteInputProbe(ctx, store, lease, base, mismatch, ProbeOptions{})
	if err != nil || outcome.ErrorCode != "request_fence_invalid" {
		t.Fatalf("fence outcome=%+v err=%v", outcome, err)
	}
	// Elapsed deadline.
	expired := request
	expired.Deadline = time.Now().Add(-time.Minute)
	outcome, err = ExecuteInputProbe(ctx, store, lease, base, expired, ProbeOptions{})
	if err != nil || outcome.ErrorCode != "request_deadline_elapsed" {
		t.Fatalf("deadline outcome=%+v err=%v", outcome, err)
	}
	// OAuth input without an access envelope.
	oauth := base
	oauth.Type = "oauth"
	outcome, err = ExecuteInputProbe(ctx, store, lease, oauth, request, ProbeOptions{})
	if err != nil || outcome.ErrorCode != "oauth_access_missing" {
		t.Fatalf("oauth outcome=%+v err=%v", outcome, err)
	}
	// api_key input with an empty pool.
	outcome, err = ExecuteInputProbe(ctx, store, lease, base, request, ProbeOptions{})
	if err != nil || outcome.ErrorCode != "api_key_pool_missing" {
		t.Fatalf("api key outcome=%+v err=%v", outcome, err)
	}
	// newOutcomeID uniqueness.
	first, second := newOutcomeID(), newOutcomeID()
	if first == "" || first == second {
		t.Fatalf("outcome ids %q %q", first, second)
	}
}

// ---------------------------------------------------------------------------
// Projector cursor arms over the sqlite business fixture
// ---------------------------------------------------------------------------

func TestW9HProjectorCursorArms(t *testing.T) {
	fixture := newProjectionFixture(t)
	ctx := context.Background()

	// Empty database: no cursor.
	if cursor, err := fixture.projector.loadCursor(ctx); err != nil || cursor != nil {
		t.Fatalf("empty cursor=%+v err=%v", cursor, err)
	}
	// Two textual renderings of the same instant (Node writes millisecond
	// precision, Go writes nanosecond): the cursor arm canonicalizes in place.
	next := w9hCursor("2026-09-06T12:00:00.000Z", "w9h-out-1")
	// First advance inserts.
	if updated, err := fixture.projector.advanceCursor(ctx, next); err != nil || !updated {
		t.Fatalf("first advance=%t err=%v", updated, err)
	}
	precision := w9hCursor("2026-09-06T12:00:00Z", "w9h-out-1")
	if updated, err := fixture.projector.advanceCursor(ctx, precision); err != nil || !updated {
		t.Fatalf("precision advance=%t err=%v", updated, err)
	}
	if updated, err := fixture.projector.advanceCursor(ctx, precision); err != nil || updated {
		t.Fatalf("idempotent advance=%t err=%v", updated, err)
	}
	// A re-anchored cursor loads with the storage-observed timestamp.
	cursor, err := fixture.projector.loadCursor(ctx)
	if err != nil || cursor == nil || cursor.OutcomeID != "w9h-out-1" {
		t.Fatalf("load cursor=%+v err=%v", cursor, err)
	}
	// A newer position overwrites.
	newer := w9hCursor("2026-09-06T12:01:00Z", "w9h-out-2")
	if updated, err := fixture.projector.advanceCursor(ctx, newer); err != nil || !updated {
		t.Fatalf("newer advance=%t err=%v", updated, err)
	}
	// An older position stays behind.
	if updated, err := fixture.projector.advanceCursor(ctx, next); err != nil || updated {
		t.Fatalf("older advance=%t err=%v", updated, err)
	}
	// 不可达分支登记：游标"半空存储损坏"臂（observed_at/outcome_id 恰一为
	// NULL）被表级 CHECK 约束 (observed_at IS NULL AND outcome_id IS NULL)
	// OR (... NOT NULL ...) 阻断，只能由绕过约束的外部写入触发；显式跳过。
	// normalizeProjectionCursor rejects junk timestamps.
	if _, err := normalizeProjectionCursor("junk", "out"); err == nil {
		t.Fatal("junk timestamp must fail")
	}
	// resolveCursorStorage: missing outcome anchors empty.
	if resolved, err := fixture.projector.resolveCursorStorage(ctx, "absent"); err != nil || resolved != "" {
		t.Fatalf("absent resolve=%q err=%v", resolved, err)
	}
	// The projection cursor storage query covers both dialects.
	if got := projectionCursorStorageQuery(StorePostgres); !strings.Contains(got, "juhe_jobs.account_health_outcomes") {
		t.Fatalf("pg query=%q", got)
	}
	if got := projectionCursorStorageQuery(StoreSQLite); !strings.Contains(got, "FROM account_health_outcomes") {
		t.Fatalf("sqlite query=%q", got)
	}
}

// ---------------------------------------------------------------------------
// Config loading matrix
// ---------------------------------------------------------------------------

func w9hValidConfigEnv(storeMode, storePath, inputDir string) map[string]string {
	return map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":        "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":       "inst-w9h",
		"JUHE_AI_ACCOUNT_HEALTH_STORE":             storeMode,
		"JUHE_AI_ACCOUNT_HEALTH_DATABASE_PATH":     storePath,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":   inputDir,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY": base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET": "secret-w9h",
	}
}

func TestW9HLoadConfigMatrix(t *testing.T) {
	inputDir := t.TempDir()
	storePath := filepath.Join(t.TempDir(), "j1.sqlite3")

	load := func(env map[string]string) (Config, error) {
		return LoadConfig(func(key string) string { return env[key] })
	}
	// Explicit non-go owner is rejected (default/absent owner means go now).
	env := w9hValidConfigEnv("sqlite", storePath, inputDir)
	env["JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER"] = "node"
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "仅支持 go") {
		t.Fatalf("owner err=%v", err)
	}
	// Bad store mode.
	env = w9hValidConfigEnv("redis", storePath, inputDir)
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "sqlite 或 postgres") {
		t.Fatalf("store err=%v", err)
	}
	// sqlite mode without a path derives <DATA_DIR>/account-health.sqlite3
	// (2026-09-19 zero-config decision); DATA_DIR points at a temp dir so the
	// derived store stays on the same volume as the input directory.
	dataDir := t.TempDir()
	env = w9hValidConfigEnv("sqlite", "", inputDir)
	env["JUHE_AI_DATA_DIR"] = dataDir
	cfg, err := load(env)
	if err != nil {
		t.Fatalf("sqlite path err=%v", err)
	}
	if expected := filepath.Join(dataDir, "account-health.sqlite3"); cfg.Store.DatabasePath != expected {
		t.Fatalf("derived store path = %q, want %q", cfg.Store.DatabasePath, expected)
	}
	// postgres mode without a URL.
	env = w9hValidConfigEnv("postgres", storePath, inputDir)
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "POSTGRES_URL") {
		t.Fatalf("pg url err=%v", err)
	}
	// postgres mode with pool bounds violating the platform idle ceiling.
	env = w9hValidConfigEnv("postgres", storePath, inputDir)
	env["JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL"] = "postgres://example"
	env["JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_OPEN_CONNS"] = "4"
	env["JUHE_AI_ACCOUNT_HEALTH_POSTGRES_MAX_IDLE_CONNS"] = "9"
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "连接池配置无效") {
		t.Fatalf("pg pool err=%v", err)
	}
	// Missing input directory derives <DATA_DIR>/account-health-input and is
	// created on load (2026-09-19 zero-config decision).
	dataDir = t.TempDir()
	env = w9hValidConfigEnv("sqlite", storePath, "")
	env["JUHE_AI_DATA_DIR"] = dataDir
	cfg, err = load(env)
	if err != nil {
		t.Fatalf("input dir err=%v", err)
	}
	expectedInputDir := filepath.Join(dataDir, "account-health-input")
	if cfg.InputDirectory != expectedInputDir {
		t.Fatalf("derived input dir = %q, want %q", cfg.InputDirectory, expectedInputDir)
	}
	if _, statErr := os.Stat(expectedInputDir); statErr != nil {
		t.Fatalf("derived input dir must be created: %v", statErr)
	}
	// Store file inside the input directory is rejected.
	env = w9hValidConfigEnv("sqlite", filepath.Join(inputDir, "j1.sqlite3"), inputDir)
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "不得放入 input 目录") {
		t.Fatalf("isolation err=%v", err)
	}
	// Store file colliding with a business SQLite path is rejected.
	env = w9hValidConfigEnv("sqlite", storePath, inputDir)
	env["JUHE_AI_DATABASE_PATH"] = storePath
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "共用 SQLite 文件") {
		t.Fatalf("collision err=%v", err)
	}
	// Bad signing key material.
	env = w9hValidConfigEnv("sqlite", storePath, inputDir)
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY"] = "short"
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "SIGNING_KEY") {
		t.Fatalf("signing key err=%v", err)
	}
	// Missing credential secret（production；非生产回退开发密钥）。
	env = w9hValidConfigEnv("sqlite", storePath, inputDir)
	env["JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET"] = ""
	env["NODE_ENV"] = "production"
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "CREDENTIAL_SECRET") {
		t.Fatalf("credential secret err=%v", err)
	}
	// Owner lease must exceed the probe timeout.
	env = w9hValidConfigEnv("sqlite", storePath, inputDir)
	env["JUHE_AI_ACCOUNT_HEALTH_OWNER_LEASE"] = "15s"
	env["JUHE_AI_ACCOUNT_HEALTH_PROBE_TIMEOUT"] = "15s"
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "必须大于") {
		t.Fatalf("lease vs timeout err=%v", err)
	}
	// PG direct input requires the postgres jobs store.
	env = w9hValidConfigEnv("sqlite", storePath, inputDir)
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE"] = "postgres"
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "只允许与 postgres jobs store") {
		t.Fatalf("direct input store err=%v", err)
	}
	// PG direct input without a business URL.
	env = w9hValidConfigEnv("postgres", storePath, inputDir)
	env["JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL"] = "postgres://example"
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE"] = "postgres"
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "INPUT_POSTGRES_URL") {
		t.Fatalf("direct input url err=%v", err)
	}
	// The happy sqlite path parses every knob.
	cfg, err = load(w9hValidConfigEnv("sqlite", storePath, inputDir))
	if err != nil {
		t.Fatalf("valid sqlite config err=%v", err)
	}
	if cfg.InstanceID != "inst-w9h" || cfg.InputSource != "sqlite" {
		t.Fatalf("cfg=%+v", cfg)
	}
	// The happy postgres path accepts pool limits and the direct input source.
	env = w9hValidConfigEnv("postgres", storePath, inputDir)
	env["JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL"] = "postgres://example"
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE"] = "postgres"
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL"] = "postgres://business"
	cfg, err = load(env)
	if err != nil || cfg.BusinessPostgresURL != "postgres://business" {
		t.Fatalf("valid pg config=%+v err=%v", cfg, err)
	}
}

// ---------------------------------------------------------------------------
// store guard arms (sqlite)
// ---------------------------------------------------------------------------

func TestW9HStoreGuardArms(t *testing.T) {
	store, err := OpenStore(StoreConfig{Mode: StoreSQLite, DatabasePath: filepath.Join(t.TempDir(), "guard.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := store.AcquireOwnerLease(ctx, "w9h-guard-owner", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire: %v acquired=%t", err, acquired)
	}
	// verifyLease rejects a foreign lease (bad fence token).
	forged := lease
	forged.FenceToken = lease.FenceToken + 1
	if _, err := store.AppendOutcome(ctx, forged, Outcome{OutcomeID: "w9h-x"}); err == nil {
		t.Fatal("forged lease must fail")
	}
	// Renewing a forged lease fails.
	if renewed, err := store.RenewOwnerLease(ctx, forged, time.Minute); err != nil || renewed {
		t.Fatalf("forged renew=%t err=%v", renewed, err)
	}
	// AppendOutcome field validation.
	if _, err := store.AppendOutcome(ctx, lease, Outcome{}); err == nil || !strings.Contains(err.Error(), "缺少幂等或账户字段") {
		t.Fatalf("empty outcome err=%v", err)
	}
	// LoadCurrentState on an unknown account reads as absent.
	if _, found, err := store.LoadCurrentState(ctx, "w9h-absent"); err != nil || found {
		t.Fatalf("absent state found=%t err=%v", found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close err=%v", err)
	}
	var nilStore *Store
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil close err=%v", err)
	}
}

func w9hCursor(observedAtText, outcomeID string) *OutcomeCursor {
	cursor, err := normalizeProjectionCursor(observedAtText, outcomeID)
	if err != nil {
		panic(err)
	}
	return cursor
}
