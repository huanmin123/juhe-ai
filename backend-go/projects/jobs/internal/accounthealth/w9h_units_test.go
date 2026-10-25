package accounthealth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/exactkeyprobe"
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
	if _, err := exactkeyprobe.EncryptV1Envelope("  ", []byte("x")); err == nil || !strings.Contains(err.Error(), "secret 不能为空") {
		t.Fatalf("empty secret err=%v", err)
	}
	if _, err := exactkeyprobe.DecryptV1Envelope("s", "v1:only:three"); err == nil || !strings.Contains(err.Error(), "不支持的凭据 envelope 格式") {
		t.Fatalf("bad shape err=%v", err)
	}
	if _, err := exactkeyprobe.DecryptV1Envelope("s", "v2:a:b:c"); err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("bad version err=%v", err)
	}
	if _, err := exactkeyprobe.DecryptV1Envelope("s", "v1:!!!:!!!:!!!"); err == nil {
		t.Fatal("bad base64 must fail")
	}
	// Round trip proves the secret/key derivation; a wrong secret must fail.
	sealed, err := exactkeyprobe.EncryptV1Envelope("secret-w9h", []byte(`{"k":"v"}`))
	if err != nil {
		t.Fatalf("seal err=%v", err)
	}
	plaintext, err := exactkeyprobe.DecryptV1Envelope("secret-w9h", sealed)
	if err != nil || string(plaintext) != `{"k":"v"}` {
		t.Fatalf("round trip=%q err=%v", plaintext, err)
	}
	if _, err := exactkeyprobe.DecryptV1Envelope("other-secret", sealed); err == nil {
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
	input := exactkeyprobe.Input{AccountID: "acc-w9h", InputVersion: 2}
	signedInput := w9hSign(t, keys["k1"], "k1", input)
	decoded, err := VerifySignedInput(signedInput, keys)
	if err != nil || decoded.AccountID != "acc-w9h" || decoded.InputVersion != 2 {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	if _, err := VerifySignedInput(w9hSign(t, keys["k1"], "k1", "not-an-object"), keys); err == nil || !strings.Contains(err.Error(), "解析 input payload 失败") {
		t.Fatalf("bad input payload err=%v", err)
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

func w9hValidConfigEnv(_, _, inputDir string) map[string]string {
	// PG 唯一形态：store/input URL 必填（原 sqlite storeMode/storePath 参数
	// 保留签名以减少调用点扰动，一律忽略）。
	return map[string]string{
		"JUHE_AI_ACCOUNT_HEALTH_JOBS_OWNER":         "go",
		"JUHE_AI_ACCOUNT_HEALTH_INSTANCE_ID":        "inst-w9h",
		"JUHE_AI_ACCOUNT_HEALTH_POSTGRES_URL":       "postgres://w9h.invalid/jobs",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL": "postgres://w9h.invalid/business",
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_DIRECTORY":    inputDir,
		"JUHE_AI_ACCOUNT_HEALTH_INPUT_SIGNING_KEY":  base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
		"JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET":  "secret-w9h",
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
	// Bad input direct URL.
	env = w9hValidConfigEnv("", "", inputDir)
	env["JUHE_AI_ACCOUNT_HEALTH_INPUT_POSTGRES_URL"] = ""
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "INPUT_POSTGRES_URL") {
		t.Fatalf("input err=%v", err)
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
	// The happy path parses every knob（D12：InputSource 恒 postgres）。
	cfg, err := load(w9hValidConfigEnv("", "", inputDir))
	if err != nil {
		t.Fatalf("valid config err=%v", err)
	}
	if cfg.InstanceID != "inst-w9h" || cfg.InputSource != "postgres" {
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

func w9hCursor(observedAtText, outcomeID string) *OutcomeCursor {
	cursor, err := normalizeProjectionCursor(observedAtText, outcomeID)
	if err != nil {
		panic(err)
	}
	return cursor
}
