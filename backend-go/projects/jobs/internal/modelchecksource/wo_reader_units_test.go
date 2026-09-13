package modelchecksource

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/accounthealth"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckexecutor"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckinput"
	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/modelcheckprofile"
)

// ---- defaultBaseURL：档案到上游默认端点的映射契约 ----

func TestDefaultBaseURLProfileTable(t *testing.T) {
	cases := []struct {
		name          string
		profileID     string
		provider      string
		credentialTyp string
		oauthType     string
		want          string
	}{
		{"codex oauth", "profile_gpt_openai_v1", "openai", "oauth", "", "https://chatgpt.com/backend-api/codex"},
		{"codex api key 走 openai 默认", "profile_gpt_openai_v1", "openai", "api_key", "", "https://api.openai.com"},
		{"xai oauth", "profile_xai_openai_v1", "xai", "oauth", "", "https://cli-chat-proxy.grok.com/v1"},
		{"xai api key", "profile_xai_openai_v1", "xai", "api_key", "", "https://api.x.ai/v1"},
		{"deepseek", "profile_deepseek_openai_v1", "deepseek", "api_key", "", "https://api.deepseek.com"},
		{"deepseek anthropic", "profile_deepseek_anthropic_v1", "deepseek", "api_key", "", "https://api.deepseek.com/anthropic"},
		{"glm general", "profile_glm_general_openai_v1", "glm", "api_key", "", "https://open.bigmodel.cn/api/paas/v4"},
		{"glm coding", "profile_glm_coding_openai_v1", "glm", "api_key", "", "https://open.bigmodel.cn/api/coding/paas/v4"},
		{"glm coding anthropic", "profile_glm_coding_anthropic_v1", "glm", "api_key", "", "https://open.bigmodel.cn/api/anthropic"},
		{"gemini code assist", "profile_gemini_native_v1beta", "gemini", "google_oauth", "code_assist", "https://cloudcode-pa.googleapis.com"},
		{"gemini google one", "profile_gemini_native_v1beta", "gemini", "google_oauth", "google_one", "https://cloudcode-pa.googleapis.com"},
		{"gemini api key", "profile_gemini_native_v1beta", "gemini", "api_key", "", "https://generativelanguage.googleapis.com"},
		{"gemini openai chat", "profile_gemini_openai_chat_v1beta", "gemini", "api_key", "", "https://generativelanguage.googleapis.com/v1beta/openai"},
		{"anthropic", "profile_anthropic_anthropic_v1", "anthropic", "api_key", "", "https://api.anthropic.com"},
		{"hybrid anthropic", "profile_hybrid_anthropic_messages_v1", "anthropic", "api_key", "", "https://api.anthropic.com"},
		{"hybrid gemini", "profile_hybrid_gemini_native_v1beta", "gemini", "api_key", "", "https://generativelanguage.googleapis.com"},
		{"provider anthropic 兜底", "profile_unknown", "anthropic", "api_key", "", "https://api.anthropic.com"},
		{"provider gemini 兜底", "profile_unknown", "gemini", "api_key", "", "https://generativelanguage.googleapis.com"},
		{"最终 openai 兜底", "profile_unknown", "openai", "api_key", "", "https://api.openai.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultBaseURL(tc.profileID, tc.provider, tc.credentialTyp, tc.oauthType); got != tc.want {
				t.Fatalf("defaultBaseURL=%s，期望 %s", got, tc.want)
			}
		})
	}
}

// ---- 端点模式与协议匹配 ----

