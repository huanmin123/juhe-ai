package mockupstream

// OpenAI images face for the AI 账户能力域拆分 T5 验收（计划
// 计划-20261011T141004000Z-AI账户能力域拆分「行为级验收」T5）：POST
// /v1/images/generations 的确定性 Mock。此前引擎无 images 端点（未知路径
// 404），T5 的图像共享 lane 全链路验收按「没有可用 Mock 的新增路径先补依赖
// 边界」补齐本面。
//
// 响应为 OpenAI images 形态（url 形态，canned 确定性载荷）；网关对 openai
// 族账户的 images 请求是零改写透传，验收断言客户端收到的 URL 与鉴权/路径/
// 模型名出站记录（出站记录由引擎 seenReqs / 验收层 callsByKey 承载）。
// scenario 复用 X-Mock-Scenario / ?scenario= 机制：本面无专属错误场景，
// 通用 status/fault 场景（status_429 等）在 media 分发上游按既有组合语义
// 生效，供换账户候选验收注入；未知场景默认 OK（镜像 chat 默认哲学）。

import "net/http"

// ScenarioMediaImagesOK 是 images 创建面的 OK 场景（媒体场景命名家族
// media_<face>_<behavior>，同 media_tts_ok 先例）。
const ScenarioMediaImagesOK Scenario = "media_images_ok"

// imagesGenerationsPath 是 images 创建端点（OpenAI POST /v1/images/generations；
// edits/variations 变体不在本面，需要时按同构扩展）。
const imagesGenerationsPath = "/v1/images/generations"

// imagesGenerationsOKBody 是确定性 OK 载荷（url 形态 OpenAI images 响应）。
const imagesGenerationsOKBody = `{"created":1700000000,"data":[{"url":"https://mock-images.example/generated/mock-0001.png"}]}`

// serveImages implements POST /v1/images/generations：url 形态的 OpenAI
// images 响应，载荷对断言确定。
func (m *Server) serveImages(w http.ResponseWriter, _ int, _ Scenario) {
	writeJSONStatus(w, http.StatusOK, imagesGenerationsOKBody)
}
