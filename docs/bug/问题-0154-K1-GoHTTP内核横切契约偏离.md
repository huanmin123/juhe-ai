# BUG-0154 K1 Go HTTP 内核横切契约偏离

## 基本信息

- 编号：BUG-0154
- 状态：待修复
- 严重程度：P1
- 发现时间：2026-09-04
- 发现方式：自查（已提交 Git 历史审计与独立复核）
- 模块：后端 / 网关 / 迁移
- 关联计划：`docs/migration/Node全量清零迁移总计划-20260904.md`
- 关联 bug：BUG-0152
- 责任人：待定

## 问题概述

- 现象：K1 提交 `b3115e675` 新增 `backend-go/projects/gateway/internal/kernel`，作为后续 Go 管理入口的横切 HTTP 内核；与 Node `shared/http-compression`、`request-context`、`http-security`、`deduplication/mutation-guard` 对照后，存在多项客户端可观察差异。
- 期望：Go 入口接管后，响应压缩、安全头、trace、请求体错误和 mutation 去重应与 Node 的状态码、头部、缓存及重试语义一致。
- 实际：
  1. `compression.go` 只按 token 接受 gzip，忽略 `q=0`/权重、br/deflate、compressible Content-Type、`Cache-Control: no-transform` 和 HEAD；会累计多次写入并在总量达到 1024B 时压缩，但过滤和协商边界仍与 Node 不同；不压缩响应也不会按 Node 保留 `Vary: Accept-Encoding`。
  2. Go 不在响应中设置 `x-trace-id`，也不回退 `x-trace-id`/`x-correlation-id`；`traceparent` 未按 Node 的四段、版本和全零 ID 规则严格校验。
  3. Go CSP 的 `script-src` 包含 `'unsafe-inline'`，Node 仅允许 `'self'`，安全策略被放宽。
  4. Go mutation guard 在 JSON parser 前读取并尝试解析原始 body，忽略 `ReadAll`/JSON 错误后仍可能先 claim；畸形或超大请求随后才由 decoder 返回 400/413，TTL 内重试可变成 409。Go decoder 还接受非 `application/json` 的合法 JSON，而 Node `express.json()` 默认不解析该类型。
- 影响范围：所有未来接入 Go kernel 的管理/公开 HTTP 请求都可能出现压缩解码失败、缓存协商错误、trace 丢失、安全策略变化、畸形请求重复 claim 或重试状态码漂移。当前 gateway 仍未把 kernel 接入生产业务 listener，故尚未在真实入口放大。

## 复现步骤

1. 在同一响应上分别发送 `Accept-Encoding: gzip;q=0`、`br`、`gzip`，以及 `Content-Type: image/png`、`Cache-Control: no-transform`、HEAD 请求，比较 Node 与 Go 的 `Content-Encoding`、`Vary` 和正文。
2. 发送无 `traceparent` 但带 `x-trace-id`/`x-correlation-id` 的请求，或发送全零/错误版本 `traceparent`，检查响应 `x-trace-id`。
3. 发送 `Content-Type: text/plain` 加合法 JSON、畸形 JSON 和超过 body limit 的 JSON，重复同一 mutation key，比较 Node/Go 的首个及第二个状态码。
4. 检查 `HEAD:backend-go/projects/gateway/internal/kernel/{compression.go,ctx.go,security.go,dedup.go}` 与对应 Node shared middleware。

## 环境信息

- 分支 / 版本：审计范围 `af841ce7a094bc66fbbb0c3817ea1fc0797245f1..fbad7b341b4fc5a7ae7457668b867f6b7091213d`（仅已提交对象）
- 数据状态：无需业务数据；问题由 HTTP 头、请求体和内核代码路径构造
- 浏览器 / 系统 / Node 版本：未执行浏览器验证
- 是否稳定复现：静态路径和边界条件稳定可构造；真实 production listener 尚未接线

## 根因分析

