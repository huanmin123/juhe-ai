package mockupstream

// OpenAI images face for the AI 账户能力域拆分 T5 验收（计划
// 计划-20261011T141004000Z-AI账户能力域拆分「行为级验收」T5）：POST
// /v1/images/generations 的确定性 Mock。2026-11-11 图像同步批扩展为多形态：
// gpt 中转（url/b64/缺字段/别家模型）、xAI（url + mime_type + cost ticks）、
// 火山（created/model/url/size/output_format + generated_images）；qwen 原生
// multimodal-generation 端点由 serveQwenImage 承载（独立路径）。2026-11-12
// T216 追加 POST /v1/images/edits 场景（gpt/xai 编辑共用该路径；火山编辑由
// 网关改写为 /api/v3/images/generations 复用同一 200 判定）。
//
// 响应为 OpenAI images 兼容形态的 canned 确定性载荷；网关对 gpt 是同步 JSON
// 直通（`stream`/`partial_images` 请求侧删除并回显，不做 SSE），对
// xai/volcengine 只做请求归一，对 qwen 做响应转换。scenario 复用
// X-Mock-Scenario / ?scenario= 机制；未知场景默认 OK（镜像 chat 默认哲学）。

import "net/http"

// ScenarioMediaImagesOK 是 images 创建面的 OK 场景（媒体场景命名家族
// media_<face>_<behavior>，同 media_tts_ok 先例）。
const (
	ScenarioMediaImagesOK                 Scenario = "media_images_ok"
	ScenarioMediaImagesB64OK              Scenario = "media_images_b64_ok"
	ScenarioMediaImagesNoImage            Scenario = "media_images_no_image"
	ScenarioMediaImagesRelayModelMismatch Scenario = "media_images_relay_model_mismatch"
	// ScenarioMediaImagesEditOK 是编辑面的 OK 场景（契约 §3.2.4 编辑场景）：
	// OpenAI/gpt 形态 created + data[].url，供 gpt/xai 编辑端点与
	// volcengine/qwen 复用同一 200 判定。
	ScenarioMediaImagesEditOK           Scenario = "media_images_edit_ok"
	ScenarioMediaImagesEditNoImage      Scenario = "media_images_edit_no_image"
	ScenarioMediaXaiImageOK             Scenario = "media_xai_image_ok"
	ScenarioMediaXaiImageNoImage        Scenario = "media_xai_image_no_image"
	ScenarioMediaVolcengineImageOK      Scenario = "media_volcengine_image_ok"
	ScenarioMediaVolcengineImageNoImage Scenario = "media_volcengine_image_no_image"
	ScenarioMediaQwenImageOK            Scenario = "media_qwen_image_ok"
	ScenarioMediaQwenImageV3OK          Scenario = "media_qwen_image_v3_ok"
	ScenarioMediaQwenImageNoImage       Scenario = "media_qwen_image_no_image"
)

// imagesGenerationsPath 是 images 创建端点（OpenAI POST /v1/images/generations）。
const imagesGenerationsPath = "/v1/images/generations"

// imagesEditsPath 是 images 编辑端点（OpenAI POST /v1/images/edits；gpt 与
// xai 共用该路径，按账户主机区分）。
const imagesEditsPath = "/v1/images/edits"

// qwenImageGenerationPath 是 Qwen-Image 原生同步端点（契约 §10.3）。
const qwenImageGenerationPath = "/api/v1/services/aigc/multimodal-generation/generation"

// imagesEditOKBody 是编辑面的确定性 OK 载荷（契约 §3.2.4，url 形态）。
const imagesEditOKBody = `{"created":1700000000,"data":[{"url":"https://mock-images.example/generated/edit-0001.png"}]}`

// imagesGenerationsOKBody 是确定性 OK 载荷（url 形态 OpenAI images 响应）。
const imagesGenerationsOKBody = `{"created":1700000000,"data":[{"url":"https://mock-images.example/generated/mock-0001.png"}]}`