func TestEndpointModeFamilyTable(t *testing.T) {
	cases := []struct {
		mode   string
		want   modelcheckprofile.EndpointFamily
		wantOK bool
	}{
		{"responses_json", modelcheckprofile.EndpointResponses, true},
		{"responses_sse", modelcheckprofile.EndpointResponses, true},
		{"chat_json", modelcheckprofile.EndpointChatCompletions, true},
		{"chat_sse", modelcheckprofile.EndpointChatCompletions, true},
		{"messages_json", modelcheckprofile.EndpointMessages, true},
		{"messages_sse", modelcheckprofile.EndpointMessages, true},
		{"generate_content_json", modelcheckprofile.EndpointGenerateContent, true},
		{"generate_content_sse", modelcheckprofile.EndpointStreamGenerate, true},
		{"unknown_mode", "", false},
		{"  responses_json  ", modelcheckprofile.EndpointResponses, true},
	}
	for _, tc := range cases {
		got, ok := endpointModeFamily(tc.mode)
		if ok != tc.wantOK || got != tc.want {
			t.Fatalf("endpointModeFamily(%q)=%q,%v，期望 %q,%v", tc.mode, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestEndpointModeMatchesAllProtocols(t *testing.T) {
	cases := []struct {
		protocol modelcheckprofile.Protocol
		mode     string
		want     bool
	}{
		{modelcheckprofile.ProtocolOpenAIResponses, "responses_json", true},
		{modelcheckprofile.ProtocolOpenAIResponses, "responses_sse", true},
		{modelcheckprofile.ProtocolOpenAIResponses, "chat_json", false},
		{modelcheckprofile.ProtocolOpenAIChat, "chat_json", true},
		{modelcheckprofile.ProtocolOpenAIChat, "chat_sse", true},
		{modelcheckprofile.ProtocolOpenAIChat, "messages_json", false},
		{modelcheckprofile.ProtocolAnthropic, "messages_json", true},
		{modelcheckprofile.ProtocolAnthropic, "messages_sse", true},
		{modelcheckprofile.ProtocolAnthropic, "chat_json", false},
		{modelcheckprofile.ProtocolGeminiNative, "generate_content_json", true},
		{modelcheckprofile.ProtocolGeminiNative, "generate_content_sse", true},
		{modelcheckprofile.ProtocolGeminiNative, "responses_json", false},
		{modelcheckprofile.Protocol("weird"), "responses_json", false},
	}
	for _, tc := range cases {
		if got := endpointModeMatches(tc.protocol, tc.mode); got != tc.want {
			t.Fatalf("endpointModeMatches(%v,%q)=%v，期望 %v", tc.protocol, tc.mode, got, tc.want)
		}
	}
}

func TestMappingTransportCompatible(t *testing.T) {
	if !mappingTransportCompatible(modelcheckprofile.EndpointResponses, modelcheckprofile.EndpointResponses) {
		t.Fatalf("同族映射必须兼容")
	}
	if !mappingTransportCompatible(modelcheckprofile.EndpointStreamGenerate, modelcheckprofile.EndpointGenerateContent) {
		t.Fatalf("流式 generate_content 到非流式的降级必须兼容")
	}
	if mappingTransportCompatible(modelcheckprofile.EndpointChatCompletions, modelcheckprofile.EndpointMessages) {
		t.Fatalf("跨协议转换不属于兼容映射")
	}
}

// ---- 凭据材料解析 ----

func TestResolveCredentialMaterialBranches(t *testing.T) {
	secret := "wo-credential-secret"
	encrypt := func(plain string) string {
		envelope, err := accounthealth.EncryptV1Envelope(secret, []byte(plain))
		if err != nil {
			t.Fatal(err)
		}
		return envelope
	}
	if _, err := resolveCredentialMaterial(secret, postgresCandidate{credentialsEncrypted: "not-an-envelope"}); err == nil {
		t.Fatalf("解密失败应报错")
	}
	if _, err := resolveCredentialMaterial(secret, postgresCandidate{credentialsEncrypted: encrypt("not-json")}); err == nil {
		t.Fatalf("非 JSON 凭据应报错")
	}
	noModes := encrypt(`{"api_keys":["k"],"base_url":"https://x.example"}`)
	if _, err := resolveCredentialMaterial(secret, postgresCandidate{credentialsEncrypted: noModes, endpointMode: "responses_json"}); err == nil {
		t.Fatalf("缺少 supported_endpoint_modes 应报错")
	}
	badModes := encrypt(`{"supported_endpoint_modes":"responses_json"}`)
	if _, err := resolveCredentialMaterial(secret, postgresCandidate{credentialsEncrypted: badModes}); err == nil {
		t.Fatalf("端点模式列表非法应报错")
	}
	if credentialSupportsEndpointMode(map[string]json.RawMessage{"supported_endpoint_modes": []byte(`[" responses_json "]`)}, "responses_json") != true {
		t.Fatalf("端点模式比较应裁剪空白")
	}
	modeMismatch := encrypt(`{"api_keys":["k"],"base_url":"https://x.example","supported_endpoint_modes":["chat_json"]}`)
	if _, err := resolveCredentialMaterial(secret, postgresCandidate{credentialsEncrypted: modeMismatch, endpointMode: "responses_json"}); err == nil {
		t.Fatalf("端点模式不匹配应报错")
	}
	// base_url 缺失时回退 defaultBaseURL，并裁剪尾部斜杠。
	defaulted := encrypt(`{"api_keys":["k"],"supported_endpoint_modes":["responses_json"]}`)
	material, err := resolveCredentialMaterial(secret, postgresCandidate{
		credentialsEncrypted: defaulted, endpointMode: "responses_json",
		profileID: "profile_openai_openai_v1", providerCode: "openai", credentialType: "api_key",
	})
	if err != nil {
		t.Fatalf("默认端点回退不应报错: %v", err)
	}
	if material.baseURL != "https://api.openai.com" {
		t.Fatalf("默认端点不符: %s", material.baseURL)
	}
	oauth := encrypt(`{"base_url":"https://x.example/","oauth_type":"code_assist","quota_project_id":"proj-1","supported_endpoint_modes":["responses_json"]}`)
	material, err = resolveCredentialMaterial(secret, postgresCandidate{credentialsEncrypted: oauth, endpointMode: "responses_json"})
	if err != nil {
		t.Fatalf("OAuth 材料解析不应报错: %v", err)
	}
	if material.baseURL != "https://x.example" || material.oauthType != "code_assist" || material.quotaProjectID != "proj-1" {
		t.Fatalf("材料字段不符: %#v", material)
	}
}

// ---- 代理信封 ----

func TestBuildProxyEnvelopeBranches(t *testing.T) {
	secret := "wo-proxy-secret"
	proxyWithoutID := postgresCandidate{}
	proxy0, version0, err := buildProxyEnvelope(secret, proxyWithoutID)
	if err != nil || proxy0 != nil || version0 != "direct" {
		t.Fatalf("无代理应返回 direct: %#v %s %v", proxy0, version0, err)
	}
	disabled := postgresCandidate{proxyID: validNullString("proxy-1"), proxyEnabled: sql.NullBool{Bool: false, Valid: true}, proxyPort: sql.NullInt64{Int64: 8080, Valid: true}, proxyHost: validNullString("h")}
	if _, _, err := buildProxyEnvelope(secret, disabled); err == nil {
		t.Fatalf("禁用代理应报错")
	}
	badPort := postgresCandidate{proxyID: validNullString("proxy-1"), proxyEnabled: sql.NullBool{Bool: true, Valid: true}, proxyPort: sql.NullInt64{Int64: 0, Valid: false}, proxyHost: validNullString("h")}
	if _, _, err := buildProxyEnvelope(secret, badPort); err == nil {
		t.Fatalf("端口缺失应报错")
	}
	emptyHost := postgresCandidate{proxyID: validNullString("proxy-1"), proxyEnabled: sql.NullBool{Bool: true, Valid: true}, proxyPort: sql.NullInt64{Int64: 8080, Valid: true}, proxyHost: validNullString(" ")}
	if _, _, err := buildProxyEnvelope(secret, emptyHost); err == nil {
		t.Fatalf("主机为空应报错")
	}
	badScheme := postgresCandidate{proxyID: validNullString("proxy-1"), proxyEnabled: sql.NullBool{Bool: true, Valid: true}, proxyPort: sql.NullInt64{Int64: 1080, Valid: true}, proxyHost: validNullString("h"), proxyType: validNullString("ftp")}
	if _, _, err := buildProxyEnvelope(secret, badScheme); err == nil {
		t.Fatalf("不支持的代理协议应报错")
	}
	// 用户名存在但密码为空：proxyPassword 缺失应报错。
	noPassword := postgresCandidate{proxyID: validNullString("proxy-1"), proxyEnabled: sql.NullBool{Bool: true, Valid: true}, proxyPort: sql.NullInt64{Int64: 8080, Valid: true}, proxyHost: validNullString("h"), proxyType: validNullString("http"), proxyUsername: validNullString("user")}
	if _, _, err := buildProxyEnvelope(secret, noPassword); err == nil {
		t.Fatalf("代理密码缺失应报错")
	}
	// 完整路径：socks5 映射为 socks5h，密码解密后进入信封。
	password, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"password":"pw-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	full := postgresCandidate{proxyID: validNullString("proxy-1"), proxyEnabled: sql.NullBool{Bool: true, Valid: true}, proxyPort: sql.NullInt64{Int64: 1080, Valid: true}, proxyHost: validNullString("h"), proxyType: validNullString("socks5"), proxyUsername: validNullString("user"), proxyPasswordEncrypted: validNullString(password)}
	envelope, version, err := buildProxyEnvelope(secret, full)
	if err != nil || envelope == nil || envelope.Kind != "proxy_url" || version == "" {
		t.Fatalf("完整代理应生成信封: %#v %q %v", envelope, version, err)
	}
}

func TestProxyPasswordBranches(t *testing.T) {
	secret := "wo-proxy-password-secret"
	if _, err := proxyPassword(secret, " "); err == nil {
		t.Fatalf("密码密文缺失应报错")
	}
	if _, err := proxyPassword(secret, "bad-envelope"); err == nil {
		t.Fatalf("密码解密失败应报错")
	}
	invalid, err := accounthealth.EncryptV1Envelope(secret, []byte("not-json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proxyPassword(secret, invalid); err == nil {
		t.Fatalf("密码 JSON 非法应报错")
	}
	empty, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"password":""}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proxyPassword(secret, empty); err == nil {
		t.Fatalf("密码为空应报错")
	}
	ok, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := proxyPassword(secret, ok); err != nil || got != "pw" {
		t.Fatalf("合法密码应解密: %q %v", got, err)
	}
}

// ---- resolveUpstreamModel 分支 ----

func woProfile(t *testing.T) modelcheckprofile.ProtocolProfile {
	t.Helper()
	profile, ok := modelcheckprofile.FindForModel("openai", "profile_openai_openai_v1", "gpt-5.6-sol")
	if !ok {
		t.Fatalf("测试档案应可解析")
	}
	return profile
}

func TestResolveUpstreamModelBranches(t *testing.T) {
	profile := woProfile(t)
	empty := Candidate{}
	if got, err := resolveUpstreamModel(profile, "m", empty); err != nil || got != "m" {
		t.Fatalf("无限制模型应原样返回: %q %v", got, err)
	}
	listed := Candidate{SupportedModels: []string{" a ", "m"}}
	if got, err := resolveUpstreamModel(profile, "m", listed); err != nil || got != "m" {
		t.Fatalf("受支持模型应原样返回: %q %v", got, err)
	}
	// 请求模型受限：端点模式无法解析家族 → 报错。
	restricted := Candidate{SupportedModels: []string{"other"}, EndpointMode: "bad_mode"}
	if _, err := resolveUpstreamModel(profile, "m", restricted); err == nil {
		t.Fatalf("端点模式无家族应报错")
	}
	// 家族不属于档案的源端点家族 → 报错。
	wrongFamily := Candidate{SupportedModels: []string{"other"}, EndpointMode: "messages_json"}
	if _, err := resolveUpstreamModel(profile, "m", wrongFamily); err == nil {
		t.Fatalf("源端点家族不匹配应报错")
	}
	// 存在启用映射但上游不在受支持列表 → 报错。
	noUpstream := Candidate{SupportedModels: []string{"other"}, EndpointMode: "responses_json", ModelMappings: []ModelMapping{{
		Enabled: true, SourceModel: "m", UpstreamModel: "ghost", SourceEndpointFamily: "responses_json", UpstreamEndpointFamily: "responses_json",
	}}}
	if _, err := resolveUpstreamModel(profile, "m", noUpstream); err == nil {
		t.Fatalf("上游不在受支持列表应报错")
	}
	// 映射存在但禁用/源模型不匹配/上游为空 → 最终报错。
	disabled := Candidate{SupportedModels: []string{"other"}, EndpointMode: "responses_json", ModelMappings: []ModelMapping{{
		Enabled: false, SourceModel: "m", UpstreamModel: "up", SourceEndpointFamily: "responses_json", UpstreamEndpointFamily: "responses_json",
	}}}
	if _, err := resolveUpstreamModel(profile, "m", disabled); err == nil {
		t.Fatalf("禁用映射不应生效")
	}
	// 命中兼容映射：返回上游模型（端点家族是 "responses" 而非模式 "responses_json"）。
	mapped := Candidate{SupportedModels: []string{"up"}, EndpointMode: "responses_json", ModelMappings: []ModelMapping{{
		Enabled: true, SourceModel: "m", UpstreamModel: "up", SourceEndpointFamily: "responses", UpstreamEndpointFamily: "responses",
	}}}
	if got, err := resolveUpstreamModel(profile, "m", mapped); err != nil || got != "up" {
		t.Fatalf("映射应解析为上游模型: %q %v", got, err)
	}
	// 命中映射但要求不支持的跨族转换 → 报错。
	incompatible := Candidate{SupportedModels: []string{"up"}, EndpointMode: "responses_json", ModelMappings: []ModelMapping{{
		Enabled: true, SourceModel: "m", UpstreamModel: "up", SourceEndpointFamily: "responses", UpstreamEndpointFamily: "messages",
	}}}
	if _, err := resolveUpstreamModel(profile, "m", incompatible); err == nil {
		t.Fatalf("不支持的跨族转换应报错")
	}
	// 无任何可用映射 → 报错。
	none := Candidate{SupportedModels: []string{"other"}, EndpointMode: "responses_json"}
	if _, err := resolveUpstreamModel(profile, "m", none); err == nil {
		t.Fatalf("无映射应报错")
	}
}

// ---- Freeze 校验分支 ----

func woBaseCandidate(t *testing.T, secret string) Candidate {
	t.Helper()
	envelope, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"api_keys":["k"],"base_url":"https://x.example","supported_endpoint_modes":["responses_json"]}`))
	if err != nil {
		t.Fatal(err)
	}
	return Candidate{
		AccountID: "account-1", SystemAccountID: "system-1",
		TargetName: "T", TargetOwnerSystemID: "system-1", GroupID: "group-1",
		ConfigRevision: "7", ProviderCode: "openai", ProtocolProfileID: "profile_openai_openai_v1",
		ProtocolRevision: "rev-1", Status: "active", Eligible: true,
		EndpointMode: "responses_json", Endpoint: "https://x.example",
		CredentialType: "api_key",
		Credential:     accounthealth.CredentialEnvelope{Kind: "account_credentials", Ciphertext: envelope},
	}
}

func TestFreezeValidationBranches(t *testing.T) {
	secret := "wo-freeze-identity"
	request := Request{SystemAccountID: "system-1", AccountID: "account-1", Model: "gpt-5.6-sol"}
	if _, err := Freeze(request, woBaseCandidate(t, secret), " "); err == nil {
		t.Fatalf("身份密钥缺失应报错")
	}
	if _, err := Freeze(Request{SystemAccountID: "system-1"}, woBaseCandidate(t, secret), secret); err == nil {
		t.Fatalf("请求不完整应报错")
	}
	if _, err := Freeze(request, woBaseCandidate(t, secret), secret); err != nil {
		t.Fatalf("合法冻结不应报错: %v", err)
	}
	mismatched := woBaseCandidate(t, secret)
	mismatched.SystemAccountID = "system-2"
	if _, err := Freeze(request, mismatched, secret); err == nil {
		t.Fatalf("作用域不匹配应报错")
	}
	ineligible := woBaseCandidate(t, secret)
	ineligible.Eligible = false
	if _, err := Freeze(request, ineligible, secret); err == nil {
		t.Fatalf("不可用账号应报错")
	}
	disabled := woBaseCandidate(t, secret)
	disabled.Status = "Disabled"
	if _, err := Freeze(request, disabled, secret); err == nil {
		t.Fatalf("disabled 账号应报错")
	}
	isolated := woBaseCandidate(t, secret)
	isolated.Status = "quality_isolated"
	if _, err := Freeze(request, isolated, secret); err == nil {
		t.Fatalf("未授权质量隔离应报错")
	}
	if _, err := Freeze(request, woBaseCandidate(t, secret), secret); err != nil {
		t.Fatalf("基线不应报错: %v", err)
	}
	badModel := request
	badModel.Model = "unknown-model"
	if _, err := Freeze(badModel, woBaseCandidate(t, secret), secret); err == nil {
		t.Fatalf("档案不支持的模型应报错")
	}
	modeMismatch := woBaseCandidate(t, secret)
	modeMismatch.EndpointMode = "chat_json"
	if _, err := Freeze(request, modeMismatch, secret); err == nil {
		t.Fatalf("端点模式与档案协议不匹配应报错")
	}
	incomplete := woBaseCandidate(t, secret)
	incomplete.ConfigRevision = " "
	if _, err := Freeze(request, incomplete, secret); err == nil {
		t.Fatalf("快照不完整应报错")
	}
	// CredentialRef 缺失时回退指纹；ProxyVersion 缺失且 Proxy 为空时回退 direct。
	fallback := woBaseCandidate(t, secret)
	fallback.CredentialRef = ""
	fallback.ProxyVersion = ""
	frozen, err := Freeze(request, fallback, secret)
	if err != nil {
		t.Fatalf("回退路径不应报错: %v", err)
	}
	if frozen.DurableAccount.CredentialEnvelopeRef == "" || frozen.DurableAccount.ProxyConfigurationVersion != "direct" {
		t.Fatalf("回退凭据引用/代理版本不符: %#v", frozen.DurableAccount)
	}
}

// ---- 读取器构造与 SQLite 管理作用域 ----

func TestNewPostgresReaderValidation(t *testing.T) {
	if _, err := NewPostgresReader(nil, "s", "i", time.Now); err == nil {
		t.Fatalf("nil 数据库应报错")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewPostgresReader(db, " ", "i", time.Now); err == nil {
		t.Fatalf("凭据密钥缺失应报错")
	}
	if _, err := NewPostgresReader(db, "s", "", time.Now); err == nil {
		t.Fatalf("身份密钥缺失应报错")
	}
	reader, err := NewPostgresReader(db, "s", "i", nil)
	if err != nil || reader == nil {
		t.Fatalf("nil 时钟应回退系统时钟: %v", err)
	}
}

func TestOpenSQLiteReaderLifecycle(t *testing.T) {
	if _, err := OpenSQLiteReader("", "s", "i", time.Now); err == nil {
		t.Fatalf("空路径应报错")
	}
	path := openWoSQLiteBusinessDB(t)
	reader, err := OpenSQLiteReader(path, "wo-secret", "wo-identity", time.Now)
	if err != nil {
		t.Fatalf("打开只读读取器不应报错: %v", err)
	}
	// 管理作用域：命中账号返回其归属系统账号。
	scope, err := reader.ResolveManagementSystemAccount(context.Background(), "account-1")
	if err != nil || scope != "system-1" {
		t.Fatalf("管理作用域应为 system-1: %q %v", scope, err)
	}
	if _, err := reader.ResolveManagementSystemAccount(context.Background(), "missing"); err == nil {
		t.Fatalf("缺失账号应报错")
	}
	if _, err := reader.ResolveManagementSystemAccount(context.Background(), "account-deleted"); err == nil {
		t.Fatalf("已删除账号应视为不存在")
	}
	if _, err := reader.ResolveManagementSystemAccount(context.Background(), "account-empty"); err == nil {
		t.Fatalf("空系统作用域应报错")
	}
	if _, err := reader.ResolveManagementSystemAccount(context.Background(), " "); err == nil {
		t.Fatalf("空账号应报错")
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("关闭不应报错: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("重复关闭应幂等: %v", err)
	}
	// 非持有连接的读取器 Close 不触碰外部 db。
	var detached *SQLiteReader
	if err := detached.Close(); err != nil {
		t.Fatalf("nil 读取器关闭应安全: %v", err)
	}
}

func TestSQLiteReaderNilReceiverGuards(t *testing.T) {
	var reader *SQLiteReader
	if err := reader.CheckContract(context.Background()); err == nil {
		t.Fatalf("nil 读取器契约检查应报错")
	}
	if _, err := reader.FreezeTarget(context.Background(), Request{}); err == nil {
		t.Fatalf("nil 读取器冻结应报错")
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	attached, err := NewSQLiteReader(db, "s", "i", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attached.FreezeTarget(context.Background(), Request{SystemAccountID: " ", AccountID: " ", Model: " "}); err == nil {
		t.Fatalf("冻结请求不完整应报错")
	}
	if _, err := reader.ResolveManagementSystemAccount(context.Background(), "account-1"); err == nil {
		t.Fatalf("nil 读取器作用域解析应报错")
	}
}

func TestSQLiteReaderFreezeTargetUnsupportedModelPath(t *testing.T) {
	const secret = "wo-unsupported-model-secret"
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(sqliteReaderFixtureSchema); err != nil {
		t.Fatal(err)
	}
	credentials, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"api_keys":["k"],"base_url":"https://x.example","supported_endpoint_modes":["responses_json"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO provider_protocol_profiles(id,enabled,base_url,updated_at) VALUES(?,?,?,?)`, "profile_openai_openai_v1", 1, "https://api.openai.com", "2026-08-27T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,config_revision,status,schedulable,health_check_endpoint_mode,provider_code,provider_protocol_profile_id,protocol_code,protocol_version,type,credentials_encrypted,deleted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,NULL)`, "account-1", "system-1", 7, "active", 1, "responses_json", "openai", "profile_openai_openai_v1", "openai", "v1", "api_key", credentials); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups(id,system_account_id,enabled) VALUES(?,?,?)`, "group-1", "system-1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(system_account_id,group_id,account_id,account_authorization_id,enabled) VALUES(?,?,?,?,?)`, "system-1", "group-1", "account-1", nil, 1); err != nil {
		t.Fatal(err)
	}
	reader, err := NewSQLiteReader(db, secret, "wo-identity", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	// 候选存在但模型不受支持：冻结在档案校验处失败。
	if _, err := reader.FreezeTarget(context.Background(), Request{SystemAccountID: "system-1", AccountID: "account-1", Model: "not-in-profile"}); err == nil {
		t.Fatalf("不受支持模型应导致冻结失败")
	}
	// 候选不存在：报错文案应指向账号不可用。
	if _, err := reader.FreezeTarget(context.Background(), Request{SystemAccountID: "system-1", AccountID: "ghost", Model: "gpt-5.6-sol"}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("缺失候选应报账号不存在: %v", err)
	}
}

func TestSQLiteReaderResolveStaleSnapshot(t *testing.T) {
	const secret = "wo-stale-secret"
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(sqliteReaderFixtureSchema); err != nil {
		t.Fatal(err)
	}
	credentials, err := accounthealth.EncryptV1Envelope(secret, []byte(`{"api_keys":["k"],"base_url":"https://x.example","supported_endpoint_modes":["responses_json"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO provider_protocol_profiles(id,enabled,base_url,updated_at) VALUES(?,?,?,?)`, "profile_openai_openai_v1", 1, "https://api.openai.com", "2026-08-27T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(id,system_account_id,config_revision,status,schedulable,health_check_endpoint_mode,provider_code,provider_protocol_profile_id,protocol_code,protocol_version,type,credentials_encrypted,deleted_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,NULL)`, "account-1", "system-1", 7, "active", 1, "responses_json", "openai", "profile_openai_openai_v1", "openai", "v1", "api_key", credentials); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO groups(id,system_account_id,enabled) VALUES(?,?,?)`, "group-1", "system-1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO group_accounts(system_account_id,group_id,account_id,account_authorization_id,enabled) VALUES(?,?,?,?,?)`, "system-1", "group-1", "account-1", nil, 1); err != nil {
		t.Fatal(err)
	}
	reader, err := NewSQLiteReader(db, secret, "wo-identity", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	stale := modelcheckinput.AccountSnapshot{ID: "account-1", ConfigRevision: "999"}
	request := modelcheckexecutor.ResolutionRequest{
		Input:   modelcheckinput.IssuedInput{SystemAccountID: "system-1", Model: "gpt-5.6-sol", Trigger: modelcheckinput.TriggerManual},
		Account: stale,
	}
	if _, err := reader.Resolve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("过期快照应报错: %v", err)
	}
}

// ---- 测试辅助 ----

func validNullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

// openWoSQLiteBusinessDB 创建带最小 accounts 表的业务库文件，
// 供只读读取器的生命周期与管理作用域测试使用。
func openWoSQLiteBusinessDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "business.sqlite3")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("打开业务库失败: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE accounts (id TEXT PRIMARY KEY, system_account_id TEXT NOT NULL, deleted_at TEXT)`); err != nil {
		t.Fatalf("建 accounts 表失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, deleted_at) VALUES ('account-1', 'system-1', NULL)`); err != nil {
		t.Fatalf("插入账号失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, deleted_at) VALUES ('account-empty', ' ', NULL)`); err != nil {
		t.Fatalf("插入空白作用域账号失败: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, system_account_id, deleted_at) VALUES ('account-deleted', 'system-1', '2026-01-01T00:00:00.000Z')`); err != nil {
		t.Fatalf("插入已删除账号失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭业务库失败: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("业务库文件应存在: %v", err)
	}
	return path
}