- 表象：K1 包内正常路径测试通过，提交说明写有压缩、trace、去重等契约对照。
- 真实根因：Go 内核采用简化的 gzip/trace/body 流程，未完整复制 Node 的内容协商、标准过滤器、严格 traceparent 解析及 parser-before-guard 顺序；测试没有覆盖上述边界。
- 为什么会发生：把“核心 happy path 相同”当成“横切 HTTP 契约等价”，且生产入口未接线，缺少从真实 listener 到客户端的回放门禁。

## 修复方案

- 修改点：以 Node middleware 和 golden 请求集冻结协商、过滤、头部、trace、body parser 与 dedupe 时序；在共享 kernel 中实现标准语义并补齐边界测试，再接入唯一 gateway listener。
- 行为影响：修复后所有 Go 切片共享同一套可验证的 HTTP 契约；在修复和回放完成前不得宣称 K1 archived 或继续扩大 Go 入口范围。
- 发布异常处理：压缩/trace/去重任一 golden 不一致即停止切换；不通过 Node↔Go 跨进程调用绕过内核差异。

## 已确认子项：Accept-Encoding 的 q=0 被 Go 错当成接受 gzip

- 对照事实：Node `compression` 在响应头阶段使用 `Negotiator.encoding(...)` 选择编码；当请求为 `Accept-Encoding: gzip;q=0` 且响应体达到 1024 字节时，gzip 的质量为 0，不会选择 gzip，响应保持 identity（同时仍会按 middleware 流程处理 `Vary: Accept-Encoding`）。
- 历史 Go：`backend-go/projects/gateway/internal/kernel/compression.go` 的 `acceptsGzip` 只把 `;` 前的 token 与 `gzip` 比较，忽略 `q` 参数。`gzip;q=0` 会返回 `true`，随后达到阈值的可写响应设置 `Content-Encoding: gzip`。
- 可观察结果：同一可压缩、长度超过阈值的响应，在客户端明确声明不接受 gzip 时，Node 返回未压缩正文，Go 返回 gzip 正文；不支持 gzip 解码或按协商严格校验的客户端会出现正文解析失败，缓存/代理的内容协商结果也会偏离。
- 证据范围：Node 历史实现 `backend/src/shared/http-compression.ts` 调用 `compression.filter`，依赖版本 `backend/node_modules/compression/index.js` 的 `Negotiator` 分支（`encoding(...)`）；Go 证据为提交 `b3115e675` 中的 `acceptsGzip` 实现。该结论不依赖当前未提交工作区。
- 修复门槛：实现与 Node 一致的编码权重/不可接受处理，并补充 `gzip;q=0`、缺失 header、并列权重和大响应 golden 用例；在回放通过前不得将 K1 标记为已归档。

## 已确认子项：Go 缺少 Node 的 Content-Type 可压缩性过滤

- 对照事实：Node `compression.filter` 先调用 `compressible(Content-Type)`；`Content-Type` 缺失、`image/png`、`application/zip`、`application/octet-stream` 等不可压缩类型会直接跳过压缩，只有可压缩类型（例如 `text/plain`、`application/json`）继续进入阈值和协商流程。
- 历史 Go：`compressionWriter.start` 只排除已有 `Content-Encoding`、`text/event-stream` 和 `attachment`，没有检查 `Content-Type` 是否可压缩。只要请求接受 gzip 且响应声明长度或累计写入达到 1024 字节，`image/png`、`application/zip` 或无 `Content-Type` 的正文也会设置 `Content-Encoding: gzip`。
- 可观察结果：以 2048 字节 `image/png` 为例，Node 保持原始二进制正文，Go 改写为 gzip；未带 `Content-Encoding` 解码链的客户端会把压缩字节当作图片/归档内容读取而失败，且已压缩媒体被再次压缩会改变缓存体积与校验结果。
- 证据范围：Node 历史 `backend/src/shared/http-compression.ts` 使用 `compression.filter`；锁定依赖 `compressible@2.0.18` 的实际行为为 `image/png=false`、`application/zip=false`、`application/octet-stream=false`、`text/plain=true`。Go 证据为提交 `b3115e675` 的 `compressionWriter.start` 条件分支。该结论不依赖当前未提交工作区。
- 修复门槛：复用与 Node 对齐的 MIME 可压缩性判定（含缺失/未知类型的结果），并补充二进制、已压缩媒体、可压缩 JSON/文本及大响应 golden；在过滤结果一致前不得宣称 K1 压缩契约通过。

