# Codex Responses 协议转换自审报告

## 1. 范围与结论

本报告审计 `sub2api-lite` 在下游为 Codex Responses、上游为 Chat Completions 时的请求转换、响应转换、会话恢复和跨账号重放路径，并对照本地 Codex 源码提交 `1bbdb32789e1f79932df44941236ea3658f6e965` 的 Responses item ID 契约。

结论：当前实现确实会产生与本次外部问题同类的协议污染，且已确认至少两条可触发链路。

1. Chat->Responses bridge 对所有工具调用先分配 `fc_*`，随后才确定是否为 `custom_tool_call`。custom 工具最终被写成 `type=custom_tool_call + id=fc_*`。
2. bridge 会把其合成的 `outputItems` 保存为会话状态；同一 internal `previous_response_id` 后续允许调度到原生 Responses 账号，但 native 重放路径不会删除这些 item ID。`store=false` 场景下，这些非原生持久化 ID 可被带给新上游，具备触发“item 未持久化”类错误的条件。

原生 Responses 直通路径未发现主动把 item ID 改名为错误前缀的代码。它的问题是未对历史和响应执行类型/前缀一致性校验，因此无法阻断 bridge 或异常上游带来的污染。

本报告只记录源码可证明的结论。未将尚未复现的 SSE 客户端兼容性差异、上游实际报错频率或物理模型身份写成事实。

## 2. 审计依据

### 2.1 Codex item ID 契约

本地 Codex 源码 `codex-rs/protocol/src/models.rs` 的 `ResponseItem::id_prefix()` 规定：

| item type | 允许前缀 |
| --- | --- |
| `message` | `msg_*` |
| `reasoning` | `rs_*` |
| `function_call` | `fc_*` |
| `function_call_output` | `fco_*` |
| `custom_tool_call` | `ctc_*` |
| `custom_tool_call_output` | `ctco_*` |
| `compaction` / `context_compaction` | `cmp_*` |

Codex 当前出站预处理只检查 ID 是否具有任意非空 `<prefix>_<suffix>` 形式，而不验证该前缀与 item type 是否一致：`codex-rs/core/src/client.rs:910`。因此 `fc_bad` 可以保留在 `custom_tool_call` 上，直到严格上游或下游检查到类型冲突。

当 item IDs 功能关闭且 `store=false` 时，Codex 会删除全部请求 item ID；但这不是网关可以依赖的安全边界，因为它取决于客户端能力状态，网关本身会把带 ID 的重放内容发给不同类型的上游。Codex 相关逻辑位于 `codex-rs/core/src/client.rs:910`。

## 3. 审计路径

```text
Codex /responses 请求
  -> 请求预检与 previous_response_id 状态恢复
  -> 账号选择
  -> Chat-only bridge: Responses input -> Chat messages
  -> Chat SSE -> Responses JSON/SSE
  -> 保存 bridge outputItems
  -> 下一请求可能重放到 bridge 或原生 Responses 账号
```

重点文件：

- `backend/src/modules/providers/drivers/_shared/codex-responses-chat-bridge.ts`
- `backend/src/modules/gateway/codex-responses/chat-bridge-state.ts`
- `backend/src/modules/gateway/adapters/gpt-codex/oauth-normalizer.ts`
- `backend/src/modules/gateway/protocols/openai-v1/api-key-client-compatibility.ts`

## 4. 已确认发现

### F-01: custom tool 的 item 类型与 ID 前缀不一致

**严重性：高**

`appendResponsesToolCallDelta()` 在工具调用首次出现时先创建状态：

```ts
id: `fc_${state.idPrefix}_${index}_${Date.now().toString(36)}`
```

见 `codex-responses-chat-bridge.ts:1372`、`1377`、`1379`。此时 `adapter` 尚未从 Chat tool name 解析；后续才执行：

```ts
const adapter = state.toolAdaptersByChatName.get(chatName)
activeToolCall.adapter = adapter ?? activeToolCall.adapter
```

见 `codex-responses-chat-bridge.ts:1393` 至 `1396`。

同一个 `toolCall.id` 随后被用于 `toolCallInProgressItem()` 和 `completedToolCallItem()`。这两个函数在 adapter 类型为 custom 时都输出 `custom_tool_call`：

```ts
{
  id: toolCall.id,
  type: 'custom_tool_call',
  ...
}
```

见 `codex-responses-chat-bridge.ts:1525` 至 `1547`、`1550` 至 `1573`。

因此 custom tool 的稳定输出为：

```text
type = custom_tool_call
id   = fc_<...>
```

根据 Codex 契约，该 item 必须使用 `ctc_*`。该问题同时影响：

- 下游请求 `stream=true` 的 SSE 输出；
- 下游请求 `stream=false` 的 JSON 输出，因为两条路径共用 `processChatSseEvent()` 和相同 `ChatToResponsesState`；见 `codex-responses-chat-bridge.ts:1066` 至 `1166`；
- 会话状态持久化，因为完成时保存的正是 `state.outputItems`；见 `chat-bridge-state.ts:305` 至 `318`。

