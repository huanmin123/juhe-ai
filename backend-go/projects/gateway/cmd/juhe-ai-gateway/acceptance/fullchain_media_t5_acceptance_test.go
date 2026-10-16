// AI 账户能力域拆分 M2·T5 行为级验收欠账补齐（计划
// 计划-20261011T141004000Z-AI账户能力域拆分「行为级验收」T5）：在既有
// fullchain 媒体矩阵（fullchain_media_audio_test.go /
// fullchain_media_video_test.go）之上补齐三块缺口，全部跑在隔离临时目录 +
// 空闲端口 + 全 Mock 上游（不触真实付费上游、不触开发库）：
//
//  1. TestFullchainT5VolcengineSpeechMissingCredentials：volcengine
//     audio_speech 语音凭据缺失负例——管理面创建 audio_speech 模式但缺
//     speech_token 的账户（写侧单值可选、能力由派发显式失败表达，
//     internal/accounts/credentials_normalize.go 契约），真实 /v1
//     请求链显式 400 失败（speech_credentials_missing，账户级配置缺位的
//     确定性错误，不静默回退）且零上游出站（对齐链级
//     TestChainMediaVolcengineSpeechMissingCredentials 语义）；
//  2. TestFullchainT5QwenAudioJobCancel：qwen 长音频任务型能力的取消链
//     ——§10.2 面无上游取消 API，DELETE /v1/audio/jobs/{id} 本地收敛
//     cancelled + 204，零上游出站，终态轮询回本地对象（补齐
//     TestFullchainMediaAudioJobParaformer 覆盖的创建/查询/内容之外的
//     第四个任务型操作）；
//  3. TestFullchainT5ImagesSharedLaneCoCandidates：图像请求的共同候选与
//     换户链（v8 后语义：images_json 声明隔离在 image 域内，chat 域新增
//     声明不再携带图像 lane——原 D2 跨域共享 lane 对照随域纯粹化裁撤，
//     本测试保留其派发行为面：同分组内两个 images_json-only 账户对
//     POST /v1/images/generations 按候选顺序共同候选，A 429 换 B 成功，
//     两账户各收到创建请求），出站路径/鉴权/模型名与响应透传逐项核对。
//     mock 上游 images 端点为本次补的依赖边界（mockupstream/images.go）。
//
// capability_domain 不参与派发的对照说明（D2 零改动裁决）：本文件与既有
// 媒体矩阵按 M2 写侧契约显式声明能力域（audio/video/image/chat），而网关
// 派发代码不读取 capability_domain 列——chat 域账户承接 images 请求
// （本文件第 3 条 A 账户）、image 域账户与 chat 域账户共同候选同一图像请求
// 即域不参与派发的行为级证据；媒体 lane（audio/video 域账户）的正常派发
// 由本文件第 1/2 条与既有媒体矩阵共同覆盖。
package acceptance

import (
	"net/http"
	"strings"
	"testing"

	platformmock "github.com/huanminabc/juhe-ai/backend-go-platform/mockupstream"
)

// ---------------------------------------------------------------------------
// 1. volcengine audio_speech 语音凭据缺失负例
// ---------------------------------------------------------------------------