## 已确认子项：Go 忽略 `Cache-Control: no-transform`

- 对照事实：Node `compression` 在自定义 `filter` 通过后仍执行 `shouldTransform`；响应的 `Cache-Control` 含 `no-transform` 时跳过压缩，不设置 `Content-Encoding`，以遵守缓存/代理对实体不可变换的要求。
- 历史 Go：`compressionWriter.start` 仅检查已有编码、SSE 和 attachment，没有读取 `Cache-Control`。请求接受 gzip 且正文达到阈值时，即使响应头为 `Cache-Control: no-transform`，仍会调用 `beginGzip`。
- 可观察结果：同一大响应在 Node 保持原始正文，在 Go 被改写为 gzip。对依赖 `no-transform` 保证签名、哈希或下游媒体格式不被改变的客户端/代理，Go 会产生违反响应契约的内容编码和缓存实体。
- 证据范围：Node 依赖 `backend/node_modules/compression/index.js` 的 `shouldTransform`（`no-transform` 分支）；Go 证据为提交 `b3115e675` 的 `compressionWriter.start`。示例头部使用规范小写指令 `Cache-Control: no-transform`，不依赖当前未提交工作区。
- 修复门槛：在压缩决策前实现与 Node 一致的 `no-transform` 检查，并补充大小超过阈值、大小低于阈值和多指令 `Cache-Control` 的 golden；确认头部与正文均一致后再关闭该子项。

## 已确认子项：Go 未短路 HEAD 响应的压缩

- 对照事实：Node `compression` 在响应头阶段先完成过滤、变换许可和阈值判断，随后明确检查 `req.method === 'HEAD'` 并执行 `nocompress`；即使 handler 写入了足够大的正文或声明了大 `Content-Length`，HEAD 响应也不会设置 `Content-Encoding`。
- 历史 Go：`CompressionMiddleware` 只按 `Accept-Encoding` 决定是否包装 writer，`compressionWriter.start` 没有读取 `r.Method`。HEAD 请求的 handler 一旦写入达到阈值，或先声明 `Content-Length >= 1024`，就会调用 `beginGzip` 设置 `Content-Encoding: gzip`。
- 可观察结果：同一 HEAD 请求 Node 返回未压缩的头部语义，Go 可能返回 gzip 编码头（正文即使被 net/http 丢弃也不改变该头部差异）。客户端按 HEAD 语义复用响应头、代理缓存或后续 GET 校验时会得到不同的编码协商结果。
- 证据范围：Node 依赖 `backend/node_modules/compression/index.js` 的 HEAD 分支；Go 证据为提交 `b3115e675` 的 `CompressionMiddleware` 与 `compressionWriter.start`，其中没有方法门禁。该结论不依赖当前未提交工作区。
- 修复门槛：在 Go 压缩包装入口或响应头决策处实现与 Node 一致的 HEAD 短路，并补充“HEAD + 大声明长度”“HEAD + 大写入量”以及对应 GET 对照 golden。

## 已确认子项：小响应和未协商响应缺少 Node 的 `Vary`

