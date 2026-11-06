# BUG-0356 Responses 多输出项提取在首个 done 即返回致探针误判

## 基本信息

- 编号：BUG-0356
- 状态：已修复待发布（2026-11-06）
- 严重程度：P1
- 发现时间：2026-11-05
- 发现方式：用户反馈（GPT 账户手动测试失败）+ 响应原文取证 + 真实上游矩阵复验
- 模块：后端 / 账户探针 / OpenAI Responses 响应文本提取（`shared/platform/accounttest/accountprobe`）
- 关联计划：无
- 关联 bug：BUG-0355（同期探针缺陷，Anthropic body 形态）、BUG-0201（探针轻量契约）
- 责任人：网关维护者

## 问题概述

- 现象：GPT API Key 账户（`gpt-6.1-sol`，`responses_sse`）生产手动测试返回 `invalid_probe_output`「上游返回 HTTP 2xx 且协议完成，但输出未包含预期答案」，但响应原文中 `response.completed` 完整、答案 `138` 明确出现在第二个 output item 的 `output_text`。
- 期望：协议完成且可见输出包含挑战答案时判定成功。
- 实际：提取函数遇到第一个 `response.output_text.done` 就返回，而上游把回答拆成两个 message output item（prelude 空文本 `" "` + `final_answer` 正文 `"138"`），首个 done 只有空格，答案被丢弃。
- 影响范围：所有返回多 message output item 的 OpenAI Responses 上游（当前生产 `gpt-6.1-sol` 稳定复现）；影响手动测试与自动健康检查的成功判定，真实流量转发不受影响。

## 复现步骤

1. 构造 Responses SSE：第一个 item 的 `response.output_text.done` 为 `" "`，第二个 item 的 done 为 `"138"`，末尾 `response.completed` 的 `response.output` 含两个 item。
2. 断言 `extractOpenAIOutputText` / `extractRawVisibleOutputText`：修复前返回 `" "`（不含 138），修复后返回 `"138"`。
3. 真实上游等价路径：`gpt-6.1-sol` 的 `responses_sse` 探针请求。

## 环境信息

- 分支 / 版本：当前工作区（Go gateway，未发布）
- 上游：`https://api.zeekai.cc/v1`（GPT 中转），模型 `gpt-6.1-sol`
- 是否稳定复现：是（多 output item 形态稳定触发）

## 根因分析

- `classify.go` 的 `extractOpenAIOutputText` 原逻辑：遍历 SSE 事件，遇到 `response.output_text.done` 即 `return text`，遇到 `response.completed` 即 `return`；隐含「一条流只有一个 output item」的假设。
- `extractRawOpenAIVisibleOutputText`（挑战判定实际消费的原始可见输出）存在同一模式。
- 该假设来自 2026-09-07 Go 迁移提交 `7e36e20ba` 的移植，**不是本轮改造引入**；此前上游通常只返回单个 message item，缺陷长期潜伏。新模型/上游开始返回 prelude + final_answer 双 item 后触发。
- 次要同类风险：`extractOpenAIResponsePayloadText` / `extractRawOpenAIResponseVisibleText` 的 `output_text` 便捷投影为空白色时，会遮蔽 `output` 数组里的正文。

## 修复

- `extractOpenAIOutputText`：收集全部 `output_text.done`；`response.completed` 的 response 快照优先（含全部 output items），其次全部 done 拼接，最后退回 delta 拼接。
- `extractRawOpenAIVisibleOutputText`：同口径修复（保留原始字节语义）。
- `output_text` 投影只在 `TrimSpace` 后非空时采用，空白投影回退到 `output` 数组。
- 回归：新增 `TestW7CResponsesMultiOutputItemText`（completed 快照 / 多 done 两条路径）与 `TestW7CMultiItemExtractionMatrix`（Responses JSON 多 item、空白投影、Chat 多 choice、Anthropic 多 content block、Gemini 多 candidate，以及 raw 可见输出）。

## 验证记录

- 单测：`go test ./accounttest/accountprobe/ -count=1` 全绿；新增用例修复前必红（首个 done 返回空格）；`gofmt -l` 无输出、`go vet` 通过。
- 真实上游矩阵（2026-11-06 凌晨，一次性探针测试跑完即删）6/6 通过：
  - `gpt-responses-sse` 3.9s、`gpt-responses-json` 3.1s、`gpt-chat-sse` 2.4s、`gpt-chat-json` 3.5s（`https://api.zeekai.cc/v1`，`gpt-6.1-sol`）；
  - `anthropic-messages-sse` 5.7s、`anthropic-messages-json` 4.7s（`https://direct.2chat.cc/v1`，`claude-opus-5-5`）。
- 多供应商真实矩阵（2026-11-06，同一批一次性测试）14 项 **12 项通过**：gpt `chat_sse/json`、`responses_sse/json`；xai `chat_sse/json`；anthropic `messages_sse/json`；deepseek `chat_sse/json`；glm `chat_sse/json`。2 项为上游站点限制，非探针缺陷：`gpt-images` 403「Image generation is not enabled for this group」、`xai-images` 502。首次运行有 3 项 40s 超时，单独复测全部通过，属站点波动。
- 同类面审计：`classify.go` 全部 13 个提取函数逐一核对，「首个 item/事件即返回」仅存在于本次修复的两处；Anthropic/Gemini/Chat 均遍历全部片段。

## 附带发现（不纳入本修复）

- Base URL 缺少版本根 `/v1` 时请求会落到站点 SPA fallback（200 text/html），属用户配置问题；UI 已在 Base URL 标签行提示「系统不会自动补充 /v1 或其他版本」。
