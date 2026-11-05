package exactkeyprobe

// B3-H25 回归：恢复/显式探针构建 Key 池时必须排除 account_api_key_runtime_states
// 中人工置 disabled 的指纹；可用 Key 为空时返回可识别错误，探针不得发上游请求。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// b3h25CountingServer 记录上游命中次数：用于证明 disabled Key 不会发上游请求。
func b3h25CountingServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"response","choices":[{"message":{"content":"2"}}]}`))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func TestB3H25DirectInputExcludesDisabledKeys(t *testing.T) {
	secret := "b3h25-direct-input-secret"
	now := time.Date(2030, 8, 16, 0, 0, 0, 0, time.UTC)
	server, hits := b3h25CountingServer(t)
	credentials, err := EncryptV1Envelope(secret, []byte(`{"api_keys":["sk-b3h25-active","sk-b3h25-disabled"]}`))
	if err != nil {
		t.Fatal(err)
	}
	activeFingerprint := directKeyFingerprint(secret, "sk-b3h25-active")
	disabledFingerprint := directKeyFingerprint(secret, "sk-b3h25-disabled")
	fixture := func(disabled []string) DirectInput {
		return DirectInput{
			Account:      DirectAccount{ID: "b3h25-account", ConfigRevision: 1, DispatchRevision: 1, Provider: "gpt", ProtocolCode: "openai", ProtocolVersion: "v1", Type: "api_key", Status: "active", Schedulable: true, EndpointMode: "responses_json", HealthModel: "gpt-test", CredentialsEncrypted: credentials, ConnectionBaseURL: server.URL},
			Binding:      DirectBinding{GroupID: "group-1", Enabled: true},
			InputVersion: 1, IssuedAt: now, ExpiresAt: now.Add(time.Hour), TLSPolicy: "j1-direct-upstream-v1",
			Schedule:                Schedule{HealthIntervalMS: 1, FailureThreshold: 1, FailureRetryMS: 1, CooldownNeutralBaseMS: 1, CooldownNeutralMaxMS: 1, CooldownFailureBackoffMS: 1},
			DisabledKeyFingerprints: disabled,
		}
	}

	input, err := fixture([]string{disabledFingerprint}).ToInput(secret, now)
	if err != nil {
		t.Fatalf("排除 disabled 后仍应可构建输入: %v", err)
	}
	if len(input.APIKeys) != 1 || input.APIKeys[0].Fingerprint != activeFingerprint {
		t.Fatalf("Key 池只应保留 active Key: %#v", input.APIKeys)
	}
	if input.KeySetFingerprint != directKeySetFingerprint(input.APIKeys) {
		t.Fatal("KeySetFingerprint 必须按过滤后的 Key 池重算")
	}

	// 恢复探针针对 disabled 指纹时在本地判定不可用，不进入 HTTP 路径。
	result := ProbeExactKeyModel(context.Background(), input, disabledFingerprint, "gpt-test", "responses_json", ProbeOptions{Secret: secret, Client: server.Client(), Now: func() time.Time { return now }})
	if result.Outcome != OutcomeTaskFailed || result.ErrorCode != "key_model_key_unavailable" {
		t.Fatalf("disabled 指纹必须本地短路: %#v", result)
	}
	if hits.Load() != 0 {
		t.Fatalf("disabled Key 不得发上游请求，实际 %d 次", hits.Load())
	}

	// 对照组：active Key 仍会真实发一次上游请求（证明排除逻辑不误伤可用 Key）。
	ProbeExactKeyModel(context.Background(), input, activeFingerprint, "gpt-test", "responses_json", ProbeOptions{Secret: secret, Client: server.Client(), Now: func() time.Time { return now }})
	if hits.Load() != 1 {
		t.Fatalf("active Key 应发一次上游请求，实际 %d 次", hits.Load())
	}

	// 全部 Key 被禁用 → 可识别的「无可用 Key」错误，调用方无从发请求。
	if _, err := fixture([]string{activeFingerprint, disabledFingerprint}).ToInput(secret, now); !errors.Is(err, ErrNoAvailableAPIKey) {
		t.Fatalf("全部 disabled 必须返回 ErrNoAvailableAPIKey: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("全部 disabled 不得新增上游请求，实际 %d 次", hits.Load())
	}
}