- 对照事实：Node `compression` 在过滤和 `no-transform` 检查通过后、阈值判断之前调用 `vary(res, 'Accept-Encoding')`。因此可压缩但低于 1024 字节的响应，以及没有 `Accept-Encoding` 或协商最终选择 identity 的响应，仍会带 `Vary: Accept-Encoding`。
- 历史 Go：`compressionWriter.beginGzip` 只有在实际启动 gzip 时才 `Header().Add("Vary", "Accept-Encoding")`；请求不接受 gzip 时 middleware 直接旁路，缓冲后低于阈值的响应也不添加 `Vary`。
- 可观察结果：例如 `text/plain` 的 100 字节响应，Node 返回未压缩正文并带 `Vary: Accept-Encoding`，Go 返回同样正文但没有该头；缓存代理据此得到不同的缓存键/协商元数据，后续相同 URL 的编码复用行为会偏离。
- 证据范围：Node 依赖 `backend/node_modules/compression/index.js` 的 `vary` 调用位于阈值和编码协商之前；Go 证据为提交 `b3115e675` 的 `beginGzip` 和 `CompressionMiddleware`。该结论不依赖当前未提交工作区。
- 修复门槛：按 Node 的过滤/变换时序补齐 `Vary` 合并语义（包括低于阈值、无 Accept-Encoding、identity 协商），并增加响应头 golden；不得只在 gzip 已启动时补写。

## 已确认子项：Go 未回写 Node 契约要求的 `x-trace-id`

- 对照事实：Node `requestContextMiddleware` 先从 `traceparent`、`x-trace-id`、`x-correlation-id` 归一化或生成 trace ID，然后对每个响应执行 `res.setHeader('x-trace-id', traceId)`；因此成功、业务错误和网关错误都能通过响应头把请求 trace 关联回客户端。
- 历史 Go：`RequestContextMiddleware` 只把 trace ID 放进 request context，未对 `http.ResponseWriter` 设置 `x-trace-id`；`Kernel.Handler` 的其余包装也没有统一回写该头。即使 Go 已生成 UUID 或接受了合法 `traceparent`，普通管理/公开响应仍缺少该响应头。
- 可观察结果：同一请求 Node 返回可供客户端日志关联的 `x-trace-id`，历史 Go 返回没有该头；客户端按响应头关联异步错误、支持工单追踪或重试时无法得到同一 trace 标识，导致外部可见诊断行为偏离。
- 证据范围：Node 历史 `backend/src/shared/request-context.ts` 的 `res.setHeader`；Go 证据为提交 `b3115e675` 的 `ctx.go` 与 `kernel.go`，其中没有任何响应头回写。该结论不依赖当前未提交工作区。
- 修复门槛：在共享 kernel 的响应提交前统一写入归一化/生成的 trace ID，并补充无入站 trace、合法 `traceparent`、业务 4xx/5xx 和路由 404 的响应头 golden；不能只在个别 chat handler 中补写。

## 已确认子项：Go 丢失 `x-trace-id`/`x-correlation-id` 入站回退

- 对照事实：Node `normalizeTraceId` 先尝试严格解析 `traceparent`；解析失败或缺失时，按顺序取第一个合法的 `x-trace-id`，再取 `x-correlation-id`，并把该值作为当前请求 trace ID。
- 历史 Go：`RequestContextMiddleware` 只把 `r.Header.Get("traceparent")` 传给 `normalizeTraceID`；当该头缺失或无效时直接生成 UUID，没有读取两个兼容回退头。
- 可观察结果：请求带 `x-trace-id: client-chain-42`（或仅带 `x-correlation-id: client-chain-42`）时，Node 会沿用 `client-chain-42` 记录、响应和下游审计，Go 会新建另一条 trace；跨服务日志、客户端重试和故障工单无法按原链路合并。
- 证据范围：Node 历史 `backend/src/shared/request-context.ts` 的 `normalizeTraceId`/`normalizeHeaderId`；Go 证据为提交 `b3115e675` 的 `ctx.go`，仅解析 `traceparent`。该结论不依赖当前未提交工作区。
- 修复门槛：补齐相同顺序、字符集、长度和逗号首值规则，并增加“无效 traceparent + 合法 x-trace-id”“仅 correlation-id”“非法回退头” golden，确认最终 trace ID 与响应头/日志一致。

## 已确认子项：Go 接受 Node 会拒绝的非法 `traceparent`