**根因**：item ID 的生成顺序早于工具语义类型的确定。此处不存在后续重命名逻辑，因此不是偶发的事件顺序问题，而是确定性构造缺陷。

### F-02: bridge 状态可以把合成 item ID 重放给原生 Responses 上游

**严重性：高**

bridge 完成后将 request input 与 `completion.outputItems` 存入状态：

```ts
outputItems: completion.outputItems
```

见 `chat-bridge-state.ts:305` 至 `318`。

后续请求引用 internal `previous_response_id` 时，恢复逻辑把每一轮的原始 input 和 bridge 生成 outputItems 直接拼回新的 input：

```ts
restored.push(...responsesInputAsItems(payload.request.input))
restored.push(...cloneArray(payload.outputItems))
```

见 `chat-bridge-state.ts:930` 至 `938`。

对于普通 `/responses` 请求，internal previous response 并不限制只能选 bridge 账号。`codexResponsesContextAllowsAccount()` 只阻止“external previous response -> bridge”，internal previous response 可通过：`chat-bridge-state.ts:583` 至 `594`。

当同一请求最终选择原生 Responses 账号时，`prepareCodexResponsesContextForAccount()` 使用 `nativeResponsesInputFromMaterialized()` 渲染恢复结果，并删除 `previous_response_id`：`chat-bridge-state.ts:596` 至 `634`。但 `nativeResponsesInputFromMaterialized()` 只把网关内部的 compaction summary 转成 message；其他 item 原样返回：`chat-bridge-state.ts:676` 至 `738`。

这意味着下列 bridge 合成 ID 会直接进入原生 Responses 请求：

- `rs_chat_bridge_*` reasoning item；
- `msg_chat_bridge_*` message item；
- `fc_chat_bridge_*` function/custom tool item，其中 custom 情况还带 F-01 的错误前缀。

这些 ID 只是网关生成的客户端 wire ID，并不是新原生上游账户中的可持久化对象。对依赖 item ID 持久化语义的上游，它们满足本次观察到的 `store=false` / 跨账号“item 未持久化”问题的触发条件。

**根因**：网关把 `clientItemId` 与 `upstreamItemId` 视为同一可重放身份，且在 bridge -> native 交接时没有执行 ID 清洗。删除 `previous_response_id` 本身不能消除 input item 上的持久化引用。

### F-03: 原生直通没有 Responses item 契约验证

**严重性：中**

原生 Codex 请求规范化确实强制 `store=false`，并做字段兼容处理：

- OAuth：`oauth-normalizer.ts:50` 至 `77`；
- API Key Codex compatibility：`api-key-client-compatibility.ts:111` 至 `151`。

两条路径均未按 item type 检查 `id` 前缀，也未在 `store=false` 或跨账号重放时删除 item ID。对于原生响应，网关现有的 stream 语义提取只将 `function_call` 和 `custom_tool_call` 识别为可调用输出类型，用于通用响应处理；它不是 Responses contract validator，见 `stream-events.ts:259` 至 `274`。

因此原生直通本身不主动制造 `fc -> ctc`，但会：

1. 允许 F-02 的 bridge 产物进入原生上游；
2. 原样向 Codex 透传异常上游返回的错误前缀、重复 item ID 或生命周期不一致事件；
3. 缺少事件写出前的结构性防线，无法对污染来源做归因。

### F-04: bridge 省略工具参数的增量事件，且没有事件级身份校验

**严重性：中，兼容性风险待端到端复现确认**

Chat SSE -> Responses 的工具调用转换只输出 `response.output_item.added` 与最终 `response.output_item.done`：`codex-responses-chat-bridge.ts:1403` 至 `1411`、`1502` 至 `1523`。工具参数在状态中累积，但未输出：

- `response.function_call_arguments.delta`；
- `response.custom_tool_call_input.delta`。

而项目内其他 Responses 转换器会输出 function arguments delta，例如 `openai-anthropic-bridge.ts:4815` 至 `4821`、`4847` 至 `4853`。

只凭缺少 delta 不能断言 Codex 一定拒绝，因为 done item 仍携带完整 arguments/input；但是这会使 bridge 的流式行为与现有转换器和 Codex 工具流预期不一致。更关键的是，当前 bridge 没有“added/delta/done 的 item type、ID、call_id、output_index 必须一致”的事件状态机校验，因此 F-01 一类身份错误会被直接写给客户端。

## 5. 已核对且未发现主动污染的路径

### 5.1 原生 Responses 直通

未发现 API Key 或 OAuth 原生 Responses 请求路径把 `item.id` 强制改写为 `fc_*`、`ctc_*` 或 `rs_*`。它们主要进行 input role、内置工具别名、请求字段和 `store=false` 归一。

这不是“原生路径完全安全”的结论：F-03 表明它缺少对外来污染的验证与清洗。

