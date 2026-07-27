# BUG-0128 Codex 压缩被网关多层时限误杀

## 基本信息

- 编号：BUG-0128
- 状态：已修复（定向回归通过，待完整隔离预演）
- 严重程度：P1
- 发现时间：2026-07-27
- 发现方式：用户反馈与生产审计排查
- 模块：Node / 网关 / Codex Responses / 上下文压缩 / 超时 / 跨协议调度
- 关联计划：PLAN-20260726T172021035Z
- 关联 bug：BUG-0032、BUG-0045、BUG-0060
- 责任人：AI

## 问题概述

- 现象：Codex 上下文压缩在上游仍正常计算时被本地首响应、首字、未提交 attempt 生命周期或整请求墙钟提前终止，随后连续切换账号并放大上游错误。
- 期望：`POST /responses/compact` 和带 `compaction_trigger` 的 Codex `POST /responses` 不受网关总墙钟、路由首字、响应头/首响应、首字和未提交 attempt 总时限约束。
- 实际：压缩请求沿用了普通文本 lane 的多层计时器；Chat-only compact 改写成内部 `/v1/chat/completions` 后还会丢失原始 compact 路径，重新落回普通超时策略。
- 影响范围：Node 网关的原生 Responses、OpenAI OAuth、OpenAI-compatible、Responses -> Chat 以及混合供应商分发路径。Go 不在本次发布范围。

## 根因分析

- 普通文本 lane 的 timeout profile、`GatewayRequestWallBudget`、普通路由首字截止和返回侧读取时限默认覆盖所有可重放文本请求，没有为 compact 建立请求级显式策略。
- Chat-only gateway summary compact 使用合成的 `/v1/chat/completions` 请求重新进入调度；下层只按改写后路径识别时无法知道它仍是压缩请求。
- 大 JSON 请求的旧兜底只扫描正文首尾各 64 KiB；`compaction_trigger` 位于约 1 MiB 正文中段时可能在完整解析前漏识别，导致预检阶段仍创建普通墙钟和首字计时器。

## 修复方案

- timeout profile 新增显式 `timeoutsDisabled`，只由已识别的 Codex compact 请求启用；普通请求保持原默认 120 秒 transport fallback 和现有 lane 配置。
- compact 请求使用不设上限的 `GatewayRequestWallBudget`，不创建普通路由首字/速度观察，不设置响应头、首响应、首字、响应提交或未提交 attempt 最大生命周期计时器。
- 请求协调上下文携带 `codex_compaction_unbounded`，合成 Chat 摘要请求即使路径已改写也继承同一策略。
- 流式压缩在上游尚未返回任何字节时不启动首 chunk、首语义输出或总生命周期计时；一旦开始收到原始字节，仍保留 raw stream idle 断链保护。
- 保留客户端取消、明确上游 HTTP 错误、真实网络断开、协议契约失败、候选耗尽和零可派发等待预算；这些不是压缩计算的首字或总墙钟限制。
- JSON metadata scanner 只检查有效 JSON 顶层 `input[]` 中直接 item 的 `type = compaction_trigger`，避免大正文中段漏识别，同时不会把 metadata、工具 schema、消息内容或字符串中的同名值误判为压缩。
- Anthropic/Gemini 的普通 Responses 转换不等于 compact 能力；`/responses/compact` 和 `compaction_trigger` 候选会排除这些转换账号。只有原生 Responses compact 或已实现网关摘要的 Chat bridge 承接，不能靠返回侧契约失败兜底。
- 无限时 compact 不承担账户短电路的有限 confirmation lease；SUSPECT/HALF_OPEN 候选保持阻断，避免压缩超过租约后产生并发确认。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- |
| 类型检查 | Node backend TypeScript | `pnpm --filter juhe-ai-backend typecheck` | 通过 | 通过 |
| 核心策略 | profile、墙钟、transport 和大 JSON marker | `test:gateway-timeout-profile`、`test:gateway-json-parser-lifecycle` | 通过 | 通过 |
| Chat-only 行为 | 内部 Chat compact 首响应延迟 10.2 秒，系统阈值 10 秒 | `test:deepseek-gateway-mock-ai` | HTTP 200，单次上游命中 | 通过 |
| 普通请求反向回归 | 普通首字、流式首输出、非流式 lifetime、请求墙钟、跨分组墙钟 | 对应 gateway regression | 均通过 | 通过 |
| 返回契约 | Codex compact 响应检查和账号切换 | `test:response-inspection-gateway-e2e` | 通过 | 通过 |
| 跨供应商 | native / Chat bridge / Anthropic / Gemini compact 候选分类与普通桥接 | `test:codex-cross-protocol-context` 及对应 mock AI regression | 均通过 | 通过 |
| 完整预演 | 构建、不可变包、临时多进程请求/DB/worker/日志门禁 | PLAN-20260726T172021035Z | 待执行 | 待执行 |

## 下次遇到

- 先确认审计 metadata 是否出现 `codex_compaction_timeouts_disabled`，以及请求级 wall budget 是否为 unbounded。
- Chat-only compact 要检查协调上下文，而不是只看已经改写成 `/v1/chat/completions` 的路径。
- 长请求出现连续账号错误时，先区分上游明确失败与本地计时器中止；不能把本地超时放大误判成多个上游同时异常。
- 新增公共 timeout、wall budget 或 precommit deadline 时，必须同时覆盖 `/responses/compact`、大正文中段 `compaction_trigger` 和 synthetic Chat compact。

## 完成总结

- 完成时间：待完整隔离预演后填写
- 结论：代码和定向回归已修复多层时限误杀，生产切换仍被完整本地临时实例预演阻断。
- 后续建议：Anthropic/Gemini 如需 compact 输出能力应另立协议任务；当前会受控排除，不与普通 Responses 转换混为已支持。
