package manualtest

// w12h 补充 arms：draft 规范化的形状过滤分支、协议谓词、凭据选择、端点
// 形态过滤，执行器的依赖校验、保存账户错误传播、取消与诊断错误分类，
// 结果信封的池摘要/文案变体。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountprobe"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/accountquality"
	"github.com/huanminabc/juhe-ai/backend-go-platform/accounttest/proberepo"
)

func TestW12HNormalizeDraftShapeFilters(t *testing.T) {
	if normalizeDraftSnapshot([]any{"x"}) != nil {
		t.Fatal("非对象草稿必须作废")
	}
	if got := stringListValue([]any{1, "  ", " m1 ", true}); len(got) != 1 || got[0] != "m1" {
		t.Fatalf("stringListValue 过滤不符: %v", got)
	}
	if got := stringListValue([]any{1, "  "}); got != nil {
		t.Fatalf("全空白列表必须为 nil: %v", got)
	}
	if got := stringListValue("not-a-list"); got != nil {
		t.Fatalf("非列表必须为 nil: %v", got)
	}
	if got := modelMappingsValue([]any{"x", map[string]any{
		"sourceModel": "a", "sourceEndpointFamily": "chat_completions",
		"upstreamModel": "b", "upstreamEndpointFamily": "messages",
	}}); len(got) != 1 || got[0].UpstreamModel != "b" {
		t.Fatalf("modelMappingsValue 应跳过非对象条目: %v", got)
	}
	disabled := modelMappingsValue([]any{map[string]any{
		"sourceModel": "a", "sourceEndpointFamily": "chat_completions",
		"upstreamModel": "b", "upstreamEndpointFamily": "messages", "enabled": false,
	}})
	if disabled != nil {
		t.Fatalf("enabled=false 映射必须被忽略: %v", disabled)
	}
	incomplete := modelMappingsValue([]any{map[string]any{"sourceModel": "a"}})
	if incomplete != nil {
		t.Fatalf("字段缺失映射必须被忽略: %v", incomplete)
	}
}

func TestW12HGatewaySupportedDraftProtocol(t *testing.T) {
	cases := []struct {
		code, version string
		want          bool
	}{
		{"openai", "v1", true},
		{"OPENAI ", " v1", true},
		{"anthropic", "v1", true},
		{"gemini", "v1beta", true},
		{"gemini", "v1", false},
		{"openai", "v2", false},
		{"coze", "v1", false},
		{"", "", false},
	}
	for _, test := range cases {
		if got := isGatewaySupportedDraftProtocol(test.code, test.version); got != test.want {
			t.Fatalf("isGatewaySupportedDraftProtocol(%q,%q)=%v", test.code, test.version, got)
		}
	}
}

func TestW12HAPIKeyEntriesAndSelection(t *testing.T) {
	credentials := map[string]any{
		"api_keys": []any{"  sk-a  ", "", 7, "sk-a", "sk-b"},
	}
	entries := apiKeyEntries(testSecret, credentials)
	if len(entries) != 2 || entries[0].Key != "sk-a" || entries[0].Index != 0 || entries[1].Key != "sk-b" || entries[1].Index != 4 {
		t.Fatalf("池条目不符: %+v", entries)
	}
	// api_keys 为空列表时回落单 api_key。
	fallback := apiKeyEntries(testSecret, map[string]any{"api_keys": []any{}, "api_key": " sk-solo "})
	if len(fallback) != 1 || fallback[0].Key != "sk-solo" {
		t.Fatalf("单 key 回落不符: %+v", fallback)
	}

	oauthDraft := &DraftSnapshot{Type: "oauth", Credentials: map[string]any{"access_token": " at "}}
	if got := selectedAPIKeyForDraft(oauthDraft, nil); got != "at" {
		t.Fatalf("oauth 凭据 = %q", got)
	}
	googleDraft := &DraftSnapshot{Type: "google_oauth", Credentials: map[string]any{"access_token": "at", "refresh_token": "rt"}}
	if got := selectedAPIKeyForDraft(googleDraft, nil); got != "at" {
		t.Fatalf("google_oauth access = %q", got)
	}
	googleRefresh := &DraftSnapshot{Type: "google_oauth", Credentials: map[string]any{"refresh_token": "rt"}}
	if got := selectedAPIKeyForDraft(googleRefresh, nil); got != "rt" {
		t.Fatalf("google_oauth refresh = %q", got)
	}
	apiKeyDraft := &DraftSnapshot{Type: "api_key", Credentials: credentials}
	if got := selectedAPIKeyForDraft(apiKeyDraft, entries); got != "sk-a" {
		t.Fatalf("api_key 池首选 = %q", got)
	}
	emptyPool := &DraftSnapshot{Type: "api_key", Credentials: map[string]any{}}
	if got := selectedAPIKeyForDraft(emptyPool, nil); got != "" {
		t.Fatalf("空池必须为空串: %q", got)
	}
	if got := credentialText(nil, "access_token"); got != "" {
		t.Fatalf("nil credentials 必须为空: %q", got)
	}
	if got := credentialText(map[string]any{"k": 7}, "k"); got != "" {
		t.Fatalf("非字符串凭据必须为空: %q", got)
	}
	if got := draftBaseURL(nil); got != "https://api.openai.com/v1" {
		t.Fatalf("默认 base_url = %q", got)
	}
	if got := draftBaseURL(map[string]any{"base_url": " https://w12h.invalid/v1 "}); got != "https://w12h.invalid/v1" {
		t.Fatalf("自定义 base_url = %q", got)
	}
}

