# PLAN-0061 Responses 图像生成本地 Provider 桥接

## 基本信息

- 编号：PLAN-0061
- 状态：进行中
- 创建时间：2026-06-24
- 更新时间：2026-06-24
- 需求来源：用户对话
- 执行者：AI
- 关联模块：后端 / 网关 / Anthropic bridge / Responses / 图像生成 / 权限 / 审计 / 文档 / 验证

## 需求目标

- 背景：OpenAI 到 Anthropic Messages 桥接已覆盖基础四入口、function tools、web_search、file_search、thinking、文件输入和 compact；剩余显著缺口是 Responses `image_generation` hosted tool。Anthropic Messages 不会直接输出 OpenAI `image_generation_call`，如果继续只返回受控失败，Codex / OpenAI 客户端在图像生成任务上仍不能无感使用混合路由。
- 目标：新增可插拔的本地图像生成 provider。桥接层在 Responses 请求声明 `tools[].type=image_generation` 且 provider 可用时，先用文本模型生成或抽取图像提示词，再调用本地图像 provider，最后按 OpenAI Responses 形态返回 `image_generation_call`、`result`、`revised_prompt` 和必要的 SSE 事件。
- 交付物：长期设计边界、runtime 配置、provider 接口、Responses JSON / SSE 渲染、权限和审计策略、mock 回归、真实 provider 可选联调、凭据扫描和验证记录。

## 范围边界

### 本次包含

- [x] 建立独立计划，明确 `image_generation` 不能由 Anthropic Messages 字段转换伪造。
- [x] 更新高兼容能力矩阵和基础桥接设计，指向本计划的 provider 承接方式。
- [x] 新增 runtime 配置：图像 provider endpoint、认证、默认模型、超时和最大响应体；provider streaming 仍复用同一 endpoint。
- [x] 新增本地图像 provider executor，支持 OpenAI Images API 兼容 JSON 响应中的 `data[0].b64_json`，并在 provider 返回 `text/event-stream` 时解析 `image_generation.partial_image` / `image_generation.completed`。
- [x] Responses JSON 路径：配置 provider 后返回 OpenAI 形态 `image_generation_call`，使用 Anthropic revised prompt 调用 provider。
- [x] Responses SSE 路径：返回 `response.output_item.added`、`response.image_generation_call.completed`、`response.output_item.done`、`response.completed`；当请求显式带 `partial_images` 且 provider 返回 OpenAI Images SSE 时，额外透出 `response.image_generation_call.partial_image`。
- [x] 处理 `action=generate|edit|auto`、`size`、`quality`、`output_format`、`output_compression`、`partial_images`、`input_image_mask` 的支持矩阵；首批只支持无输入图片的 `generate|auto`，`edit` / mask / 历史图像复用已覆盖 guidance 回归。
- [x] 无 provider、provider 错误、moderation blocked、强制 edit 或 mask 时返回 OpenAI 形态 guidance / failed response，不请求不支持的 Anthropic image tool，不伪造成功图片。
- [~] 补 mock 回归和真实 provider 可选联调；mock 已覆盖首批路径，真实凭据只走临时环境变量；本轮真实 provider 探针已执行但当前账户不满足 OpenAI Images API 兼容 provider 接口。

### 本次不包含

- 不把 Anthropic 文本回答描述当作图片结果。
- 不实现完整图像编辑质量等价；首批只保证协议闭环和可诊断错误。
- 不默认引入外部对象存储；首批 `result` 使用 base64 响应。
- 不改变系统账户图像生成权限的现有前置校验语义。

## 关联文档

- 高兼容矩阵：`docs/functions/OpenAI到Anthropic高兼容能力矩阵.md`
- 基础桥接设计：`docs/functions/OpenAI到Anthropic协议桥接设计.md`
- 图像权限：`docs/functions/核心功能设计.md`
- 请求处理分层：`docs/functions/请求处理分层设计.md`
- 验证手册：`docs/develop/测试与验证说明.md`

## 方案概述

- 方案原则：`image_generation` 是 L3 本地模拟能力，只有 provider 可用时启用；无 provider 或参数超出首批支持范围时稳定失败。
- 数据变化：首批不新增数据库表；provider 凭据和默认参数来自 runtime 环境变量。
- 接口变化：不新增下游接口；继续承接 `/v1/responses` 中的 `tools[].type=image_generation`。
- 前端变化：首批不改前端；后续如需要 provider 管理 UI 再单独计划。
- 后端变化：新增图像 provider executor，Anthropic bridge 在请求转换和响应渲染阶段挂载本地图像结果。
- 数据处理策略：图像 base64 只在有明确响应体大小上限的 provider 响应中读取；审计和运行日志默认不写完整图片正文。

