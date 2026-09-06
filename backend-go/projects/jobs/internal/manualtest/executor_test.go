package manualtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accountquality"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/oauthrefresh"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/opsjobs"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/proberepo"
)

const testSecret = "0123456789abcdef0123456789abcdef"

// stubCandidateSource 满足 accountprobe.NewService 的必填依赖；draft 路径
// 视图由执行器直接组装，不触达该来源。
type stubCandidateSource struct{}

func (stubCandidateSource) LoadProbeView(context.Context, accountquality.ProbeRequest) (*accountprobe.View, error) {
	return nil, nil
}

type fakeSavedSource struct {
	account   *proberepo.AccountForTestView
	candidate *proberepo.CandidateAccount
}

func (f *fakeSavedSource) LoadAccountForTest(context.Context, string) (*proberepo.AccountForTestView, error) {
	return f.account, nil
}

func (f *fakeSavedSource) LoadAccountForGroup(context.Context, string, string, string) (*proberepo.CandidateAccount, error) {
	return f.candidate, nil
}

// newTestExecutor 构建执行器：分级超时压缩为毫秒级（结构不变：3 阶段仅
// 真实上游尝试后超时晋级），以便取消/失败路径快速收敛。
func newTestExecutor(t *testing.T, saved SavedAccountSource) *Executor {
	t.Helper()
	probe, err := accountprobe.NewService(accountprobe.Options{
		Source:        stubCandidateSource{},
		Client:        &http.Client{},
		Secret:        testSecret,
		RetryTimeouts: []time.Duration{40 * time.Millisecond, 60 * time.Millisecond, 80 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorOptions{
		Probe:         probe,
		SavedAccounts: saved,
		Secret:        testSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func draftEnvelope(t *testing.T, credentials map[string]any) string {
	t.Helper()
	draft := DraftSnapshot{
		ID:                        "acct-draft-1",
		OwnerSystemAccountID:      "sys-1",
		GroupID:                   "grp-1",
		ProviderCode:              "openai",
		ProviderProtocolProfileID: "profile_openai_openai_v1",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		Name:                      "草稿账户",
		Type:                      "api_key",
		Credentials:               credentials,
		ClientCompatibility:       "openai_standard",
		SupportedModels:           []string{"gpt-test"},
		HealthCheckModel:          "gpt-test",
		HealthCheckEndpointMode:   "chat_json",
	}
	return encryptDraft(t, &draft)
}

func encryptDraft(t *testing.T, draft *DraftSnapshot) string {
	t.Helper()
	envelope, err := oauthrefresh.EncryptJSON(testSecret, draft)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func manualTestTask(envelope string) opsjobs.ManualTestTaskRecord {
	startedAt := "2030-01-01T00:00:00.000Z"
	return opsjobs.ManualTestTaskRecord{
		ID:                    "task-1",
		AccountID:             "acc-1",
		Model:                 "gpt-test",
		TestEndpointMode:      "chat_json",
		Diagnostics:           "full",
		StartedAt:             &startedAt,
		DraftAccountEncrypted: envelope,
	}
}

func chatUpstream(t *testing.T, statusCode int, body string, requests *[][]string, hold *chan struct{}) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			*requests = append(*requests, []string{r.URL.Path, r.Header.Get("authorization")})
		}
		if hold != nil {
			// 取消路径专用：挂住响应直到测试显式释放（不依赖服务端
			// r.Context().Done() 的断连感知；客户端取消由 attempt 超时与
			// ctx 取消覆盖）。
			select {
			case <-r.Context().Done():
			case <-*hold:
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	if hold != nil {
		// Cleanup LIFO：先释放 hold（handler 返回），再 Server.Close。
		t.Cleanup(func() {
			close(*hold)
		})
	}
	return server
}

func chatSuccessBody() string {
	return `{"choices":[{"finish_reason":"stop","message":{"content":"juhe"}}]}`
}

// 快乐路径：draft 解密 → chat_json 诊断 → 成功信封（result_json 形状对照
// Node AccountTestResult）。
func TestExecutorDraftSuccessWritesEnvelope(t *testing.T) {
	var requests [][]string
	server := chatUpstream(t, http.StatusOK, chatSuccessBody(), &requests, nil)
	executor := newTestExecutor(t, nil)
	task := manualTestTask(draftEnvelope(t, map[string]any{
		"api_key":                  "sk-test-123456",
		"base_url":                 server.URL + "/v1",
		"supported_endpoint_modes": []any{"chat_json"},
	}))
	var progress []string
	result, err := executor.Execute(context.Background(), task, func(message string) {
		progress = append(progress, message)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("应成功: %+v", result)
	}
	if result.Message != "OpenAI Chat Completions 测试通过" {
		t.Fatalf("message = %q", result.Message)
	}
	if len(progress) != 1 || progress[0] != "真实请求测试中：本次诊断最长等待 60s" {
		t.Fatalf("进度消息 = %v", progress)
	}
	if len(requests) != 1 || requests[0][0] != "/v1/chat/completions" {
		t.Fatalf("上游请求 = %v", requests)
	}
	if requests[0][1] != "Bearer sk-test-123456" {
		t.Fatalf("认证头 = %q", requests[0][1])
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(result.ResultJSON), &envelope); err != nil {
		t.Fatalf("result_json 不是 JSON: %v", err)
	}
	if envelope["accountId"] != "acct-draft-1" || envelope["accountName"] != "草稿账户" ||
		envelope["providerCode"] != "openai" || envelope["protocolCode"] != "openai" ||
		envelope["providerProtocolProfileId"] != "profile_openai_openai_v1" ||
		envelope["type"] != "api_key" || envelope["success"] != true ||
		envelope["model"] != "gpt-test" || envelope["testEndpointMode"] != "chat_json" ||
		envelope["accountStatus"] != "active" {
		t.Fatalf("信封字段不符: %v", envelope)
	}
	if code, ok := envelope["statusCode"].(float64); !ok || int(code) != 200 {
		t.Fatalf("statusCode = %v", envelope["statusCode"])
	}
	if trace, ok := envelope["traceId"].(string); !ok || !strings.HasPrefix(trace, "trace-") {
		t.Fatalf("traceId = %v", envelope["traceId"])
	}
	if _, ok := envelope["durationMs"].(float64); !ok {
		t.Fatalf("durationMs 缺失: %v", envelope)
	}
}

// 诊断失败路径：上游 401 → fail 结果仍带信封（Node complete(failed) 行形状）。
func TestExecutorDraftUpstreamFailureCarriesEnvelope(t *testing.T) {
	server := chatUpstream(t, http.StatusUnauthorized, `{"error":{"message":"Incorrect API key","code":"invalid_api_key"}}`, nil, nil)
	executor := newTestExecutor(t, nil)
	result, err := executor.Execute(context.Background(), manualTestTask(draftEnvelope(t, map[string]any{
		"api_key":                  "sk-bad",
		"base_url":                 server.URL + "/v1",
		"supported_endpoint_modes": []any{"chat_json"},
	})), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatal("401 不应成功")
	}
	if result.Message == "" {
		t.Fatal("失败消息缺失")
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(result.ResultJSON), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["success"] != false {
		t.Fatalf("信封 success = %v", envelope["success"])
	}
	if envelope["errorCode"] != "invalid_api_key" {
		t.Fatalf("errorCode = %v", envelope["errorCode"])
	}
}

// 解密失败回退：draft 信封损坏 → Node 语义回退保存账户路径 → 账户缺失
// 时按 “账户不存在” fail（无结果信封）。
func TestExecutorDecryptFailureFallsBackToSavedPath(t *testing.T) {
	executor := newTestExecutor(t, &fakeSavedSource{})
	result, err := executor.Execute(context.Background(), manualTestTask("not-a-valid-envelope"), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.Message != accountMissingMessage {
		t.Fatalf("应按账户不存在失败: %+v", result)
	}
	if result.ResultJSON != "" {
		t.Fatalf("fail 路径不得携带结果信封: %q", result.ResultJSON)
	}
}

// draft 形状不合法（缺必填字段）→ 同样回退保存账户路径。
func TestExecutorInvalidDraftFallsBackToSavedPath(t *testing.T) {
	executor := newTestExecutor(t, &fakeSavedSource{})
	incomplete := encryptDraft(t, &DraftSnapshot{ID: "acct-draft-1"})
	result, err := executor.Execute(context.Background(), manualTestTask(incomplete), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.Message != accountMissingMessage {
		t.Fatalf("应按账户不存在失败: %+v", result)
	}
}

// 协议门禁：非 OpenAI/Anthropic/Gemini 协议草稿 → Node 同文案 fail。
func TestExecutorUnsupportedProtocolFails(t *testing.T) {
	executor := newTestExecutor(t, nil)
	draft := DraftSnapshot{
		ID: "acct-draft-1", OwnerSystemAccountID: "sys-1", GroupID: "grp-1",
		ProviderCode: "coze", ProviderProtocolProfileID: "profile_coze_v1",
		ProtocolCode: "coze", ProtocolVersion: "v1",
		Name: "草稿", Type: "api_key",
		Credentials:             map[string]any{"api_key": "sk-1"},
		ClientCompatibility:     "openai_standard",
		SupportedModels:         []string{"gpt-test"},
		HealthCheckModel:        "gpt-test",
		HealthCheckEndpointMode: "chat_json",
	}
	result, err := executor.Execute(context.Background(), manualTestTask(encryptDraft(t, &draft)), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || result.Message != unsupportedGatewayProtocolTestMessage {
		t.Fatalf("应按协议不支持失败: %+v", result)
	}
}

// 取消路径：上游挂住请求，执行中取消 ctx → Canceled 响应（队列写 cancel）。
func TestExecutorCancelRespondsCanceled(t *testing.T) {
	hold := make(chan struct{})
	server := chatUpstream(t, http.StatusOK, chatSuccessBody(), nil, &hold)
	executor := newTestExecutor(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	result, err := executor.Execute(ctx, manualTestTask(draftEnvelope(t, map[string]any{
		"api_key":                  "sk-test-123456",
		"base_url":                 server.URL + "/v1",
		"supported_endpoint_modes": []any{"chat_json"},
	})), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Canceled {
		t.Fatalf("应响应取消: %+v", result)
	}
}

// Key 池路径：双 Key 草稿，首把 401、次把成功 → 池摘要信封与聚合文案。
func TestExecutorPoolSummaryEnvelope(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"bad","code":"invalid_api_key"}}`))
			return
		}
		_, _ = w.Write([]byte(chatSuccessBody()))
	}))
	t.Cleanup(server.Close)
	executor := newTestExecutor(t, nil)
	task := manualTestTask(draftEnvelope(t, map[string]any{
		"api_keys":                 []any{"sk-key-aaaaaaaa", "sk-key-bbbbbbbb"},
		"base_url":                 server.URL + "/v1",
		"supported_endpoint_modes": []any{"chat_json"},
	}))
	result, err := executor.Execute(context.Background(), task, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("池内第二把 Key 应胜出: %+v", result)
	}
	if result.Message != "API Key 池测试通过：已测 2/2，1 个 Key 可用，1 个 Key 未通过" {
		t.Fatalf("池文案 = %q", result.Message)
	}
	var envelope struct {
		APIKeyPool *struct {
			Total        int `json:"total"`
			Tested       int `json:"tested"`
			SuccessCount int `json:"successCount"`
			FailedCount  int `json:"failedCount"`
			Results      []struct {
				KeyIndex  int    `json:"keyIndex"`
				KeyPrefix string `json:"keyPrefix"`
				KeySuffix string `json:"keySuffix"`
				Success   bool   `json:"success"`
			} `json:"results"`
		} `json:"apiKeyPool"`
		Success bool `json:"success"`
	}
	if err := json.Unmarshal([]byte(result.ResultJSON), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.APIKeyPool == nil {
		t.Fatal("apiKeyPool 信封缺失")
	}
	pool := envelope.APIKeyPool
	if pool.Total != 2 || pool.Tested != 2 || pool.SuccessCount != 1 || pool.FailedCount != 1 {
		t.Fatalf("池摘要不符: %+v", pool)
	}
	if len(pool.Results) != 2 || pool.Results[0].KeyIndex != 0 || pool.Results[1].KeyIndex != 1 {
		t.Fatalf("池明细 = %+v", pool.Results)
	}
	if pool.Results[0].KeyPrefix != "sk-k" || pool.Results[0].KeySuffix != "aaaa" ||
		pool.Results[1].KeyPrefix != "sk-k" || pool.Results[1].KeySuffix != "bbbb" {
		t.Fatalf("Key 前后缀 = %+v", pool.Results)
	}
}

// 保存账户路径：LoadAccountForTest + LoadAccountForGroup 双查询投影 → 视图诊断。
func TestExecutorSavedAccountPath(t *testing.T) {
	server := chatUpstream(t, http.StatusOK, chatSuccessBody(), nil, nil)
	saved := &fakeSavedSource{
		account: &proberepo.AccountForTestView{
			AccountForTest: accountquality.AccountForTest{
				ID: "acc-saved", Name: "保存账户", Type: "api_key", Status: "active",
				BoundGroupID: "grp-1", OwnerSystemAccountID: "sys-1",
			},
			ProviderCode:              "openai",
			ProviderProtocolProfileID: "profile_openai_openai_v1",
			ProtocolVersion:           "v1",
			HealthCheckModel:          "gpt-test",
			HealthCheckEndpointMode:   "chat_json",
			SupportedModels:           []string{"gpt-test"},
			Credentials: map[string]any{
				"api_key": "sk-saved",
			},
		},
		candidate: &proberepo.CandidateAccount{
			OpenAIAccountCandidate: accountquality.OpenAIAccountCandidate{
				ID:     "acc-saved",
				Name:   "保存账户",
				Type:   "api_key",
				Status: "active",
			},
			ProviderCode:    "openai",
			ProtocolCode:    "openai",
			ProtocolVersion: "v1",
			Credentials:     map[string]any{"api_key": "sk-saved", "base_url": server.URL + "/v1"},
			SelectedAPIKey:  "sk-saved",
			APIKeyEntries: []proberepo.KeyEntry{
				{Key: "sk-saved", Fingerprint: "fp", Index: 0},
			},
		},
	}
	executor := newTestExecutor(t, saved)
	task := manualTestTask("")
	task.ID = "task-saved"
	task.AccountID = "acc-saved"
	result, err := executor.Execute(context.Background(), task, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("保存账户路径应成功: %+v", result)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(result.ResultJSON), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["accountId"] != "acc-saved" {
		t.Fatalf("accountId = %v", envelope["accountId"])
	}
}