### 5.2 Anthropic -> Responses function 转换

`openai-anthropic-bridge.ts` 的 Anthropic tool use 转 Responses 明确输出 `function_call + fc_*`，见 `1764` 至 `1789`；在该转换范围内，类型与前缀一致。该路径没有把已知 custom tool 映射为 `custom_tool_call`，因此没有发现与 F-01 相同的错误前缀构造。

### 5.3 compact

compact 的内部 bridge 路径受上下文状态和账号类型约束；native 交接时唯一专门转换的是网关内部 compaction summary，见 `chat-bridge-state.ts:724` 至 `738`。未发现 compact 专属的 `fc/ctc` 生成点。

但这也印证 F-02：普通历史 tool/reasoning item 没有类似的 native 交接清洗逻辑。

## 6. 风险矩阵

| 场景 | 是否已确认 | 影响 |
| --- | --- | --- |
| Chat-only bridge 返回 custom tool | 是 | `custom_tool_call` 带 `fc_*`，可污染 Codex 会话 |
| 同一 bridge 的非流式 JSON | 是 | 共享状态机，输出同样错误 item |
| bridge 会话后切到原生 Responses 账号 | 是 | 重放合成 item ID，可能被新上游判为未持久化 |
| 原生上游返回坏 item ID | 未知是否发生 | 当前网关原样透传，缺少阻断与告警 |
| custom tool 流式参数增量 | 缺失已确认，客户端影响待复现 | 可能降低工具流兼容性 |
| compact 专属错误 ID 生成 | 未发现 | 仍需在修复后的端到端回归中覆盖 |

## 7. 对设计文档的影响

本报告直接验证了 [Codex Responses 协议防火墙与会话修复设计](2026-07-22-Codex-Responses协议防火墙与会话修复设计.md) 中的关键假设：

1. item type 与 ID prefix 的 registry 不能只用于诊断，必须覆盖 bridge 输出前的验证；
2. `clientItemId`、`upstreamItemId`、`callId` 与 `previousResponseId` 必须分离；
3. 请求历史 sanitizer 必须在 bridge -> native、账号切换和 `store=false` 组合中删除可重放 item 的 ID，而不修改 `call_id` 或正文；
4. tool adapter 必须在 item ID 分配前确定 type，不能让通用防火墙事后猜测 function/custom；
5. SSE 的 `added/delta/done` 需要围绕一个内部 item key 建立一致性断言，并以 `semanticCommitted` 为不可透明重试边界。

## 8. 建议修复优先级

1. **P0**：在 Chat->Responses bridge 内先解析 adapter 类型，再分配 `fc_*` 或 `ctc_*`；为 custom tool 添加流式和非流式精确回归。
2. **P0**：bridge -> native 交接前执行请求历史 ID sanitizer。对于具有完整可重放内容的 item 删除 `id`，保留 `call_id`、工具内容、reasoning、顺序和 output 语义；对于只有持久化 ID 的 item 显式失败，不得静默删除 item。
3. **P1**：落地版本化 Responses contract registry 和双检查点验证，分别记录 raw upstream 与 gateway bridge provenance。
4. **P1**：为 SSE 增加事件身份状态机，至少验证 item ID/type/call_id/output_index 不漂移；补全 function/custom 参数 delta 或以 fixture 证明 Codex 可接受无 delta 的 done-only 形式。
5. **P2**：把修复、拦截、能力级隔离和 usage 诊断按设计文档灰度上线；不要把 `gateway_bridge` 问题归咎于上游账号。

## 9. 最小回归集

以下回归应在修复前先失败，并在修复后通过：

1. Responses `tools=[{type:'custom', name:'apply_patch'}]` 经 Chat-only bridge 返回工具调用时，SSE added/done 与 JSON output 都必须为 `type=custom_tool_call + id=ctc_*`。
2. function tool 仍必须为 `type=function_call + id=fc_*`，且 `call_id` 原样稳定。
3. custom tool 的 added、input delta（若支持）和 done 必须共享同一 `ctc_*`、`call_id` 与 `output_index`。
4. bridge 产出 `rs_*`、`msg_*`、工具 item 后转到原生 Responses 账号，发给原生上游的重放 input 不得保留这些 item 的 `id`，但不得丢失正文、工具 input/output 或 `call_id`。
5. 只有 ID、没有可重放负载的历史 item 在 bridge -> native 交接时必须显式报 `unrecoverable_history`，不得静默删除。
6. 原生上游返回 `type=custom_tool_call + id=fc_*` 时，contract validator 必须在写出客户端前产生明确诊断；严格模式不得写出该事件。

## 10. 审计限制

- 本次为静态源码追踪与本地 Codex 源码对照，未使用真实上游账号或外部请求。
- 未修改运行时代码、配置、数据库或账户状态。
- 报告中的风险判断针对当前提交工作树；实施修复后必须以新增 fixture 和真实网关 mock 回归重新验证。