### Runtime 配置

provider 首批支持 OpenAI Images API 兼容 JSON；Responses SSE 请求显式带 `partial_images` 时，会向同一 provider endpoint 发送 `stream: true` 和 `partial_images`，如果 provider 返回 `text/event-stream`，则按 OpenAI Images SSE 解析 partial / completed 事件：

| 环境变量 | 作用 | 默认值 / 边界 |
| --- | --- | --- |
| `JUHE_AI_IMAGE_GENERATION_PROVIDER_ENDPOINT` | 图像生成 provider 的完整 `/v1/images/generations` 兼容地址；未配置时 `image_generation` 保持 L4 agent guidance | 未配置 |
| `JUHE_AI_IMAGE_GENERATION_PROVIDER_API_KEY` | provider Bearer token；本地 mock provider 可不配置 | 未配置 |
| `JUHE_AI_IMAGE_GENERATION_PROVIDER_MODEL` | provider 图像模型 | `gpt-image-2` |
| `JUHE_AI_IMAGE_GENERATION_PROVIDER_TIMEOUT_MS` | 图像 provider 请求超时 | 默认 120000，范围 1000-300000 |
| `JUHE_AI_IMAGE_GENERATION_PROVIDER_MAX_BODY_MB` | provider JSON 响应读取上限 | 默认 64，范围 1-256 |

首批只支持 `action=generate|auto` 且无输入图片 / mask 的生成路径。`action=edit`、`input_image_mask`、历史 `image_generation_call` 复用和多图编辑在 provider 编辑端点落地前必须受控失败。provider 不支持 streaming 或返回普通 JSON 时，Responses SSE 仍返回 completed-only 图像事件，不伪造 partial。

## 执行拆解

