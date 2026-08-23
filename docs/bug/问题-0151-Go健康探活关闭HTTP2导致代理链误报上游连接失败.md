# BUG-0151 Go 健康探活关闭 HTTP/2 导致代理链误报上游连接失败

## 基本信息

- 编号：BUG-0151
- 状态：已修复（Go 定向/全量测试与隔离生产凭据验证通过；待发布到生产）
- 严重程度：P1
- 发现时间：2026-08-23
- 发现方式：用户反馈 / 隔离环境复现
- 模块：backend-go / J1 AI 健康探活 / SOCKS5H 代理
- 关联计划：无
- 关联 bug：无
- 责任人：主 agent / 维护者

## 问题概述

- 现象：Node 迁移到 Go 后，AI 健康监控把可正常经网关访问的账户大量标为“不可用”，Go J1 结果集中出现 `upstream_connection` 或 `upstream_connection_closed`。
- 期望：经现有 SOCKS5H 出口访问 OpenAI-compatible 上游时，探活应按 TLS 协商出的协议读取响应，并把真实 HTTP 状态/协议结果写入 J1。
- 实际：Go 探活在自定义 `DialContext` 下显式关闭 HTTP/2；出口代理返回 HTTP/2 SETTINGS 二进制帧，Go HTTP/1 解析器将其判为连接失败。
- 影响范围：使用 SOCKS5/SOCKS5H 或其他自定义拨号器的 Go J1 探活；网关实际请求链路未被本修复改动。

## 复现步骤

1. 在隔离数据库导入同一组生产账户、代理和协议配置，不导入生产状态或历史结果。
2. 通过 SSH 本地端口转发接入生产 SOCKS5H 出口，运行 Go J1。
3. 使用 `ForceAttemptHTTP2=false` 的探活传输访问 `https://api.openai.com/v1/models`。
4. 观察 `malformed HTTP response`（HTTP/2 SETTINGS）被分类为 `upstream_connection`。

## 环境信息

- 分支 / 版本：当前工作区；Go `1.26.0`；Windows 开发机；隔离 PostgreSQL scratch 数据库
- 数据状态：仅导入 huanmin 的 3 个指定账户、1 个代理、1 个分组及必要协议档案；凭据在内存中从生产密钥重加密为 dev 密钥
- 是否稳定复现：是

## 根因分析

- 表象：页面显示最近检查失败或不可用。
- 真实根因：J1 为支持 SOCKS5H 设置了自定义拨号器，却沿用 `ForceAttemptHTTP2=false`。该组合不能正确消费代理链协商出的 HTTP/2 响应，导致协议层错误被误报为上游连接错误。
- 为什么会发生：Node 原链路具备 HTTP/2/代理兼容能力；Go 迁移时把“关闭 HTTP/2 以保持线协议稳定”写成了固定策略，没有覆盖自定义拨号器与 h2 出口组合。

## 修复方案

- 修改点：将传输能力收口到 `backend-go/shared/platform/upstreamhttp`，统一启用 `ForceAttemptHTTP2=true`、禁用直连环境代理、设置响应头上限/请求头超时、禁止隐式重定向，并把 SOCKS5/SOCKS5H 握手实现从 J1/J2/J3a 三处迁入共享层。J1、J2、J3a 的业务请求和协议解析仍留在各自项目。
- 行为影响：TLS ALPN 协商到 HTTP/2 时由共享 transport 正确读取；未协商 HTTP/2 的上游仍使用 HTTP/1.1。J2 直连不再读取 `HTTP(S)_PROXY` 环境变量；J3a 保留 Node 代理探测的压缩、CONNECT 头和 SOCKS5H 远程解析语义。错误分类、探活请求体和状态投影不变。
- 发布异常处理：本次未执行生产发布或生产写操作；需按现有 GitOps candidate/handover 流程发布后再做生产验证。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- |
| Go 定向测试 | J1 探活与 HTTP/2 自定义拨号器回归 | `go test ./internal/accounthealth` | 通过 | 通过 |
| Go 全量测试 | jobs 项目所有包 | `go test ./...` | 通过 | 通过 |
| 共享传输测试 | `shared/platform/upstreamhttp` 的 HTTP/2、代理校验、重定向、SOCKS5/SOCKS5H 认证与解析模式 | `go test ./...`（`backend-go/shared/platform`） | 通过 | 通过 |
| J2/J3a 传输回归 | 余额直连 transport、J3a SOCKS transport 的 HTTP/2/头限制/代理语义 | `go test ./internal/accountbalance ./internal/accounthealth ./internal/proxylatency` | 通过 | 通过 |
| Go 全项目静态检查 | gateway/jobs/maintenance/shared 模块 | 各模块分别执行 `go test ./...`、`go test -race ./...`（有测试的模块）和 `go vet ./...` | 全部通过；workspace 根目录直接执行 `go test ./...` 不适用，因为根目录不是 go.work module | 通过 |
| 隔离直连验证 | 同一组 huanmin 账户、同一出口代理 | 修复前 Go 3 个账户均为连接类失败；修复后 aierxin / 神影为 HTTP 200 `complete_success` | 通过 |
| 隔离调度验证 | Go J1 写入 outcomes/current_state | 2 个账户回到 `active`，`failure_count=0`；mdkj 返回真实 `403 INSUFFICIENT_BALANCE` | 通过 |
| 生产变更边界 | 生产数据库与生产配置 | 仅只读导出；未写生产、未复制生产 secret 到 dev 文件 | 通过 |

## 下次遇到

- 先检查是否使用自定义 `DialContext`、SOCKS/HTTP 代理和 TLS ALPN；不要只看 `upstream_connection` 汇总文案。
- 对 Go transport 的 `ForceAttemptHTTP2`、`TLSNextProto` 和代理出口做真实协议回归，不能只用本地 HTTP/1 `httptest`。
- 账户返回真实 HTTP 4xx 时保留状态码和上游错误（本次 mdkj 的 `403 INSUFFICIENT_BALANCE` 不应再被隐藏成连接失败）。

## 全项目复查（2026-08-23）

- `gateway` 当前只有上游审计/流式契约与 listener/Store 基础设施，尚未接入生产上游 HTTP client；`maintenance` 当前没有外部上游请求，二者未发现需要迁移的重复 transport。
- `jobs` 的 J1 健康探活、J2 余额查询和 J3a 代理延迟/手动出口检测均改用 `shared/platform/upstreamhttp`；供应商请求体、OpenAI Responses SSE、余额适配器、代理探测 header 和结果错误码仍由业务包维护。
- `gateway` 与 `jobs` 原先重复的 PostgreSQL pool registry 已收口到 `shared/platform/sqlpool`，两侧只保留各自的 `pgx`/driver opener 薄适配层；`maintenance` 当前无数据库 pool registry 或上游请求实现。
- 复查扫描已确认生产代码中不再存在 J1/J2/J3a 各自的 SOCKS5 握手、上游 client 包装或关闭 HTTP/2 的 transport；上游响应的有界读取/排空已进入 shared，SSE/JSON 字段解析和 gateway 本地输入读取仍保持各自协议边界。

## 完成总结

- 完成时间：2026-08-23
- 结论：已修复 Go J1 在 SOCKS5H/HTTP2 出口上的系统性误报；生产发布和生产页面复核仍待用户授权的发布窗口。
- 后续建议：发布后抽样核对相同账户的 HTTP 状态、J1 outcome、账户状态和实际网关调用，不要仅依据页面红色色块判断可用性。