- 对照事实：Node `parseTraceParent` 只接受严格四段格式 `version-traceid-parentid-flags`，各段长度固定为 2/32/16/2 个十六进制字符；拒绝版本 `ff`、全零 trace ID 和全零 parent ID，格式失败后才进入兼容回退头。
- 历史 Go：`normalizeTraceID` 仅要求以 `-` 分割后至少三段、第二段恰好 32 个十六进制字符；不校验版本、parent ID、flags 的长度/字符、全零值，也接受额外段。收到这些非法头时会直接采用其中的 trace ID，不会像 Node 一样回退或生成新 ID。
- 可观察结果：例如 `ff-<32hex>-<16hex>-01`、`00-<32hex>-0000000000000000-01`、`bad-<32hex>-x` 或带第五段的值，Node 不采纳并按回退规则处理，Go 却把 `<32hex>` 当成当前 trace。无效传播上下文会污染日志关联、跨服务采样和重试链路。
- 证据范围：Node 历史 `backend/src/shared/request-context.ts` 的正则与零值/`ff` 检查；Go 证据为提交 `b3115e675` 的 `normalizeTraceID`。该结论不依赖当前未提交工作区。
- 修复门槛：按 Node 的四段、版本、ID 全零和 flags 规则实现严格解析，并增加上述非法样例与合法大小写样例的 golden，验证失败后回退/生成路径也一致。

## 已确认子项：管理面 CSP 在 Go 放宽了 `script-src`

- 对照事实：Node `managementHeaders` 的 CSP 明确为 `script-src 'self'`，只允许同源脚本；同一策略仅在 `style-src` 保留 `'unsafe-inline'` 以兼容样式。
- 历史 Go：`managementHeaders` 将 `script-src` 写成 `script-src 'self' 'unsafe-inline'`，把内联脚本执行加入允许集合。
- 可观察结果：相同管理页响应在 Node 会阻止未带 nonce/hash 的 inline `<script>`，Go 会放行；若管理端页面、错误页或注入内容包含内联脚本，浏览器执行边界发生变化，防护强度和客户端行为不再一致。
- 证据范围：Node 历史 `backend/src/shared/http-security.ts` 第 12 行的 CSP；Go 证据为提交 `b3115e675` 的 `security.go` 第 8–12 行。该结论不依赖当前未提交工作区。
- 修复门槛：恢复 `script-src 'self'`，保留其他 directive 的原值，并以管理页面、错误响应和内联脚本阻断/允许矩阵做浏览器或 CSP 解析 golden；未通过前不能宣称安全头迁移等价。

## 已确认子项：畸形 JSON 会在 Go mutation guard 中先占用去重键

- 对照事实：Node `system-api-app.ts` 先挂载 `express.json(...)` 与 `handleJsonBodyError`，再进入各路由的 `mutationGuard`。畸形 JSON 在 parser 阶段直接返回 400，guard 不会执行 fingerprint 或 claim；同一请求重复发送仍得到 parser 的 400。
- 历史 Go：`MutationGuardMiddleware` 先 `io.ReadAll` 并尝试 `json.Unmarshal`，但忽略 unmarshal 错误，仍把原始 body 恢复后继续执行 fingerprint/`store.Claim`。下游 handler 再调用 `DecodeJSON` 时才返回 400，已产生的 claim 则按失败短 TTL 保留。
- 可观察结果：对 `POST` 创建类端点发送同一畸形 JSON（例如 body 为 `{`）时，Node 首次和短时间内的重复请求都返回 400；Go 首次返回 400 但会留下 `failed` 去重项，短 TTL 内第二次先被 guard 拦截为 409“请求刚刚失败”。这会把客户端的参数错误改写成重复提交错误。
- 证据范围：Node 历史 `backend/src/modules/system-api/system-api-app.ts` 的 parser 顺序、`mutation-guard.middleware.ts` 的 claim 位置；Go 证据为提交 `b3115e675` 的 `dedupe.go` 第 255–289 行。该结论不依赖当前未提交工作区。
- 修复门槛：让 JSON 解析/大小错误在 claim 前以 Node 同样的 400/413 收口；补充首发与重复畸形请求、合法空 body、合法 JSON 的状态码和去重状态 golden，确认无错误输入会污染去重缓存。

## 已确认子项：超大 body 会在 Go mutation guard 中先占用去重键

