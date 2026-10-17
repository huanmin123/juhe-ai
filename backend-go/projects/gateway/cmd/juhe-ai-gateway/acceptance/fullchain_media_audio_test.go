package acceptance

// M3f 长音频任务 E2E（契约 §10.2 paraformer；媒体设计 §4.2 /v1/audio/jobs
// 端点族）：管理面配置（qwen 分组 + paraformer-v2 账户，audio_job_* 端点
// 模式写侧门禁）→ 创建（transcription 报文改写、无 X-DashScope-Async 头、
// 统一 audio_job 对象、language→language_hints 映射经 provider_options 命中
// 回显）→ 轮询 queued → in_progress → completed（transcription_url 冻结）→
// content 经绝对 URL 无凭据直连下载转写结果 JSON → 管理面 media-jobs 行终态
//（kind=audio_transcription、usage duration 时长计量照落、目录无 USD 秒价
// cost=62.5s×$0.000012/s=0.00075，2026-10-05 起静态秒价）→ 账户亲和（上游命中只打 qwen 账户 key 的 transcription
// 创建与 /api/v1/tasks 轮询端点）。
import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// fullchainQwenASRCreateBody 是 qwen 长转写创建请求体：input_url 公网音频
// URL（唯一输入形态——契约 §10.2 上游只收 file_urls、零存储不暂存）、
// language "zh" 映射 parameters.language_hints ["zh"]、diarization_enabled
// 经 provider_options.qwen 命中 → provider_options_applied 回显。
const fullchainQwenASRCreateBody = `{"model":"paraformer-v2","input_url":"https://oss.example.com/audio/meeting.mp3","language":"zh","provider_options":{"qwen":{"parameters":{"diarization_enabled":true}}}}`

// fullchainCreateQwenASRAccount 经管理面创建指向 mock 上游的 qwen api_key
// 账户并绑定分组（qwen 供应商 + 媒体档案 profile_qwen_openai_v1）；
// 能力域显式 audio（M2 写侧域规则：新建声明集
// ⊆ 域允许集，audio 域 lanes = audio_speech + audio_job_* 四值）；凭据显式
// 声明 openai 族词表的 audio_job_* 端点模式（opt-in，候选过滤按
// audio_job_create 消费；qwen 档案 Capabilities 声明 video_generation +
// audio_transcription，本用例同时覆盖长转写端点模式的写侧门禁）。
// fullchainEnsureQwenASRCatalogModel 保障 paraformer-v2 在管理面供应商模型
// 目录中（账户创建校验 supportedModels ⊆ 目录）：paraformer-v2 静态目录行已
// 随 2025 前模型清理批移除，此处种零价自定义行（秒价 0 → 成本 0、计量照落，
// 与链级断言同口径）。
func (f *fullchainFixture) fullchainEnsureQwenASRCatalogModel(model string) {
	f.t.Helper()
	_, payload := f.admin.do(http.MethodGet,
		"/__aisys__/api/providers/qwen/models?includeInactive=true&includeUnpriced=true",
		nil, wantStatus(http.StatusOK))
	items, _ := payload["data"].([]any)
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok && str(item["model"]) == model {
			return
		}
	}
	f.admin.do(http.MethodPost, "/__aisys__/api/providers/qwen/models", map[string]any{
		"model":                 model,
		"scope":                 "global",
		"status":                "active",
		"mode":                  "audio",
		"supportedApiProtocols": []string{"audio_transcription"},
		// 自定义模型 API 价字段仅 per-1M 形态（custom_models 归一集）：零价
		// 自定义行 → 运行时计价 0 → 成本 0、计量照落，与链级断言同口径。
		"audioInputUsdPer1M":  0,
		"audioOutputUsdPer1M": 0,
	}, wantStatus(http.StatusCreated))
}

func (f *fullchainFixture) fullchainCreateQwenASRAccount(name, upstreamKey, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "qwen",
		"providerProtocolProfileId": "profile_qwen_openai_v1",
		"name":                      name,
		"type":                      "api_key",
		"capabilityDomain":          "audio",
		"credentials": map[string]any{
			"api_key":  upstreamKey,
			"base_url": f.mock.server.URL,
			"supported_endpoint_modes": []string{
				"audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel",
			},
		},
		"supportedModels": []string{"paraformer-v2"},
		"status":          "active",
		"groupId":         groupID,
	}, wantStatus(http.StatusCreated))
	accountID := str(data(created)["id"])
	if accountID == "" {
		f.t.Fatalf("qwen asr account create payload wrong: %#v", created)
	}
	return accountID
}

