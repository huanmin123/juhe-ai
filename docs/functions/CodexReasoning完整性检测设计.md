# Codex Reasoning 完整性检测设计

## 1. 目标与非目标

本设计只处理 Codex 客户端经过 OpenAI Responses 协议时的请求历史 reasoning 结构、响应 reasoning 生命周期和响应终态，不扩散到 Chat Completions、Anthropic Messages、Gemini 或未知协议。

可以检测的是可观察的协议事实：事件是否成对、item 是否完成、响应是否以成功终态结束、最终对象是否与流中 identity 一致。不能检测的是服务端隐藏思维在语义上是否“想完了”；OpenAI/Codex 没有暴露预期 reasoning 长度、校验和或 `reasoning_complete` 标志。

## 2. Codex 源码契约

以 OpenAI Codex 仓库提交 `1bbdb32789e1f79932df44941236ea3658f6e965` 为本次基线：

- `ResponseItem::Reasoning` 的 `summary` 是数组，允许为空；`content` 与 `encrypted_content` 都可缺失或为空。
- Codex 请求仅在模型声明支持且配置没有关闭时发送 reasoning summary 参数；请求会包含 `reasoning.encrypted_content`，用于获取可重放的加密 reasoning。
- reasoning 可以只有 `encrypted_content`，没有任何可读 summary 或 raw content。这是合法的不透明状态，不是失败。
- item 的权威完成事件是 `response.output_item.done`；`response.reasoning_summary_text.done` 只结束一个 summary 片段，不能替代 item 完成。
- 整个响应的成功终态是 `response.completed`。`response.incomplete`、`response.failed`、流在 `response.completed` 前关闭都属于失败。
- Codex 开源客户端没有渲染 `[reasoning unavailable]` 或 `<thinking>[reasoning unavailable]</thinking>` 的实现。该文字单独出现不能证明 OpenAI reasoning 损坏，应按上游包装层或客户端展示层证据继续定位。

## 3. 状态模型

每条 Responses 流只维护有界增量状态：

| 维度 | 枚举 | 含义 |
| --- | --- | --- |
| `responseTerminal` | `completed / incomplete / failed / missing` | 响应终态 |
| `reasoningLifecycle` | `absent / started / done / incomplete / invalid` | reasoning item 生命周期 |
| `reasoningVisibility` | `none / summary / raw / encrypted_only / mixed` | 可见材料形态 |
| `reasoningMaterial` | `present / opaque / empty` | 是否有可读或可重放材料 |

`reasoningLifecycle=done` 的依据只能是对应 reasoning identity 收到合法 `response.output_item.done`，或非流式最终 `response.output[]` 中存在结构合法的 reasoning item。summary 的 delta/done 不能把 item 标成完成。

请求侧额外记录 `reasoningReplayMaterial=readable / encrypted_only / empty / absent`。它只描述历史 item 是否携带可重放材料，不推断历史思考质量。请求进入账户适配前和最终 outbound 发送前都应执行同一 contract 检查；只有最终 outbound 是上游实际收到的事实。

## 4. 判定分级

### 4.1 确定失败

以下证据可用于严格拦截：

- 收到 `response.failed` 或 `response.incomplete`。
- 非流式 Responses 对象的 `status` 为 `failed` 或 `incomplete`；HTTP 200 不覆盖对象终态。
- EOF、连接关闭或总超时发生在 `response.completed` 之前。
- reasoning delta、summary delta 或 summary done 无法关联当前活动 reasoning item。
- 已 `added` 的 reasoning item 在 `response.completed` 时仍未收到 `output_item.done`。
- `response.completed.response.output` 存在时，与流中已完成 identity、item type、output index 或 ID 不一致。
- reasoning item 字段类型非法、ID contract 非法、重复 identity、done 后继续 delta、completed 后继续发送事件。
- 请求历史 reasoning item 的字段类型、ID contract 或 identity 非法。

### 4.2 合法但不可见

以下情况必须放行，不能处罚账户：