func TestW12HNormalizeDraftEndpointModes(t *testing.T) {
	if modes := normalizeDraftEndpointModes(&DraftSnapshot{Credentials: map[string]any{}}); len(modes) != 0 {
		t.Fatalf("缺失 supported_endpoint_modes 必须为空集合: %v", modes)
	}
	modes := normalizeDraftEndpointModes(&DraftSnapshot{Credentials: map[string]any{
		"supported_endpoint_modes": []any{"chat_json", "images_json", "bogus_mode", 7},
	}})
	if !modes[accountprobe.ModeChatJSON] || !modes[accountprobe.ModeImagesJSON] || len(modes) != 2 {
		t.Fatalf("形态过滤不符: %v", modes)
	}
}

func TestW12HMarshalJSONEnvelopeError(t *testing.T) {
	if _, err := marshalJSONEnvelope(map[string]any{"ch": make(chan int)}); err == nil {
		t.Fatal("不可序列化值必须报错")
	}
	encoded, err := marshalJSONEnvelope(map[string]any{"ok": true})
	if err != nil || !strings.Contains(encoded, `"ok":true`) {
		t.Fatalf("正常序列化不符: %q %v", encoded, err)
	}
}

func TestW12HNewExecutorValidation(t *testing.T) {
	if _, err := NewExecutor(ExecutorOptions{Secret: testSecret}); err == nil || !strings.Contains(err.Error(), "探针") {
		t.Fatalf("缺探针必须报错: %v", err)
	}
	probe, err := accountprobe.NewService(accountprobe.Options{Source: stubCandidateSource{}, Secret: testSecret})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecutor(ExecutorOptions{Probe: probe, Secret: "   "}); err == nil || !strings.Contains(err.Error(), "密钥") {
		t.Fatalf("缺密钥必须报错: %v", err)
	}
}

// w12hErrSavedSource 在指定环节注入错误。
type w12hErrSavedSource struct {
	loadTestErr   error
	loadGroupErr  error
	candidateNil  bool
	accountLoaded *proberepo.AccountForTestView
}

func (s *w12hErrSavedSource) LoadAccountForTest(context.Context, string) (*proberepo.AccountForTestView, error) {
	if s.loadTestErr != nil {
		return nil, s.loadTestErr
	}
	return s.accountLoaded, nil
}

func (s *w12hErrSavedSource) LoadAccountForGroup(context.Context, string, string, string) (*proberepo.CandidateAccount, error) {
	if s.loadGroupErr != nil {
		return nil, s.loadGroupErr
	}
	if s.candidateNil {
		return nil, nil
	}
	return &proberepo.CandidateAccount{}, nil
}

func TestW12HSavedPathErrorArms(t *testing.T) {
	executor := newTestExecutor(t, nil)
	// 无 draft 且 savedAccounts 为 nil → 账户不存在。
	task := manualTestTask("")
	result, err := executor.Execute(context.Background(), task, func(string) {})
	if err != nil || result.Success || result.Message != accountMissingMessage {
		t.Fatalf("无 draft 无 saved 必须按账户不存在失败: %+v %v", result, err)
	}

	// LoadAccountForTest 错误上抛。
	failing := &w12hErrSavedSource{loadTestErr: errors.New("w12h load test failure")}
	executor = newTestExecutor(t, failing)
	if _, err := executor.Execute(context.Background(), task, func(string) {}); err == nil || !strings.Contains(err.Error(), "w12h load test failure") {
		t.Fatalf("加载账户错误必须上抛: %v", err)
	}

	// 账户缺失 → 账户不存在。
	executor = newTestExecutor(t, &w12hErrSavedSource{})
	result, err = executor.Execute(context.Background(), task, func(string) {})
	if err != nil || result.Success || result.Message != accountMissingMessage {
		t.Fatalf("账户缺失必须按账户不存在失败: %+v %v", result, err)
	}

	// LoadAccountForGroup 错误上抛。
	executor = newTestExecutor(t, &w12hErrSavedSource{
		accountLoaded: &proberepo.AccountForTestView{},
		loadGroupErr:  errors.New("w12h load group failure"),
	})
	if _, err := executor.Execute(context.Background(), task, func(string) {}); err == nil || !strings.Contains(err.Error(), "w12h load group failure") {
		t.Fatalf("分组候选错误必须上抛: %v", err)
	}

	// 候选缺失 → 分组文案。
	executor = newTestExecutor(t, &w12hErrSavedSource{
		accountLoaded: &proberepo.AccountForTestView{},
		candidateNil:  true,
	})
	result, err = executor.Execute(context.Background(), task, func(string) {})
	if err != nil || result.Success || !strings.Contains(result.Message, "不在当前分组") {
		t.Fatalf("候选缺失必须按分组文案失败: %+v %v", result, err)
	}
}