- [x] 创建 PLAN-0061 并纳入计划索引。
- [x] 更新高兼容矩阵和桥接设计。
- [x] 实现 runtime 配置和 provider executor。
- [x] 实现 Responses JSON `image_generation_call` 渲染。
- [x] 实现 Responses SSE 图像完成事件渲染。
- [x] 补权限、错误、审计和大响应体边界。
- [~] 补 mock 回归和真实 provider 可选联调；mock 已覆盖 JSON、completed-only SSE、provider partial SSE、失败和边界，真实 provider 仍受账户 Images API 401 阻塞。
- [~] 更新验证记录和完成总结。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 命令类验证 | 后端类型检查 | `pnpm --dir backend typecheck` | 后端 TypeScript 类型检查通过 | 已通过 | 2026-06-24 已通过 |
| Mock 回归 | 无 provider guidance | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `image_generation` 不请求 Anthropic 且返回 OpenAI 形态 guidance | 已通过 | 新增 Responses `image_generation` required 无 provider 断言；脚本通过 |
| Mock 回归 | Responses JSON 图像生成 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 返回 `image_generation_call`、`result` 和 `revised_prompt` | 已通过 | mock provider 返回 `data[0].b64_json`；Anthropic 只生成 revised prompt，不接收 OpenAI image tool |
| Mock 回归 | Responses SSE 图像生成 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 返回 OpenAI Responses 图像生成事件，不透出 provider 私有格式 | 已通过 | 覆盖 `response.output_item.added`、`response.image_generation_call.completed`、`response.output_item.done`、`response.completed` |
| Mock 回归 | Responses SSE partial image | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 请求带 `partial_images` 时，provider SSE partial 转为 OpenAI Responses `response.image_generation_call.partial_image` | 已通过 | mock provider 收到 `stream=true` 和 `partial_images=2`，网关输出 partial、completed、done 和 completed response 事件 |
| Mock 回归 | Provider moderation blocked | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 返回 Responses `status=failed`，保留 `moderation_blocked`、`image_generation_user_error` 和 `moderation_details`，且不伪造图片 | 已通过 | mock provider 返回官方风格 400；桥接返回本地生成的 Responses failed 对象，响应检查不改写为 503 |
| Mock 回归 | edit / mask 受控 guidance | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 不请求 Anthropic、不调用图像 provider，并返回 OpenAI 形态 guidance | 已通过 | provider 已配置时，`action=edit` 和 `input_image_mask` 均返回 guidance |
| Mock 回归 | 历史图片复用受控 guidance | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 不请求 Anthropic、不调用图像 provider，并返回 OpenAI 形态 guidance | 已通过 | 历史 `image_generation_call` 作为输入上下文时返回 guidance |
| Mock 回归 | Provider 非 JSON / 缺失图片结果 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 返回 Responses `status=failed`，错误码为 provider invalid response，且不伪造图片 | 已通过 | mock provider 返回 `text/plain`，桥接返回 `openai_anthropic_bridge_image_generation_provider_invalid_response` |
| Mock 回归 | Provider 超大响应体 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 返回 Responses `status=failed`，错误码为 response too large，且不继续读取超限正文 | 已通过 | 临时收紧 provider body 上限后返回 `openai_anthropic_bridge_image_generation_provider_response_too_large` |
| Mock 回归 | Provider 请求超时 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | 返回 Responses `status=failed`，错误码为 provider timeout，且不伪造图片 | 已通过 | 临时收紧 provider timeout 后返回 `openai_anthropic_bridge_image_generation_provider_timeout` |
| Mock 回归 | 审计图像正文省略 | `pnpm --dir backend test:gateway-audit-payload-storage` | 流式 `partial_image_b64` 和非流式 `image_generation_call.result` 不写入审计正文 | 已通过 | 审计记录保留请求 payload 和 omission 元数据，响应图片正文不落 payload body |
| Mock 回归 | 图像生成权限 | `pnpm --dir backend test:api-key-image-permission` | 禁用图像生成时强制工具拒绝、auto 工具降级为文本、开启后同 Key 放行 | 已通过 | 默认禁用图片接口不上游；normal 路由后强制工具仍拦截；开启权限后立即放行 |
| 安全检查 | 凭据与图片正文扫描 | 固定真实 key 短前缀扫描 | 仓库无真实 key；docs / 运行模块无真实图片正文 | 已通过 | 未命中真实 key 短前缀；mock 图片正文只以测试 base64 fixture 存在 |
| 真实联调 | 真实 provider 可选探针 | 临时环境变量调用真实 `/v1/images/generations` | provider 可用时图像生成成功；不可用时记录原因 | 已执行未通过 | 当前真实账户调用 `https://vsllm.com/v1/images/generations` 返回 401 `invalid_api_key`，未返回 `data[0].b64_json`；该账户暂不能作为 OpenAI Images API 兼容 provider |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-24 | 进行中 | AI | 已按官方图像生成文档确认 Responses `image_generation` 输出使用 `image_generation_call`，结果字段为 base64 `result`，streaming 可有 `response.image_generation_call.partial_image` 和 completed 事件；本计划先定义本地 provider 承接边界。 |
| 2026-06-24 | 进行中 | AI | 已把本计划链接回高兼容能力矩阵和基础桥接设计；先补“无 provider guidance 且不命中 Anthropic”mock 覆盖，provider 成功路径继续按本计划后续实现。 |
| 2026-06-24 | 进行中 | AI | 已执行 `pnpm --dir backend test:openai-anthropic-bridge-mock` 与 `pnpm --dir backend typecheck`，并扫描真实 key 前缀；无 provider 失败路径通过，真实账户尚未联调。 |
| 2026-06-24 | 进行中 | AI | 已实现本地图像 provider 首批 generate 路径：runtime 配置、OpenAI Images JSON executor、Responses JSON / SSE `image_generation_call` 渲染和 mock provider 成功回归；真实账户尚未联调。 |
| 2026-06-24 | 进行中 | AI | 已补 provider `moderation_blocked` mock：保留 `image_generation_user_error`、`moderation_blocked` 和 `moderation_details`，返回 OpenAI Responses `status=failed`，不返回图片 output。 |
| 2026-06-24 | 进行中 | AI | 已补 `action=edit` 和 `input_image_mask` guidance 回归；即使 provider 已配置，首批不支持的编辑 / mask 请求也不命中 Anthropic 或图像 provider。 |
| 2026-06-24 | 进行中 | AI | 已补历史 `image_generation_call` 复用 guidance，以及 provider 非 JSON、超大响应体、请求超时三类 mock；均返回 OpenAI Responses `status=failed` 或 guidance，不伪造图片。 |
| 2026-06-24 | 进行中 | AI | 已补图像审计正文省略：现有图像流 `partial_image_b64` 省略继续通过，新增非流式 `image_generation_call.result` 省略回归，客户端仍收到完整响应但审计不保存图片 base64。 |
| 2026-06-24 | 进行中 | AI | 已用用户提供真实账户做 OpenAI Images API 兼容 provider 低成本探针；`/v1/images/generations` 返回 401 `invalid_api_key`，因此未执行网关 image_generation 真实 provider E2E。 |
| 2026-06-24 | 进行中 | AI | 已补 provider partial image streaming：Responses SSE 请求带 `partial_images` 时，图像 provider 收到 `stream=true` / `partial_images`，provider `image_generation.partial_image` 转为 Responses `response.image_generation_call.partial_image`；mock 已通过，真实 provider partial 仍需可用 Images API key 后复测。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-24 | `image_generation` 采用本地 provider，不直转 Anthropic Messages | Anthropic Messages 不产生 OpenAI 图像结果；文本伪装会破坏客户端语义 | provider 未配置时继续 L4 agent guidance；配置后由网关渲染 OpenAI Responses 图像 item |
| 2026-06-24 | provider 兼容 OpenAI Images JSON 和 SSE partial 响应 | 复用现有 OpenAI 图像生态，便于接入 gpt-image 或第三方兼容图像服务；partial streaming 必须来自 provider 真实 SSE，不由桥接层伪造 | 后续仍可扩展编辑、多图和对象存储；真实 partial 需可用 provider key 复测 |
| 2026-06-24 | 图像 provider 失败用本地生成 Responses failed 对象承接 | provider 审核失败是客户端可理解的图像生成结果状态，不应被响应检查改写成网关 503 | 仅对带内部标记的本地图像 provider failed response 跳过非流式响应检查；结构化输出 schema mismatch 仍维持现有 503 行为 |

