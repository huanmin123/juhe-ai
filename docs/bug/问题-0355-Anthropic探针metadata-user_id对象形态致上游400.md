# BUG-0355 Anthropic 探针 metadata.user_id 发成对象致上游 400

## 基本信息

- 编号：BUG-0355
- 状态：已修复待发布（2026-11-05）
- 严重程度：P1
- 发现时间：2026-11-05
- 发现方式：用户反馈（2chat 中转手动测试失败）+ 上游请求体二分实测
- 模块：后端 / 账户探针 / Anthropic Messages 请求构造（`shared/platform/accounttest/accountprobe`）
- 关联计划：无
- 关联 bug：BUG-0201（同一函数的探针轻量契约）、BUG-0352（同期人工测试目录解析缺陷）
- 责任人：网关维护者

## 问题概述

- 现象：Anthropic API Key 账户（中转站 `direct.2chat.cc`）生产手动测试失败，`errorCode=invalid_protocol_success_response`、`statusCode=200`、`responseText` 为站点 SPA 的 `text/html` 首页；改对 Base URL 后错误变为上游 `400 Failed to parse request body` / `upstream_error`。同一 Key、同一模型用 curl 标准形态请求 `POST /v1/messages` 正常返回 Messages JSON。
- 期望：探针 body 符合 Anthropic Messages 协议，上游 200 并带回完成证据。
- 实际：探针把 `metadata.user_id` 序列化为 JSON 对象（`{"device_id":..,"account_uuid":"","session_id":..}`），而 Anthropic 协议该字段类型为 **string**；严格解析的 Anthropic 兼容上游（sub2api 系中转）直接以 `400 Failed to parse request body` 拒绝。
- 影响范围：所有 Anthropic Messages 探针（`messages_json` / `messages_sse`），即手动测试与自动健康检查两条路径，覆盖官方与中转 Anthropic 账户。真实 Claude Code 客户端流量是原样透传、不经该构造点，不受影响。

## 复现步骤

1. `POST https://direct.2chat.cc/v1/messages`，头 `x-api-key: <key>` + `anthropic-version: 2023-06-01`。
2. body 使用探针同形状（`system` 数组、`tools: []`、`messages` 带 `cache_control`、`max_tokens: 1024`），`metadata.user_id` 为对象 → `400 {"error":{"message":"Failed to parse request body","type":"invalid_request_error"}}`。
3. 其余字段逐字不变，仅把 `metadata.user_id` 改为 JSON 字符串（`"{\"device_id\":..,\"account_uuid\":\"\",\"session_id\":..}"`）→ `200`，返回标准 `type=message` 响应。
4. 删除 `metadata` 字段 → 同样 `200`。

## 环境信息

- 分支 / 版本：当前工作区（Go gateway，未发布）
- 上游：`https://direct.2chat.cc/v1`（sub2api 系中转），模型 `claude-opus-5-5`
- 是否稳定复现：是（对象形态 100% 400，字符串形态连续多次 200）

## 根因分析

- `backend-go/shared/platform/accounttest/accountprobe/protocol.go` 的 `anthropicMessagesBodyFields` 先构造 `metadataUser := map[string]any{...}`，再把它直接作为 `metadata.user_id` 的值，最终出站 JSON 为对象。
- 该形状由提交 `982ed8f2f`（2026-10-12，M1 读通道）引入；单测 `w4_envelope_final_wiring_test.go` 以 `metadata["user_id"].(map[string]any)` 断言对象形态，把错误契约钉死，因此既有测试全绿未暴露。
- Anthropic 协议 `metadata.user_id` 为 string（最长 256 字符），Claude Code 真实请求把设备/会话身份 JSON 序列化后作为字符串提交；严格实现（如 sub2api 的请求解析）对对象形态直接 400。

## 修复

- `protocol.go`：三个身份字段改由 `json.Marshal` 序列化为字符串后写入 `metadata.user_id`；函数签名改为返回 `([]upstreamenvelope.BodyField, error)`，序列化失败显式报错，不静默降级。
- `probe.go`：`buildProbeSpec` 的 anthropic 分支处理新增 error。
- `w4_envelope_final_wiring_test.go`：断言 `metadata.user_id` 必须是 string，反序列化后校验 `session_id` 与 `x-claude-code-session-id` 头同源。
- 文档：`docs/functions/ClaudeCode客户端画像兼容设计.md` 探针契约补充「JSON 序列化字符串，对象形态会被严格解析的上游 400 拒绝」。

## 验证记录

- 上游二分（2026-11-05，2chat）：对象 400 / 字符串 200 / 无 metadata 200；字符串形态连续多次 `200` 且返回 `probe-ok`。
- 本机：`go test ./accounttest/accountprobe/ -count=1` 全绿；`gofmt -l` 无输出；`go vet` 通过；`go build ./...`（platform）与 gateway `go build ./...` 通过。
- 本地 dev 真机（2026-11-05，重启 gateway 加载新代码；2chat `https://direct.2chat.cc/v1` + `claude-opus-5-5`）两种请求形态均通过：
  - `messages_json`：测试通过，4.2s，trace `trace-3cbde464-412d-42a3-ba5a-0d23cc7f3b09`；
  - `messages_sse`：测试通过，2.6s，trace `trace-fc2f9158-7517-4861-9cbe-5f0d6637720c`。
- 未覆盖：Anthropic OAuth 形态（无可用 OAuth token，无法真机验证；该形态与 API Key 共用同一 body 构造，仅认证头为 `AuthOAuth`）、`message_token_counting`（探针无该形态）。
- 待发布后生产验证：任一 Anthropic API Key 账户手动测试通过（不再出现 `Failed to parse request body` 或 `invalid_protocol_success_response`）。

## 附带发现（不纳入本修复）

- Base URL 缺少版本根 `/v1` 时，探针请求发往 `POST /messages`，被中转站 nginx 的 SPA fallback 以 `200 text/html` 返回首页，最终误判为 `invalid_protocol_success_response`。Base URL 语义为版本根（设计文档 §1.1 R1/R3），属用户配置问题；2026-11-05 用户裁决不处理，不加自动补版本根或额外提示。