// TestFullchainMediaAudioJobParaformer 验证 M3f qwen（paraformer）长转写链
// 主流程（契约 §10.2）：创建 200 + 统一 audio_job 对象（audiojob_ 前缀、
// status queued、provider_job_id 为 uuid 任务 id、params_applied 含
// model/input_url/language、provider_options_applied 含 parameters）→ 轮询
// 三态推进 → content 下载转写结果 JSON（application/json + transcripts
// 字段）→ 管理面 media-jobs 行 kind=audio_transcription 终态（时长计量
// 62.5s 照落；paraformer-v2 目录行已随 2025 前模型清理批移除 → 无秒价
// 成本 0 不虚计）→ 账户亲和（创建 1 次 + 轮询 3 次，
// content 直连无 Authorization）。
func TestFullchainMediaAudioJobParaformer(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	key := fullchainUpstreamKey(t, "mvqa")
	f.fullchainEnsureQwenASRCatalogModel("paraformer-v2")
	groupID := f.createGroupWithProvider("MVqwenASR组", "qwen")
	accountID := f.fullchainCreateQwenASRAccount("全链路-MVqwenASR账户", key, groupID)
	strategyID := f.createStrategy("全链路-MVqwenASR策略", "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := f.createAPIKey("全链路-MVqwenASR-Key", strategyID)
	apiKeyID := f.apiKeyIDByName("全链路-MVqwenASR-Key")

	// 创建：200 + 统一 audio_job 对象。paraformer 天然异步（无 X-DashScope-
	// Async 头，契约 §10.2），创建成功即证明链上未注入万相异步头（链级测试
	// 直接断言头缺席）。
	f.mock.script(key, platformmock.ScenarioMediaQwenASRCreatePending)
	created := f.videoT(t, apiKey, http.MethodPost, "/v1/audio/jobs", fullchainQwenASRCreateBody)
	if created.Status != http.StatusOK {
		t.Fatalf("MV qwen asr create status=%d body=%s", created.Status, created.Body)
	}
	job := decodeVideoJob(t, created)
	jobID := str(job["id"])
	if !strings.HasPrefix(jobID, "audiojob_") {
		t.Fatalf("MV qwen asr job id 缺少 audiojob_ 前缀: %#v", job)
	}
	if job["object"] != "audio_job" {
		t.Fatalf("MV qwen asr object = %v, want audio_job", job["object"])
	}
	if job["status"] != "queued" {
		t.Fatalf("MV qwen asr create status 字段 = %v, want queued", job["status"])
	}
	if job["provider"] != "qwen" {
		t.Fatalf("MV qwen asr provider = %v, want qwen", job["provider"])
	}
	if job["language"] != "zh" {
		t.Fatalf("MV qwen asr language 回显 = %v, want zh", job["language"])
	}
	providerJobID := str(job["provider_job_id"])
	if len(providerJobID) != 36 || !strings.Contains(providerJobID, "-") {
		t.Fatalf("MV qwen asr provider_job_id = %q, want uuid 任务 id 形态（与万相同一任务接口）", providerJobID)
	}
	applied := fmt.Sprintf("%v", job["params_applied"])
	for _, want := range []string{"model", "input_url", "language"} {
		if !strings.Contains(applied, want) {
			t.Fatalf("MV qwen asr params_applied 缺少 %s: %v", want, job["params_applied"])
		}
	}
	if !strings.Contains(fmt.Sprintf("%v", job["provider_options_applied"]), "parameters") {
		t.Fatalf("MV qwen asr provider_options_applied 缺少 parameters（qwen 子对象命中键名回显）: %v", job["provider_options_applied"])
	}

	// 轮询三次：queued → in_progress（RUNNING 归一）→ completed（SUCCEEDED +
	// output.results[].transcription_url 冻结 + usage duration 时长计量）。
	for index, want := range []string{"queued", "in_progress", "completed"} {
		polled := f.videoT(t, apiKey, http.MethodGet, "/v1/audio/jobs/"+jobID, "")
		if polled.Status != http.StatusOK {
			t.Fatalf("MV qwen asr poll #%d status=%d body=%s", index+1, polled.Status, polled.Body)
		}
		if object := decodeVideoJob(t, polled); object["status"] != want {
			t.Fatalf("MV qwen asr poll #%d status 字段 = %v, want %s", index+1, object["status"], want)
		}
	}

	// content 下载：completed 的 transcription_url 是引擎渲染的绝对 URL，网关
	// 无凭据直连。产物是转写结果 JSON 文件（契约 §10.2），非音频字节。
	content := f.videoT(t, apiKey, http.MethodGet, "/v1/audio/jobs/"+jobID+"/content", "")
	if content.Status != http.StatusOK {
		t.Fatalf("MV qwen asr content status=%d body=%s", content.Status, content.Body)
	}
	if !strings.Contains(content.ContentType, "application/json") {
		t.Fatalf("MV qwen asr content-type=%q, want application/json（转写结果 JSON）", content.ContentType)
	}
	if !strings.Contains(content.Body, `"transcripts"`) || !strings.Contains(content.Body, "meeting.mp3") {
		t.Fatalf("MV qwen asr content 非转写结果 JSON（transcripts/file_url 缺失）: %s", content.Body)
	}

	// 管理面 media-jobs：行终态 completed、kind=audio_transcription、归因
	// qwen 账户；usage JSON 字符串解析出 62.5 秒时长计量照落（链级测试断言
	// audioInputSeconds=62.5 的 spool 记录）。paraformer-v2 目录行已随 2025
	// 前模型清理批移除 → 无秒价 → 成本 0 不虚计（计量与成本解耦语义，
	// 契约 §2.8 兜底；与链级 chain_media_audio_test.go 同口径）。
	row := f.waitMediaJobRow(t, apiKeyID, jobID, func(row fullchainMediaJobRow) bool {
		return row.Status == "completed"
	}, "completed")
	if row.AccountID != accountID {
		t.Fatalf("MV qwen asr row accountId=%s, want %s", row.AccountID, accountID)
	}
	if row.Kind != "audio_transcription" {
		t.Fatalf("MV qwen asr row kind=%s, want audio_transcription", row.Kind)
	}
	if row.CostUsd != 0 {
		t.Fatalf("MV qwen asr row costUsd=%v, want 0（paraformer-v2 目录行已移除：无秒价成本 0、计量照落）", row.CostUsd)
	}

	// 账户亲和 + 出站形态：qwen key 命中创建（POST /api/v1/services/audio/
	// asr/transcription）与三轮轮询（GET /api/v1/tasks/{id}），无其它带凭据
	// 流量打到该 key（content 直连无 Authorization，不落入该 key 的命中记录）。
	creates, polls := 0, 0
	for _, call := range f.mock.callsByKey(key) {
		switch {
		case call.Method == http.MethodPost && call.Path == "/api/v1/services/audio/asr/transcription":
			creates++
		case call.Method == http.MethodGet && strings.HasPrefix(call.Path, "/api/v1/tasks/"):
			polls++
		default:
			t.Fatalf("MV qwen asr 未预期的上游请求: %#v", call)
		}
	}
	if creates != 1 || polls != 3 {
		t.Fatalf("MV qwen asr 上游命中 creates=%d polls=%d, want 1/3", creates, polls)
	}
}

// ---------------------------------------------------------------------------
// M6 同步语音 E2E（契约 §7.2 glm 透传 / §9.2 火山 TTS adapter）
// ---------------------------------------------------------------------------

// fullchainCreateGlmSpeechAccount 经管理面创建指向 mock 上游的 glm api_key
// 语音账户并绑定分组（glm 供应商 + 通用档案 profile_glm_general_openai_v1；
// 目录 seed 预置 cogtts 行）；能力域显式 audio（M2 写侧域规则）；凭据显式
// 声明 openai 族词表的 audio_speech 端点模式（opt-in；M6 起 glm 写侧词表
// 放行同步音频 token）。
func (f *fullchainFixture) fullchainCreateGlmSpeechAccount(name, upstreamKey, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "glm",
		"providerProtocolProfileId": "profile_glm_general_openai_v1",
		"name":                      name,
		"type":                      "api_key",
		"capabilityDomain":          "audio",
		"credentials": map[string]any{
			"api_key":  upstreamKey,
			"base_url": f.mock.server.URL,
			"supported_endpoint_modes": []string{
				"audio_speech",
			},
		},
		"supportedModels": []string{"cogtts"},
		"status":          "active",
		"groupId":         groupID,
	}, wantStatus(http.StatusCreated))
	accountID := str(data(created)["id"])
	if accountID == "" {
		f.t.Fatalf("glm speech account create payload wrong: %#v", created)
	}
	return accountID
}