## 验收标准

- [x] 无 provider 时，强制 `image_generation` 返回 OpenAI 形态 guidance，且不请求 Anthropic。
- [x] 配置 provider 后，Responses JSON 返回合法 `image_generation_call`，客户端可从 `result` 解出图片。
- [x] 配置 provider 后，Responses SSE 返回 OpenAI Responses 图像生成事件，不透出 Anthropic 或 provider 私有事件。
- [x] provider 支持 OpenAI Images SSE 时，Responses SSE 可透出 `response.image_generation_call.partial_image`，且 partial image 正文继续走审计省略。
- [x] provider moderation blocked 时返回 Responses `status=failed`，保留稳定错误码和审核详情，不伪造图片。
- [x] `action=edit`、`input_image_mask` 和历史 `image_generation_call` 复用首批不支持时返回 guidance，不请求 Anthropic 或图像 provider。
- [x] provider 非 JSON / 缺失结果、超大响应体、请求超时时返回 Responses failed object，且不伪造图片。
- [x] 权限、审计和大响应体边界不泄露图片正文或真实 provider 凭据；大响应体上限、审计图像正文省略和凭据扫描已覆盖。
- [~] mock 回归、类型检查和真实 provider 可选联调完成或明确未验证原因；mock 与 typecheck 已通过，真实 provider 探针已执行但当前账户不满足 OpenAI Images API 兼容 provider 接口。

## 验证记录

- 类型检查：`pnpm --dir backend typecheck` 已通过。
- Mock 回归：`pnpm --dir backend test:openai-anthropic-bridge-mock` 已通过，覆盖 `image_generation` 无 provider guidance、JSON 成功路径、completed-only SSE 成功路径、provider partial image SSE 成功路径、provider `moderation_blocked` failed response、`action=edit` guidance、`input_image_mask` guidance、历史 `image_generation_call` 复用 guidance、provider 非 JSON / invalid response、provider 超大响应体和 provider timeout。
- 审计回归：`pnpm --dir backend test:gateway-audit-payload-storage` 已通过，覆盖图像流 `partial_image_b64` 和非流式 `image_generation_call.result` 审计正文省略。
- 权限回归：`pnpm --dir backend test:api-key-image-permission` 已通过，覆盖图像权限默认禁用、auto `image_generation` 工具降级、强制图像工具拒绝和开启后放行。
- 真实联调：已执行 OpenAI Images API 兼容 provider 低成本探针；当前真实账户调用 `https://vsllm.com/v1/images/generations` 返回 401 `invalid_api_key`，未返回 `data[0].b64_json`，因此没有继续跑网关 image_generation 真实 provider E2E。
- 凭据检查：已用用户提供真实 key 的短前缀扫描 `backend`、`frontend`、`docs`、`package.json`、`pnpm-lock.yaml`，未命中；计划文档不记录真实前缀。
- 未验证项：需要可用的 OpenAI Images API 兼容 provider endpoint / key 后复测真实 provider E2E，包括真实 partial image streaming；当前账户 Images API 返回 401，不能作为结论。

## 风险与注意事项

- 图像生成成本、延迟和安全审核与文本请求不同，必须保留系统账户图像生成权限前置校验。
- provider 返回的 base64 可能很大，必须使用响应体上限和审计正文省略策略。
- 多轮图像编辑需要保存或引用历史图像结果，首批只保证当前响应协议闭环。
- 发布异常处理：如 provider 异常，关闭图像 provider 配置后恢复到当前 L4 agent guidance，不影响基础文本、web_search、file_search、thinking 和 compact 桥接。

## 完成总结

- 完成时间：待补充
- 实际完成内容：待补充
- 主要改动位置：待补充
- 验证结果：待补充
- 后续建议：待补充