- 对照事实：Node 对系统 API 在进入路由前使用 `express.json({ limit: '256kb' })`，`handleJsonBodyError` 在超过上限时直接返回 413；mutation guard 位于 parser 之后，不会为超大请求创建 claim。
- 历史 Go：kernel 先用 `http.MaxBytesReader` 包装 body，但 `MutationGuardMiddleware` 的 `io.ReadAll(r.Body)` 忽略 `MaxBytesError`，仍继续执行 fingerprint 和 `store.Claim`；下游 `DecodeJSON` 才再次读到 sticky 的超限错误并返回 413。
- 可观察结果：同一超大 JSON 首次请求在 Node 和 Go 都可能返回 413，但 Go 已留下 `failed/processing` 去重项，短 TTL 内重复请求会先被 guard 返回 409“请求刚刚失败”；Node 重复请求仍由 parser 返回 413。错误请求因此污染去重容量并改变客户端重试状态码。
- 证据范围：Node 历史 `backend/src/modules/system-api/system-api-app.ts` 第 129 行及 `handleJsonBodyError` 链；Go 证据为提交 `b3115e675` 的 `kernel.go` body limit 与 `dedupe.go` 第 255–289 行。该结论不依赖当前未提交工作区。
- 修复门槛：在任何 claim 前识别并收口 `MaxBytesError` 为 Node 同样的 413，保证 body 未成功读取时不执行 fingerprint/claim；补充刚好等于上限、超过 1 字节和重复发送的状态/缓存 golden。

## 已确认子项：Go `DecodeJSON` 接受 Node 会跳过的非 JSON media type

- 对照事实：Node `express.json()`/`body-parser` 默认 `type` 为 `application/json`，只对匹配的 JSON media type（含 `application/*+json`）读取并解析；`Content-Type: text/plain`、`application/octet-stream` 等请求会跳过 parser，路由随后看到未解析的 body 并按参数缺失返回 400。
- 历史 Go：`DecodeJSON` 直接 `io.ReadAll` 后 `json.Unmarshal`，没有检查 `Content-Type` 或 media type；只要正文是合法 JSON，即使请求声明 `text/plain` 也会填充 target 并继续业务处理。
- 可观察结果：对创建/更新端点发送合法 JSON 但 `Content-Type: text/plain` 时，Node 不解析并返回参数校验 400，Go 解析后可能成功写入或返回业务成功；客户端错误输入因此从拒绝变成接受，响应状态和持久化副作用均发生偏离。
- 证据范围：Node 历史 `backend/src/modules/system-api/system-api-app.ts` 的 `express.json` 挂载及 `body-parser@1.20.5` `json.js` 的默认 type-check；Go 证据为提交 `b3115e675` 的 `DecodeJSON`（第 127–150 行）未读取 Content-Type。该结论不依赖当前未提交工作区。
- 修复门槛：在解析前按 Node 的 media type 匹配规则决定是否解析，并补充 `application/json`、`application/problem+json`、`text/plain`、缺失 Content-Type 与合法/非法 JSON 的状态及副作用 golden。

## 已确认子项：Go 压缩协商仅支持 gzip，漏掉 Node 的 br/deflate

