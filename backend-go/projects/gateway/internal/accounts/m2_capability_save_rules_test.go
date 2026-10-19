package accounts

// AI 账户能力域拆分 M2 服务端统一保存规则（A2）行为级测试（T4，计划
// §服务端统一校验；D6 三集合已废除，声明集严格 ⊆ 域允许集全链路强制）：
//   - 创建 ⊆ allowed 正/负例（矩阵外组合拒绝、默认集按域、健康检查默认落值）；
//   - 编辑严格声明校验（显式数组含域外值拒绝、域内缩小按普通编辑放行、
//     modes 未携带不重置、删键按域默认集重置且重置结果 ⊆ 域允许集（BUG-0323
//     根治后按域默认集恒域内）、域不可变、显式 null/空数组文案不变、gemini
//     chat 对话能力）；
//   - 克隆契约（404/403/域不等/回显）；
//   - 导入严格校验（混合包冲突 + 预览回显推导域 + 执行落域）；
//   - 批量同域整批拒绝 + 越界整批 400；
//   - 导出 domain 过滤映射。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/accounts/accountscore"
)

// accountscoreDefaultForTest 是门面谓词到 accountscore 按域默认集的测试桥。
func accountscoreDefaultForTest(providerCode, accountType string, profile protocolPredicateInput) []string {
	return accountscore.DefaultEndpointModesForDomain(accountscore.CapabilityDomainChat, providerCode, accountType, toProtocolPredicate(profile))
}

// m2SeedProvider 种一个供应商 + 协议档案 + 归属默认分组（gpt 之外的域矩阵
// 供应商）。
func m2SeedProvider(t *testing.T, env *testEnv, ownerID, code, profileID, protocolCode, protocolVersion string, accountTypes []string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	typesJSON, err := json.Marshal(accountTypes)
	if err != nil {
		t.Fatal(err)
	}
	env.exec(t, `INSERT OR IGNORE INTO providers (id, code, name, enabled, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?)`, "prov-"+code, code, code, now, now)
	env.exec(t, `INSERT OR IGNORE INTO provider_protocol_profiles (id, provider_code, name, enabled,
		protocol_code, protocol_version, base_url, default_health_check_model, account_types_json,
		capabilities_json, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, 'https://upstream.example.com/v1', 'm-default', ?, '[]', ?, ?)`,
		profileID, code, code+" 协议", protocolCode, protocolVersion, string(typesJSON), now, now)
	env.exec(t, `INSERT OR IGNORE INTO groups (id, system_account_id, name, provider_code, enabled, is_default, group_type, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, 1, 'personal', ?, ?)`, "grp-"+code+"-"+ownerID, ownerID, "默认分组-"+code, code, now, now)
}

// m2SeedAccountWithModes 直插一个带指定 modes 的存量账户（遗留混合账户夹具）。
func m2SeedAccountWithModes(t *testing.T, env *testEnv, id, ownerID, name string, modes []string) {
	t.Helper()
	credentials := Credentials{"api_key": "sk-mixed-" + id, "base_url": "https://api.openai.com/v1"}
	if modes != nil {
		credentials["supported_endpoint_modes"] = anyStrings(modes)
	}
	sealed, err := EncryptJSON(testSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// B4 W2：profile 列停写——直插行落空串，与 API 创建行的批量签名键一致。
	env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, created_at, updated_at)
		VALUES (?, ?, 'gpt', '', 'openai', 'v1', ?, 'api_key', 'active', ?, 'sk-***', 'gpt-4o-mini', ?, ?)`,
		id, ownerID, name, sealed, now, now)
}

// m2SeedDomainAccountWithModes 直插一个指定供应商档案与能力域的存量账户
// （M2 删键默认集用例夹具；与 m2SeedAccountWithModes 同款直插手法，绕过写
// 校验构造任意域 × 供应商组合）。
func m2SeedDomainAccountWithModes(t *testing.T, env *testEnv, id, ownerID, name, providerCode, profileID, domain string, modes []string) {
	t.Helper()
	credentials := Credentials{"api_key": "sk-domain-" + id, "base_url": "https://upstream.example.com/v1"}
	if modes != nil {
		credentials["supported_endpoint_modes"] = anyStrings(modes)
	}
	sealed, err := EncryptJSON(testSecret, credentials)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO accounts (id, system_account_id, provider_code, provider_protocol_profile_id,
		protocol_code, protocol_version, name, type, status, credentials_encrypted, credential_mask,
		health_check_model, capability_domain, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'openai', 'v1', ?, 'api_key', 'active', ?, 'sk-***', 'm-default', ?, ?, ?)`,
		id, ownerID, providerCode, profileID, name, sealed, domain, now, now)
}

// m2ConnectionContractByProvider 是 B1 断点批夹具的 provider → (连接 preset,
// 合法 base URL) 映射（registry §1.1 同词表；媒体 preset 用根路径 suffix）。
var m2ConnectionContractByProvider = map[string]struct {
	preset  string
	baseURL string
}{
	"gpt":        {"gpt_openai_v1", "https://api.openai.com/v1"},
	"xai":        {"xai_openai_v1", "https://api.x.ai/v1"},
	"deepseek":   {"deepseek_openai_v1", "https://api.deepseek.com"},
	"anthropic":  {"anthropic_v1", "https://api.anthropic.com/v1"},
	"gemini":     {"gemini_native_v1beta", "https://generativelanguage.googleapis.com"},
	"glm":        {"glm_general_openai_v1", "https://open.bigmodel.cn/api/paas/v4"},
	"minimax":    {"minimax_media", "https://api.minimax.chat"},
	"volcengine": {"volcengine_ark", "https://ark.cn-beijing.volces.com"},
	"qwen":       {"qwen_dashscope", "https://dashscope.aliyuncs.com"},
	"openai":     {"generic_openai_v1", "https://upstream.example.com/v1"},
	"hybrid":     {"generic_openai_v1", "https://upstream.example.com/v1"},
}

