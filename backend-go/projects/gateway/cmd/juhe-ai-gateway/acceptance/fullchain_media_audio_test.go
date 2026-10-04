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