- 对照事实：历史 Node `compression` 在运行时支持 `br`、`gzip`、`deflate`、`identity`，按 `Accept-Encoding` 权重选择首选编码；在响应体达到 1024 字节且客户端仅声明 `br` 或 `deflate` 时，会分别返回对应 `Content-Encoding`。
- 历史 Go：提交 `b3115e675` 的 `CompressionMiddleware` 只在 `acceptsGzip` 返回 true 时包装响应，`acceptsGzip` 仅匹配 `gzip` token；`Accept-Encoding: br` 或 `Accept-Encoding: deflate` 会直接旁路，既不压缩也不设置 `Vary`。
- 可观察结果：同一 2048 字节 `text/plain` 响应，Node 在 `br`/`deflate` 客户端上分别返回 `Content-Encoding: br`/`deflate` 和压缩正文，Go 返回 identity 正文。支持这些编码的客户端会看到响应体积、编码头和缓存变体集合不同；若上游/回放断言协商结果，Go 会把可接受编码错误地降级为未压缩。
- 运行证据：在仓库历史 Node 依赖上启动 `compression({threshold:1024})`，实测 `Accept-Encoding: br` 返回 `ce=br`、`Accept-Encoding: deflate` 返回 `ce=deflate`、`Accept-Encoding: gzip` 返回 `ce=gzip`；Go 分支由 `acceptsGzip` 的单一 token 判断可静态确定前两者不进入压缩路径。该结论不依赖当前未提交工作区。
- 修复门槛：实现与 Node 一致的 `br`/`gzip`/`deflate`/`identity` 协商和权重处理（或明确冻结并验证等价的受支持编码集合），补充三种编码、并列权重、不可接受和大响应 golden；在响应头与正文编码均一致前不得关闭 K1 压缩契约。

## 已确认子项：Go 对无 Content-Length 的分块响应错误执行 1024B 阈值

- 对照事实：历史 Node `compression` 在没有 `Content-Length` 的响应上，第一次 `res.write()` 会先提交响应头；此时长度估算为空，不会命中“低于阈值”分支，随后按协商结果立即创建压缩流。也就是说，分块响应即使首块只有 1 字节，后续仍以 gzip/br/deflate 流输出。
- 历史 Go：提交 `b3115e675` 的 `compressionWriter.Write` 在第一次写入时调用 `decide(c.buffer.Len()+len(body))`；由于 `buffer` 从未接收首块，首个小分片（例如 1 或 600 字节）低于 1024 时直接写原文并将 `bodyWritten` 置为 true，后续写入不再重新评估，也不会启动 gzip。
- 可观察结果：同一无 `Content-Length`、分两次写出的响应（首块 1 字节、末块 1 字节），Node 在 `Accept-Encoding: gzip` 下返回 `Content-Encoding: gzip` 的压缩流，Go 返回 identity 且没有 `Vary`；SSE 之外的 chunked 大响应会出现编码头、正文字节和客户端解码路径偏离。该差异与单次 `Write` 大于阈值的 happy path 不同，现有 K1 测试未覆盖。
- 运行证据：仓库历史 Node 依赖实测 `res.write('a'); res.end('b')`、无 `Content-Length`、`Accept-Encoding: gzip` 返回 `ce=gzip`；Go `Write`/`decide` 的首块判断和 `bodyWritten` 单次门禁可静态确定相同请求不会压缩。该结论不依赖当前未提交工作区。
- 修复门槛：按 Node 的 `Content-Length`/首写时序重建分块响应决策，区分“明确小 Content-Length 的 end 响应”和“未知长度的 chunked 响应”；补充首块小于阈值、后续累计超过阈值、首写即 flush 及显式小/大 `Content-Length` 的 Node/Go golden。

## 已确认子项：Go `Flush()` 提前锁定 identity，Node 不会

- 对照事实：历史 Node `compression` 暴露的 `res.flush()` 只有在压缩流已经创建后才刷新该流；尚未写入正文时调用 `res.flush()` 不会提交响应头，也不会放弃后续压缩。随后 `res.end` 写入大正文仍按 `Content-Length`/首写时序协商编码。
- 历史 Go：提交 `b3115e675` 的 `compressionWriter.Flush` 在 `!headerPassed` 时立即调用 `WriteHeader(200)`、`decide(0)` 并把 `bodyWritten` 置为 true。此时总量为 0，必然不创建 gzip；后续 `Write` 直接走原文路径，无法重新决策。
- 可观察结果：同一请求先调用 `Flush()`、再结束 2048 字节 `text/plain` 正文，Node 在 `Accept-Encoding: gzip` 下返回 gzip，Go 已提交的 identity 头和正文保持未压缩；依赖 flush 提前发送或统一封装响应的非 SSE handler 会出现编码、体积和缓存变体差异。
- 运行证据：仓库历史 Node 依赖实测“`res.flush(); res.end('x'.repeat(2048))`、无 `Content-Length`、`Accept-Encoding: gzip`”返回 `ce=gzip`；Go `Flush` 的 `decide(0)`/`bodyWritten` 分支可静态确定相同序列不会压缩。该结论不依赖当前未提交工作区。
- 修复门槛：保持 `Flush` 在无压缩流时不提交最终压缩决策，按 Node 的首写/Content-Length 时序延迟头部；补充 flush-before-write、flush-after-small-write、flush-after-large-write 及 SSE 过滤场景的 Node/Go golden。