// m2CreateBody 构造 POST /accounts 负载。B1 断点批（设计 §8）：连接集合
// endpointModes 与请求 modes 同集显式提交（越界 modes 仍由写链既有域校验先行
// 拒绝），健康检查三件套显式提交（检查形态取 modes 中首个可检查形态，
// 检查模型取支持模型 m-default）。
func m2CreateBody(name, provider, profileID, accountType, domain string, credentials map[string]any, modes []string) string {
	body := map[string]any{
		"providerCode":              provider,
		"providerProtocolProfileId": profileID,
		"name":                      name,
		"type":                      accountType,
		"credentials":               credentials,
		"supportedModels":           []any{"m-default"},
		"status":                    "active",
	}
	if domain != "" {
		body["capabilityDomain"] = domain
	}
	if modes != nil {
		list := []any{}
		for _, mode := range modes {
			list = append(list, mode)
		}
		body["credentials"].(map[string]any)["supported_endpoint_modes"] = list
	}
	if contract, ok := m2ConnectionContractByProvider[provider]; ok {
		// gemini OpenAI Chat 档案按 profileID 覆写连接契约（registry preset
		// gemini_openai_chat_v1beta，suffix /v1beta/openai）。
		if provider == "gemini" && profileID == geminiOpenAIChatV1BetaProfileID {
			contract = struct {
				preset  string
				baseURL string
			}{"gemini_openai_chat_v1beta", "https://generativelanguage.googleapis.com/v1beta/openai"}
		}
		connectionModes := modes
		if len(connectionModes) == 0 {
			// 缺省 modes 臂（含显式空数组臂——该臂测的是凭据归一化对空数组的
			// 既有 500 路径，连接集合仍需合法非空 modes）：DefaultEndpointModes
			// 需要完整档案谓词，夹具谓词不可解析时按域 lane 收敛到该 preset 词表
			// 内的基础集合。
			effectiveDomain := domain
			if effectiveDomain == "" {
				effectiveDomain = accountscore.CapabilityDomainChat
			}
			connectionModes = map[string][]string{
				accountscore.CapabilityDomainChat:  {"chat_json", "chat_sse"},
				accountscore.CapabilityDomainImage: {"images_json"},
				accountscore.CapabilityDomainVideo: {"video_create", "video_get", "video_content", "video_cancel"},
				accountscore.CapabilityDomainAudio: {"audio_speech"},
			}[effectiveDomain]
			if len(connectionModes) == 0 {
				connectionModes = []string{"chat_json", "chat_sse"}
			}
		}
		connections := []any{map[string]any{
			"connectionPresetCode": contract.preset,
			"baseUrl":              contract.baseURL,
			"endpointModes":        connectionModes,
			"enabled":              true,
		}}
		body["connections"] = connections
		body["healthCheckConnectionPresetCode"] = contract.preset
		healthMode := ""
		for _, mode := range connectionModes {
			if accountHealthCheckEndpointModes[mode] {
				healthMode = mode
				break
			}
		}
		if healthMode == "" {
			healthMode = "chat_json"
		}
		body["healthCheckEndpointMode"] = healthMode
		body["healthCheckModel"] = "m-default"
	}
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// m2AccountModes 解密库内凭据并返回 supported_endpoint_modes。
func m2AccountModes(t *testing.T, env *testEnv, accountID string) []string {
	t.Helper()
	sealed := env.queryCell(t, `SELECT credentials_encrypted FROM accounts WHERE id = ?`, accountID)
	credentials := Credentials{}
	if err := DecryptJSON(testSecret, sealed, &credentials); err != nil {
		t.Fatal(err)
	}
	return credentialEndpointModes(credentials)
}

func m2Message(payload map[string]any) string {
	if message, ok := payload["message"].(string); ok {
		return message
	}
	return ""
}

func TestM2CreateCapabilityDomainContract(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)
	m2SeedProvider(t, env, adminID, "anthropic", "profile_anthropic_v1", "anthropic", "v1", []string{"api_key", "oauth"})
	m2SeedProvider(t, env, adminID, "volcengine", volcengineOpenAIV1ProfileID, "openai", "v1", []string{"api_key"})
	m2SeedProvider(t, env, adminID, "gemini", "profile_gemini_native_v1beta", "gemini", "v1beta", []string{"api_key", "google_oauth"})
	m2SeedProvider(t, env, adminID, "gemini", geminiOpenAIChatV1BetaProfileID, "openai", "v1", []string{"api_key"})
	m2SeedProvider(t, env, adminID, "deepseek", deepSeekOpenAIV1ProfileID, "openai", "v1", []string{"api_key"})
	m2SeedProvider(t, env, adminID, "xai", xaiOpenAIV1ProfileID, "openai", "v1", []string{"api_key", "oauth"})

	apiKeyCredentials := func() map[string]any {
		return map[string]any{"api_key": "sk-live-m2-" + strings.ToLower(t.Name()), "base_url": "https://api.openai.com/v1"}
	}

	// chat 缺省（域与 modes 都缺省）= 现行行为逐项不变（openai 族现行默认
	// 本就域内，BUG-0323 根治口径下逐项 pin）。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-chat-absent", "gpt", "prof-gpt", "api_key", "", apiKeyCredentials(), nil))
	if code != http.StatusCreated {
		t.Fatalf("chat 缺省创建应 201：%d %v", code, payload)
	}
	chatID := dataMap(t, payload)["id"].(string)
	if domain := env.queryCell(t, `SELECT capability_domain FROM accounts WHERE id = ?`, chatID); domain != "chat" {
		t.Fatalf("缺省域应落 chat：%s", domain)
	}
	if modes := m2AccountModes(t, env, chatID); !unorderedStringListEqual(modes, []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}) {
		t.Fatalf("chat 缺省默认集应与现行一致：%v", modes)
	}
	if mode := env.queryCell(t, `SELECT health_check_endpoint_mode FROM accounts WHERE id = ?`, chatID); mode != "chat_json" {
		t.Fatalf("chat 域健康检查默认应保持 chat_json：%s", mode)
	}

	// 显式 chat 域等价缺省。
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-chat-explicit", "gpt", "prof-gpt", "api_key", "chat", apiKeyCredentials(), nil))
	if code != http.StatusCreated {
		t.Fatalf("显式 chat 域创建应 201：%d", code)
	}

	// 非法域 400「能力域无效」。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-bogus-domain", "gpt", "prof-gpt", "api_key", "bogus", apiKeyCredentials(), nil))
	if code != http.StatusBadRequest || m2Message(payload) != "能力域无效" {
		t.Fatalf("非法域应 400 能力域无效：%d %v", code, payload)
	}

	// video 域：显式四值组 + 健康检查默认 video_create。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-video-explicit", "gpt", "prof-gpt", "api_key", "video", apiKeyCredentials(),
			[]string{"video_create", "video_get", "video_content", "video_cancel"}))
	if code != http.StatusCreated {
		t.Fatalf("gpt video 创建应 201：%d %v", code, payload)
	}
	videoID := dataMap(t, payload)["id"].(string)
	if domain := env.queryCell(t, `SELECT capability_domain FROM accounts WHERE id = ?`, videoID); domain != "video" {
		t.Fatalf("video 域应落列：%s", domain)
	}
	if modes := m2AccountModes(t, env, videoID); len(modes) != 4 {
		t.Fatalf("video 声明集应原样保留：%v", modes)
	}
	if mode := env.queryCell(t, `SELECT health_check_endpoint_mode FROM accounts WHERE id = ?`, videoID); mode != "video_create" {
		t.Fatalf("video 域健康检查默认应为 video_create：%s", mode)
	}

	// video 域 modes 缺省 → 按域默认集（volcengine 词表含视频四值组）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-video-absent", "volcengine", volcengineOpenAIV1ProfileID, "api_key", "video",
			map[string]any{"api_key": "ak-volc-m2", "base_url": "https://ark.example.com/api/v3"}, nil))
	if code != http.StatusCreated {
		t.Fatalf("volcengine video 缺省 modes 创建应 201：%d %v", code, payload)
	}
	volcVideoID := dataMap(t, payload)["id"].(string)
	if modes := m2AccountModes(t, env, volcVideoID); !unorderedStringListEqual(modes, []string{"video_create", "video_get", "video_content", "video_cancel"}) {
		t.Fatalf("video 域缺省 modes 应落四值组：%v", modes)
	}

	// audio 域 modes 缺省 → audio_speech。
	// B1 断点批（§1.1 组合表）：volcengine_ark 无 audio 域，audio 缺省 modes 臂
	// 改走 gpt（gpt_openai_v1 audio lane 注册 audio_speech），断言语义不变。
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-audio-absent", "gpt", "prof-gpt", "api_key", "audio",
			map[string]any{"api_key": "ak-volc-m2", "base_url": "https://api.openai.com/v1"}, nil))
	if code != http.StatusCreated {
		t.Fatalf("volcengine audio 创建应 201：%d", code)
	}

	// chat 域 modes 缺省 → 按域默认集播种（BUG-0323 已根治：媒体优先供应商
	// 现行缺省 video_get 不在对话域，缺省落词表内基础对话对，声明 ⊆ chat
	// 允许集，不再与严格校验冲突 400）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-volc-chat-absent", "volcengine", volcengineOpenAIV1ProfileID, "api_key", "chat",
			map[string]any{"api_key": "ak-volc-m2", "base_url": "https://ark.example.com/api/v3"}, nil))
	if code != http.StatusCreated {
		t.Fatalf("volcengine chat 缺省 modes 创建应 201：%d %v", code, payload)
	}
	if modes := m2AccountModes(t, env, dataMap(t, payload)["id"].(string)); !unorderedStringListEqual(modes, []string{"chat_json", "chat_sse"}) {
		t.Fatalf("volcengine chat 缺省 modes 应播种词表内基础对话对：%v", modes)
	}

	// image 域 modes 缺省 → images_json + 健康检查 images_json。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-image-absent", "gpt", "prof-gpt", "api_key", "image", apiKeyCredentials(), nil))
	if code != http.StatusCreated {
		t.Fatalf("gpt image 创建应 201：%d %v", code, payload)
	}
	imageID := dataMap(t, payload)["id"].(string)
	if modes := m2AccountModes(t, env, imageID); !unorderedStringListEqual(modes, []string{"images_json"}) {
		t.Fatalf("image 域缺省 modes 应落 images_json：%v", modes)
	}
	if mode := env.queryCell(t, `SELECT health_check_endpoint_mode FROM accounts WHERE id = ?`, imageID); mode != "images_json" {
		t.Fatalf("image 域健康检查默认应为 images_json：%s", mode)
	}

	// 矩阵外组合：allowed 为空 → 「{供应商} 不支持{域}账户」。
	// volcengine+image 2026-10-17 随矩阵开通（ark /api/v3/images/generations
	// 实测取证），原「volcengine 不支持图像账户」400 转为创建成功：modes 缺省
	// → 图像域默认集 images_json + 健康检查 images_json。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-volc-image", "volcengine", volcengineOpenAIV1ProfileID, "api_key", "image",
			map[string]any{"api_key": "ak-volc-m2", "base_url": "https://ark.example.com/api/v3"}, nil))
	if code != http.StatusCreated {
		t.Fatalf("volcengine image 创建应 201（矩阵已开通图像域）：%d %v", code, payload)
	}
	volcImageID := dataMap(t, payload)["id"].(string)
	if modes := m2AccountModes(t, env, volcImageID); !unorderedStringListEqual(modes, []string{"images_json"}) {
		t.Fatalf("volcengine image 域缺省 modes 应落 images_json：%v", modes)
	}
	if mode := env.queryCell(t, `SELECT health_check_endpoint_mode FROM accounts WHERE id = ?`, volcImageID); mode != "images_json" {
		t.Fatalf("volcengine image 域健康检查默认应为 images_json：%s", mode)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-anthropic-video", "anthropic", "profile_anthropic_v1", "api_key", "video",
			map[string]any{"api_key": "sk-ant-m2", "base_url": "https://api.anthropic.com"}, []string{"messages_json"}))
	if code != http.StatusBadRequest || m2Message(payload) != "anthropic 不支持视频账户" {
		t.Fatalf("anthropic+video 应 400 不支持：%d %v", code, payload)
	}

	// 矩阵收紧格（2026-10-12 裁决：矩阵标「—」的格一律拒绝）：
	// deepseek（openai 档案）+ video → 400（词表交集非空但矩阵拒绝）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-deepseek-video", "deepseek", deepSeekOpenAIV1ProfileID, "api_key", "video", apiKeyCredentials(),
			[]string{"video_create", "video_get", "video_content", "video_cancel"}))
	if code != http.StatusBadRequest || m2Message(payload) != "deepseek 不支持视频账户" {
		t.Fatalf("deepseek+video 应 400 不支持：%d %v", code, payload)
	}
	// deepseek（openai 档案）+ chat 矩阵内正值 → 201（modes 缺省 = 现行默认）。
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-deepseek-chat", "deepseek", deepSeekOpenAIV1ProfileID, "api_key", "chat", apiKeyCredentials(), nil))
	if code != http.StatusCreated {
		t.Fatalf("deepseek+chat 应 201：%d", code)
	}
	// xai + audio → 400（音频待契约回填后随矩阵更新开通）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-xai-audio", "xai", xaiOpenAIV1ProfileID, "api_key", "audio", apiKeyCredentials(),
			[]string{"audio_speech"}))
	if code != http.StatusBadRequest || m2Message(payload) != "xai 不支持音频账户" {
		t.Fatalf("xai+audio 应 400 不支持：%d %v", code, payload)
	}
	// xai + video 矩阵内正值 → 201。
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-xai-video", "xai", xaiOpenAIV1ProfileID, "api_key", "video", apiKeyCredentials(),
			[]string{"video_create", "video_get", "video_content", "video_cancel"}))
	if code != http.StatusCreated {
		t.Fatalf("xai+video 应 201：%d", code)
	}
	// gemini（OpenAI Chat 档案）+ video → 400（媒体能力挂 native 档案）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-gemini-openai-video", "gemini", geminiOpenAIChatV1BetaProfileID, "api_key", "video", apiKeyCredentials(),
			[]string{"video_create", "video_get", "video_content", "video_cancel"}))
	if code != http.StatusBadRequest || m2Message(payload) != "gemini 不支持视频账户" {
		t.Fatalf("gemini openai-chat+video 应 400 不支持：%d %v", code, payload)
	}
	// gemini（OpenAI Chat 档案）+ chat 矩阵内正值（modes 缺省 = chat 对）→ 201。
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-gemini-openai-chat", "gemini", geminiOpenAIChatV1BetaProfileID, "api_key", "chat", apiKeyCredentials(), nil))
	if code != http.StatusCreated {
		t.Fatalf("gemini openai-chat+chat 应 201：%d", code)
	}
	// gemini native + video 矩阵内正值 → 201 + 健康检查默认 video_create。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-gemini-native-video", "gemini", "profile_gemini_native_v1beta", "api_key", "video",
			map[string]any{"api_key": "AIza-m2-video", "base_url": "https://generativelanguage.googleapis.com"},
			[]string{"video_create", "video_get", "video_content", "video_cancel"}))
	if code != http.StatusCreated {
		t.Fatalf("gemini native+video 应 201：%d %v", code, payload)
	}
	if mode := env.queryCell(t, `SELECT health_check_endpoint_mode FROM accounts WHERE id = ?`, dataMap(t, payload)["id"].(string)); mode != "video_create" {
		t.Fatalf("gemini native video 健康检查默认应为 video_create：%s", mode)
	}

	// gpt oauth + video：oauth 词表只剩 responses 对，视频域允许集为空 →
	// 400「{供应商} 不支持{域}账户」（矩阵外组合自然为空集）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-oauth-video", "gpt", "prof-gpt", "oauth", "video",
			map[string]any{"access_token": "tok-m2", "refresh_token": "rtr-m2", "account_id": "acc-m2", "base_url": "https://api.openai.com/v1"},
			[]string{"video_create"}))
	if code != http.StatusBadRequest || m2Message(payload) != "gpt 不支持视频账户" {
		t.Fatalf("gpt oauth+video 应 400 不支持：%d %v", code, payload)
	}

	// chat 域声明携带媒体 lane → 400 列出越界值（新建不允许任何域外声明）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-chat-outlier", "gpt", "prof-gpt", "api_key", "chat", apiKeyCredentials(),
			[]string{"chat_json", "video_create"}))
	if code != http.StatusBadRequest || !strings.Contains(m2Message(payload), "video_create") {
		t.Fatalf("chat 域越界声明应 400：%d %v", code, payload)
	}

	// v8-A 对话域纯粹化：新建对话账户声明被移出三项（images_json /
	// audio_transcription_json / realtime_session）→ 400 列出全部越界值；
	// 音频域承载同一声明则放行（归属迁移，新增隔离只约束对话域）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-chat-outlier-v8a", "gpt", "prof-gpt", "api_key", "chat", apiKeyCredentials(),
			[]string{"chat_json", "images_json", "audio_transcription_json", "realtime_session"}))
	if code != http.StatusBadRequest {
		t.Fatalf("v8-A 新建对话账户声明移出三项应 400：%d %v", code, payload)
	}
	for _, outlier := range []string{"images_json", "audio_transcription_json", "realtime_session"} {
		if !strings.Contains(m2Message(payload), outlier) {
			t.Fatalf("越界消息应列出 %s：%v", outlier, payload)
		}
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-audio-v8a", "gpt", "prof-gpt", "api_key", "audio", apiKeyCredentials(),
			[]string{"audio_transcription_json", "realtime_session"}))
	if code != http.StatusCreated {
		t.Fatalf("v8-A 音频域声明短转写+实时会话应 201（按词表求交）：%d %v", code, payload)
	}

	// modes 显式 null / 空数组：与现状逐项一致（基线探针证实：写侧归一化
	// 对 null/空数组返回普通 error，HTTP 面现状即 500「服务器内部错误」；
	// 本切片不改变该路径，文案与状态码原样保留）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		`{"providerCode":"gpt","providerProtocolProfileId":"prof-gpt","name":"m2-null-modes","type":"api_key",`+
			`"credentials":{"api_key":"sk-null-modes","base_url":"https://api.openai.com/v1","supported_endpoint_modes":null},`+
			`"supportedModels":["m-default"],"status":"active"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("创建显式 null 应与现状一致（500）：%d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-empty-modes", "gpt", "prof-gpt", "api_key", "", apiKeyCredentials(), []string{}))
	if code != http.StatusInternalServerError {
		t.Fatalf("创建空数组应与现状一致（500）：%d %v", code, payload)
	}

	// gemini chat 域必须至少一种对话能力（既有 assertGeminiEndpointModesCompatible）。
	geminiCredentials := func() map[string]any {
		return map[string]any{"api_key": "AIza-m2", "base_url": "https://generativelanguage.googleapis.com"}
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-gemini-nochat", "gemini", "profile_gemini_native_v1beta", "api_key", "chat",
			geminiCredentials(), []string{"count_tokens"}))
	if code != http.StatusBadRequest || !strings.Contains(m2Message(payload), "generateContent") {
		t.Fatalf("gemini chat 无对话能力应 400：%d %v", code, payload)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-gemini-chat", "gemini", "profile_gemini_native_v1beta", "api_key", "chat",
			geminiCredentials(), []string{"generate_content_json", "count_tokens"}))
	if code != http.StatusCreated {
		t.Fatalf("gemini chat 携带对话能力应 201：%d", code)
	}
}

// TestM2ChatDomainLegacyDefaultsPin 把 chat 域默认集按 BUG-0323 根治口径钉住：
// 现行默认本就域内的供应商（openai 族/gemini/anthropic/glm）与现行归一化缺省
// 逐项全等；媒体优先供应商（volcengine/qwen/hybrid）改为域内断言——缺省恒 ⊆
// chat 域允许集，volcengine/qwen 落词表内基础对话对。
func TestM2ChatDomainLegacyDefaultsPin(t *testing.T) {
	contexts := []struct {
		name         string
		providerCode string
		accountType  string
		profile      protocolPredicateInput
	}{
		{name: "gpt api_key", providerCode: "gpt", accountType: "api_key", profile: protocolPredicateInput{providerCode: "gpt", protocolCode: "openai", protocolVersion: "v1", providerProtocolProfileID: gptOpenAIV1ProfileIDConstant}},
		{name: "gpt oauth", providerCode: "gpt", accountType: "oauth", profile: protocolPredicateInput{providerCode: "gpt", protocolCode: "openai", protocolVersion: "v1", providerProtocolProfileID: gptOpenAIV1ProfileIDConstant}},
		{name: "openai api_key", providerCode: "openai", accountType: "api_key", profile: protocolPredicateInput{providerCode: "openai", protocolCode: "openai", protocolVersion: "v1", providerProtocolProfileID: openAICompatibleProfileID}},
		{name: "anthropic api_key", providerCode: "anthropic", accountType: "api_key", profile: protocolPredicateInput{providerCode: "anthropic", protocolCode: "anthropic", protocolVersion: "v1", providerProtocolProfileID: "profile_anthropic_v1"}},
		{name: "deepseek anthropic", providerCode: "deepseek", accountType: "api_key", profile: protocolPredicateInput{providerCode: "deepseek", protocolCode: "anthropic", protocolVersion: "v1", providerProtocolProfileID: deepSeekAnthropicV1ProfileID}},
		{name: "glm openai", providerCode: "glm", accountType: "api_key", profile: protocolPredicateInput{providerCode: "glm", protocolCode: "openai", protocolVersion: "v1", providerProtocolProfileID: glmGeneralOpenAIV1ProfileID}},
		{name: "glm anthropic", providerCode: "glm", accountType: "api_key", profile: protocolPredicateInput{providerCode: "glm", protocolCode: "anthropic", protocolVersion: "v1", providerProtocolProfileID: glmCodingAnthropicV1ProfileID}},
		{name: "gemini native", providerCode: "gemini", accountType: "api_key", profile: protocolPredicateInput{providerCode: "gemini", protocolCode: "gemini", protocolVersion: "v1beta", providerProtocolProfileID: "profile_gemini_native_v1beta"}},
		{name: "xai api_key", providerCode: "xai", accountType: "api_key", profile: protocolPredicateInput{providerCode: "xai", protocolCode: "openai", protocolVersion: "v1", providerProtocolProfileID: xaiOpenAIV1ProfileID}},
		// minimax 不进本对照：其 chat 域被矩阵门关闭（计划矩阵 minimax 行
		// chat = ✗），DefaultEndpointModesForDomain(chat, minimax) 恒为空集；
		// 其 chat 建户在现状下本就被写侧归一化拒绝（「MiniMax 账户上游接口
		// 能力只支持语音合成 (audio_speech) 或视频生成端点 (video_*)」），无
		// 可失去的成功路径，零变化不受影响。
	}
	for _, tc := range contexts {
		t.Run(tc.name, func(t *testing.T) {
			legacy, err := normalizeEndpointModesForWrite(optionalValue{}, endpointModeDefaultContext{
				providerCode:              tc.providerCode,
				accountType:               tc.accountType,
				protocolCode:              tc.profile.protocolCode,
				protocolVersion:           tc.profile.protocolVersion,
				providerProtocolProfileID: tc.profile.providerProtocolProfileID,
			})
			if err != nil {
				t.Fatalf("现行缺省归一化不应失败：%v", err)
			}
			domainDefault := accountscoreDefaultForTest(tc.providerCode, tc.accountType, tc.profile)
			if !stringSlicesEqual(legacy, domainDefault) {
				t.Fatalf("现行默认本就域内，chat 域默认集必须逐项不变：现行=%v 按域=%v", legacy, domainDefault)
			}
		})
	}
	// 媒体优先供应商（BUG-0323 根治口径）：现行默认不含对话值或为全词表并集，
	// 不再逐项 pin 现行值，改为域内断言。volcengine/qwen 现行缺省 video_get
	// 与 chat lane 集交集为空 → 落词表内基础对话对（Volcengine/Qwen
	// EndpointModeValues 均含 chat 对）。
	for _, tc := range []struct {
		name         string
		providerCode string
		profile      protocolPredicateInput
	}{
		{name: "volcengine", providerCode: "volcengine", profile: protocolPredicateInput{providerCode: "volcengine", protocolCode: "openai", protocolVersion: "v1", providerProtocolProfileID: volcengineOpenAIV1ProfileID}},
		{name: "qwen", providerCode: "qwen", profile: protocolPredicateInput{providerCode: "qwen", protocolCode: "openai", protocolVersion: "v1", providerProtocolProfileID: qwenOpenAIV1ProfileID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			domainDefault := accountscoreDefaultForTest(tc.providerCode, "api_key", tc.profile)
			if !stringSlicesEqual(domainDefault, []string{"chat_json", "chat_sse"}) {
				t.Fatalf("%s chat 默认集应落词表内基础对话对：%v", tc.providerCode, domainDefault)
			}
		})
	}
	// hybrid 现行缺省为全词表并集 → 与 chat lane 集求交收敛为非空域内子集。
	hybridProfile := protocolPredicateInput{providerCode: "hybrid"}
	hybridDefault := accountscoreDefaultForTest("hybrid", "api_key", hybridProfile)
	hybridAllowed := accountscore.AllowedEndpointModes(accountscore.CapabilityDomainChat, "hybrid", "api_key", toProtocolPredicate(hybridProfile))
	if len(hybridDefault) == 0 {
		t.Fatalf("hybrid chat 默认集不应为空：%v", hybridDefault)
	}
	for _, mode := range hybridDefault {
		if !containsString(hybridAllowed, mode) {
			t.Fatalf("hybrid chat 默认集应 ⊆ chat 允许集，越界值 %s：%v（允许集 %v）", mode, hybridDefault, hybridAllowed)
		}
	}
	// 矩阵关闭格（2026-10-12 裁决）：minimax chat 域的允许集与默认集恒空。
	minimaxProfile := protocolPredicateInput{providerCode: "minimax", protocolCode: "openai", protocolVersion: "v1", providerProtocolProfileID: minimaxOpenAIV1ProfileID}
	if got := accountscore.AllowedEndpointModes(accountscore.CapabilityDomainChat, "minimax", "api_key", toProtocolPredicate(minimaxProfile)); len(got) != 0 {
		t.Fatalf("minimax chat 允许集应为空（矩阵拒绝）：%v", got)
	}
	if got := accountscoreDefaultForTest("minimax", "api_key", minimaxProfile); len(got) != 0 {
		t.Fatalf("minimax chat 默认集应为空（矩阵拒绝）：%v", got)
	}
}

// TestM2PatchStrictDeclarationSubset 覆盖编辑面严格声明校验（账户能力域
// 不变式：声明集必须 ⊆ 所在域允许集，全链路同一规则）：直插带域外值的存量
// 行后，显式数组仍含域外值一律 400（扩大/换元/域内扩展混带越界值同判）；
// 显式数组只含域内值（含缩小遗留值）按普通编辑放行；modes 键缺省不重置
// 声明集；删键（null）在补丁准备处替换为按域默认集数组后走同一严格校验
// ——BUG-0323 根治后按域默认集恒 ⊆ 域允许集，删键一律 200 域内重置生效，
// 不再静默回写。
func TestM2PatchStrictDeclarationSubset(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	// 存量混合账户（chat 域 + 域外 video_create 遗留值，直插绕过写校验）。
	m2SeedAccountWithModes(t, env, "acc-m2-mixed", adminID, "m2-mixed", []string{"chat_json", "video_create"})
	revision := int64(1)

	patch := func(t *testing.T, body string) (int, map[string]any) {
		return env.do(t, http.MethodPatch, "/__aisys__/api/accounts/acc-m2-mixed", body)
	}
	modesJSON := func(modes ...string) string {
		raw, _ := json.Marshal(modes)
		return string(raw)
	}

	// 仅改名：modes 未携带 → 不重置声明集（存量声明原样保留）。
	code, payload := patch(t, `{"expectedConfigRevision":`+itoa64(revision)+`,"name":"m2-mixed-renamed"}`)
	if code != http.StatusOK {
		t.Fatalf("仅改名应 200：%d %v", code, payload)
	}
	revision++
	if modes := m2AccountModes(t, env, "acc-m2-mixed"); !unorderedStringListEqual(modes, []string{"chat_json", "video_create"}) {
		t.Fatalf("仅改名不得触碰声明集：%v", modes)
	}

	// 显式数组扩大（加入 video_get）→ 400 严格拒绝并列出全部越界值。
	code, payload = patch(t, `{"expectedConfigRevision":`+itoa64(revision)+`,"credentialsPatch":{"supported_endpoint_modes":`+modesJSON("chat_json", "video_create", "video_get")+`}}`)
	if code != http.StatusBadRequest || m2Message(payload) != "账户能力域对话不支持上游接口能力：video_create、video_get" {
		t.Fatalf("扩大越界应 400：%d %v", code, payload)
	}

	// 显式数组域内扩展混带域外值 → 同一规则 400（换元与混带无特殊分支）。
	code, payload = patch(t, `{"expectedConfigRevision":`+itoa64(revision)+`,"credentialsPatch":{"supported_endpoint_modes":`+modesJSON("chat_json", "chat_sse", "video_create")+`}}`)
	if code != http.StatusBadRequest || m2Message(payload) != "账户能力域对话不支持上游接口能力：video_create" {
		t.Fatalf("混带越界应 400：%d %v", code, payload)
	}

	// 显式数组只含域内值（缩小遗留值）→ 200：清理退化为普通编辑，无特殊
	// 分支、无单独审计。
	code, payload = patch(t, `{"expectedConfigRevision":`+itoa64(revision)+`,"credentialsPatch":{"api_key":"sk-mixed-rotated","supported_endpoint_modes":`+modesJSON("chat_json")+`}}`)
	if code != http.StatusOK {
		t.Fatalf("域内缩小应 200：%d %v", code, payload)
	}
	revision++
	if modes := m2AccountModes(t, env, "acc-m2-mixed"); !unorderedStringListEqual(modes, []string{"chat_json"}) {
		t.Fatalf("域外遗留值应被裁掉：%v", modes)
	}
	if key := env.queryCell(t, `SELECT credential_mask FROM accounts WHERE id = 'acc-m2-mixed'`); key == "sk-***" {
		t.Fatalf("凭据应已轮换：%s", key)
	}
	for _, action := range env.sink.actions() {
		if action == "cleanup_capability_legacy" {
			t.Fatalf("清理已退化为普通编辑，不得再落清理审计：%v", env.sink.actions())
		}
	}

	// 域内扩展 → 200。
	code, payload = patch(t, `{"expectedConfigRevision":`+itoa64(revision)+`,"credentialsPatch":{"supported_endpoint_modes":`+modesJSON("chat_json", "chat_sse")+`}}`)
	if code != http.StatusOK {
		t.Fatalf("域内扩展应 200：%d %v", code, payload)
	}
	revision++
	if modes := m2AccountModes(t, env, "acc-m2-mixed"); !unorderedStringListEqual(modes, []string{"chat_json", "chat_sse"}) {
		t.Fatalf("域内扩展应生效：%v", modes)
	}

	// 显式 null（credentialsPatch 删键）→ 补丁准备处替换为按域默认集数组，
	// 走同一显式数组严格校验：gpt chat 按域默认集与现行默认集逐项一致（现行
	// 默认本就域内）域内，重置生效（严格不变式下声明重置即域内集合）。
	code, payload = patch(t, `{"expectedConfigRevision":`+itoa64(revision)+`,"credentialsPatch":{"supported_endpoint_modes":null}}`)
	if code != http.StatusOK {
		t.Fatalf("删键默认集重置应 200：%d %v", code, payload)
	}
	revision++
	if modes := m2AccountModes(t, env, "acc-m2-mixed"); !unorderedStringListEqual(modes, []string{"chat_json", "chat_sse", "responses_json", "responses_sse"}) {
		t.Fatalf("删键应重置为现行默认集：%v", modes)
	}
	// 显式空数组（credentialsPatch）：与现状一致——写侧归一化按普通 error
	// 拒绝（基线探针证实现状即 500），本切片不改变该路径。
	code, payload = patch(t, `{"expectedConfigRevision":`+itoa64(revision)+`,"credentialsPatch":{"supported_endpoint_modes":[]}}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("空数组应与现状一致（500）：%d %v", code, payload)
	}
	// legacy credentials 显式 null：现行合并语义恢复库中现值 → 无变化 200。
	code, payload = patch(t, `{"expectedConfigRevision":`+itoa64(revision)+`,"credentials":{"supported_endpoint_modes":null}}`)
	if code != http.StatusOK {
		t.Fatalf("legacy null 恢复现值应 200（现状语义）：%d %v", code, payload)
	}
	if got := env.queryCell(t, `SELECT config_revision FROM accounts WHERE id = 'acc-m2-mixed'`); got != itoa64(revision) {
		t.Fatalf("无变化提交不得推进版本：got %s want %s", got, itoa64(revision))
	}

	// 删键（null）按域默认集收敛（D6 废除后静默击穿口的回归钉）：BUG-0323
	// 根治后按域默认集恒 ⊆ 域允许集（chat 分支与域 lane 集求交 + 媒体优先
	// 供应商落词表内基础对话对），替换后经同一严格校验一律 200 重置生效；
	// 写侧供应商缺省集不再有任何直通路径。
	patchAccount := func(accountID, body string) (int, map[string]any) {
		return env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+accountID, body)
	}

	// minimax×video：写侧缺省集 audio_speech 对 video 域越界（直通归一化会
	// 静默回写域外声明）→ 替换后的按域默认集 = 视频四值组 → 200，持久化
	// 声明 = DefaultEndpointModesForDomain(video, minimax)，不含 audio_speech。
	m2SeedProvider(t, env, adminID, "minimax", minimaxOpenAIV1ProfileID, "openai", "v1", []string{"api_key"})
	m2SeedDomainAccountWithModes(t, env, "acc-m2-minimax-video", adminID, "m2-minimax-video",
		"minimax", minimaxOpenAIV1ProfileID, "video", []string{"audio_speech"})
	code, payload = patchAccount("acc-m2-minimax-video", `{"expectedConfigRevision":1,"credentialsPatch":{"supported_endpoint_modes":null}}`)
	if code != http.StatusOK {
		t.Fatalf("minimax video 删键应按域默认集重置 200：%d %v", code, payload)
	}
	minimaxVideoDefaults := accountscore.DefaultEndpointModesForDomain(accountscore.CapabilityDomainVideo, "minimax", "api_key", accountscore.ProtocolPredicate{
		ProviderCode:              "minimax",
		ProtocolCode:              "openai",
		ProtocolVersion:           "v1",
		ProviderProtocolProfileID: minimaxOpenAIV1ProfileID,
	})
	if len(minimaxVideoDefaults) == 0 {
		t.Fatalf("minimax video 按域默认集不应为空：%v", minimaxVideoDefaults)
	}
	for _, mode := range minimaxVideoDefaults {
		if mode == "audio_speech" {
			t.Fatalf("video 域按域默认集不应含 audio_speech：%v", minimaxVideoDefaults)
		}
	}
	if modes := m2AccountModes(t, env, "acc-m2-minimax-video"); !unorderedStringListEqual(modes, minimaxVideoDefaults) {
		t.Fatalf("minimax video 删键应落按域默认集：%v want %v", modes, minimaxVideoDefaults)
	}

	// volcengine×chat：删键重置为按域默认集。BUG-0323 已根治：chat 分支与
	// 域 lane 集求交后现行缺省 video_get 交集为空，落词表内基础对话对
	// （chat_json、chat_sse）→ 200 重置生效且声明 ⊆ chat 允许集（修复前
	// 该路径返回 400「video_get 越界」）。
	m2SeedProvider(t, env, adminID, "volcengine", volcengineOpenAIV1ProfileID, "openai", "v1", []string{"api_key"})
	m2SeedDomainAccountWithModes(t, env, "acc-m2-volc-chat", adminID, "m2-volc-chat",
		"volcengine", volcengineOpenAIV1ProfileID, "chat", []string{"chat_json"})
	code, payload = patchAccount("acc-m2-volc-chat", `{"expectedConfigRevision":1,"credentialsPatch":{"supported_endpoint_modes":null}}`)
	if code != http.StatusOK {
		t.Fatalf("volcengine chat 删键应按域默认集重置 200：%d %v", code, payload)
	}
	if modes := m2AccountModes(t, env, "acc-m2-volc-chat"); !unorderedStringListEqual(modes, []string{"chat_json", "chat_sse"}) {
		t.Fatalf("volcengine chat 删键应重置为词表内基础对话对：%v", modes)
	}

	// 域不可变：携带 ≠ 现值 → 400「能力域不可更改」。
	code, payload = patch(t, `{"expectedConfigRevision":`+itoa64(revision)+`,"capabilityDomain":"video"}`)
	if code != http.StatusBadRequest || m2Message(payload) != "能力域不可更改" {
		t.Fatalf("改域应 400：%d %v", code, payload)
	}
	code, payload = patch(t, `{"expectedConfigRevision":`+itoa64(revision)+`,"capabilityDomain":null}`)
	if code != http.StatusBadRequest || m2Message(payload) != "能力域不可更改" {
		t.Fatalf("null 域应 400：%d %v", code, payload)
	}
	// 携带相同域 = 无操作（域无操作，改名生效 → 版本 +1）。
	code, payload = patch(t, `{"expectedConfigRevision":`+itoa64(revision)+`,"capabilityDomain":"chat","name":"m2-mixed-renamed2"}`)
	if code != http.StatusOK {
		t.Fatalf("携带相同域应 200：%d %v", code, payload)
	}
	revision++
}

