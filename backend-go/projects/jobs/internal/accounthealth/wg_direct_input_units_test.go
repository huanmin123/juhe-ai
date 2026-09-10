package accounthealth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// rawJSON 是凭据 JSON 字段读取助手的值类型别名。
type rawJSON = json.RawMessage

// getenvFrom 构造 env 读取闭包。
func getenvFrom(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// TestConfigHelpersMatrix 表驱动覆盖 config.go 的四个解析助手（默认回落、
// 非法值、边界）。
func TestConfigHelpersMatrix(t *testing.T) {
	t.Run("configDuration", func(t *testing.T) {
		duration, err := configDuration(getenvFrom(nil), "X", 5*time.Second, time.Second)
		if err != nil || duration != 5*time.Second {
			t.Fatalf("缺省必须回落: %v %v", duration, err)
		}
		duration, err = configDuration(getenvFrom(map[string]string{"X": "10s"}), "X", time.Second, time.Second)
		if err != nil || duration != 10*time.Second {
			t.Fatalf("合法值必须生效: %v %v", duration, err)
		}
		for _, bad := range []string{"abc", "500ms"} {
			if _, err := configDuration(getenvFrom(map[string]string{"X": bad}), "X", time.Second, time.Second); err == nil {
				t.Fatalf("%q 必须报错", bad)
			}
		}
	})
	t.Run("configInt", func(t *testing.T) {
		value, err := configInt(getenvFrom(nil), "X", 7, 1, 10)
		if err != nil || value != 7 {
			t.Fatalf("缺省必须回落: %d %v", value, err)
		}
		if value, err = configInt(getenvFrom(map[string]string{"X": "9"}), "X", 1, 1, 10); err != nil || value != 9 {
			t.Fatalf("合法值必须生效: %d %v", value, err)
		}
		for _, bad := range []string{"abc", "0", "11"} {
			if _, err := configInt(getenvFrom(map[string]string{"X": bad}), "X", 1, 1, 10); err == nil {
				t.Fatalf("%q 必须报错", bad)
			}
		}
	})
	t.Run("configPositiveInt", func(t *testing.T) {
		value, err := configPositiveInt(getenvFrom(nil), "X", 3)
		if err != nil || value != 3 {
			t.Fatalf("缺省必须回落: %d %v", value, err)
		}
		if value, err = configPositiveInt(getenvFrom(map[string]string{"X": "2"}), "X", 1); err != nil || value != 2 {
			t.Fatalf("正整数必须生效: %d %v", value, err)
		}
		for _, bad := range []string{"abc", "0", "-1"} {
			if _, err := configPositiveInt(getenvFrom(map[string]string{"X": bad}), "X", 1); err == nil {
				t.Fatalf("%q 必须报错", bad)
			}
		}
	})
	t.Run("configInt64", func(t *testing.T) {
		value, err := configInt64(getenvFrom(nil), "X", 8, 1, 100)
		if err != nil || value != 8 {
			t.Fatalf("缺省必须回落: %d %v", value, err)
		}
		if value, err = configInt64(getenvFrom(map[string]string{"X": "50"}), "X", 1, 1, 100); err != nil || value != 50 {
			t.Fatalf("合法值必须生效: %d %v", value, err)
		}
		for _, bad := range []string{"abc", "0", "101"} {
			if _, err := configInt64(getenvFrom(map[string]string{"X": bad}), "X", 1, 1, 100); err == nil {
				t.Fatalf("%q 必须报错", bad)
			}
		}
	})
	t.Run("configMilliseconds", func(t *testing.T) {
		duration, err := configMilliseconds(getenvFrom(nil), "X", time.Hour, time.Minute, 7*24*time.Hour)
		if err != nil || duration != time.Hour {
			t.Fatalf("缺省必须回落: %v %v", duration, err)
		}
		duration, err = configMilliseconds(getenvFrom(map[string]string{"X": "7200000"}), "X", time.Hour, time.Minute, 7*24*time.Hour)
		if err != nil || duration != 2*time.Hour {
			t.Fatalf("合法毫秒必须生效: %v %v", duration, err)
		}
		for _, bad := range []string{"abc", "0", "-5", "999", "999999999"} {
			if _, err := configMilliseconds(getenvFrom(map[string]string{"X": bad}), "X", time.Hour, time.Minute, 7*24*time.Hour); err == nil {
				t.Fatalf("%q 必须报错", bad)
			}
		}
	})
}

// TestValidateSQLiteIsolation 覆盖 store/input 路径隔离的放行与冲突分支。
func TestValidateSQLiteIsolation(t *testing.T) {
	base := map[string]string{}
	if err := validateSQLiteIsolation("/tmp/j1/jobs.sqlite3", "/tmp/j1/inputs", getenvFrom(base)); err != nil {
		t.Fatalf("无冲突路径必须放行: %v", err)
	}
	// 与业务库共用文件 → 报错。
	if err := validateSQLiteIsolation("/tmp/j1/jobs.sqlite3", "/tmp/j1/inputs", getenvFrom(map[string]string{
		"JUHE_AI_DATABASE_PATH": "/TMP/J1/jobs.sqlite3",
	})); err == nil {
		t.Fatal("共用业务库文件必须报错（Windows 大小写不敏感等价）")
	}
	// store 放进 input 目录 → 报错。
	if err := validateSQLiteIsolation("/tmp/j1/inputs/jobs.sqlite3", "/tmp/j1/inputs", getenvFrom(base)); err == nil {
		t.Fatal("store 不得放入 input 目录")
	}
	// input 目录内一层子目录仍然算放入 → 报错。
	if err := validateSQLiteIsolation("/tmp/j1/inputs/sub/jobs.sqlite3", "/tmp/j1/inputs", getenvFrom(base)); err == nil {
		t.Fatal("input 子目录内的 store 必须报错")
	}
	// input 目录之外 → 放行。
	if err := validateSQLiteIsolation("/tmp/j1/other/jobs.sqlite3", "/tmp/j1/inputs", getenvFrom(base)); err != nil {
		t.Fatalf("input 之外的 store 必须放行: %v", err)
	}
}

// TestEqualPathWindowsCaseInsensitive 锁定 Windows 路径等价比较语义。
func TestEqualPathWindowsCaseInsensitive(t *testing.T) {
	if !equalPath(`C:\A\B`, `c:\a\b`) {
		t.Fatal("Windows 路径必须大小写不敏感等价")
	}
	if equalPath(`C:\A\B`, `C:\A\C`) {
		t.Fatal("不同路径不得判等")
	}
}

// TestCryptoEnvelopeRoundTripAndRejects 覆盖 V1 凭据封套的往返、空 secret
// 拒绝与各损坏分支。
func TestCryptoEnvelopeRoundTripAndRejects(t *testing.T) {
	if _, err := EncryptV1Envelope("  ", []byte("x")); err == nil {
		t.Fatal("空 secret 必须拒绝加密")
	}
	envelope, err := EncryptV1Envelope("secret-1", []byte(`{"api_key":"k"}`))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := DecryptV1Envelope("secret-1", envelope)
	if err != nil || string(plaintext) != `{"api_key":"k"}` {
		t.Fatalf("往返必须还原明文: %s %v", plaintext, err)
	}
	if _, err := DecryptV1Envelope("secret-2", envelope); err == nil {
		t.Fatal("错误 secret 必须认证失败")
	}
	for _, bad := range []string{
		"",
		"not-an-envelope",
		"v1:only:three",
		"v2:a:b:c",
		"v1:!!!:!!!:!!!",
		"v1:" + "AQ" + ":" + "AQ" + ":" + "AQ", // IV/tag 长度无效
	} {
		if _, err := DecryptV1Envelope("secret-1", bad); err == nil {
			t.Fatalf("损坏 envelope %q 必须报错", bad)
		}
	}
}

// TestIsSupportedDirectProfileMatrix 表驱动锁定协议 profile 支持矩阵
// （provider/type/mode 组合是 J1 的探活资格边界）。
func TestIsSupportedDirectProfileMatrix(t *testing.T) {
	cases := []struct {
		profile  string
		provider string
		typ      string
		mode     string
		want     bool
	}{
		{"", "openai", "api_key", "chat_json", true},
		{"", "openai", "api_key", "generate_content_json", false},
		{"", "gpt", "oauth", "responses_sse", true},
		{"", "gpt", "oauth", "chat_json", false},
		{"", "anthropic", "api_key", "chat_json", false},
		{"profile_gpt_openai_v1", "gpt", "api_key", "chat_json", true},
		{"profile_gpt_openai_v1", "gpt", "api_key", "images_json", true},
		{"profile_gpt_openai_v1", "openai", "api_key", "chat_json", false},
		{"profile_openai_openai_v1", "openai", "api_key", "responses_json", true},
		{"profile_openai_openai_v1", "openai", "oauth", "chat_json", false},
		{"profile_xai_openai_v1", "xai", "api_key", "chat_sse", true},
		{"profile_deepseek_openai_v1", "deepseek", "api_key", "chat_json", true},
		{"profile_deepseek_openai_v1", "deepseek", "oauth", "chat_json", false},
		{"profile_glm_general_openai_v1", "glm", "api_key", "chat_json", true},
		{"profile_glm_general_openai_v1", "glm", "api_key", "responses_json", false},
		{"profile_glm_coding_openai_v1", "glm", "api_key", "chat_sse", true},
		{"profile_gemini_openai_chat_v1beta", "gemini", "api_key", "chat_json", true},
		{"profile_hybrid_openai_chat_v1", "hybrid", "api_key", "messages_json", true},
		{"profile_hybrid_openai_chat_v1", "hybrid", "api_key", "generate_content_sse", true},
		{"profile_hybrid_anthropic_messages_v1", "hybrid", "api_key", "messages_sse", true},
		{"profile_hybrid_anthropic_messages_v1", "hybrid", "api_key", "chat_json", false},
		{"profile_hybrid_gemini_native_v1beta", "hybrid", "api_key", "generate_content_json", true},
		{"profile_hybrid_gemini_native_v1beta", "hybrid", "api_key", "interactions_json", true},
		{"profile_anthropic_anthropic_v1", "anthropic", "oauth", "messages_json", true},
		{"profile_anthropic_anthropic_v1", "anthropic", "api_key", "chat_json", false},
		{"profile_deepseek_anthropic_v1", "deepseek", "api_key", "messages_json", true},
		{"profile_glm_coding_anthropic_v1", "glm", "api_key", "messages_sse", true},
		{"profile_gemini_native_v1beta", "gemini", "api_key", "generate_content_json", true},
		{"profile_gemini_native_v1beta", "gemini", "google_oauth", "interactions_sse", true},
		{"profile_gemini_native_v1beta", "gemini", "google_oauth", "chat_json", false},
		{"profile_unknown", "openai", "api_key", "chat_json", false},
	}
	for _, item := range cases {
		if got := isSupportedDirectProfile(item.profile, item.provider, item.typ, item.mode); got != item.want {
			t.Errorf("isSupportedDirectProfile(%q,%q,%q,%q)=%v 期望 %v", item.profile, item.provider, item.typ, item.mode, got, item.want)
		}
	}
}

// TestDirectProbeProtocolForModeMatrix 锁定 profile/mode → 探针协议映射。
func TestDirectProbeProtocolForModeMatrix(t *testing.T) {
	cases := []struct {
		profile  string
		provider string
		mode     string
		want     string
	}{
		{"profile_anthropic_anthropic_v1", "anthropic", "messages_json", "anthropic"},
		{"profile_deepseek_anthropic_v1", "deepseek", "messages_sse", "anthropic"},
		{"profile_glm_coding_anthropic_v1", "glm", "messages_json", "anthropic"},
		{"profile_gemini_native_v1beta", "gemini", "generate_content_json", "gemini"},
		{"profile_gpt_openai_v1", "gpt", "chat_json", "openai"},
		{"profile_hybrid_openai_chat_v1", "hybrid", "messages_json", "anthropic"},
		{"profile_hybrid_openai_chat_v1", "hybrid", "generate_content_sse", "gemini"},
		{"profile_hybrid_openai_chat_v1", "hybrid", "chat_sse", "openai"},
		{"profile_hybrid_openai_chat_v1", "hybrid", "bogus", ""},
		{"profile_hybrid_anthropic_messages_v1", "hybrid", "messages_json", "anthropic"},
		{"profile_hybrid_gemini_native_v1beta", "hybrid", "generate_content_json", "gemini"},
		{"", "openai", "chat_json", "openai"},
		{"", "anthropic", "chat_json", ""},
	}
	for _, item := range cases {
		if got := directProbeProtocolForMode(item.profile, item.provider, item.mode); got != item.want {
			t.Errorf("directProbeProtocolForMode(%q,%q,%q)=%q 期望 %q", item.profile, item.provider, item.mode, got, item.want)
		}
	}
}

// TestProbeProfileProviderMatrix 锁定 profile → provider 归属映射。
func TestProbeProfileProviderMatrix(t *testing.T) {
	cases := map[string]string{
		"profile_gpt_openai_v1":                "gpt",
		"profile_openai_openai_v1":             "openai",
		"profile_xai_openai_v1":                "xai",
		"profile_deepseek_openai_v1":           "deepseek",
		"profile_deepseek_anthropic_v1":        "deepseek",
		"profile_glm_general_openai_v1":        "glm",
		"profile_glm_coding_openai_v1":         "glm",
		"profile_glm_coding_anthropic_v1":      "glm",
		"profile_anthropic_anthropic_v1":       "anthropic",
		"profile_gemini_native_v1beta":         "gemini",
		"profile_gemini_openai_chat_v1beta":    "gemini",
		"profile_hybrid_openai_chat_v1":        "hybrid",
		"profile_hybrid_anthropic_messages_v1": "hybrid",
		"profile_hybrid_gemini_native_v1beta":  "hybrid",
		"":                                     "openai",
	}
	for profile, protocol := range cases {
		if got := probeProfileProvider(profile, protocol); got != protocol {
			t.Errorf("probeProfileProvider(%q)=%q 期望 %q", profile, got, protocol)
		}
	}
}

// TestDirectBaseURLResolution 覆盖 base_url 解析：凭据配置优先、各 profile
// 冻结端点、hybrid 缺 URL 报错、协议兜底。
func TestDirectBaseURLResolution(t *testing.T) {
	credentials := map[string]rawJSON{"base_url": rawJSON(`"https://example.com/v1"`)}
	base, err := directBaseURL(credentials, DirectAccount{Provider: "openai", Type: "api_key"}, "openai")
	if err != nil || base != "https://example.com/v1" {
		t.Fatalf("凭据 base_url 必须优先: %q %v", base, err)
	}
	trailing := map[string]rawJSON{"base_url": rawJSON(`"https://example.com/v1///"`)}
	base, err = directBaseURL(trailing, DirectAccount{Provider: "openai", Type: "api_key"}, "openai")
	if err != nil || base != "https://example.com/v1" {
		t.Fatalf("尾斜杠必须裁剪: %q %v", base, err)
	}
	frozen := []struct {
		profile string
		typ     string
		want    string
	}{
		{"profile_gpt_openai_v1", "oauth", "https://chatgpt.com/backend-api/codex"},
		{"profile_xai_openai_v1", "oauth", "https://cli-chat-proxy.grok.com/v1"},
		{"profile_xai_openai_v1", "api_key", "https://api.x.ai/v1"},
		{"profile_deepseek_openai_v1", "api_key", "https://api.deepseek.com"},
		{"profile_deepseek_anthropic_v1", "api_key", "https://api.deepseek.com/anthropic"},
		{"profile_glm_general_openai_v1", "api_key", "https://open.bigmodel.cn/api/paas/v4"},
		{"profile_glm_coding_openai_v1", "api_key", "https://open.bigmodel.cn/api/coding/paas/v4"},
		{"profile_glm_coding_anthropic_v1", "api_key", "https://open.bigmodel.cn/api/anthropic"},
		{"profile_gemini_openai_chat_v1beta", "api_key", "https://generativelanguage.googleapis.com/v1beta/openai"},
		{"profile_anthropic_anthropic_v1", "api_key", "https://api.anthropic.com"},
		{"profile_hybrid_anthropic_messages_v1", "api_key", "https://api.anthropic.com"},
		{"profile_hybrid_gemini_native_v1beta", "api_key", "https://generativelanguage.googleapis.com"},
	}
	for _, item := range frozen {
		base, err := directBaseURL(map[string]rawJSON{}, DirectAccount{ProtocolProfileID: item.profile, Type: item.typ}, "openai")
		if err != nil || base != item.want {
			t.Errorf("directBaseURL(%s,%s)=%q,%v 期望 %q", item.profile, item.typ, base, err, item.want)
		}
	}
	// Gemini 原生 OAuth 分叉。
	geminiBase, err := directBaseURL(map[string]rawJSON{"oauth_type": rawJSON(`"code_assist"`)}, DirectAccount{ProtocolProfileID: "profile_gemini_native_v1beta", Type: "google_oauth"}, "gemini")
	if err != nil || geminiBase != "https://cloudcode-pa.googleapis.com" {
		t.Fatalf("code_assist 必须走 cloudcode 端点: %q %v", geminiBase, err)
	}
	// hybrid 缺冻结 URL → 显式报错。
	if _, err := directBaseURL(map[string]rawJSON{}, DirectAccount{ProtocolProfileID: "profile_hybrid_openai_chat_v1", Type: "api_key"}, "openai"); err == nil {
		t.Fatal("hybrid 缺 base_url 必须报错")
	}
	// 协议兜底。
	for protocol, want := range map[string]string{"anthropic": "https://api.anthropic.com", "gemini": "https://generativelanguage.googleapis.com", "openai": "https://api.openai.com"} {
		if base, _ := directBaseURL(map[string]rawJSON{}, DirectAccount{Provider: "unknown"}, protocol); base != want {
			t.Errorf("协议兜底 %q → %q 期望 %q", protocol, base, want)
		}
	}
}

// TestDirectAPIKeysDedup 覆盖 Key 池提取的列表/单值/去重/空白分支。
func TestDirectAPIKeysDedup(t *testing.T) {
	keys := directAPIKeys(map[string]rawJSON{
		"api_keys": rawJSON(`["a", " ", "b", "a"]`),
	})
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("api_keys 列表必须去重去空白: %v", keys)
	}
	single := directAPIKeys(map[string]rawJSON{"api_key": rawJSON(`"solo"`)})
	if len(single) != 1 || single[0] != "solo" {
		t.Fatalf("单 api_key 必须兜底: %v", single)
	}
	if keys := directAPIKeys(map[string]rawJSON{}); len(keys) != 0 {
		t.Fatalf("无凭据必须为空: %v", keys)
	}
}

