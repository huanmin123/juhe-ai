# J3b 模型检测 L2 输入、结果与存储契约

> 冻结日期：2026-08-26。
> 状态：L2 数据契约已冻结，Go 已具备 profile、规范化 `IssuedInput`、durable input/claim/outcome Store、request/response parser、基础 item evaluator 与 run/item/observation store 基线；尚未接入 Go runtime、管理 API/SSE、scheduler、GitOps 或 Node 归档。本文件不授权切流。
> 上位边界：[J3b 模型检测完整迁移契约](J3b-模型检测完整迁移契约.md)。

## 1. 目标和非目标

本契约将 Node 当前的模型检测请求、持久事实和直接质量投影收敛为 Go `jobs` 的版本化输入与 outcome。它只定义 J3b 的可恢复执行单元，不以 Node HTTP、DB-service、IPC、队列或另一个 Go 服务作为实现依赖。

`account-quality-refresh` 的 usage 统计重算与 `account-quality-failure-precheck-queue` 的失败前置确认属于 J3c，不能读取、写入或重试 J3b input/outcome。J3b 则拥有模型检测产生的 run、item、observation、trust aggregation receipt/cursor、quality decision、enforcement、quality recovery 和 health-hour sync/retry。

## 2. 外部请求与签发输入

Go 管理入口保持现有 Node 外部请求域：

```json
{
  "targetType": "account",
  "targetId": "account-id",
  "model": "full-model-id",
  "profile": "quick|full",
  "trustedComparison": false,
  "trustedComparisonAccountId": "optional-account-id"
}
```

请求只接受严格对象，未知字段、空 target/model、非法 profile 必须返回 `400`。管理 scope、认证 actor、管理员权限、target 资格和 comparison 资格由 Go 在签发前解析；它们不能由客户端声明。JSON 与 SSE 使用同一份已签发 input，不得分别重新解析业务配置。

`IssuedInput` 至少包含以下不可变字段：

- `schema_version=1`、`input_id`、`identity`、`trigger_kind`（`manual|scheduled|quality_recovery`）、`issued_at`、`deadline_at`；
- actor/system account、target account、可选 comparison account 的 ID、逻辑删除状态、authorization scope、`config_revision` 与 provider protocol profile revision；
- requested model、mapped upstream model、quick/full profile、trusted comparison 设置、schedule ID（仅 scheduled）、policy snapshot/revision；
- 只可由 jobs 解开的 credential envelope、代理/endpoint 快照和每个 probe 的 protocol/payload version；
- SHA-256 `input_digest`，由以上规范化字段生成；不能含明文 API Key、token、cookie、代理密码、原始上游响应或用户请求正文。

同一 `input_id` 的重放必须同时匹配已签发版本和 digest；不同 immutable payload 一律拒绝，不得覆盖旧 input。同一 logical identity 的新快照分配下一个单调 `input_version`，旧版本不被覆盖。签发和执行前均须确认 target/comparison 未删除、未禁用且其 config/policy revision 未漂移；漂移产生明确 `stale` 事实，不发起旧快照的上游请求。

## 3. 执行、取消和 outcome

每个 input 有一个 owner lease 和单调 fence token。manual、scheduled、quality_recovery 使用同一执行/结果状态机；health-sync retry 是已完成 outcome 的投影重试，不创建新的 probe input。

run 状态固定为 `running|completed|failed|canceled`，item 状态固定为 `passed|warning|failed|skipped`，质量等级固定为 `high_confidence|likely|uncertain|suspicious|unavailable`。包装 HTTP status、上游 status、取消、超时、代理失败和协议 neutral 必须独立记录，不能折叠成单一失败文案。

outcome 按 `input_id + input_digest + fence_token` 提交，包含：

- run summary、所有 item、score/max score、trace ID、started/finished/observed time；
- request/result summary、probe set version、trust report、modelCheckUnverified 与 error class；
- policy snapshot、quality decision、enforcement result、health-sync 状态；
- observation ID 列表和 outcome digest。

提交只有在 lease/fence、input digest、target revision 和 policy revision 均有效时才能写入。重复的同 digest outcome 是幂等成功；过期 lease、fence 落后、配置漂移、删除/禁用和不同 digest 必须记录 stale/rejected，不能写回较新的状态。任意单 probe 失败只完成对应 item；token integrity 的两次非 200 只停止该 token probe 后续 round，不得取消其他 probe。