func TestM2PatchGeminiChatConversationRequirement(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	m2SeedProvider(t, env, adminID, "gemini", "profile_gemini_native_v1beta", "gemini", "v1beta", []string{"api_key", "google_oauth"})

	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-gemini-patch", "gemini", "profile_gemini_native_v1beta", "api_key", "chat",
			map[string]any{"api_key": "AIza-m2-patch", "base_url": "https://generativelanguage.googleapis.com"},
			[]string{"generate_content_json", "count_tokens"}))
	if code != http.StatusCreated {
		t.Fatalf("gemini chat 创建应 201：%d %v", code, payload)
	}
	accountID := dataMap(t, payload)["id"].(string)
	revision := dataMap(t, payload)["configRevision"].(float64)

	// modes 参与变更且移除全部对话能力 → 400。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+accountID,
		`{"expectedConfigRevision":`+itoa64(int64(revision))+`,"credentialsPatch":{"supported_endpoint_modes":["count_tokens","embed_content"]}}`)
	if code != http.StatusBadRequest || !strings.Contains(m2Message(payload), "generateContent") {
		t.Fatalf("gemini chat 移除对话能力应 400：%d %v", code, payload)
	}

	// 保留对话能力 → 200。
	code, payload = env.do(t, http.MethodPatch, "/__aisys__/api/accounts/"+accountID,
		`{"expectedConfigRevision":`+itoa64(int64(revision))+`,"credentialsPatch":{"supported_endpoint_modes":["generate_content_json","embed_content"]}}`)
	if code != http.StatusOK {
		t.Fatalf("gemini chat 保留对话能力应 200：%d %v", code, payload)
	}
}

