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
- [ ] 更新高兼容能力矩阵和基础桥接设计，指向本计划的 provider 承接方式。
- [ ] 新增 runtime 配置：图像 provider endpoint、认证、默认模型、超时、最大响应体和是否允许 streaming partial image。
- [ ] 新增本地图像 provider executor，优先支持 OpenAI Images API 兼容 JSON 响应中的 `data[0].b64_json`。
- [ ] Responses JSON 路径：配置 provider 后返回 OpenAI 形态 `image_generation_call`，并保留 Anthropic 文本回答或 revised prompt。
- [ ] Responses SSE 路径：至少返回 `response.output_item.added`、`response.image_generation_call.completed`、`response.completed`；provider 支持 partial 时再转发 partial image 事件。
- [ ] 处理 `action=generate|edit|auto`、`size`、`quality`、`output_format`、`output_compression`、`partial_images`、`input_image_mask` 的支持矩阵。
- [ ] 无 provider、provider 错误、moderation blocked、强制 edit 但缺少图片上下文时返回 OpenAI 形态错误，不请求 Anthropic 或不伪造成功。
- [ ] 补 mock 回归和真实 provider 可选联调；真实凭据只走临时环境变量。

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

## 执行拆解

- [x] 创建 PLAN-0061 并纳入计划索引。
- [ ] 更新高兼容矩阵和桥接设计。
- [ ] 实现 runtime 配置和 provider executor。
- [ ] 实现 Responses JSON `image_generation_call` 渲染。
- [ ] 实现 Responses SSE 图像完成事件渲染。
- [ ] 补权限、错误、审计和大响应体边界。
- [ ] 补 mock 回归和真实 provider 可选联调。
- [ ] 更新验证记录和完成总结。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 命令类验证 | 后端类型检查 | `pnpm --dir backend typecheck` | 后端 TypeScript 类型检查通过 | 未执行 | 待补充 |
| Mock 回归 | 无 provider 受控失败 | `pnpm --dir backend test:openai-anthropic-bridge-mock` | `image_generation` 不请求 Anthropic 且返回 OpenAI 形态错误 | 未执行 | 待补充 |
| Mock 回归 | Responses JSON 图像生成 | 扩展 bridge mock 脚本 | 返回 `image_generation_call`、`result` 和 `revised_prompt` | 未执行 | 待补充 |
| Mock 回归 | Responses SSE 图像生成 | 扩展 bridge mock 脚本 | 返回 OpenAI Responses 图像生成事件，不透出 provider 私有格式 | 未执行 | 待补充 |
| 安全检查 | 凭据与图片正文扫描 | `rg` 固定 key 前缀和测试图片正文特征 | 仓库无真实 key；docs / 运行模块无真实图片正文 | 未执行 | 待补充 |
| 真实联调 | 真实 provider 可选探针 | 临时环境变量运行真实脚本 | provider 可用时图像生成成功；不可用时记录原因 | 未执行 | 待补充 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-24 | 进行中 | AI | 已按官方图像生成文档确认 Responses `image_generation` 输出使用 `image_generation_call`，结果字段为 base64 `result`，streaming 可有 `response.image_generation_call.partial_image` 和 completed 事件；本计划先定义本地 provider 承接边界。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-24 | `image_generation` 采用本地 provider，不直转 Anthropic Messages | Anthropic Messages 不产生 OpenAI 图像结果；文本伪装会破坏客户端语义 | provider 未配置时继续 L4 受控失败；配置后由网关渲染 OpenAI Responses 图像 item |
| 2026-06-24 | 首批 provider 兼容 OpenAI Images JSON 响应 | 复用现有 OpenAI 图像生态，便于接入 gpt-image 或第三方兼容图像服务 | 后续可扩展到 streaming partial、编辑、多图和对象存储 |

## 验收标准

- [ ] 无 provider 时，强制 `image_generation` 返回 OpenAI 形态本地错误，且不请求 Anthropic。
- [ ] 配置 provider 后，Responses JSON 返回合法 `image_generation_call`，客户端可从 `result` 解出图片。
- [ ] 配置 provider 后，Responses SSE 返回 OpenAI Responses 图像生成事件，不透出 Anthropic 或 provider 私有事件。
- [ ] 权限、审计和大响应体边界不泄露图片正文或真实 provider 凭据。
- [ ] mock 回归、类型检查和真实 provider 可选联调完成或明确未验证原因。

## 验证记录

- 类型检查：未执行。
- Mock 回归：未执行。
- 真实联调：未执行。
- 凭据检查：未执行。
- 未验证项：待实现后补充。

## 风险与注意事项

- 图像生成成本、延迟和安全审核与文本请求不同，必须保留系统账户图像生成权限前置校验。
- provider 返回的 base64 可能很大，必须使用响应体上限和审计正文省略策略。
- 多轮图像编辑需要保存或引用历史图像结果，首批只保证当前响应协议闭环。
- 发布异常处理：如 provider 异常，关闭图像 provider 配置后恢复到当前 L4 受控失败，不影响基础文本、web_search、file_search、thinking 和 compact 桥接。

## 完成总结

- 完成时间：待补充
- 实际完成内容：待补充
- 主要改动位置：待补充
- 验证结果：待补充
- 后续建议：待补充
