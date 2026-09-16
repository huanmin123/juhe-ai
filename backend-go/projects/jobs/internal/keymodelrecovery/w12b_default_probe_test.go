package keymodelrecovery

// w12b 波次 defaultProbe 分支覆盖测试。defaultProbe 生产修复后从
// JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET 读取凭据 secret（与 account-health
// 输入读取器同一契约），因此成功/上游中性路径均可通过真实 httptest 服务器触达。
//
// 不可达语句登记：无（四个返回臂均已覆盖）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
)

const w12bProbeSecret = "w12b-probe-secret"

func w12bProbeInput(baseURL string) accounthealth.Input {
	now := time.Now().UTC()
	return accounthealth.Input{
		AccountID:            "w12b-account",
		InputVersion:         1,
		ConfigRevision:       1,
		DispatchRevision:     9,
		Provider:             "openai",
		Type:                 "api_key",
		EndpointMode:         "chat_json",
		HealthModel:          "placeholder",
		BaseURL:              baseURL,
		IssuedAt:             now,
		ExpiresAt:            now.Add(time.Hour),
		TLSPolicyVersion:     "test",
		AllowInsecureBaseURL: strings.HasPrefix(baseURL, "http://"),
	}
}

func w12bProbeState() State {
	state := State{CapabilityKey: key(), CapabilityHash: "w12b-hash", Generation: 1, Phase: HalfOpen}
	return state
}

func w12bAPIKeyInput(t *testing.T, fingerprint string) accounthealth.Input {
	t.Helper()
	ciphertext, err := accounthealth.EncryptV1Envelope(w12bProbeSecret, []byte(`{"api_key":"sk-w12b-key"}`))
	if err != nil {
		t.Fatal(err)
	}
	input := w12bProbeInput("")
	input.APIKeys = []accounthealth.APIKeyInput{{Fingerprint: fingerprint, Credential: accounthealth.CredentialEnvelope{Kind: "api_key", Ciphertext: ciphertext}}}
	return input
}

func TestW12bDefaultProbeSuccess(t *testing.T) {
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET", w12bProbeSecret)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("意外探针路径: %s", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer sk-w12b-key" {
			t.Errorf("意外 Authorization: %q", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"juhe"}}]}`))
	}))
	defer server.Close()

	input := w12bAPIKeyInput(t, key().KeyFingerprint)
	input.BaseURL = server.URL
	input.AllowInsecureBaseURL = true
	state := w12bProbeState()
	if got := defaultProbe(context.Background(), state, input); got != CompleteSuccess {
		t.Fatalf("成功探针应返回 complete_success: %s", got)
	}
}

func TestW12bDefaultProbeUpstreamNeutral(t *testing.T) {
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET", w12bProbeSecret)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "w12b upstream unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()

	input := w12bAPIKeyInput(t, key().KeyFingerprint)
	input.BaseURL = server.URL
	input.AllowInsecureBaseURL = true
	state := w12bProbeState()
	if got := defaultProbe(context.Background(), state, input); got != UpstreamNotComplete {
		t.Fatalf("上游 5xx 应返回 upstream_not_complete: %s", got)
	}
}

func TestW12bDefaultProbeTaskFailureArms(t *testing.T) {
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET", w12bProbeSecret)
	// 缺失 key-model 路由 → task failure。
	state := w12bProbeState()
	state.FinalUpstreamModel = " "
	if got := defaultProbe(context.Background(), state, w12bProbeInput("http://127.0.0.1:1")); got != Unknown {
		t.Fatalf("路由缺失应返回 unknown: %s", got)
	}
	// 非法凭据类型 → task failure。
	input := w12bProbeInput("http://127.0.0.1:1")
	input.Type = "oauth"
	state2 := w12bProbeState()
	if got := defaultProbe(context.Background(), state2, input); got != Unknown {
		t.Fatalf("非 api_key 类型应返回 unknown: %s", got)
	}
	// 指纹未命中 → task failure。
	input3 := w12bAPIKeyInput(t, "w12b-other-fingerprint")
	input3.BaseURL = "http://127.0.0.1:1"
	state3 := w12bProbeState()
	if got := defaultProbe(context.Background(), state3, input3); got != Unknown {
		t.Fatalf("指纹未命中应返回 unknown: %s", got)
	}
}

func TestW12bDefaultProbeMissingSecretDegradesToUnknown(t *testing.T) {
	// 回归守卫：secret 未配置时探针退化为 unknown，而不是 panic 或误报成功。
	t.Setenv("JUHE_AI_ACCOUNT_HEALTH_CREDENTIAL_SECRET", "")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"juhe"}}]}`))
	}))
	defer server.Close()

	input := w12bAPIKeyInput(t, key().KeyFingerprint)
	input.BaseURL = server.URL
	input.AllowInsecureBaseURL = true
	state := w12bProbeState()
	if got := defaultProbe(context.Background(), state, input); got != Unknown {
		t.Fatalf("secret 缺失应退化为 unknown: %s", got)
	}
}
