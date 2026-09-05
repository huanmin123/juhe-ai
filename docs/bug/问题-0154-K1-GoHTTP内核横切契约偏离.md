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
  1. `compression.go` 只按 token 接受 gzip，忽略 `q=0`/权重、br/deflate、compressible Content-Type、`Cache-Control: no-transform` 和 HEAD；只在单次首次写入达到 1024B 时压缩，多次小写入不会累计；不压缩响应也不会按 Node 保留 `Vary: Accept-Encoding`。
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