func TestW12HDiagnosticsErrorClassification(t *testing.T) {
	// ctx 已取消 + 上游失败 → 诊断错误归类为取消。
	server := chatUpstream(t, http.StatusUnauthorized, `{"error":{"code":"x"}}`, nil, nil)
	executor := newTestExecutor(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := executor.Execute(ctx, manualTestTask(draftEnvelope(t, map[string]any{
		"api_key": "sk-x", "base_url": server.URL + "/v1",
		"supported_endpoint_modes": []any{"chat_json"},
	})), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Canceled {
		t.Fatalf("取消 ctx 下的诊断错误必须归类为取消: %+v", result)
	}
}

func TestW12HBuildResultAndPoolMessageArms(t *testing.T) {
	executor := &Executor{now: func() time.Time { return time.Unix(0, 0) }}
	task := manualTestTask("")
	code := 200
	observation := &accountquality.ProbeObservation{Result: accountquality.ProbeResult{
		Success: true, StatusCode: &code, Message: "ok", FirstTokenMS: 120,
	}}
	built := executor.buildResult(task, &accountprobe.View{}, observation, nil, time.Unix(0, 0).Add(-time.Second))
	if !built.Success || !strings.Contains(built.envelopeJSON, `"firstTokenMs":120`) {
		t.Fatalf("firstToken 信封不符: %+v %s", built, built.envelopeJSON)
	}

	// 池摘要：全失败携带错误码、DurationMs；tested<total 的文案；全失败文案。
	poolAttempts := []accountprobe.PoolKeyAttempt{
		{Entry: accountprobe.KeyEntry{Key: "sk-key-aaaaaaaa", Index: 0}, Observation: &accountquality.ProbeObservation{Result: accountquality.ProbeResult{
			Success: false, Message: "bad", ErrorCode: "invalid_api_key", DurationMs: 35,
		}}},
		{Entry: accountprobe.KeyEntry{Key: "sk-key-bbbbbbbb", Index: 1}, Observation: &accountquality.ProbeObservation{Result: accountquality.ProbeResult{
			Success: false, Message: "worse", ErrorCode: "timeout",
		}}},
	}
	built = executor.buildResult(task, &accountprobe.View{APIKeyEntries: []accountprobe.KeyEntry{
		{Key: "sk-key-aaaaaaaa", Index: 0}, {Key: "sk-key-bbbbbbbb", Index: 1},
	}}, observation, poolAttempts, time.Unix(0, 0))
	if built.Success {
		t.Fatal("全失败池不应成功")
	}
	if !strings.Contains(built.Message, "API Key 池测试未通过：0/2") {
		t.Fatalf("全失败文案不符: %q", built.Message)
	}
	if !strings.Contains(built.envelopeJSON, `"durationMs":35`) || !strings.Contains(built.envelopeJSON, `"errorCode":"invalid_api_key"`) {
		t.Fatalf("池明细不符: %s", built.envelopeJSON)
	}

	// 部分未测试的成功文案（suffix）。
	message := poolTestMessage(&apiKeyPoolResult{Total: 3, Tested: 2, SuccessCount: 1, FailedCount: 1, Results: []poolItemResult{}})
	if !strings.Contains(message, "2/3，1 个 Key 可用，1 个 Key 未通过，1 个未测试") {
		t.Fatalf("部分未测试文案不符: %q", message)
	}
	message = poolTestMessage(&apiKeyPoolResult{Total: 2, Tested: 2, SuccessCount: 2, FailedCount: 0})
	if !strings.Contains(message, "API Key 池测试通过：已测 2/2，2 个 Key 可用") {
		t.Fatalf("全通过文案不符: %q", message)
	}
	message = poolTestMessage(&apiKeyPoolResult{Total: 3, Tested: 1, SuccessCount: 0, FailedCount: 1})
	if !strings.Contains(message, "API Key 池测试未完成：0/3") {
		t.Fatalf("未完成文案不符: %q", message)
	}
}

func TestW12HKeySliceForDisplayArms(t *testing.T) {
	if keySliceForDisplay("", 0, 4) != nil {
		t.Fatal("空 key 必须为 nil")
	}
	if keySliceForDisplay("sk", -1, 2) != nil {
		t.Fatal("负 start 必须为 nil")
	}
	if got := keySliceForDisplay("sk-abc", 2, 99); got == nil || *got != "-abc" {
		t.Fatalf("end 截断不符: %v", got)
	}
	if keySliceForDisplay("sk", 1, 1) != nil {
		t.Fatal("start>=end 必须为 nil")
	}
}