// TestFullchainT5VolcengineSpeechMissingCredentials 验证 T5 volcengine
// 语音凭据的失败面：管理面创建 audio_speech 模式但缺 speech_token 的账户
// （单值写入面可选，2026-10-17 TTS 迁移批语义——speech_token = 新版语音
// 控制台 API Key）→ POST /v1/audio/speech 经真实网关链显式 400 失败
// （speech_credentials_missing：账户级配置缺位的确定性错误，非可重试上游
// 故障、不静默回退），mock 上游零出站（无 /api/v3/tts/create 流量）。
func TestFullchainT5VolcengineSpeechMissingCredentials(t *testing.T) {
	requireFullchainGate(t)
	// 火山语音出站 host 恒固定语音服务域（生产缺省 openspeech.bytedance.com
	// 不经 base_url），测试 seam env 指回本夹具 mock——负例断言「零出站」
	// 需要出站面可达才能证明「没有发生」，与既有正例同款夹具形态。
	f := startFullchainFixtureExtraEnv(t, func(mockURL string) map[string]string {
		return map[string]string{
			"JUHE_AI_GATEWAY_VOLCENGINE_SPEECH_BASE_URL": mockURL,
		}
	})

	arkKey := fullchainUpstreamKey(t, "t5-vnc")
	f.fullchainEnsureVolcengineTTSCatalogModel("doubao-tts")
	groupID := f.createGroupWithProvider("T5火山凭据缺失组", "volcengine")
	// 管理面创建：credentials 只带 api_key/base_url + audio_speech 端点模式，
	// 不带 speech_token（写侧契约：单值可选，不在写入面强制）。
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "volcengine",
		"providerProtocolProfileId": "profile_volcengine_openai_v1",
		"name":                      "全链路-T5火山缺凭据账户",
		"type":                      "api_key",
		"capabilityDomain":          "audio",
		"credentials": map[string]any{
			"api_key":  arkKey,
			"base_url": f.mock.server.URL,
			"supported_endpoint_modes": []string{
				"audio_speech",
			},
		},
		"supportedModels": []string{"doubao-tts"},
		"status":          "active",
		"groupId":         groupID,
	}, wantStatus(http.StatusCreated))
	if str(data(created)["id"]) == "" {
		t.Fatalf("T5 volcengine 缺凭据账户创建载荷异常: %#v", created)
	}
	strategyID := f.createStrategy("T5火山凭据缺失策略", "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := f.createAPIKey("T5火山凭据缺失-Key", strategyID)

	response := f.videoT(t, apiKey, http.MethodPost, "/v1/audio/speech",
		`{"model":"doubao-tts","input":"你好火山","voice":"BV700_streaming","response_format":"mp3"}`)
	if response.Status != http.StatusBadRequest {
		t.Fatalf("T5 volcengine 缺凭据 speech status=%d, want 400（speech_credentials_missing 确定性配置错误，非 5xx 上游故障）body=%s", response.Status, response.Body)
	}
	if !strings.Contains(response.Body, "speech_credentials_missing") {
		t.Fatalf("T5 volcengine 缺凭据响应缺少稳定错误码 speech_credentials_missing: %s", response.Body)
	}
	// 零上游出站：凭据缺失在出站构造期失败，不打 mock（含语音域 seam 面）。
	if calls := f.mock.callsByKey(arkKey); len(calls) != 0 {
		t.Fatalf("T5 volcengine 缺凭据不得出站: %+v", calls)
	}
}

// ---------------------------------------------------------------------------
// 2. qwen 长音频任务取消链（任务型能力第四操作）
// ---------------------------------------------------------------------------

