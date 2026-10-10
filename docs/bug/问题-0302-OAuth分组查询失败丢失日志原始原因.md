# BUG-0302 OAuth 分组查询失败丢失日志原始原因

## 基本信息

- 状态：已修复（2026-10-10，待发布验证）
- 严重程度：P3
- 发现时间：2026-10-09
- 模块：gateway / OAuth 管理 / 错误日志
- 关联：[全面审查复核与残留问题报告](../reports/全面审查复核与残留问题报告-2026-10-09.md)

## 现象、触发条件与根因

`internal/oauthmgmt/routes.go` 的通用 OAuth 建户 `handleCreate`（第 605–607 行）和 Grok SSO 导入（第 1005–1007 行）先成功解析 provider profile，再执行分组查询。分组查询返回 `groupErr != nil` 时，两处都把前一步的外层 `err` 传给 `kernel.WriteErrorCause`。

前一步若 `err != nil` 已提前返回，所以到达这两处时外层 `err` 必为 nil。`internal/kernel/envelope.go` 的 `WriteErrorCause` 仅在 `cause != nil && status >= 500` 时调用 `RecordFailureReason`。因此真实分组查询错误不会进入该请求的 `failureReason` 日志。

客户端仍收到通用 HTTP 500；本问题影响故障定位，不会因此放行无效分组，也没有证据表明客户端会收到数据库错误。先前报告将影响表述为错误响应丢失原因不够准确：客户端按契约始终应只收到通用文案。

## 验证记录

- 本轮逐行核对两条控制流，确认外层 err 在分组错误臂为 nil；核对 `WriteErrorCause` 的实际记录条件。
- 分组数据库失败是触发条件；没有连接真实数据库制造故障。
- 现有 `w14d_final_gaps_test.go` 的 `TestW14dCreateGroupServerErrorArm` 通过删除隔离 SQLite 测试库的 `groups` 表触发该错误，但只断言 HTTP 状态与通用文案；本轮未运行该测试。
- 修复验收可复用这一隔离测试或按查询顺序注入数据库 Mock，断言 HTTP 500 保持通用文案，同时请求上下文 / 完成日志的 `failureReason` 保留该原始错误。不能仅检查 HTTP 状态码。

## 待修复方案

两处分组错误分支传递 `groupErr`，补充对应日志断言。实施记录见下节。

## 修复记录（2026-10-10）

- 实施：`routes.go:608`（handleCreate）与 `routes.go:1009`（ssoToOAuth/grok SSO 导入）cause 实参 `err` → `groupErr`，各加一行注释（对齐 `internal/accounts/m11_routes.go` 同类先例措辞）；`rg WriteErrorCause` 核实包内仅此两处调用。
- 测试：新增 `bug0302_group_failure_reason_test.go`——经 `kernel.SetRequestEventSink` 装记录型 sink（`t.Cleanup` 复位；包内无 `t.Parallel`）捕获完成日志，两条路径各自 `DROP TABLE groups` 后断言：HTTP 500 + 通用文案"服务器内部错误" + 响应体不含原始错误 + 完成日志 `failureReason` 含 "no such table: groups"。用例 A 复用 create-from-code 既有故障手法；用例 B 为 grok sso-to-oauth 路径首个 groups 故障测试，最小合法请求构造依据：路由 `routes.go:51-53` + grok 计划 `plans.go:365-374` + `seedOAuthCatalog` 种入 `profile_xai_openai_v1`，分组查询臂先于任何 SSO 传输调用。红-绿实证：实参临时改回 `err` 时两用例即失败（failureReason 为空），恢复后通过。
- 验证：`go test -p 1 ./internal/oauthmgmt` 全包绿；独立复审确认断言链无旁路（`failureReason` 唯一写点为 `RecordFailureReason`，唯一调用者 `WriteErrorCause`）。