func TestM2CloneContract(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	createBodyFor := func(name, domain string) string {
		return m2CreateBody(name, "gpt", "prof-gpt", "api_key", domain,
			map[string]any{"api_key": "sk-clone-source-" + name, "base_url": "https://api.openai.com/v1"},
			func() []string {
				if domain == "video" {
					return []string{"video_create", "video_get", "video_content", "video_cancel"}
				}
				return nil
			}())
	}

	// 源（chat 域）+ 视频域账户各一。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts", createBodyFor("m2-clone-source", ""))
	if code != http.StatusCreated {
		t.Fatalf("源账户创建应 201：%d %v", code, payload)
	}
	sourceID := dataMap(t, payload)["id"].(string)
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts", createBodyFor("m2-clone-video", "video"))
	if code != http.StatusCreated {
		t.Fatalf("video 源创建应 201：%d", code)
	}

	// clone-context 回显 capabilityDomain。
	code, payload = env.do(t, http.MethodGet, "/__aisys__/api/accounts/"+sourceID+"/clone-context", "")
	if code != http.StatusOK {
		t.Fatalf("clone-context 应 200：%d %v", code, payload)
	}
	if context := dataMap(t, payload); context["capabilityDomain"] != "chat" {
		t.Fatalf("clone-context 应回显源域：%v", context["capabilityDomain"])
	}

	// 正常克隆：目标域 = 源域 → 201。
	clonePayload := map[string]any{
		"providerCode":              "gpt",
		"providerProtocolProfileId": "prof-gpt",
		"name":                      "m2-clone-copy",
		"type":                      "api_key",
		"cloneSourceId":             sourceID,
		"credentials":               map[string]any{"api_key": "sk-clone-copy", "base_url": "https://api.openai.com/v1"},
		"supportedModels":           []any{"m-default"},
		"status":                    "active",
	}
	raw, _ := json.Marshal(clonePayload)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", string(raw))
	if code != http.StatusCreated {
		t.Fatalf("同域克隆应 201：%d %v", code, payload)
	}

	// 域不等 → 400。
	clonePayload["name"] = "m2-clone-mismatch"
	clonePayload["capabilityDomain"] = "video"
	raw, _ = json.Marshal(clonePayload)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", string(raw))
	if code != http.StatusBadRequest || m2Message(payload) != "克隆账户能力域必须与源账户一致" {
		t.Fatalf("克隆域不等应 400：%d %v", code, payload)
	}

	// 源不存在 → 404。
	clonePayload["name"] = "m2-clone-missing"
	clonePayload["cloneSourceId"] = "acc-missing"
	delete(clonePayload, "capabilityDomain")
	raw, _ = json.Marshal(clonePayload)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", string(raw))
	if code != http.StatusNotFound || m2Message(payload) != "账户不存在" {
		t.Fatalf("克隆源缺失应 404：%d %v", code, payload)
	}

	// 授权实例禁克隆 → 403。
	env.exec(t, `UPDATE accounts SET authorization_instance_source_account_id = 'authz-x' WHERE id = ?`, sourceID)
	clonePayload["name"] = "m2-clone-instance"
	clonePayload["cloneSourceId"] = sourceID
	raw, _ = json.Marshal(clonePayload)
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts", string(raw))
	if code != http.StatusForbidden || m2Message(payload) != "授权实例不能克隆" {
		t.Fatalf("授权实例克隆应 403：%d %v", code, payload)
	}
}