## 已确认子项：Go 内核前缀中间件缺少 Express 路径段边界

- 对照事实：历史 Node 通过 `app.use('/__aisys__/api', ...)` 挂载 system API 的 body limit、限流和 `no-store` 中间件。Express mount 按路径段匹配：`/__aisys__/api`、`/__aisys__/api/`、`/__aisys__/api/x` 命中，而相邻路径 `/__aisys__/apix` 不命中。
- 历史 Go：提交 `b3115e675` 的 `prefixMiddleware` 和 `bodyLimitMiddleware` 统一使用 `strings.HasPrefix(r.URL.Path, prefix)`，没有检查路径结束或下一个字符是否为 `/`。因此 `/__aisys__/apix` 也会进入 system API 的 `no-store`、body limit 和可选 IP 限流链。
- 可观察结果：访问未挂载的相邻路径 `/__aisys__/apix` 时，Node 保持普通路由行为和普通缓存头，Go 错误套用 system API 横切策略；若该路径后续被其他公开/静态 handler 接管，响应的 `Cache-Control`、请求体读取上限和限流桶都会发生偏移，错误路径也可能被提前拒绝。
- 运行证据：仓库历史 Express 运行探针实测 `/__aisys__/api`、`/__aisys__/api/`、`/__aisys__/api/x` 命中 mount，`/__aisys__/apix` 返回未命中；Go 前缀判断可由提交 `b3115e675` 的 `prefixMiddleware`/`bodyLimitMiddleware` 静态确定会命中。该结论不依赖当前未提交工作区。
- 修复门槛：抽取与 Express mount 等价的路径段匹配函数，覆盖精确前缀、斜杠子路径、相邻前缀、空/根前缀和 URL 编码边界；分别验证 management、system API、public API 的所有横切中间件不越界。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 定向测试 | K1 内核现有测试 | `go test ./internal/kernel -count=1` | 正常路径通过 | 现有测试通过，未覆盖边界矩阵 | 部分通过 |
| 压缩回归 | 协商、过滤、阈值、多次 Write、HEAD、Vary | 构造请求并对照 Node | 头部与正文完全一致 | 多项差异已由代码确认 | 未通过 |
| trace 回归 | fallback、严格 traceparent、响应头 | 构造请求并对照 Node | trace ID 与响应头一致 | 多项差异已由代码确认 | 未通过 |
| body/dedupe 回归 | parser 顺序、Content-Type、畸形/超大 body | 构造重复 mutation | 首次/重试状态与 Node 一致 | Go 可能先 claim 后返回 400/413/409 | 未通过 |
| 生产接线 | gateway `main.go` | 静态调用链检查 | kernel 在真实业务入口生效 | 当前未接线 | 未通过 |

## 复发记录

- 时间：无
- 环境：无
- 现象：无
- 关联处理：无

## 下次遇到

- 先查什么：先锁定 Node middleware 的执行顺序和标准依赖行为，再看 Go 是否只覆盖 happy path。
- 重点看什么：`Accept-Encoding`、Content-Type、缓存指令、HEAD、trace fallback、body parser 与 dedupe claim 的先后。
- 如何避免误判：正常单次写入和单元测试通过，不能证明 HTTP 横切契约等价；必须做边界回放。

## 完成总结

- 完成时间：待修复
- 结论：K1 内核当前存在多项已确认的行为偏差，不能作为后续完整迁移的合格基础。
- 后续建议：先修复并建立 Node/Go HTTP golden，再重审 K2 及各管理切片。