// TestFullchainMediaSpeechGlmTTS 验证 M6 glm 语音透传链（契约 §7.2）：管理面
// 配置（glm 分组 + cogtts 语音账户，audio_speech 端点模式写侧门禁）→
// POST /v1/audio/speech → 出站 POST /api/paas/v4/audio/speech（官方通用根
// 归一、Bearer 认证；provider_options.glm 的 ref_audio/ref_text 深合并由链级
// 测试断言，E2E 侧以 mock 按请求 response_format 协商回 wav 佐证透传面）→
// 客户端收到 wav 二进制（RIFF magic bytes）；usage 按请求字符自算落账
// （透传面无上游回报）。
func TestFullchainMediaSpeechGlmTTS(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	key := fullchainUpstreamKey(t, "mvgt")
	groupID := f.createGroupWithProvider("MVglmTTS组", "glm")
	f.fullchainCreateGlmSpeechAccount("全链路-MVglmTTS账户", key, groupID)
	strategyID := f.createStrategy("全链路-MVglmTTS策略", "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := f.createAPIKey("全链路-MVglmTTS-Key", strategyID)
	apiKeyID := f.apiKeyIDByName("全链路-MVglmTTS-Key")

	f.mock.script(key, platformmock.ScenarioMediaGlmTTSOK)
	response := f.videoT(t, apiKey, http.MethodPost, "/v1/audio/speech",
		`{"model":"cogtts","input":"你好智谱","voice":"tongtong","response_format":"wav","provider_options":{"glm":{"ref_audio":"data:audio/wav;base64,aGVsbG8=","ref_text":"样本文本"}}}`)
	if response.Status != http.StatusOK {
		t.Fatalf("MV glm tts status=%d body=%s", response.Status, response.Body)
	}
	if !strings.Contains(response.ContentType, "audio/wav") {
		t.Fatalf("MV glm tts content-type=%q, want audio/wav", response.ContentType)
	}
	payload := []byte(response.Body)
	if len(payload) < 12 || string(payload[0:4]) != "RIFF" || string(payload[8:12]) != "WAVE" {
		t.Fatalf("MV glm tts 响应不是 wav 载荷（RIFF/WAVE magic bytes 缺失）: % x", payload[:min(12, len(payload))])
	}

	// 账户亲和 + 出站形态：POST /api/paas/v4/audio/speech（官方通用根归一，
	// 非 openai /v1 补缀）+ Bearer 认证头；无其它带凭据流量打到该 key。
	for _, call := range f.mock.callsByKey(key) {
		if call.Method != http.MethodPost || call.Path != "/api/paas/v4/audio/speech" {
			t.Fatalf("MV glm tts 未预期的上游请求: %#v", call)
		}
		if call.AuthHeader != "Bearer "+key {
			t.Fatalf("MV glm tts Authorization=%q, want Bearer %s", call.AuthHeader, key)
		}
	}
	if len(f.mock.callsByKey(key)) != 1 {
		t.Fatalf("MV glm tts 上游命中 %d 次, want 1: %+v", len(f.mock.callsByKey(key)), f.mock.callsByKey(key))
	}

	// usage：透传面无上游字符回报 → 请求字符自算（4 runes）落账。
	records := f.waitUsageRecords(t, apiKeyID, func(rows []fullchainUsageRecord) bool {
		for _, row := range rows {
			if row.Endpoint == "POST /v1/audio/speech" && row.Success && row.Model == "cogtts" {
				return true
			}
		}
		return false
	}, "glm tts usage record")
	_ = records
}

// fullchainEnsureVolcengineTTSCatalogModel 确保 TTS 占位模型名在 volcengine
// 供应商目录可用（协议声明 audio_speech 以过账户支持模型的档案协议承接
// 校验）：先查目录已有该模型（含未定价/未启用行）则跳过创建——2026-10-12
// 核对批起 doubao-tts 已落系统内置行，重复创建会 400「该模型已由系统内置
// 提供」。火山 V3 TTS 无官方模型 ID（模型由语音应用决定，契约 §9.2），网关
// 模型门要求账户显式声明支持模型——目录行承载占位名，值不透传上游；缺失时
// 沿 ensureProbeModelInCatalog 先例补零价自定义行（mode=audio 的启用态价格
// 校验要求 audio 价字段在场）。
func (f *fullchainFixture) fullchainEnsureVolcengineTTSCatalogModel(model string) {
	f.t.Helper()
	_, payload := f.admin.do(http.MethodGet,
		"/__aisys__/api/providers/volcengine/models?includeInactive=true&includeUnpriced=true",
		nil, wantStatus(http.StatusOK))
	items, _ := payload["data"].([]any)
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok && str(item["model"]) == model {
			return
		}
	}
	f.admin.do(http.MethodPost, "/__aisys__/api/providers/volcengine/models", map[string]any{
		"model":                 model,
		"scope":                 "global",
		"status":                "active",
		"mode":                  "audio",
		"supportedApiProtocols": []string{"audio_speech"},
		// mode=audio 的启用态价格校验要求 audio 价字段在场：官方无可查证
		// USD 字符价（契约 §9.2），显式配 0（零价自定义行——计量照落、
		// 成本 0 不虚计，与目录不落价的官方行同口径）。
		"audioInputUsdPer1M":  0,
		"audioOutputUsdPer1M": 0,
	}, wantStatus(http.StatusCreated))
}

