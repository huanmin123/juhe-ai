# Responses 协议防火墙与历史会话自愈设计

## 1. 背景与目标

Codex Responses 客户端会把历史 `input`、工具调用和流式事件继续发送给上游。上游或中转层如果把 item 类型映射错，可能出现 `fc_*` 被用于要求 `ctc_*`、`rs_*` 在 `store=false` 时无法查找、重复 item ID、非法事件阶段以及错误模型污染响应等问题。网关需要在不破坏合法 Responses 语义的前提下，将这些问题归类、修复或隔离。

本设计的目标是：

- 在请求进入自定义上游前，按当前上游能力和目标协议生成正确的 ID，清理不能安全转发的历史 item。
- 在响应边界识别上游是否把请求映射到了错误模型、错误协议或错误 item 类型。
- 默认以安全修复为主，保留原有客户端重试和账户调度语义；严格拦截是显式策略，不把不确定证据当成账户故障。
- 将检查结果写入使用记录、审计和账户运行态，区分 `clean`、`repaired`、`observed_unknown`、`blocked`、`late_violation`。
- 保持健康账户的低开销路径：关闭模式不创建 guard，已完成且未变化的上下文不重复深度扫描。

## 2. 适用边界

### 2.1 只解释 Codex Responses

防火墙只在客户端画像为 Codex、目标端点属于 Responses、且上游边界明确标记为 `raw_upstream` 或 `gateway_bridge` 时启用。通用 OpenAI、Anthropic、Gemini 和未知客户端默认保持不透明，不能因为字段长得相似而套用 Codex 规则。

### 2.2 provenance 必须显式传递

响应 provenance 由产生响应的边界写入 marker：

- `raw_upstream`：上游原始 Responses 响应。
- `gateway_bridge`：网关协议桥接器生成的 Responses 响应。

guard 不根据账户类型、URL、响应字段或缺少 marker 推断 provenance。经过任意转换器后，marker 必须由转换器显式替换，禁止“没有 marker 就当作 raw”。

### 2.3 不处理无法证明的未知情况

解析超限、事件截断、未知事件、无法关联 response 资源或转换链丢失 marker 时，结果为 `observed_unknown`，只记录有界诊断，不触发账户切换或永久隔离。只有确定违反当前协议且证据来源明确时，才进入 `repairable` 或 `blocked`。

## 3. 两层运行策略

AI 账户配置最终提供两个独立开关：

1. 账户凭据 `codex_responses_safe_repair_enabled`（前端字段 `codexResponsesSafeRepairEnabled`），默认开启。
   - 对确定的 R0 ID/字段错误执行确定性修复。
   - 不改变上游选择，不把账户标成故障。
   - 使用记录标记为黄色 `repaired`，并记录规则 ID、provenance 和是否在语义提交前完成。
2. 账户凭据 `codex_responses_strict_intercept_enabled`（前端字段 `codexResponsesStrictInterceptEnabled`），默认关闭。
   - 命中确定的 R2 违规或错误模型证据时，丢弃当前响应，切换到下一账户或返回受控错误。
   - 进入账户异常处理，保存异常原因和证据摘要。
   - 与 safe repair 互斥：严格拦截启用时，不能先把同一响应修复后继续交付；不确定结果仍不得切号。

全局运行模式用于灰度和紧急回退：`off`、`shadow`、`safe_repair`、`strict_intercept`。Node 网关按“全局 off 优先，其余取更严格者”解析；旧账户缺少字段时按安全修复开启、严格拦截关闭处理。严格拦截命中后通过已有服务端 retry/exclude 机制换号，并进入账户运行态抑制；未知结果不会换号。

## 4. ID 与历史 item 自愈

### 4.1 源头生成

ID 工厂接收目标 item contract，而不是先生成一种 ID 再修改前缀。每个 contract 固定：允许的事件阶段、ID 前缀、可修复字段、是否允许 ID、`call_id` 约束和大小上限。目标类型为 `custom_tool_call` 时直接生成 `ctc_*`，目标类型为 `reasoning` 时直接生成对应允许前缀。

### 4.2 请求历史

历史 sanitizer 采用 copy-on-write：

- 合法 item 和未触碰的根对象保持引用复用。
- 错误 ID 只在明确 item contract 能推导目标前缀时替换。
- 无法确定 item 类型、重复 ID、禁止携带 ID、`store=false` 下不可恢复的远程 `rs_*` 等内容，移入受控的历史降级路径，并记录原因。
- 任何替换都建立 `upstreamItemId -> clientItemId` 映射，后续流事件、done 事件和 completed 输出必须复用同一个客户端 ID。

