# BUG-0242 aipublic 捕获层无界请求体缓冲

## 基本信息

- 编号：BUG-0242
- 状态：已修复（2026-09-30 批次）
- 严重程度：P2（未认证可达的内存耗尽 DoS 面）
- 发现时间：2026-09-30
- 发现方式：全面审查（多子代理复审+主代理核验）
- 模块：后端（gateway / aipublic）
- 关联计划：无
- 关联 bug：无
- 责任人：待定

## 问题概述

- 现象：公网未认证客户端可对 `/__aipublic__` 路径提交任意大小 body，每请求全量内存缓冲。
- 期望：public 链路请求体有大小上限（Node 版对 publicApiPrefix 链路为 256kb json limit，超限 413）。
- 实际：`aipublic/capture.go` 对每个 POST/PUT/PATCH 无上限 `io.ReadAll(r.Body)`；401 请求同样全量读（capture 包装位于 bearer 鉴权之前）。
- 影响范围：gateway 公开面（`/__aipublic__` 全路径，公网经 Caddy 兜底转发全路径可达）；可被未认证客户端用于内存耗尽。

## 复现步骤

1. 向公网 `/__aipublic__` 任意 POST 端点提交大 body（无需有效凭据，401 亦触发全量读）；
2. gateway 进程内存随请求体大小线性增长；
3. 并发放大可致内存耗尽。

## 环境信息

- 分支 / 版本：master
- 数据状态：无数据影响（内存面缺陷）
- 是否稳定复现：是

## 根因分析

- 表象：公开面请求体无上限缓冲。
- 真实根因：`aipublic/capture.go`（约 :114）对每个 POST/PUT/PATCH `io.ReadAll(r.Body)` 全量缓冲且无上限；kernel 的 `http.MaxBytesReader` 只套 SystemAPIPrefix（`/__aisys__/api`），`/__aipublic__` 不在覆盖范围；capture 包装位于 bearer 鉴权之前，401 请求同样全量读。
- 为什么会发生：Node 版对 publicApiPrefix 链路有 256kb json limit（超限 413），Go 迁移未覆盖该语义，属迁移回归。

## 修复方案

- `bufferCaptureRequestBody` 加 LimitReader 上限（对齐 Node 256kb 语义）并处理超限行为（超限不再继续缓冲，按对应错误返回）。

## 验证结果

- Node 契约查证：system-api-app.ts:69 `systemApiJsonBodyLimit='256kb'`（262144B）、:129 限流在鉴权前、超限 413 `{"message":"请求体过大"}`；public-api-log-capture.middleware.ts:286-300 reason=`request_body_too_large`。
- 修复落点：`capture.go` 加 `captureRequestBodyLimitBytes=256*1024` 常量；`bufferCaptureRequestBody` 改 LimitReader(limit+1) 有界读，超限返回 tooLarge；`withCapture` 超限时 `kernel.WriteError` 413 "请求体过大" 并跳过 guard/handler，capture 记 BodyRejected（经既有映射产出 `request_body_too_large` dropped）。
- 新增 `capture_test.go` 2 用例——通过：
  - 超限 body → 413 + guard/handler 未执行；无 token 请求同样 413（鉴权前生效）；
  - 恰好 262144B 边界通过（正常捕获）。
- aipublic 包全量测试 4.138s ok；vet 通过。

## 防回归与观测性备注

- 超限 413 与 262144B 边界双向测试已固化；无 token 413 用例覆盖鉴权前路径。
- 已知残留：`Deps.Capture==nil` 分支（生产恒装配，仅 nil-safety 形态）仍无界。