func TestM2ImportCapabilityDomainStrict(t *testing.T) {
	env := newTestEnv(t)
	seedOpenAICompatibleProvider(t, env)
	adminID := env.login(t, "root", "root-pass", "super_admin")

	videoModes := []any{"video_create", "video_get", "video_content", "video_cancel"}
	mixedAccount := w2APIKeyAccount("m2-导入混合", "导入分组")
	mixedAccount["credentials"].(map[string]any)["supported_endpoint_modes"] = []any{"chat_json", "video_create"}
	videoAccount := w2APIKeyAccount("m2-导入视频", "导入分组")
	videoAccount["credentials"].(map[string]any)["supported_endpoint_modes"] = videoModes
	// B1 断点批：视频域账户的连接集合改写为 video lane 全集 + video_create 检查形态。
	videoAccount["connections"] = []any{map[string]any{"connectionPresetCode": "generic_openai_v1", "baseUrl": "https://api.openai.com/v1", "endpointModes": videoModes, "enabled": true}}
	videoAccount["healthCheckConnectionPresetCode"] = "generic_openai_v1"
	videoAccount["healthCheckEndpointMode"] = "video_create"
	videoAccount["healthCheckModel"] = "gpt-4o-mini"
	absentAccount := w2APIKeyAccount("m2-导入缺省", "导入分组")

	// 预览：混合包冲突 → per-account message + CanImport=false；推导域回显。
	mixedDoc := w2NativeDoc([]map[string]any{mixedAccount}, nil)
	result := w2Preview(t, env, mixedDoc, "", ImportOptions{}, adminID)
	if result.CanImport {
		t.Fatalf("混合能力包不应可导入：%+v", result.Summary)
	}
	item := w2Item(t, result, 0)
	if item.Action != importActionFailed {
		t.Fatalf("混合包条目应 failed：%s %s", item.Action, w2Messages(item))
	}
	if !strings.Contains(w2Messages(item), "video_create") {
		t.Fatalf("冲突消息应列出越界值：%s", w2Messages(item))
	}
	if item.CapabilityDomain == nil || *item.CapabilityDomain != "chat" {
		t.Fatalf("混合包推导域应回显 chat：%v", item.CapabilityDomain)
	}

	cleanDoc := w2NativeDoc([]map[string]any{videoAccount, absentAccount}, nil)
	preview := w2Preview(t, env, cleanDoc, "", ImportOptions{}, adminID)
	if !preview.CanImport {
		t.Fatalf("纯视频 + 缺省包应可导入：%+v", preview.Accounts)
	}
	videoItem := w2Item(t, preview, 0)
	if videoItem.CapabilityDomain == nil || *videoItem.CapabilityDomain != "video" {
		t.Fatalf("视频包推导域应回显 video：%v", videoItem.CapabilityDomain)
	}
	absentItem := w2Item(t, preview, 1)
	if absentItem.CapabilityDomain == nil || *absentItem.CapabilityDomain != "chat" {
		t.Fatalf("缺省声明推导域应回显 chat：%v", absentItem.CapabilityDomain)
	}

	// 执行：落域 + 按域默认集（执行链经 Store.Create 重验）。
	executed, err := env.store.ExecuteImport(context.Background(), cleanDoc, "", ImportOptions{}, AccessScope{ViewerID: adminID, IsAdmin: true})
	if err != nil {
		t.Fatalf("执行导入不应失败：%v", err)
	}
	if !executed.Imported || executed.Summary.Accounts.Create != 2 {
		t.Fatalf("执行汇总不一致：%+v", executed.Summary)
	}
	videoRowID := *w2Item(t, executed, 0).AccountID
	absentRowID := *w2Item(t, executed, 1).AccountID
	if domain := env.queryCell(t, `SELECT capability_domain FROM accounts WHERE id = ?`, videoRowID); domain != "video" {
		t.Fatalf("导入视频账户应落 video 域：%s", domain)
	}
	if modes := m2AccountModes(t, env, videoRowID); !unorderedStringListEqual(modes, []string{"video_create", "video_get", "video_content", "video_cancel"}) {
		t.Fatalf("导入视频账户声明集应保留：%v", modes)
	}
	if mode := env.queryCell(t, `SELECT health_check_endpoint_mode FROM accounts WHERE id = ?`, videoRowID); mode != "video_create" {
		t.Fatalf("导入视频账户健康检查默认应为 video_create：%s", mode)
	}
	if domain := env.queryCell(t, `SELECT capability_domain FROM accounts WHERE id = ?`, absentRowID); domain != "chat" {
		t.Fatalf("缺省导入账户应落 chat 域：%s", domain)
	}
	if modes := m2AccountModes(t, env, absentRowID); !unorderedStringListEqual(modes, []string{"chat_json", "chat_sse"}) {
		t.Fatalf("缺省导入账户应落现行默认集（openai 兼容档案为 chat 对）：%v", modes)
	}
}