// TestFullchainT5QwenAudioJobCancel 验证 T5 任务型能力的取消操作（契约
// §10.2：qwen 长转写面无上游取消 API，SupportsCancel=false 本地收敛）：
// 创建 audiojob_ 任务 → DELETE /v1/audio/jobs/{id} → 204 + media_jobs 行
// 本地收敛 cancelled → 终态 GET 回本地 cancelled 对象；上游命中仅创建
// 1 次，取消与终态轮询零上游出站。
func TestFullchainT5QwenAudioJobCancel(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	key := fullchainUpstreamKey(t, "t5-qac")
	groupID := f.createGroupWithProvider("T5qwen取消组", "qwen")
	f.fullchainCreateQwenASRAccount("全链路-T5qwen取消账户", key, groupID)
	strategyID := f.createStrategy("T5qwen取消策略", "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := f.createAPIKey("T5qwen取消-Key", strategyID)
	apiKeyID := f.apiKeyIDByName("T5qwen取消-Key")

	// 创建：queued 任务（与 paraformer 主流程用例同一 mock 场景）。
	f.mock.script(key, platformmock.ScenarioMediaQwenASRCreatePending)
	created := f.videoT(t, apiKey, http.MethodPost, "/v1/audio/jobs", fullchainQwenASRCreateBody)
	if created.Status != http.StatusOK {
		t.Fatalf("T5 qwen cancel create status=%d body=%s", created.Status, created.Body)
	}
	jobID := str(decodeVideoJob(t, created)["id"])
	if !strings.HasPrefix(jobID, "audiojob_") {
		t.Fatalf("T5 qwen cancel job id 缺少 audiojob_ 前缀: %s", jobID)
	}

	// 取消：204 + 本地行 cancelled（不依赖上游取消端点）。
	cancelled := f.videoT(t, apiKey, http.MethodDelete, "/v1/audio/jobs/"+jobID, "")
	if cancelled.Status != http.StatusNoContent {
		t.Fatalf("T5 qwen cancel DELETE status=%d body=%s, want 204", cancelled.Status, cancelled.Body)
	}
	row := f.waitMediaJobRow(t, apiKeyID, jobID, func(row fullchainMediaJobRow) bool {
		return row.Status == "cancelled"
	}, "cancelled")
	if row.Kind != "audio_transcription" {
		t.Fatalf("T5 qwen cancel row kind=%s, want audio_transcription", row.Kind)
	}

	// 终态轮询回本地对象：200 + cancelled，不再打上游。
	polled := f.videoT(t, apiKey, http.MethodGet, "/v1/audio/jobs/"+jobID, "")
	if polled.Status != http.StatusOK {
		t.Fatalf("T5 qwen cancel poll status=%d body=%s", polled.Status, polled.Body)
	}
	if object := decodeVideoJob(t, polled); object["status"] != "cancelled" {
		t.Fatalf("T5 qwen cancel poll status 字段 = %v, want cancelled", object["status"])
	}

	// 账户亲和：上游命中仅创建 1 次（POST transcription），取消与终态轮询
	// 零上游出站（无 /api/v1/tasks 流量）。
	calls := f.mock.callsByKey(key)
	if len(calls) != 1 {
		t.Fatalf("T5 qwen cancel 上游命中 %d 次, want 1（仅创建）: %+v", len(calls), calls)
	}
	if calls[0].Method != http.MethodPost || calls[0].Path != "/api/v1/services/audio/asr/transcription" {
		t.Fatalf("T5 qwen cancel 上游请求形态错误: %#v", calls[0])
	}
}

// ---------------------------------------------------------------------------
// 3. images_json 共享 lane 共同候选对照（D2）
// ---------------------------------------------------------------------------

// fullchainT5CreateImageOnlyAccount 经管理面创建 images_json-only 的图像
// 账户（gpt 供应商 + 既有 profile_gpt_openai_v1；能力域显式 image——image
// 域 lanes = images_json；模型约束种 gpt-image-1 自定义目录行）。无健康检查
// 模型、status active 跳过首检（与媒体账户同款创建形态）。
func (f *fullchainFixture) fullchainT5CreateImageOnlyAccount(name, upstreamKey, groupID string) string {
	f.t.Helper()
	_, created := f.admin.do(http.MethodPost, "/__aisys__/api/accounts", map[string]any{
		"providerCode":              "gpt",
		"providerProtocolProfileId": "profile_gpt_openai_v1",
		"name":                      name,
		"type":                      "api_key",
		"capabilityDomain":          "image",
		"credentials": map[string]any{
			"api_key":  upstreamKey,
			"base_url": f.mock.server.URL,
			"supported_endpoint_modes": []string{
				"images_json",
			},
		},
		"supportedModels": []string{"gpt-image-1"},
		"status":          "active",
		"groupId":         groupID,
	}, wantStatus(http.StatusCreated))
	accountID := str(data(created)["id"])
	if accountID == "" {
		f.t.Fatalf("T5 image-only account create payload wrong: %#v", created)
	}
	return accountID
}

// fullchainT5EnsureImageCatalogModel 确保 gpt-image-1 在 gpt 供应商目录可用
// （mode=image + images 协议声明过档案协议承接校验）：先查目录已有该模型
// （含未定价/未启用行）则跳过创建；缺失时沿 ensureProbeModelInCatalog 先例
// 补零价自定义行（outputUsdPerImage=0 满足启用态价格校验，零价不虚计）。
func (f *fullchainFixture) fullchainT5EnsureImageCatalogModel(model string) {
	f.t.Helper()
	_, payload := f.admin.do(http.MethodGet,
		"/__aisys__/api/providers/gpt/models?includeInactive=true&includeUnpriced=true",
		nil, wantStatus(http.StatusOK))
	items, _ := payload["data"].([]any)
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok && str(item["model"]) == model {
			return
		}
	}
	f.admin.do(http.MethodPost, "/__aisys__/api/providers/gpt/models", map[string]any{
		"model":                 model,
		"scope":                 "global",
		"status":                "active",
		"mode":                  "image",
		"supportedApiProtocols": []string{"images"},
		"outputUsdPerImage":     0,
	}, wantStatus(http.StatusCreated))
}

// fullchainT5EnableOwnerImageGeneration 打开 sys_admin 的图像生成开关
// （seed 默认 0，LaneImage 预检 403 gate）：PATCH 走管理面 CAS 契约——
// 先从列表行读 editVersion（= updated_at）作 expectedUpdatedAt，再携带
// imageGenerationEnabled 提交；PATCH 后 AccountCacheInvalidator 双通道失效
// 运行缓存，派发侧即时可见。
func (f *fullchainFixture) fullchainT5EnableOwnerImageGeneration() {
	f.t.Helper()
	_, payload := f.admin.do(http.MethodGet, "/__aisys__/api/system-accounts?pageSize=100",
		nil, wantStatus(http.StatusOK))
	items, _ := data(payload)["items"].([]any)
	editVersion := ""
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || str(item["id"]) != "sys_admin" {
			continue
		}
		editVersion = str(item["editVersion"])
	}
	if editVersion == "" {
		f.t.Fatalf("T5 images 未找到 sys_admin 列表行（无 editVersion）: %#v", data(payload)["items"])
	}
	f.admin.do(http.MethodPatch, "/__aisys__/api/system-accounts/sys_admin", map[string]any{
		"expectedUpdatedAt":      editVersion,
		"imageGenerationEnabled": true,
	}, wantStatus(http.StatusOK))
}