// fullchainCreateVolcengineSpeechAccount 经管理面创建指向 mock 上游的
// volcengine api_key 语音账户并绑定分组（volcengine 供应商 + 媒体档案
// profile_volcengine_openai_v1，M6 起声明 tts family）；凭据显式声明
// audio_speech 端点模式 + **单值 speech_token**（2026-10-17 TTS 迁移批
// 语义 = 新版语音控制台 API Key；语音 token 是上游鉴权 key——mock 上游
// 按它归因）。模型约束种统一面占位名（自定义目录行，见
// fullchainEnsureVolcengineTTSCatalogModel）。
func (f *fullchainFixture) fullchainCreateVolcengineSpeechAccount(name, arkKey, speechToken, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "volcengine",
		"providerProtocolProfileId": "profile_volcengine_openai_v1",
		"name":                      name,
		"type":                      "api_key",
		"capabilityDomain":          "audio",
		"credentials": map[string]any{
			"api_key":      arkKey,
			"base_url":     f.mock.server.URL,
			"speech_token": speechToken,
			"supported_endpoint_modes": []string{
				"audio_speech",
			},
		},
		"supportedModels": []string{"doubao-tts"},
		"status":          "active",
		"groupId":         groupID,
	}, wantStatus(http.StatusCreated))
	accountID := str(data(created)["id"])
	if accountID == "" {
		f.t.Fatalf("volcengine speech account create payload wrong: %#v", created)
	}
	return accountID
}