// TestDirectStringAndTimeHelpers 覆盖 JSON 字段读取助手。
func TestDirectStringAndTimeHelpers(t *testing.T) {
	values := map[string]rawJSON{
		"good":   rawJSON(`" text "`),
		"blank":  rawJSON(`"  "`),
		"number": rawJSON(`42`),
		"when":   rawJSON(`"2026-09-10T00:00:00Z"`),
		"bad":    rawJSON(`"nope"`),
	}
	if value, ok := directString(values, "good"); !ok || value != "text" {
		t.Fatalf("directString 必须裁剪: %q %v", value, ok)
	}
	if _, ok := directString(values, "blank"); ok {
		t.Fatal("空白值必须视为缺失")
	}
	if _, ok := directString(values, "number"); ok {
		t.Fatal("非字符串必须视为缺失")
	}
	if _, ok := directString(values, "missing"); ok {
		t.Fatal("缺键必须视为缺失")
	}
	if parsed, ok := directTime(values, "when"); !ok || parsed.Year() != 2026 {
		t.Fatalf("directTime 必须解析 RFC3339: %v %v", parsed, ok)
	}
	if _, ok := directTime(values, "bad"); ok {
		t.Fatal("非法时间必须失败")
	}
}

// TestValidateDirectProtocolMetadataMatrix 锁定 profile/protocol_code 一致性
// 校验（openai/anthropic/gemini 期望版本）。
func TestValidateDirectProtocolMetadataMatrix(t *testing.T) {
	valid := [][3]string{
		{"", "", ""},
		{"profile_openai_openai_v1", "openai", "v1"},
		{"profile_anthropic_anthropic_v1", "anthropic", "v1"},
		{"profile_gemini_native_v1beta", "gemini", "v1beta"},
		{"profile_openai_openai_v1", "", ""},
	}
	for _, item := range valid {
		if err := validateDirectProtocolMetadata(item[0], item[1], item[2]); err != nil {
			t.Errorf("validateDirectProtocolMetadata(%q,%q,%q) 不应报错: %v", item[0], item[1], item[2], err)
		}
	}
	invalid := [][3]string{
		{"profile_openai_openai_v1", "anthropic", "v1"},
		{"profile_gemini_native_v1beta", "gemini", "v1"},
		{"profile_anthropic_anthropic_v1", "openai", "v9"},
	}
	for _, item := range invalid {
		if err := validateDirectProtocolMetadata(item[0], item[1], item[2]); err == nil {
			t.Errorf("validateDirectProtocolMetadata(%q,%q,%q) 必须报错", item[0], item[1], item[2])
		}
	}
}

