# Anthropic 透传 dateline 隐写清洗设计

> 状态：已实施（2026-11-06，随 [计划-20261106T150648000Z](../plans/计划-20261106T150648000Z-上游身份一致性与版本门槛诊断.md) 交付）。
> 行为规格来源：Claude Code 客户端对非官方 base URL 的行为取证结论（同类开源项目 sub2api `internal/pkg/anthropicfp` 包注释为佐证）。**实现为本仓库独立实现**——sub2api 为 LGPL-3.0，不复制其源码，只采用其取证到的行为事实（目标句形态、变体码点集合、扫描范围）。

## 背景

网关对 Anthropic OAuth 账户的 `/v1` 转发采用「透传优先」：入站客户端是真实 Claude Code 时保持其 UA 与请求体逐字节转发。取证结论：Claude Code 在检测到非官方 base URL 时，会在请求体 system-reminder 的日期句 `Today's date is YYYY-MM-DD.` 中把撇号替换为变体码点，或把日期分隔符写成 `/`，作为隐写水印标记"该客户端连的不是官方端点"。网关原样透传等于把「经过中转」的证据主动送回上游——透传路线自身的信号泄漏，与上游风控强度无关，属于逻辑漏洞而非对抗。

## 目标与非目标

目标：

- Anthropic 家族 OAuth 账户的转发请求体在出站 attempt 层归一化上述水印句，抹掉非官方环境信号。
- 恒等性保证：无命中时逐字节返回原 body，出站字节与现状零差异。

非目标：

- 不做全量 body 重序列化（禁止把整个 body 经 `encoding/json` 重新编组——键序与 HTML 转义差异本身是指纹变化）。
- 不新增任何配置开关（全局规范：功能不设开关）；OAuth 路径一律生效。sub2api 侧同类能力的系统设置开关在本仓库不做。
- API Key 账户不清洗：上游是标准公开 API，水印语义不成立。
- 不仿造 Claude Code body 结构（billing attribution 块合成等深度仿真属另一量级，明确不做）。

## 契约

### 清洗规则

- 目标句：`Today<apo>s date is <YYYY><sep><MM><sep><DD>.`，其中 `<apo>` ∈ {U+2019 `’`、U+02BC `ʼ`、U+02B9 `ʹ`}（ASCII U+0027 本就规范，即使命中也不产生改写）；`<sep>` 在同一句内一致，∈ {`-`、`/`}——用两个独立正则分别匹配两种分隔符，保证句内前后分隔符一致，从而滤掉混合分隔符的普通散文（如 `2026-07/01`）。
- 归一化输出：`Today's date is YYYY-MM-DD.`（ASCII 撇号 + 连字符），句子以外的字节一律不动。
- 已是规范形态的句子不产生改写、不记命中。

### 扫描范围

- `system` 字段：字符串形态全文清洗；数组形态仅清洗 `type=="text"` 块的 `text` 值。
- `messages[i].content`：字符串形态仅清洗 `<system-reminder>...</system-reminder>` 包裹的片段；数组形态仅清洗 `type=="text"` 块内被 system-reminder 包裹的片段。真实客户端把 dateline 放在 system-reminder 内（首轮之后的对话因 system prompt caching 使顶层 system 块不再重发）。
- 其余位置（用户正文、`tool_use.input`、`tool_result.content`、代码块、shell 命令）一律不扫描——合法含撇号或斜杠日期的文本永不改写。

### 生效位置与边界

- 生效条件（全部满足）：`/v1` 网关链、anthropic 家族上游、账户 `Type=="oauth"`；判定与既有 anthropic 出站分支同口径。
- 生效位置：attempt 层（`gatewaydispatch/upstreamattempts.go`，紧邻既有 `PrepareAnthropicMessagesBodyForAttempt` 归一点），对每次 attempt 的当前出站 body 幂等清洗；候选轮换时每 attempt 从原始 body 重洗。
- 不回写 `req.Body` / `RawBody`：usage 统计、审计快照、内容指纹继续消费客户端原始体（既有边界不变）。
- Content-Length 由出站传输层按最终 body 长度重算，改写不产生 CL 失配。
- 在途额度预占（读 `max_output_tokens`）发生在 attempt 之前，且撇号归一化不触碰该字段。
- codex 加密上下文重放不涉 anthropic 主链；若未来恢复臂扩展到 anthropic，需对齐「实际发送体」口径（登记为留观项）。

### 可观测性

- 命中时 Debug 级结构化日志（命中数、撇号变体、分隔符类别）；不打点新指标、不加标签；无命中零额外开销（`strings.Contains` 快路径）。
- 快路径知情边界（2026-11-07 复查登记）：顶层快路径按原始字节匹配 `date is `，客户端若把句内字母转义（如 `\u0064ate is`）则整字段静默跳过清洗（恒等透传）——无数据破坏，属知情取舍；真实客户端不产生该形态。

## 实现

- 新包 `backend-go/shared/platform/anthropicfp`（`dateline.go`）；JSON 字段定位用 `tidwall/gjson`（`Result.Index` 指向原始 token 开引号在 body 中的字节偏移、`Result.Raw` 为含引号原始编码文本，可靠性由 `dateline_probe_gjson_test.go` 探针钉住）。
- 改写为「一次装配 + 字节保真」（2026-11-07 重写，`tidwall/sjson` 依赖退役）：引号内原始字节上做 JSON 转义感知行走（逐条镜像 gjson 解码规则），建立解码偏移 → 原始偏移映射；正则在解码文本上匹配，命中区间映射回原始区间；全部改写区间收集后按偏移排序、单次遍历装配、一次分配，复杂度 O(body + 总命中长度)。未触碰字节（既有 `\uXXXX` 转义、`<>&`、无效 UTF-8、缩进）逐字节不变，禁止整包经 `encoding/json` 重序列化——sjson 写回会按命中数全量拷贝 body（O(命中×body)，8.39MB/3961 命中实测 12.3s）且把改写字段重编码出 `\u003c` 等 HTML 转义与 U+FFFD 重编码，字节形态漂移本身是「程序改写」指纹。
- 纯函数：`NormalizeText(string) (string, []DatelineHit)` 与 `NormalizeDateline(body []byte) ([]byte, []DatelineHit, bool)`；不修改输入切片；无改写时返回原切片（identity）、nil 命中、false；字段定位或解码映射失败时该字段整字段跳过且不记命中（fail-open，清洗失败不得阻断转发）。

## 验收

见计划 §4 验证口径。核心矩阵：三种变体撇号 × 两分隔符 × system 字符串/数组 × messages 字符串/数组 × system-reminder 边界外不动 × 已规范句不动 × 非 JSON 体原样 × 空 body；恒等性：无命中输出与输入 `bytes.Equal`。字节保真：改写字段除命中句外逐字节与输入相同（含既有 `\uXXXX` 转义、`\"`、`&`、无效 UTF-8 裸字节；代理对转义字段内偏移正确；同字段多命中一次装配）；大 body 性能护栏：约 8MB/数千命中清洗 < 2s（修复前 sjson 基线 12.3s）。