// TestFullchainMediaSpeechVolcengineTTS 验证火山 TTS adapter 链（契约
// §9.2，2026-10-17 迁移批 /api/v3/tts/create 面）：管理面配置（volcengine
// 分组 + 语音单值凭据账户 + 自定义占位目录行 + audio_speech 端点模式写侧
// 门禁）→ POST /v1/audio/speech → 出站 POST /api/v3/tts/create（固定语音
// 服务域——测试 seam env 把网关出站指回 mock；`X-Api-Key: <speech_token>`
// 单头鉴权，Authorization 不出站）→ 客户端收到 base64 解码后的 mp3 二进制
// （frame sync magic bytes）；usage 按请求字符自算落账（上游无字符回报）。
func TestFullchainMediaSpeechVolcengineTTS(t *testing.T) {
	requireFullchainGate(t)
	// 组装顺序：mock 先于网关（extraEnv 回调拿 mock URL——火山语音出站
	// host 是与账户 base_url 无关的固定语音服务域，经测试 seam
	// JUHE_AI_GATEWAY_VOLCENGINE_SPEECH_BASE_URL 指回本夹具 mock；生产
	// 不设置该 env，缺省恒官方根）。
	f := startFullchainFixtureExtraEnv(t, func(mockURL string) map[string]string {
		return map[string]string{
			"JUHE_AI_GATEWAY_VOLCENGINE_SPEECH_BASE_URL": mockURL,
		}
	})

	arkKey := fullchainUpstreamKey(t, "mvvt")
	speechToken := fullchainUpstreamKey(t, "mvvt-tts")
	f.fullchainEnsureVolcengineTTSCatalogModel("doubao-tts")
	groupID := f.createGroupWithProvider("MVvolcengineTTS组", "volcengine")
	f.fullchainCreateVolcengineSpeechAccount("全链路-MVvolcengineTTS账户", arkKey, speechToken, groupID)
	strategyID := f.createStrategy("全链路-MVvolcengineTTS策略", "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := f.createAPIKey("全链路-MVvolcengineTTS-Key", strategyID)
	apiKeyID := f.apiKeyIDByName("全链路-MVvolcengineTTS-Key")

	f.mock.script(speechToken, platformmock.ScenarioMediaVolcengineTTSOK)
	response := f.videoT(t, apiKey, http.MethodPost, "/v1/audio/speech",
		`{"model":"doubao-tts","input":"你好火山","voice":"BV700_streaming","response_format":"mp3","speed":1.5}`)
	if response.Status != http.StatusOK {
		t.Fatalf("MV volcengine tts status=%d body=%s", response.Status, response.Body)
	}
	if !strings.Contains(response.ContentType, "audio/mpeg") {
		t.Fatalf("MV volcengine tts content-type=%q, want audio/mpeg", response.ContentType)
	}
	payload := []byte(response.Body)
	if len(payload) < 4 || payload[0] != 0xFF || payload[1] != 0xFB {
		t.Fatalf("MV volcengine tts 响应不是解码后的 mp3 载荷（frame sync 缺失）: % x", payload[:min(4, len(payload))])
	}

	// 账户亲和 + 出站形态：语音 token key 命中 POST /api/v3/tts/create 一次，
	// `X-Api-Key` 单头鉴权（Authorization 已被网关删除，不出站）；ark key
	//（视频面凭据）无流量。
	calls := f.mock.callsByKey(speechToken)
	if len(calls) != 1 {
		t.Fatalf("MV volcengine tts 语音 token 上游命中 %d 次, want 1: %+v", len(calls), calls)
	}
	if calls[0].Method != http.MethodPost || calls[0].Path != "/api/v3/tts/create" {
		t.Fatalf("MV volcengine tts 出站请求形态错误: %#v", calls[0])
	}
	if calls[0].APIKeyHeader != speechToken {
		t.Fatalf("MV volcengine tts X-Api-Key=%q, want speech_token（单头单值）", calls[0].APIKeyHeader)
	}
	if calls[0].AuthHeader != "" {
		t.Fatalf("MV volcengine tts Authorization=%q, want 空（语音面不得携带 ark Bearer 出站）", calls[0].AuthHeader)
	}
	// 出站报文（迁移批形态）：占位名 doubao-tts 归一映射为 seed-audio-1.0
	// 出站 + text_prompt/speaker/audio_config 映射（链级
	// TestChainMediaVolcengineSpeechFullChain 的同款断言在真实 /v1 链复核）。
	for _, fragment := range []string{`"model":"seed-audio-1.0"`, `"text_prompt":"你好火山"`, `"speaker":"BV700_streaming"`, `"format":"mp3"`, `"speech_rate":1.5`} {
		if !strings.Contains(calls[0].Body, fragment) {
			t.Fatalf("MV volcengine tts 出站报文缺少 %s: %s", fragment, calls[0].Body)
		}
	}
	if strings.Contains(calls[0].Body, "req_params") || strings.Contains(calls[0].Body, "doubao-tts") {
		t.Fatalf("MV volcengine tts 出站报文残留旧版骨架或占位名: %s", calls[0].Body)
	}
	if len(f.mock.callsByKey(arkKey)) != 0 {
		t.Fatalf("MV volcengine tts 不得经 ark 凭据出站: %+v", f.mock.callsByKey(arkKey))
	}

	// usage：V3 响应无字符回报 → 请求字符自算（4 runes）落账。
	f.waitUsageRecords(t, apiKeyID, func(rows []fullchainUsageRecord) bool {
		for _, row := range rows {
			if row.Endpoint == "POST /v1/audio/speech" && row.Success && row.Model == "doubao-tts" {
				return true
			}
		}
		return false
	}, "volcengine tts usage record")
}

// ---------------------------------------------------------------------------
// 2026-10-17 语音识别承接批 E2E（契约 §9.2 火山长转写 adapter）
// ---------------------------------------------------------------------------

// fullchainCreateVolcengineASRAccount 经管理面创建指向 mock 上游的
// volcengine api_key 长转写账户并绑定分组（volcengine 供应商 + 媒体档案
// profile_volcengine_openai_v1，承接批起声明 audio_transcription family）；
// 凭据显式声明 audio_job_* 四端点模式（opt-in，/v1/audio/jobs 创建链按
// audio_job_create 候选过滤）+ **单值 speech_token**（语音识别与 TTS 共用
// 新版语音控制台 API Key——上游鉴权 key，mock 上游按它归因）。模型约束种
// volc.seedasr.auc（目录现役行随静态层编译生效，无需自定义目录行）。
func (f *fullchainFixture) fullchainCreateVolcengineASRAccount(name, arkKey, speechToken, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "volcengine",
		"providerProtocolProfileId": "profile_volcengine_openai_v1",
		"name":                      name,
		"type":                      "api_key",
		"capabilityDomain":          "audio",
		"credentials": map[string]any{
			"api_key":      arkKey,
			"base_url":     f.mock.server.URL,
			"speech_token": speechToken,
			"supported_endpoint_modes": []string{
				"audio_job_create", "audio_job_get", "audio_job_content", "audio_job_cancel",
			},
		},
		"supportedModels": []string{"volc.seedasr.auc"},
		"status":          "active",
		"groupId":         groupID,
	}, wantStatus(http.StatusCreated))
	accountID := str(data(created)["id"])
	if accountID == "" {
		f.t.Fatalf("volcengine asr account create payload wrong: %#v", created)
	}
	return accountID
}