// TestDirectProxyEnvelopeMatrix 覆盖代理封套的合法与非法分支。
func TestDirectProxyEnvelopeMatrix(t *testing.T) {
	const secret = "proxy-secret"
	passwordEnvelope, err := EncryptV1Envelope(secret, []byte(`{"password":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	// 合法：带用户名密码的 socks5（归一 socks5h）。
	envelope, err := directProxyEnvelope(secret, DirectProxy{
		ID: "p1", Enabled: true, Type: "socks5", Host: "127.0.0.1", Port: 1080,
		Username: "u", PasswordEncrypted: passwordEnvelope,
	})
	if err != nil || envelope.Kind != "proxy_url" {
		t.Fatalf("合法代理必须产出封套: %+v %v", envelope, err)
	}
	plaintext, err := DecryptV1Envelope(secret, envelope.Ciphertext)
	if err != nil || !strings.Contains(string(plaintext), "socks5h://u:pw@127.0.0.1:1080") {
		t.Fatalf("代理 URL 必须归一 socks5h 并带凭据: %s %v", plaintext, err)
	}
	// 合法：无用户名（不需要密码）。
	if _, err := directProxyEnvelope(secret, DirectProxy{ID: "p2", Enabled: true, Type: "http", Host: "10.0.0.1", Port: 8080}); err != nil {
		t.Fatalf("匿名代理必须合法: %v", err)
	}
	invalid := []DirectProxy{
		{ID: "", Enabled: true, Type: "http", Host: "127.0.0.1", Port: 80},
		{ID: "p", Enabled: false, Type: "http", Host: "127.0.0.1", Port: 80},
		{ID: "p", Enabled: true, Type: "http", Host: "127.0.0.1", Port: 0},
		{ID: "p", Enabled: true, Type: "http", Host: "127.0.0.1", Port: 70000},
		{ID: "p", Enabled: true, Type: "gopher", Host: "127.0.0.1", Port: 80},
		{ID: "p", Enabled: true, Type: "http", Host: "127.0.0.1", Port: 80, Username: "u"},
		{ID: "p", Enabled: true, Type: "http", Host: "127.0.0.1", Port: 80, Username: "u", PasswordEncrypted: "bad"},
		{ID: "p", Enabled: true, Type: "http", Host: "127.0.0.1", Port: 80, Username: "u", PasswordEncrypted: mustEnvelopeJSON(t, secret, `{"other":"x"}`)},
	}
	for index, proxy := range invalid {
		if _, err := directProxyEnvelope(secret, proxy); err == nil {
			t.Errorf("非法代理 #%d 必须报错", index)
		}
	}
}

func mustEnvelopeJSON(t *testing.T, secret, plaintext string) string {
	t.Helper()
	envelope, err := EncryptV1Envelope(secret, []byte(plaintext))
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

// TestDirectKeyFingerprints 锁定 Key 指纹与 Key 集合指纹的确定性。
func TestDirectKeyFingerprints(t *testing.T) {
	first := directKeyFingerprint("secret", "key-a")
	second := directKeyFingerprint("secret", "key-a")
	third := directKeyFingerprint("secret", "key-b")
	if first != second || first == third {
		t.Fatal("HMAC 指纹必须确定且区分密钥")
	}
	keys := []APIKeyInput{{Fingerprint: "b"}, {Fingerprint: "a"}}
	if directKeySetFingerprint(keys) != directKeySetFingerprint([]APIKeyInput{{Fingerprint: "a"}, {Fingerprint: "b"}}) {
		t.Fatal("Key 集合指纹必须与顺序无关")
	}
}