历史清理不改变用户可理解的对话文本、工具参数和顺序；它只改变协议身份字段或删除无法安全解释的历史 item。删除历史 item 会记录计数和规则，不静默发生。

### 4.3 流式响应

流状态保存 response scope、output index、item type、upstream ID、client ID、call ID 和阶段。收到 `response.completed` 后进入终态，后续事件一律 `event_after_response_completed`。安全修复只允许在语义提交前为新 identity 生成 ID；语义提交后只能继续已有映射，不能为新错误 ID 创造新客户端身份。

SSE 修复必须改写实际下游事件，包括 `item.id`、`item_id` 和 `response.output[].id`，而不是只在审计中报告修复。JSON 修复必须以修复后的 body 写出并更新 `Content-Length`。

## 5. 响应污染识别与拦截

污染识别分三层：

- 协议层：ID 前缀、禁止字段、事件阶段、重复 identity、完成态后事件。
- 语义层：上游返回了错误协议/错误模型的确定性指纹，或命中显式响应检查策略。
- 传输层：gzip 解码失败、截断和 parser coverage gap。传输层不自动推断账户污染，除非有独立确定证据。

命中后统一产生 `CodexResponsesGuardResult`，包含 revision、provenance、mode、outcome、有限 issue 列表、修复规则、是否可重试、提交边界快照和审计字段。诊断不得包含完整请求体、凭据、长 ID 或敏感 URL；长值使用 token 化摘要并设数量/字节上限。

## 6. 协议转换自审

所有 Responses 转换器必须声明输入协议、输出协议、客户端画像、目标 item contract 和 provenance checkpoint。转换器测试至少覆盖：

- OpenAI Responses 原生透传。
- Responses 到 Chat、Chat 到 Responses、Anthropic/Gemini bridge 的 item 映射。
- `response.created` 缺失、standalone done、无 `output_index` 的合法事件。
- `response.compaction` 与 native `{ output }` compact envelope。
- `store=false` 历史不可持久化时的降级行为。

转换器不得把模型名称映射、协议桥接或 provider 选择隐藏在 ID 修复器中；guard 只验证转换器声明的结果。

## 7. 性能策略

检查窗口只在需要时建立：

- `off`：不构造 guard、不解析 Responses body。
- `shadow`：单次结构扫描，诊断限量，零修改。
- `safe_repair`：只对命中项 copy-on-write，clean 路径复用对象。
- SSE：按事件增量更新状态，生命周期结束立即 `dispose`；身份表、diagnostics、repair map 均有数量、字节和 token 上限。

同一请求内使用 inspection fingerprint（协议、body hash/事件序号、转换 checkpoint）避免重复深扫。已由上游转换器确认并带有同版本 guard stamp 的片段可跳过重复检查，但任何跨 checkpoint、跨账户重试或 body 改写都必须重新检查。性能门禁要求 off 为零 guard，JSON/SSE 时间复杂度近似 O(n)，以及固定上限下的内存增长证据。

## 8. 可观测性和账户状态

使用记录状态颜色：

- 绿色：`clean`，没有协议异常。
- 黄色：`repaired` 或 `observed_unknown`，请求可用但存在修复或检查覆盖不足。
- 红色：`blocked`、`late_violation` 或严格拦截导致切号。

记录字段至少包括：guard revision、mode、provenance、outcome、repair rule IDs、diagnostic codes、retryable、account switch、strict intercept、diagnostic count 和 omitted count。Node 使用记录在 `responseSnapshot.codexResponsesGuard` 中保存有界摘要；成功但 `repaired_safe`、`repaired_bridge` 或 `observed_unknown` 显示黄色，失败/严格拦截保持红色。账户异常处理只接受确定 raw/bridge 证据；`unknown` 和纯 gateway bridge 观察不写 R2 账户故障。

## 9. 失败安全原则

- 不在语义提交后重写已经发送给客户端的内容。
- 不把无法解析等同于上游污染。
- 不因单个错误 ID 无限生成新 ID；修复预算和序号有上限。
- guard 异常必须显式记录并遵循当前模式：shadow 透传，safe repair 回退原响应，strict intercept 只在确定违规时阻断。
- Redis/PostgreSQL/SQLite 只用于账户策略和审计事实，不把完整响应体复制到高频运行态。

## 10. 验收基线

- Codex 官方源码/契约对齐：item prefix、event stage、completed 终态和 standalone event。
- JSON/SSE 单元回归、provenance 双检查点回归、真实 mock gateway E2E。
- Node 账户开关和黄色 usage tag 的 API/UI/存储契约回归。
- SQLite、PostgreSQL、Redis 三种测试边界；测试数据库允许按现行 schema 重建，不修改生产数据。
- 真实模型测试只从本机密码文件读取，凭据不进入日志、提交和文档。