func TestM2BatchSameDomain(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	credentials := map[string]any{"api_key": "sk-batch-m2", "base_url": "https://api.openai.com/v1"}
	create := func(name, domain string, modes []string) (string, int64) {
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
			m2CreateBody(name, "gpt", "prof-gpt", "api_key", domain, credentials, modes))
		if code != http.StatusCreated {
			t.Fatalf("创建 %s 应 201：%d %v", name, code, payload)
		}
		data := dataMap(t, payload)
		return data["id"].(string), int64(data["configRevision"].(float64))
	}

	chatA, revisionA := create("m2-batch-a", "", nil)
	chatB, revisionB := create("m2-batch-b", "", nil)
	videoC, revisionC := create("m2-batch-c", "video", []string{"video_create", "video_get", "video_content", "video_cancel"})

	batchBody := func(targets []map[string]any, updates map[string]any) string {
		raw, _ := json.Marshal(map[string]any{"targets": targets, "updates": updates})
		return string(raw)
	}
	target := func(id string, revision int64) map[string]any {
		return map[string]any{"accountId": id, "configRevision": float64(revision)}
	}

	// 同域（chat）modes 覆盖 → 200（gpt codex_responses 账户的 modes 覆盖
	// 须保留 responses_sse（既有家族断言），且保留现值 chat_json 作为健康
	// 检查形态）。
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update",
		batchBody([]map[string]any{target(chatA, revisionA), target(chatB, revisionB)},
			map[string]any{"supportedEndpointModes": map[string]any{"enabled": true, "value": []any{"chat_json", "responses_sse"}}}))
	if code != http.StatusOK {
		t.Fatalf("同域批量覆盖应 200：%d %v", code, payload)
	}

	// 跨域整批 → 400「批量编辑仅支持同一能力域的账户」（纯模式覆盖与普通
	// 字段覆盖都拒绝整批）。
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update",
		batchBody([]map[string]any{target(chatA, revisionA+1), target(videoC, revisionC)},
			map[string]any{"supportedEndpointModes": map[string]any{"enabled": true, "value": []any{"chat_json"}}}))
	if code != http.StatusBadRequest || m2Message(payload) != "批量编辑仅支持同一能力域的账户" {
		t.Fatalf("跨域批量应整批拒绝：%d %v", code, payload)
	}
	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update",
		batchBody([]map[string]any{target(chatA, revisionA+1), target(videoC, revisionC)},
			map[string]any{"notes": map[string]any{"enabled": true, "value": "批量备注"}}))
	if code != http.StatusBadRequest || m2Message(payload) != "批量编辑仅支持同一能力域的账户" {
		t.Fatalf("跨域普通字段批量应整批拒绝：%d %v", code, payload)
	}

	// 严格声明校验（批量与 PATCH 同一规则）：直插带域外遗留值的存量行，
	// 覆盖数组含越界值 → 整批 400 并列出越界值；只含域内值 → 200（缩小遗留
	// 值退化为普通编辑，无特殊分支）。
	m2SeedAccountWithModes(t, env, "acc-m2-batch-legacy", adminID, "m2-batch-legacy", []string{"chat_json", "video_create"})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	env.exec(t, `INSERT INTO account_supported_models (account_id, model_id, created_at)
		VALUES ('acc-m2-batch-legacy', (SELECT id FROM models WHERE upstream_model = 'm-default'), ?)`, now)

	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update",
		batchBody([]map[string]any{target(chatA, revisionA+1), target("acc-m2-batch-legacy", 1)},
			map[string]any{"supportedEndpointModes": map[string]any{"enabled": true, "value": []any{"chat_json", "video_cancel"}}}))
	if code != http.StatusBadRequest || !strings.Contains(m2Message(payload), "video_cancel") {
		t.Fatalf("越界批量应整批 400：%d %v", code, payload)
	}
	if modes := m2AccountModes(t, env, "acc-m2-batch-legacy"); !unorderedStringListEqual(modes, []string{"chat_json", "video_create"}) {
		t.Fatalf("整批失败后遗留行声明不得变化：%v", modes)
	}

	code, payload = env.do(t, http.MethodPost, "/__aisys__/api/accounts/batch-update",
		batchBody([]map[string]any{target(chatA, revisionA+1), target("acc-m2-batch-legacy", 1)},
			map[string]any{"supportedEndpointModes": map[string]any{"enabled": true, "value": []any{"chat_json", "responses_sse"}}}))
	if code != http.StatusOK {
		t.Fatalf("域内缩小批量应 200：%d %v", code, payload)
	}
	if modes := m2AccountModes(t, env, "acc-m2-batch-legacy"); !unorderedStringListEqual(modes, []string{"chat_json", "responses_sse"}) {
		t.Fatalf("遗留行应缩小至域内集：%v", modes)
	}
}