// serveImages implements POST /v1/images/generations 与
// POST /v1/images/edits：按场景返回 gpt/xai/volcengine 形态；未知场景回落
// url OK（编辑面另有 media_images_edit_ok / _no_image 场景）。
func (m *Server) serveImages(w http.ResponseWriter, _ int, scenario Scenario) {
	switch scenario {
	case ScenarioMediaImagesB64OK:
		writeJSONStatus(w, http.StatusOK, `{"created":1700000000,"data":[{"b64_json":"bW9jay1iNjQ="}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30,"output_tokens_details":{"image_tokens":20}}}`)
	case ScenarioMediaImagesNoImage:
		writeJSONStatus(w, http.StatusOK, `{"created":1700000000,"data":[]}`)
	case ScenarioMediaImagesEditOK:
		writeJSONStatus(w, http.StatusOK, imagesEditOKBody)
	case ScenarioMediaImagesEditNoImage:
		writeJSONStatus(w, http.StatusOK, `{"created":1700000000,"data":[]}`)
	case ScenarioMediaImagesRelayModelMismatch:
		writeJSONStatus(w, http.StatusOK, `{"created":1700000000,"model":"grok-imagine-image-2.0","data":[{"url":"https://mock-images.example/generated/relay-0001.png"}]}`)
	case ScenarioMediaXaiImageOK:
		writeJSONStatus(w, http.StatusOK, `{"data":[{"url":"https://mock-images.example/generated/xai-0001.png","mime_type":"image/png"}],"usage":{"cost_in_usd_ticks":4000000}}`)
	case ScenarioMediaXaiImageNoImage:
		writeJSONStatus(w, http.StatusOK, `{"data":[]}`)
	case ScenarioMediaVolcengineImageOK:
		writeJSONStatus(w, http.StatusOK, `{"created":1700000000,"model":"doubao-seedream-5-0-flash-260915","data":[{"url":"https://mock-images.example/generated/seedream-0001.png","size":"1024x1024"}],"size":"1024x1024","output_format":"jpeg","background":"opaque","usage":{"input_images":0,"generated_images":1,"output_tokens":4096,"total_tokens":4096}}`)
	case ScenarioMediaVolcengineImageNoImage:
		writeJSONStatus(w, http.StatusOK, `{"created":1700000000,"model":"doubao-seedream-5-0-flash-260915","data":[],"usage":{"input_images":0,"generated_images":0,"output_tokens":0,"total_tokens":0}}`)
	default:
		writeJSONStatus(w, http.StatusOK, imagesGenerationsOKBody)
	}
}

// serveQwenImage implements POST /api/v1/services/aigc/multimodal-generation/
// generation：2.0/3.0 原生响应形态（content[].image + 不同 usage 字段）。
func (m *Server) serveQwenImage(w http.ResponseWriter, scenario Scenario) {
	switch scenario {
	case ScenarioMediaQwenImageNoImage:
		writeJSONStatus(w, http.StatusOK, `{"output":{"choices":[{"message":{"content":[]}}]},"usage":{"image_count":0},"request_id":"mock-qwen-img-none"}`)
	case ScenarioMediaQwenImageV3OK:
		writeJSONStatus(w, http.StatusOK, `{"output":{"choices":[{"message":{"content":[{"type":"image","image":"https://mock-images.example/generated/qwen-v3-0001.png"}]}}],"rewrite_status":"not_use"},"usage":{"output_image_count":1,"output_width":1024,"output_height":1024,"input_image_count":0,"input_image_type":"","output_image_type":"png"},"request_id":"mock-qwen-img-v3"}`)
	default:
		writeJSONStatus(w, http.StatusOK, `{"output":{"choices":[{"message":{"content":[{"image":"https://mock-images.example/generated/qwen-0001.png"}]}}]},"usage":{"image_count":1,"width":1024,"height":1024},"request_id":"mock-qwen-img-v2"}`)
	}
}