- 完全没有 reasoning item。
- `summary=[]`，或调用方配置 `summary=none`。
- `content` 缺失或不返回 raw reasoning。
- 只有 `encrypted_content`。
- usage 显示 reasoning tokens，但没有可读 reasoning。
- UI 展示 `reasoning unavailable`，但原始协议生命周期和终态均合法。

### 4.3 可疑但不能单次拦截

reasoning item 同时满足 `summary=[]`、无 `content`、无 `encrypted_content` 时属于 `empty`。Codex 的 rollout trace 归一化器会拒绝完全没有材料的 reasoning item，但通用 wire 类型仍能解析它，因此只能记为能力异常，不可依据单次样本直接切号。

历史请求缺少 `encrypted_content` 或可读材料时也只能记为 replay 风险。若材料本来就未返回，网关无法恢复；若是网关转换器在同一链路中删除了材料，则应根据转换前后证据归为确定的 gateway bridge 违规，而不是归罪于上游账户。

“summary 像半句话”、内容过短、逻辑错误、循环思考、长时间只有 reasoning 而没有工具或文本输出，都属于语义或质量启发式。它们没有官方确定性契约，只能进入模型检测评分，并要求同一账户、模型、reasoning effort 下重复采样。

## 5. 运行时动作

| 证据 | 默认安全修复 | 严格拦截 |
| --- | --- | --- |
| ID、重复字段等可确定重写问题 | 在首次暴露前确定性修复 | 修复关闭时拦截 |
| `failed / incomplete / missing terminal` | 不伪造 reasoning；按现有失败路径处理 | 语义提交前排除当前账户并重试；提交后只返回受控失败 |
| reasoning item 未 done | 不补造 done | 按确定协议违规处理 |
| encrypted-only / 无 summary | 原样放行 | 原样放行 |
| empty item / 语义异常 | `observed_unknown`，累计诊断 | 单次不拦截；稳定复现后降低模型能力健康度 |
| 请求历史缺少 replay 材料 | 不补造材料；保留来源归因 | 上游原始缺失不单次切号；bridge 确定丢字段时拦截转换结果 |

reasoning 不能被“修复”为人工生成的 summary、content、encrypted content 或 done 事件。网关只能修复可证明等价的结构和 identity；缺失的模型思考材料没有可恢复来源。

## 6. 模型检测探针

仅为 `openai_responses` profile 增加 reasoning 完整性探针：

1. 发送 `store=false` 的确定性任务，请求模型执行指定工具调用并给出精确最终答案。
2. 模型能力明确支持 summary 时请求 `auto` 或 `detailed`；同时请求 `reasoning.encrypted_content`。summary 不可见本身不扣分。
3. 保存有界事件元数据，不保存完整 reasoning：事件类型、response ID、item ID token、output index、material 形态、终态、incomplete reason 和时延。
4. 校验所有 added identity 都 done、最终 output 对账、工具生命周期完成、响应以 completed 结束。
5. 同一账户 + 模型 + effort 至少三次采样。确定协议失败直接计失败；empty item 和语义质量只形成 warning，并按稳定复现率计分。

模型检测可以证明“该账户的 Responses reasoning 协议链路稳定/不稳定”，不能证明上游物理模型身份，也不能证明隐藏 chain-of-thought 的语义完整性。

## 7. 性能与作用域

- 启用条件必须同时满足：Codex 客户端画像、Responses 端点、`raw_upstream` 或 `gateway_bridge` provenance、显式 guard marker。
- 正常流只做 O(event count) 的状态迁移；identity、diagnostic 和字节数均有固定上限。
- 不在生产热路径运行文本语义模型、句子完整性分析或第二次 LLM 审查。
- Chat Completions、Anthropic、Gemini 和未知协议不创建本状态机，不改变其成功、失败或切号语义。

## 8. 实施顺序

1. 先修正流式 `response.incomplete` 和非流式 `status=incomplete` 的失败语义。
2. 在 `response.completed` 时核对未完成 identity 和最终 output。
3. 为 reasoning 状态模型补 JSON、SSE、截断、encrypted-only 和 summary-none 回归。
4. 再接入模型检测的三次稳定性探针与账户模型能力健康，不把启发式异常接入单次严格切号。