SSE 在输入签发成功后先发送 `: connected`，每 10 秒发送 `: heartbeat`，随后发送 `progress` 和 `complete|error`。客户端断开、`EPIPE`、显式 stop 或 jobs shutdown 取消该 input context；已持久化的终态仍可通过管理 read API 读取，不能因下游断开而丢失 outcome。

## 4. 表和 writer 边界

Go J3b 在 PostgreSQL 与 SQLite 两种正式模式都必须成为以下对象的唯一 writer：

| 事实 | Go J3b owner | 顺序约束 |
| --- | --- | --- |
| `model_check_runs`、`model_check_items` | input/outcome store | 先签发 run，再追加 item，最后写终态；终态不可回退为 running |
| `model_check_observations` | outcome store | 与 outcome 同一 input identity；必须先持久化，再允许 trust aggregation |
| trust receipt/cursor/latest/dirty result | trust aggregation | receipt 去重后才标记 observation 已消费；retention 不得删除未消费 observation |
| `model_quality_policies`、`model_quality_schedules` | 管理 command store | CAS revision；同值更新零 DML，不推进 revision |
| `account_quality_enforcements` | quality projector | 只由完整已验证的 J3b evidence 驱动；`modelCheckUnverified` 不处罚 |
| `account_quality_health_hourly` | health-sync projector/retry | outcome 已提交后投影；失败写 `pending_retry|failed`，不伪造 applied |

J3c 保留 usage quality score、失败前置确认和它们的独立 queue/worker/state；J3b 不得通过共享 SQLite DB-service 或 Node writer 投影上述 J3b 表。SQLite 迁移前必须先把涉及这些表的 Node writer、worker IPC、read worker 分支完整替换为 Go SQLite Store 的唯一 writer；PostgreSQL runtime 也只能做只读 schema/permission preflight，DDL 由 maintenance 的显式加法 migration 承担。

## 5. 管理读模型和保留

`GET /model-checks/runs`、`GET /model-checks/runs/:id`、active/stop、policy/schedule/options/account-options 均从 Go 自己的 store/read model 返回。所有 read API 以 system-account scope 过滤，历史 run 不因账户后来不可运行而泄露给无权 actor。

每账户最多保留 1000 个 run 的现有语义必须保留：只删除非 running、health sync 已 applied（或无须 sync）且所有 observation 已被 trust receipt/cursor 消费的最老批次。retention blocked 是可观察状态，不得跳过未消费 observation。

## 6. L2 实现门禁

当前已落地的最小 Go L2 组件包括 `modelcheckinput`、`modelcheckdurable`、`modelcheckprofile`、`modelcheckprobe` 和 `modelcheckstore`：输入版本/identity/digest、SQLite/PostgreSQL durable input/claim/outcome、四协议请求响应解析与基础评分、以及 run/item/observation writer 均已建立。`modelcheckdurable` 的 SQLite 回归覆盖版本重放、input ID 复用拒绝、claim busy/过期接管、旧 fence 和 outcome 冲突；PostgreSQL 仍只允许通过 maintenance 预置 schema 后做 readiness，尚无真实 durable Store smoke。它们尚未接入 jobs runtime、管理 API/SSE、探针 transport、业务 revision re-read/stale、质量投影或调度恢复，因此不构成 J3b 完整迁移，也不改变 Node 的 active owner。隔离 dev scratch 的 Node/Go writer smoke 已清理数据库、角色和 PgBouncer 临时认证；计划中的 PgBouncer 并发 terminal-fence smoke 未形成有效结果，仍为未验证门禁。

开始 jobs runtime 前必须有：

1. Node request/response/SSE golden，含 `400/409/503`、stop、断开、EPIPE 和 heartbeat；
2. PostgreSQL/SQLite schema contract、最小权限、input/outcome digest、lease/fence/CAS 与重复 replay 测试；
3. 四协议的 profile/model/payload golden，以及 token/identity/trust/structured/tool/long-context/distribution probe 覆盖；
4. outcome、日志、F4 审计和管理响应的凭据泄露扫描；
5. J3c writer/queue/read 路径与 J3b 的 static ownership scan。

未完成任何一项时，J3b 只能标为 L2 实现中；不得启用 Go scheduler、删除 Node active path 或创建长期双 writer。
