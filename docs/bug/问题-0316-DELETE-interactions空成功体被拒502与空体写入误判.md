# BUG-0316 DELETE interactions 空成功体被拒（502 与本地失败误判）

## 基本信息

- 编号：BUG-0316
- 状态：已修复（2026-10-14，待发布）
- 严重程度：P1（契约违反：DELETE interactions 任意 2xx 空体应零 usage 成功）
- 发现时间：2026-10-14
- 发现方式：BUG-0315 亲和接线的链集成验证（DELETE 生命周期步骤暴露）
- 模块：后端 / 网关 / 非流式响应处理 / 协议校验
- 关联计划：无
- 关联 bug：BUG-0315（同批交付）、BUG-0314（同批复审）
- 责任人：AI

## 问题概述

- 现象（两层，第二层被第一层掩盖）：
  1. `DELETE /v1beta/interactions/{id}` 上游 2xx（204/205）空响应体在第二道协议校验被拒 → 客户端收到 `502 upstream_protocol_error`「上游成功响应不是有效 JSON，无法满足请求协议」；
  2. 修复第一层后暴露：空体写入 `SendFullyBufferedNonStreamBody` 对不可带体状态（204/304/1xx）调用 `Write`，net/http 返回 `ErrBodyNotAllowed`（即使零长度）→ 该请求被链面按管道失败兜底误判为本地失败（AlreadyFinalized + GatewayLocalFailure）：客户端虽收到 204，但 usage 结算、审计与电路分类走失败臂。
- 期望（《Gemini账号接入.md》:189 契约）：DELETE 的任意 2xx（包括 204）允许空响应体，按零 usage 成功完成，不触发空响应惩罚、账号切换或重试。
- 实际：第一层使 DELETE 空 2xx 直接 502；第二层使通过校验的请求被误分类为本地失败。
- 影响范围：所有 DELETE interactions 且上游返回空 2xx 的请求（含 204）；GET / collection / cancel 与非 2xx 响应不受影响。

## 复现步骤

1. 以 `interactions_json` 端点模式账户 DELETE 一个已创建 Interaction，上游返回 204 空体。
2. 修复前：502 `upstream_protocol_error`（第一层）。
3. 仅修第一层：客户端 204，但链面按本地失败收尾（usage 失败行 / 审计分类错误，第二层）。
4. 两层全修后：204、零 usage 成功、正常结算。

## 根因分析

- 第一层：`inspectBufferedGatewayJSONResponse`（nonstreaminspection.go）有「DELETE interactions 空成功体放行」例外，但第二道 `ValidateBufferedJsonProtocolResponse` 校验点（nonstream.go）没有同源例外——两道校验例外不一致，空体按 `NonStreamJSONStatusEmpty != Valid` 判为「不是有效 JSON」。
- 第二层：`SendFullyBufferedNonStreamBody` 无条件 `Write(body)`；net/http（含 `httptest.ResponseRecorder`）对不可带体状态（204/304/1xx）的 `Write` 一律返回 `ErrBodyNotAllowed`（即使零长度，`server.go:1650` / `recorder.go:113`），错误上抛后被链面按管道失败兜底（响应已提交 → 503 不写出 → AlreadyFinalized + GatewayLocalFailure）。
- 为什么没被发现：第一层缺陷先于第二层触发（502 直接收尾，永远到不了写入），第二层被完全掩盖；既有测试无 DELETE 空 2xx 用例。

## 修复方案

- 修改点：
  - `internal/gatewayresponse/nonstream.go`：第二道协议校验补同源空体例外（`Empty + 上游 OK + successfulEmptyUpstreamAllowed`）；`SendFullyBufferedNonStreamBody` 空正文（`len(body) == 0`）跳过 `Write`，仅提交状态与 Flush（对齐 Node `res.send` 空体只提交状态不写体）。
- 语义说明：空体例外仍严格限定 DELETE interactions 2xx（`successfulEmptyUpstreamAllowed` 既有判定），不扩散；空正文跳过 Write 对非空体路径零影响（`OnChunkWritten(0)` 与 Flush 行为不变）。
- 回归测试：`TestInteractionAffinityDeleteEmptyBodyAllowed`（DELETE 204 空体 → 204 放行、无错误载荷）；链集成生命周期 DELETE 步骤（204 + 后续 GET 404 亲和已清理）。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 回归验证 | DELETE 204 空体放行单测 | `go test ./internal/gatewayresponse/ -run 'TestInteractionAffinityDeleteEmptyBodyAllowed' -count=1` | 绿 | 绿 | 通过 |
| 集成验证 | 链生命周期 DELETE 204 | `go test ./cmd/juhe-ai-gateway/ -run 'TestChainInteractionAffinityLifecycle' -count=1` | 绿 | 绿 | 通过 |
| 包测试 | gatewayresponse 全量 | `go test ./internal/gatewayresponse/ -count=1` | 全绿 | 全绿 | 通过 |
| 静态检查 | go vet + gofmt | `go vet ./internal/gatewayresponse/`；`gofmt -l` | 干净 | 干净 | 通过 |

## 复发记录

- 暂无。

## 下次遇到

- 先查什么：502 `upstream_protocol_error` 且上游为 2xx 空体时，检查两道协议校验的放行例外是否一致。
- 重点看什么：不可带体状态（204/304/1xx）上的任何 `Write` 调用（net/http 恒错，含零长度）；链面「客户端收到成功状态但结算走失败臂」的矛盾组合。
- 如何避免误判：空体成功是 DELETE interactions 的契约行为，不是异常；`ErrBodyNotAllowed` 不是上游问题。

## 完成总结

- 完成时间：2026-10-14
- 结论：DELETE interactions 空 2xx 契约恢复——两道校验例外对齐 + 空正文跳过 Write；单测与链集成回归绿。
- 后续建议：核查其他协议是否存在「2xx 空体 + 不可带体状态」的同类路径（当前仅 interactions DELETE 允许空 2xx）。
