package acceptance

// M3f 长音频任务 E2E（契约 §10.2 paraformer；媒体设计 §4.2 /v1/audio/jobs
// 端点族）：管理面配置（qwen 分组 + paraformer-v2 账户，audio_job_* 端点
// 模式写侧门禁）→ 创建（transcription 报文改写、无 X-DashScope-Async 头、
// 统一 audio_job 对象、language→language_hints 映射经 provider_options 命中
// 回显）→ 轮询 queued → in_progress → completed（transcription_url 冻结）→
// content 经绝对 URL 无凭据直连下载转写结果 JSON → 管理面 media-jobs 行终态
//（kind=audio_transcription、usage duration 时长计量照落、目录无 USD 秒价
// cost=0 不虚计）→ 账户亲和（上游命中只打 qwen 账户 key 的 transcription
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
// 账户并绑定分组（qwen 供应商 + 媒体档案 profile_qwen_openai_v1；目录 seed
// 预置 paraformer-v2 行）；凭据显式声明 openai 族词表的 audio_job_* 端点
// 模式（opt-in，候选过滤按 audio_job_create 消费；qwen 档案 Capabilities
// 声明 video_generation + audio_transcription，本用例同时覆盖长转写端点
// 模式的写侧门禁）。
func (f *fullchainFixture) fullchainCreateQwenASRAccount(name, upstreamKey, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "qwen",
		"providerProtocolProfileId": "profile_qwen_openai_v1",
		"name":                      name,
		"type":                      "api_key",
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
// 字段）→ 管理面 media-jobs 行 kind=audio_transcription 终态（时长计量照落
// 但目录无 USD 秒价 → costUsd=0 不虚计）→ 账户亲和（创建 1 次 + 轮询 3 次，
// content 直连无 Authorization）。
func TestFullchainMediaAudioJobParaformer(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	key := fullchainUpstreamKey(t, "mvqa")
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
	// audioInputSeconds=62.5 的 spool 记录）但目录未落 USD 秒价 → cost_usd=0
	//（契约 §10.2/§2.8 计量照落成本不虚计）。
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
		t.Fatalf("MV qwen asr row costUsd=%v, want 0（目录无 USD 秒价不虚计）", row.CostUsd)
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
// 目录 seed 预置 cogtts 行）；凭据显式声明 openai 族词表的 audio_speech 端点
// 模式（opt-in；M6 起 glm 写侧词表放行同步音频 token）。
func (f *fullchainFixture) fullchainCreateGlmSpeechAccount(name, upstreamKey, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "glm",
		"providerProtocolProfileId": "profile_glm_general_openai_v1",
		"name":                      name,
		"type":                      "api_key",
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
//（透传面无上游回报）。
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

// fullchainEnsureVolcengineTTSCatalogModel 把统一面 TTS 占位模型名注册进
// volcengine 供应商目录（零价自定义行，沿 ensureProbeModelInCatalog 先例；
// 协议声明 audio_speech 以过账户支持模型的档案协议承接校验）：火山 V3 TTS
// 无官方模型 ID（模型由语音应用决定，契约 §9.2），网关模型门要求账户显式
// 声明支持模型——运营以自定义目录行承载占位名，值不透传上游。
func (f *fullchainFixture) fullchainEnsureVolcengineTTSCatalogModel(model string) {
	f.t.Helper()
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
// audio_speech 端点模式 + **语音应用双值 speech_appid/speech_token**（M6
// 凭据归一化放行键；语音 token 是上游鉴权 key——mock 上游按它归因）。
// 模型约束种统一面占位名（自定义目录行，见 fullchainEnsureVolcengine-
// TTSCatalogModel）。
func (f *fullchainFixture) fullchainCreateVolcengineSpeechAccount(name, arkKey, speechToken, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "volcengine",
		"providerProtocolProfileId": "profile_volcengine_openai_v1",
		"name":                      name,
		"type":                      "api_key",
		"credentials": map[string]any{
			"api_key":     arkKey,
			"base_url":    f.mock.server.URL,
			"speech_appid": "app-voice-777",
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

// TestFullchainMediaSpeechVolcengineTTS 验证 M6 火山 TTS adapter 链（契约
// §9.2）：管理面配置（volcengine 分组 + 语音双值凭据账户 + 自定义占位目录
// 行 + audio_speech 端点模式写侧门禁）→ POST /v1/audio/speech → 出站
// POST /api/v3/tts（固定语音服务域——测试 seam env 把网关出站指回 mock；
// `Authorization: Bearer;<speech_token>` 分号鉴权特例）→ 客户端收到 base64
// 解码后的 mp3 二进制（frame sync magic bytes）；usage 按请求字符自算落账
//（V3 响应无字符回报）。
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

	// 账户亲和 + 出站形态：语音 token key 命中 POST /api/v3/tts 一次，
	// `Bearer;<token>` 分号鉴权特例；ark key（视频面凭据）无流量。
	calls := f.mock.callsByKey(speechToken)
	if len(calls) != 1 {
		t.Fatalf("MV volcengine tts 语音 token 上游命中 %d 次, want 1: %+v", len(calls), calls)
	}
	if calls[0].Method != http.MethodPost || calls[0].Path != "/api/v3/tts" {
		t.Fatalf("MV volcengine tts 出站请求形态错误: %#v", calls[0])
	}
	if calls[0].AuthHeader != "Bearer;"+speechToken {
		t.Fatalf("MV volcengine tts Authorization=%q, want Bearer;%s（分号特例）", calls[0].AuthHeader, speechToken)
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