// TestFullchainT5ImagesSharedLaneCoCandidates 验证图像请求的共同候选与
// 换户链（v8 后语义：images_json 声明隔离在 image 域内，原 D2 跨域共享
// lane 对照随域纯粹化裁撤）：同分组内两个 image 形态账户（images_json-only）
// 对 POST /v1/images/generations 按候选顺序共同候选——A 先尝试（mock 429
// 可切换故障）→ 换 B 成功受理；断言两账户各收到出站创建请求（共同候选
// 证据）、出站路径 /v1/images/generations、Bearer 鉴权、模型名 gpt-image-1、
// 客户端响应为 mock images url 形态透传。
func TestFullchainT5ImagesSharedLaneCoCandidates(t *testing.T) {
	requireFullchainGate(t)
	f := startFullchainFixture(t)

	f.fullchainT5EnableOwnerImageGeneration()
	f.fullchainT5EnsureImageCatalogModel("gpt-image-1")

	// A/B 均为 image 域 images_json-only 图像形态账户。组内同优先级按既定
	// 候选顺序，A 先尝试（429 可切换）→ B 受理成功；派发按端点模式过滤，
	// 域不参与派发。
	keyA := fullchainUpstreamKey(t, "t5-img-a")
	keyB := fullchainUpstreamKey(t, "t5-img-b")
	groupID := f.createGroupWithProvider("T5图像共享组", "gpt")
	f.fullchainT5CreateImageOnlyAccount("全链路-T5图像共享-image账户A", keyA, groupID)
	f.fullchainT5CreateImageOnlyAccount("全链路-T5图像共享-image账户B", keyB, groupID)
	f.mock.setDefault(keyA, platformmock.ScenarioStatus429)
	f.mock.setDefault(keyB, platformmock.ScenarioMediaImagesOK)
	strategyID := f.createStrategy("T5图像共享策略", "normal", []map[string]any{
		{"groupId": groupID, "priority": 1, "weight": 100},
	}, nil)
	apiKey := f.createAPIKey("T5图像共享-Key", strategyID)

	body := `{"model":"gpt-image-1","prompt":"一只猫在弹钢琴","n":1}`
	response := f.videoT(t, apiKey, http.MethodPost, "/v1/images/generations", body)
	if response.Status != http.StatusOK {
		t.Fatalf("T5 images shared lane status=%d body=%s, want 200", response.Status, response.Body)
	}
	// 响应透传：mock images url 形态原样到达客户端。
	if !strings.Contains(response.Body, "mock-images.example/generated/mock-0001.png") ||
		!strings.Contains(response.Body, `"created":1700000000`) {
		t.Fatalf("T5 images shared lane 响应非 mock images 透传形态: %s", response.Body)
	}

	// 共同候选证据：A 收到 429 尝试、B 收到成功受理，均为 POST
	// /v1/images/generations；出站模型名 gpt-image-1、Bearer 鉴权各自账户 key。
	hitsA, hitsB := 0, 0
	for _, call := range f.mock.callsByKey(keyA) {
		if call.Method == http.MethodPost && call.Path == "/v1/images/generations" {
			hitsA++
		}
	}
	for _, call := range f.mock.callsByKey(keyB) {
		if call.Method != http.MethodPost || call.Path != "/v1/images/generations" {
			t.Fatalf("T5 images shared lane B 未预期出站: %#v", call)
		}
		if call.Model != "gpt-image-1" {
			t.Fatalf("T5 images shared lane B 出站 model=%q, want gpt-image-1", call.Model)
		}
		if call.AuthHeader != "Bearer "+keyB {
			t.Fatalf("T5 images shared lane B Authorization=%q, want Bearer <B key>", call.AuthHeader)
		}
		hitsB++
	}
	if hitsA < 1 || hitsB < 1 {
		t.Fatalf("T5 images 共享 lane 共同候选证据不足：A 命中 %d 次、B 命中 %d 次（want 各 >=1）", hitsA, hitsB)
	}
}