// fullchainEnsureVolcengineASRCatalogModel 保障 volc.seedasr.auc 在管理面
// 供应商模型目录中（账户创建校验 supportedModels ⊆ 目录；E2E 管理面目录是
// 独立 DB 表，不自动含静态层新增行）：秒价与静态层现役行同值
// 3.1746031746031745e-05（0.8 元/小时 ÷3600 ÷7.0）——无论运行时计价读静态
// 层还是自定义行，cost 断言 3.54921e-05 均一致。
func (f *fullchainFixture) fullchainEnsureVolcengineASRCatalogModel(model string) {
	f.t.Helper()
	_, payload := f.admin.do(http.MethodGet,
		"/__aisys__/api/providers/volcengine/models?includeInactive=true&includeUnpriced=true",
		nil, wantStatus(http.StatusOK))
	items, _ := payload["data"].([]any)
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok && str(item["model"]) == model {
			return
		}
	}
	f.admin.do(http.MethodPost, "/__aisys__/api/providers/volcengine/models", map[string]any{
		"model":                 model,
		"scope":                 "global",
		"status":                "active",
		"mode":                  "audio",
		"supportedApiProtocols": []string{"audio_transcription"},
		// 自定义模型 API 价字段仅 per-1M 形态（custom_models 归一集）——本行
		// 仅满足「supportedModels ⊆ 目录」创建校验；job 终态计费按模型秒价
		// 走静态层现役行（3.1746031746031745e-05/s），此处零价 per-1M 不
		// 参与秒价计费维度。
		"audioInputUsdPer1M":  0,
		"audioOutputUsdPer1M": 0,
	}, wantStatus(http.StatusCreated))
}

