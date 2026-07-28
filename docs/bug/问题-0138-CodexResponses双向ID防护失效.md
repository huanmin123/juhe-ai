# BUG-0138 Codex Responses 双向 ID 防护失效

## 基本信息

- 编号：BUG-0138
- 状态：已修复（未部署）
- 严重程度：P1
- 发现时间：2026-07-28
- 发现方式：用户反馈、生产审计与 payload 关联
- 模块：Node 网关 / Codex Responses / 账户派发 / 响应守卫
- 关联计划：PLAN-20260728T071315383Z
- 责任人：Codex

## 问题概述

- 现象：Codex 会话把上游返回的 `item_*` custom tool identity 带入后续请求，上游返回 `Expected an ID that begins with 'ctc'`；部分流在已输出 reasoning 后又泄漏错误 ID，客户端呈现 `reasoning unavailable` 或长时间无后续动作。
- 期望：请求侧删除不可跨账户重放的远程资源 ID；响应侧在错误 identity 首次暴露前确定性改写；严格模式在安全边界内切号。
- 实际：请求 sanitizer 已实现但账户派发未调用；响应 guard 检测到 `item_id_prefix_mismatch` 后，因为整条流已有语义提交而拒绝为后续新 item 建映射，结果记录为 `late_violation` 却仍透传原 ID。

## 生产证据

- 目标会话：`019fa49c-7847-7023-b56d-f65952d6dcfa`。
- 失败审计：`audit_1785213075533_c5999570-f05e-40d5-9a60-d11c879b5503`，trace `49582231-9e71-497e-8e5c-788aaf9e7520`。
- 账户尝试顺序显示路由确实执行过换号：候选一返回 503，候选二返回 400 和 `ctc_*` ID 校验错误，候选三随后被选择，但该次连接约 70 秒后由客户端终止。相邻请求也出现候选三无流量进展后超时。因此问题不是“严格换号代码完全没有运行”，而是污染历史会在每一轮继续触发失败，后续候选又存在独立的无进展问题。
- 源响应审计：`audit_1785211829247_25ee3297-afa6-4490-b0e2-e4eedd9e32c9`，trace `e19df3c8-c54d-4e92-a3c4-7e8c42fa6459`。原始上游先返回 reasoning，再在 `response.output_item.added`、`response.custom_tool_call_input.delta/done` 和 `response.output_item.done` 中使用 `item_*`；守卫报告 safe repair、`replace_stream_item_id` 和 `late_violation`，但网关响应仍包含同一坏 ID。
- 审计日志中的 body 使用独立 blob 去重；同一坏请求体被多个 payload reference 引用，说明污染已进入可重放历史，而非仅为 UI 展示异常。

生产证据只保存审计 ID、trace 和有界错误摘要，不记录凭据、完整 body、URL 或用户隐私。

## 根因分析

1. 请求历史清理 helper 存在并有局部测试，但账户派发主路径没有调用；测试只证明函数本身，没有覆盖最终出站 body。
2. 响应 guard 用全局 `semanticCommitted` 禁止创建任何新 ID 映射。该条件适合判断是否能整次重试/换号，却不适合判断一个之后首次出现的 item 是否已经暴露。
3. `response.custom_tool_call_input.done` 等 reference event 没有完整归入 identity 生命周期，真实上游事件序列缺少精确回归。
4. safe repair 对 `late_violation` 没有统一截断；strict 决策没有明确区分提交前可切号与提交后只能受控失败。

## 修复方案

- 账户适配前清理解析态历史；适配后检查最终 outbound body。原生绑定 Buffer 复用结构化对象，OAuth 字符串和未知 Buffer 才兜底解析。
- 删除可完整重放 item 的远程 `id`，保留 `call_id`、工具参数、文本和顺序，让目标上游重新分配资源身份。
- 流状态按 identity 记录是否已经观察；尚未暴露的新 identity 即使出现在全局语义提交之后，也可建立稳定映射。曾在未映射状态暴露的 identity 不允许后补映射。
- 补齐 custom tool input、function arguments、reasoning text/summary 和 output text 的 `delta/done` 阶段识别。
- safe repair 对无法安全修复的 late violation 受控截断；strict 对确定违规统一拦截。只有 `semanticCommitted=false` 才允许账户切换。

## 验证记录

| 验证类型 | 覆盖 | 状态 |
| --- | --- | --- |
| 请求回归 | 真实账户派发、适配器重新引入、OAuth 字符串 body、字段保持 | 待最终回填 |
| SSE 回归 | reasoning 先提交、后续坏 custom tool identity、完整 delta/done/output/completed | 待最终回填 |
| 状态边界 | 未映射 identity 已观察后禁止补映射 | 待最终回填 |
| 策略回归 | safe repair、strict、提交前换号、提交后不换号 | 待最终回填 |
| 性能 | 100/1000/10000 item 与 SSE identity、100000 same-identity delta | 待最终回填 |

## 下次遇到

- 必须同时检查原始上游、网关实际下游、guard outcome/repair 和下一轮客户端 request，不能只看其中一层。
- “已检测”不等于“已修复”；确认修复后的字节是否真正写给客户端。
- “发生过换号”不等于会话已恢复；要确认污染字段已从下一轮最终出站请求移除，并区分后续候选自己的无流量超时。
- 模型检测可以证明协议和行为异常，不能仅凭隐藏 reasoning 文案证明模型真假。