func TestM2ExportDomainFilter(t *testing.T) {
	env := newTestEnv(t)
	adminID := env.login(t, "root", "root-pass", "super_admin")
	env.seedProviderAndDefaultGroup(t, adminID)

	credentials := func() map[string]any {
		return map[string]any{"api_key": "sk-export-m2", "base_url": "https://api.openai.com/v1"}
	}
	code, _ := env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-export-chat", "gpt", "prof-gpt", "api_key", "", credentials(), nil))
	if code != http.StatusCreated {
		t.Fatalf("chat 账户创建应 201：%d", code)
	}
	code, _ = env.do(t, http.MethodPost, "/__aisys__/api/accounts",
		m2CreateBody("m2-export-video", "gpt", "prof-gpt", "api_key", "video", credentials(),
			[]string{"video_create", "video_get", "video_content", "video_cancel"}))
	if code != http.StatusCreated {
		t.Fatalf("video 账户创建应 201：%d", code)
	}

	exportAccounts := func(t *testing.T, filters string) []any {
		t.Helper()
		code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/export", `{"filters":`+filters+`}`)
		if code != http.StatusOK {
			t.Fatalf("导出应 200：%d %v", code, payload)
		}
		document := dataMap(t, payload)["document"].(map[string]any)
		return document["accounts"].([]any)
	}

	if got := len(exportAccounts(t, `{"domain":"video"}`)); got != 1 {
		t.Fatalf("domain=video 应只导出 1 个账户：%d", got)
	}
	if got := len(exportAccounts(t, `{"domain":"all"}`)); got != 2 {
		t.Fatalf("domain=all 应导出 2 个账户：%d", got)
	}
	if got := len(exportAccounts(t, `{}`)); got != 2 {
		t.Fatalf("缺省 domain 应导出 2 个账户：%d", got)
	}
	code, payload := env.do(t, http.MethodPost, "/__aisys__/api/accounts/export", `{"filters":{"domain":"bogus"}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("非法 domain 应 400：%d %v", code, payload)
	}
}