// TestFullchainMediaAudioJobVolcengineASR 验证火山长转写 adapter 链（契约
// §9.2，2026-10-17 承接批 /api/v3/auc/bigmodel/submit|query 头驱动协议）：
// 管理面配置（volcengine 分组 + 语音单值凭据账户 + audio_job_* 端点模式
// 写侧门禁）→ POST /v1/audio/jobs → 出站 POST /api/v3/auc/bigmodel/submit
// （固定语音服务域——测试 seam env 把网关出站指回 mock；`X-Api-Key:
// <speech_token>` 单头 + `X-Api-Resource-Id: volc.seedasr.auc` + 网关生成
// `X-Api-Request-Id` 任务 ID，Authorization 不出站；submit 成功响应 body
// 为空、成功标志在响应头——mock 按 create_ok 场景 #1 查询 running、#2 起
// 终态）→ 轮询 in_progress → completed → content 端点同构 query 再查一次
// 代理终态转写 JSON → 管理面 media-jobs 行 cost=1.118s×目录秒价
// 3.1746031746031745e-05 → fixedNumber 10 位 = 3.54921e-05。
func TestFullchainMediaAudioJobVolcengineASR(t *testing.T) {
	requireFullchainGate(t)
	// 组装顺序：mock 先于网关（extraEnv 回调拿 mock URL——火山语音出站
	// host 是与账户 base_url 无关的固定语音服务域，经测试 seam
	// JUHE_AI_GATEWAY_VOLCENGINE_SPEECH_BASE_URL 指回本夹具 mock——TTS 与
	// 长转写共用同一语音服务域 seam，语义=语音域根；生产不设置该 env）。
	f := startFullchainFixtureExtraEnv(t, func(mockURL string) map[string]string {
		return map[string]string{
			"JUHE_AI_GATEWAY_VOLCENGINE_SPEECH_BASE_URL": mockURL,
		}
	})

	arkKey := fullchainUpstreamKey(t, "mvva")
	speechToken := fullchainUpstreamKey(t, "mvva-asr")
	f.fullchainEnsureVolcengineASRCatalogModel("volc.seedasr.auc")
	groupID := f.createGroupWithProvider("MVvolcengineASR组", "volcengine")
	accountID := f.fullchainCreateVolcengineASRAccount("全链路-MVvolcengineASR账户", arkKey, speechToken, groupID)
	strategyID := f.createStrategy("全链路-MVvolcengineASR策略", "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := f.createAPIKey("全链路-MVvolcengineASR-Key", strategyID)
	apiKeyID := f.apiKeyIDByName("全链路-MVvolcengineASR-Key")

	// 创建：头驱动受理——submit 成功即 queued 统一 job 对象；任务 ID =
	// 网关生成 X-Api-Request-Id（volcasr-<24hex>，轮询/content 复用）。
	f.mock.script(speechToken, platformmock.ScenarioMediaVolcengineASRCreateOK)
	created := f.videoT(t, apiKey, http.MethodPost, "/v1/audio/jobs",
		`{"model":"volc.seedasr.auc","input_url":"https://oss.example.com/audio/meeting.wav"}`)
	if created.Status != http.StatusOK {
		t.Fatalf("MV volcengine asr create status=%d body=%s", created.Status, created.Body)
	}
	job := decodeVideoJob(t, created)
	jobID := str(job["id"])
	if !strings.HasPrefix(jobID, "audiojob_") {
		t.Fatalf("MV volcengine asr job id 缺少 audiojob_ 前缀: %#v", job)
	}
	if job["object"] != "audio_job" {
		t.Fatalf("MV volcengine asr object = %v, want audio_job", job["object"])
	}
	if job["status"] != "queued" {
		t.Fatalf("MV volcengine asr create status 字段 = %v, want queued", job["status"])
	}
	if job["provider"] != "volcengine" {
		t.Fatalf("MV volcengine asr provider = %v, want volcengine", job["provider"])
	}
	providerJobID := str(job["provider_job_id"])
	if !strings.HasPrefix(providerJobID, "volcasr-") || len(providerJobID) != len("volcasr-")+24 {
		t.Fatalf("MV volcengine asr provider_job_id = %q, want volcasr-<24hex> 网关生成任务 ID", providerJobID)
	}

	// 轮询：create_ok 场景 #1 查询 in_progress（X-Api-Status-Code 20000001）
	// → #2 终态 completed（20000000 + audio_info.duration=1118 毫秒 + 转写
	// 文本）。轮询响应的毫秒时长由 adapter ÷1000 落 audio_input_seconds。
	for index, want := range []string{"in_progress", "completed"} {
		polled := f.videoT(t, apiKey, http.MethodGet, "/v1/audio/jobs/"+jobID, "")
		if polled.Status != http.StatusOK {
			t.Fatalf("MV volcengine asr poll #%d status=%d body=%s", index+1, polled.Status, polled.Body)
		}
		if object := decodeVideoJob(t, polled); object["status"] != want {
			t.Fatalf("MV volcengine asr poll #%d status 字段 = %v, want %s", index+1, object["status"], want)
		}
	}

	// content 下载：火山无产物下载 URL（qwen transcription_url 同构物不存
	// 在），content = adapter 同构 query 请求再查一次上游，终态 body 直接
	// 代理（application/json + 实测转写文本）。
	content := f.videoT(t, apiKey, http.MethodGet, "/v1/audio/jobs/"+jobID+"/content", "")
	if content.Status != http.StatusOK {
		t.Fatalf("MV volcengine asr content status=%d body=%s", content.Status, content.Body)
	}
	if !strings.Contains(content.ContentType, "application/json") {
		t.Fatalf("MV volcengine asr content-type=%q, want application/json（转写结果 JSON）", content.ContentType)
	}
	if !strings.Contains(content.Body, "Carry an oily rag.") {
		t.Fatalf("MV volcengine asr content 缺少实测转写文本: %s", content.Body)
	}

	// 管理面 media-jobs：行终态 completed、kind=audio_transcription、归因
	// volcengine 账户；cost_usd=1.118s × $3.1746031746031745e-05/s（目录
	// 现役行 0.8 元/小时 ÷3600 ÷7.0）→ fixedNumber 10 位 = 3.54921e-05。
	row := f.waitMediaJobRow(t, apiKeyID, jobID, func(row fullchainMediaJobRow) bool {
		return row.Status == "completed"
	}, "completed")
	if row.AccountID != accountID {
		t.Fatalf("MV volcengine asr row accountId=%s, want %s", row.AccountID, accountID)
	}
	if row.Kind != "audio_transcription" {
		t.Fatalf("MV volcengine asr row kind=%s, want audio_transcription", row.Kind)
	}
	if row.CostUsd != 3.54921e-05 {
		t.Fatalf("MV volcengine asr row costUsd=%v, want 3.54921e-05（1.118s × 目录秒价，fixedNumber 10 位舍入）", row.CostUsd)
	}

	// 账户亲和 + 出站形态：语音 token key 命中 submit×1 + query×3（轮询
	// 2 + content 1）；头集断言——X-Api-Key=speech_token 单头（Authorization
	// 不出站）、X-Api-Resource-Id 恒 volc.seedasr.auc、X-Api-Request-Id 恒
	// 网关生成任务 ID（submit 与全部 query 同值）；submit 报文断言
	// request.model_name 恒 bigmodel + audio.url/format（.wav 后缀推断）。
	calls := f.mock.callsByKey(speechToken)
	submits, queries := 0, 0
	for _, call := range calls {
		switch {
		case call.Method == http.MethodPost && call.Path == "/api/v3/auc/bigmodel/submit":
			submits++
		case call.Method == http.MethodPost && call.Path == "/api/v3/auc/bigmodel/query":
			queries++
		default:
			t.Fatalf("MV volcengine asr 未预期的上游请求: %#v", call)
		}
		if call.APIKeyHeader != speechToken {
			t.Fatalf("MV volcengine asr X-Api-Key=%q, want speech_token（单头单值）", call.APIKeyHeader)
		}
		if call.AuthHeader != "" {
			t.Fatalf("MV volcengine asr Authorization=%q, want 空（长转写不得携带 ark Bearer 出站）", call.AuthHeader)
		}
		if call.ResourceIDHeader != "volc.seedasr.auc" {
			t.Fatalf("MV volcengine asr X-Api-Resource-Id=%q, want volc.seedasr.auc", call.ResourceIDHeader)
		}
		if call.RequestIDHeader != providerJobID {
			t.Fatalf("MV volcengine asr X-Api-Request-Id=%q, want 网关任务 ID %q", call.RequestIDHeader, providerJobID)
		}
	}
	if submits != 1 || queries != 3 {
		t.Fatalf("MV volcengine asr 上游命中 submits=%d queries=%d, want 1/3", submits, queries)
	}
	for _, call := range calls {
		if call.Path == "/api/v3/auc/bigmodel/submit" {
			for _, fragment := range []string{`"model_name":"bigmodel"`, `"url":"https://oss.example.com/audio/meeting.wav"`, `"format":"wav"`} {
				if !strings.Contains(call.Body, fragment) {
					t.Fatalf("MV volcengine asr submit 报文缺少 %s: %s", fragment, call.Body)
				}
			}
		}
	}
	if len(f.mock.callsByKey(arkKey)) != 0 {
		t.Fatalf("MV volcengine asr 不得经 ark 凭据出站: %+v", f.mock.callsByKey(arkKey))
	}
}
