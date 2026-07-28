# AI 健康监控设计

> 状态：账户哨兵 v1 已实施；多模型能力 v2 是完整目标契约，只有与多模型能力健康方案一起交付后才启用。

## 1. 监控回答的两个问题

AI 健康监控必须明确区分：

1. **账户哨兵观察 v1**：这个账户在某次 healthCheckModel 检查时是否完成。
2. **模型执行能力 v2**：这个账户当前配置的模型、endpoint、转换路径和凭据作用域中，哪些已确认可用、未知、正在确认、已阻断或恢复中。

v1 不能代表全部模型，v2 也不能改写 v1 历史。页面并列展示两层信息，不把一个哨兵成功渲染成“全部能力可用”。

页面不判断失败责任归属，不维护供应商错误码词典。HTTP 状态、错误码和原始错误详情可以留在现有受权限保护的 v1 usage / audit 详情中，但不参与 v2 能力状态决策。

## 2. v1 账户哨兵三态

| 状态 | 颜色 | 页面文案 | 判定 |
| --- | --- | --- | --- |
| success | 绿色 | 哨兵可用 | 哨兵检查成功完成 |
| failure | 红色 | 哨兵不可用 | 哨兵检查没有成功完成 |
| unknown | 灰色 | 哨兵无记录 | 该小时没有哨兵检查结果 |

v1 失败原因只用于诊断展示，不参与颜色分类。“哨兵不可用”不等于账户所有模型或供应商服务器不可用。

正常 active 账户默认每 1 小时检查一次，并按账户 ID 在 0 到 10 分钟内稳定错峰。只有与保存的哨兵 model + endpoint + lane + adapter route 完全相同的真实成功可以满足本轮水位；其他模型成功不能顺延哨兵。

## 3. v1 小时聚合

- 历史事实只取 traffic_source = account_health_check 的使用记录。
- 按统计时区聚合到 account_health_hourly。
- 同一账户同一小时有多次结果时，以 created_at、id 较新的记录为准。
- 没有聚合行时由查询层补为 unknown。
- 页面请求只读取预聚合表，不扫描使用记录。

v1 哨兵可用率：

~~~text
哨兵可用率 = success 小时数 / (success 小时数 + failure 小时数)
~~~

unknown 不进入分母；没有有效检查时显示 --。该百分比必须标为“哨兵检查可用率”，不得简称“账户所有模型可用率”。

account_health_hourly 保留：

~~~text
account_id
system_account_id
provider_code
stat_hour
status
last_observed_at
last_record_id
status_code
error_code
error_message
updated_at
~~~

`account_health_hourly` 继续作为兼容现有 reader 的 current 读模型，但携带 `accountListSnapshotRef` 的列表请求不得直接读取这张会原地更新的表。v1 聚合 owner 还必须维护以下版本化事实：

- `account_health_hourly_versions` 以 `v1_hour_version_id` 为主键，append-only 保存 `account_runtime_key + stat_hour`、上述完整 v1 payload、决定“最新一条”的 `source_created_at + source_record_id`、固定 `v1_partition_id + valid_from_partition_seq`、payload digest、created at 和 retain until。业务 payload 在插入后不可更新；只允许 CAS 延长 retain until。
- `account_health_hourly_current_pointers` 以 `account_runtime_key + stat_hour` 唯一指向当前 `v1_hour_version_id`；`account_health_hourly_pointer_history` 以同一身份加 `valid_from_partition_seq` 保存版本指针及可空 `superseded_at_partition_seq`。pointer history 只封闭旧有效区间，不覆盖旧版本 payload。
- 每个运行账户按稳定 hash 归入固定 v1 partition。聚合 owner 在单个 PostgreSQL 事务中锁定该小时 pointer、分配连续 partition seq、按 `created_at + id` 重算确定性 winner、插入 immutable version、封闭旧 history、插入新 history、CAS current pointer、同步兼容 `account_health_hourly`，并追加 v1 pointer dirty outbox；事务提交后 version、pointer、兼容 current 和 dirty event 才一起可见。相同 source identity 与 payload digest 是幂等 no-op。重算若从有记录变为无记录，必须追加显式 unknown tombstone version，不能删除旧版本或把 current 原地清空。

账户列表 publication 必须保存并把 `v1_hourly_cutoff_watermark_vector` 纳入 manifest digest。读取某个 `accountListSnapshotRef` 时，API 按账户固定 partition 的 cutoff seq，从 pointer history 选择 `valid_from_partition_seq <= cutoff` 且 `superseded_at_partition_seq` 为空或大于 cutoff 的版本；该 cutoff 前没有 pointer 才补 unknown。后续新检查、迟到记录或重算只能生成更大 seq 的新版本，因此不得改变已签发 snapshot 的任一页。

v1 version 与 pointer history 的 retain until 必须覆盖所有仍可签发或已签发的 list publication retain until、10 分钟引用 TTL 和 safety margin。签发引用前 reader 必须证明所需 v1 依赖可保留到 `reference_expires_at + safety_margin`；证明失败时不得签发引用。只有 pointer 已封闭、对应 stat hour 已退出历史窗口、所有引用它的 publication / snapshot 均过期且 retain until 到达后才可清理；current pointer 指向的版本不得清理。重算、清理和 current 兼容表更新都不能改变旧 publication 按 cutoff 可达的结果。

## 4. v2 能力状态

v2 使用 [AI 账户多模型能力健康与精确隔离设计](AI账户多模型能力健康与精确隔离设计.md) 的 Catalog 和 circuit ledger。

scope 状态：

- unknown
- available
- suspect
- temporarily_blocked
- half_open
- recovering

账户小时 aggregate：

- no_routable_capability
- healthy
- no_confirmed_unavailability
- confirming
- partially_unavailable
- recovering
- all_configured_capabilities_blocked

Route unknown 以及账户 `no_confirmed_unavailability` 允许尝试，但不能显示绿色 healthy；`no_routable_capability` 没有可执行 Route，不能按 unknown 放行。全部能力阻断只是派生调度门禁，不表示 accounts.status 已改变。

## 5. v2 数据结构

### 5.1 事件

account_capability_health_events 保存：

~~~text
event_id
payload_digest
stats_contract_epoch_id
system_account_id
runtime_account_id
account_runtime_key
route_scope_id
scope_id
capability_universe_revision
effective_dispatch_revision
route_membership_incarnation
credential_membership_incarnation
route_definition_revision
attempt_definition_revision
generation
ledger_revision
stats_partition_id
partition_seq
event_type
outcome
phase_before
phase_after
evidence_data_status
evidence_data_status_reason
observed_at
state_effective_at
positive_evidence_expires_at
soft_avoid_until
scope_policy_revision
trace_id
created_at
~~~

事件不保存 API Key、OAuth token、用户 payload 或上游正文，名义保留 7 天。HTTP / error code 不进入 outcome。`event_id` 幂等唯一，同一 ID 重放还必须校验规范化 payload digest 一致，否则 fail-closed 并报警；current 应用必须匹配 route / attempt definition revisions 与 membership incarnations，账户双 revision 只定位历史 publication。`stats_contract_epoch_id` 与 active capability contract epoch 一一对应，并固定该 epoch 的 `partitionCount`；`stats_partition_id = hash(accountRuntimeKey) mod partitionCount`，同一运行账户在一个 stats contract epoch 内固定落到同一分区。唯一顺序键和唯一约束均为 `stats_contract_epoch_id + stats_partition_id + partition_seq`，任何 publication、引用和 cursor 都必须绑定同一 epoch。改变 partitionCount 只能创建新 stats contract epoch，停止旧 owner，在新 epoch 全量 rebuild 并 ready 后切换；旧 epoch 只在自身保留期内可读，不能与新 epoch 共用水位或 cursor。

产生 circuit ledger / outbox 事实的同一 PostgreSQL 事务必须锁定该 epoch + partition 的 sequence 行、分配 `partition_seq`，并把带完整非敏感事件 payload 与 digest 的记录写入 durable `account_capability_health_event_outbox`；回滚不消费序号。ingest owner 在单个事务中按 sequence 幂等插入 `account_capability_health_events`、校验同 ID digest、推进 `account_capability_health_ingest_cursors` 的连续 ACK，再标记 outbox 已 materialized。outbox 只有在 ingest ACK 越过该 sequence、stats publication 已覆盖所有受影响窗口且安全余量已过后才能清理。ingest 崩溃、ACK 丢失或乱序到达由 outbox 重放；遇到 gap 必须从 durable outbox 补洞并保持分区 not-ready，不能跳号或从 circuit 当前行猜造事件。

stats owner 在 publication 事务中同步维护 `account_capability_health_event_coverage_acks`，按 event ID 保存由业务时间计算的 `impact_from / impact_until`、已覆盖的 publication ID / aggregation version 和 `coverage_complete`。只有事件影响的全部保留窗口、Route event projection 和后续状态边界都已进入同一个或后续完整 publication 时才能置 complete；迟到事件扩展影响区间时必须用 CAS 撤销旧 complete 并随新 build 重新证明。event / outbox cleaner 只读取该 durable coverage 证明，不能通过“当前没有 gap”猜测所有历史窗口已发布。

`partition_seq` 只表示 durable ingest 完整性，不是业务生效时间。Attempt 当前 phase 投影按 `(generation, ledger_revision)` 单调推进，较旧事件不能覆盖 current；但幂等的新迟到事件仍必须进入历史重算，不能被 current guard 丢弃。匹配当前 generation / ledger revision 的 deadline 事件可按唯一 deadline identity 幂等更新 evidence freshness 或 soft avoid 派生状态，但不得改变 phase 或伪造更高 ledger revision。outcome 计数按 `observed_at` 落入半开统计小时，Route phase 区间按 ledger 事务给出的 `state_effective_at` 生效，并用 `(state_effective_at, generation, ledger_revision, partition_seq, event_id)` 确定同一时刻的稳定顺序。`phase_before / phase_after` 只描述该 ledger revision 的转换；纯 observation 和 deadline event 可为空。若 revision 顺序、effective time 或 phase 链无法自洽，目标账户 projection 进入 `unconfirmed`，不得按到达时间覆盖。`positive_evidence_expires_at`、`soft_avoid_until` 和 `scope_policy_revision` 保存写入当时的权威绝对截止时间，不能由 reader 用当前 policy 反推历史。

### 5.2 每 Route 稀疏小时事实

account_capability_health_hourly 保存：

~~~text
stats_contract_epoch_id
system_account_id
runtime_account_id
account_runtime_key
route_scope_id
effective_dispatch_revision
capability_universe_revision
route_membership_incarnation
route_definition_revision
stat_hour
data_status
route_aggregate
last_capability_schedulable
last_soft_blocked
last_outcome
credential_total_count
credential_available_count
credential_unknown_count
credential_suspect_count
credential_blocked_count
credential_half_open_count
credential_recovering_count
evidence_ready_count
evidence_unconfirmed_count
available_observation_count
unavailable_observation_count
task_failure_count
last_observed_at
last_event_id
aggregation_version
superseded_at_aggregation_version
published_partition_seq
updated_at
semantics_version = 2
~~~

逻辑身份覆盖 `stats_contract_epoch_id + system_account_id + account_runtime_key + route_scope_id + route_membership_incarnation + route_definition_revision + effective_dispatch_revision + capability_universe_revision + stat_hour`；物理唯一键再追加 `aggregation_version`。新 publication 不原地覆盖旧可读版本，而是插入新行并把上一行的 `superseded_at_aggregation_version` 设为新版本；查询快照 V 选择 `aggregation_version <= V AND (superseded_at_aggregation_version IS NULL OR superseded_at_aggregation_version > V)`。该表只在 Route 于本小时出现 observation、状态转换、deadline / evidence hold 到期或 Catalog / credential baseline 切换时写入，保存本小时 outcome 计数和最后活动事实，不为无变化的每个 Route 逐小时复制整行。账户 publication revision 变化但 Route definition / membership 未变时沿用当前状态区间并在新 member 中引用，不初始化 unknown；只有 added / changed / removed 形成边界。同一小时的两代配置不能互相覆盖。保留和 snapshot-validity 规则统一见第 10 节。

### 5.3 Route 状态有效区间

account_capability_route_state_segments 保存：

~~~text
stats_contract_epoch_id
system_account_id
runtime_account_id
account_runtime_key
route_scope_id
effective_dispatch_revision
capability_universe_revision
route_membership_incarnation
route_definition_revision
segment_start_at
segment_end_at
data_status
route_aggregate
capability_schedulable
soft_blocked
credential_total_count
credential_available_count
credential_unknown_count
credential_suspect_count
credential_blocked_count
credential_half_open_count
credential_recovering_count
evidence_ready_count
evidence_unconfirmed_count
stats_partition_id
first_partition_seq
last_partition_seq
aggregation_version
superseded_at_aggregation_version
computed_at
semantics_version = 2
~~~

逻辑身份覆盖 `stats_contract_epoch_id + system_account_id + account_runtime_key + route_scope_id + route_membership_incarnation + route_definition_revision + effective_dispatch_revision + capability_universe_revision + segment_start_at`；物理唯一键追加 `aggregation_version`，并用与稀疏事实相同的 `superseded_at_aggregation_version` 支持短期 as-of 读取。同一 Route definition 在同一读取快照中的区间不得重叠，全部时间区间使用 `[segment_start_at, segment_end_at)`；`segment_end_at` 为空表示该快照中的当前开放区间。配置 staging diff 只为 added / changed Route 建立 unknown 初始区间；unchanged Route 沿用原 routeMembershipIncarnation / routeDefinitionRevision 的开放区间，并由新 publication member 引用，removed 封闭旧区间并保留 tombstone。删除后原样 re-add 必须获得新 incarnation，不能接纳删除前迟到事件。此后仅在 Route 聚合、capability 可调度性、soft-block、凭据 / evidence 计数、账户级 `data_status` 或时间驱动的证据有效性变化时封闭旧区间并建立新区间。账户级 `data_status != ready` 时 `route_aggregate` 与 `capability_schedulable` 均为 null；scope-local evidence unconfirmed 只进入 Attempt / Route 计数，不把 data_status 改成 unconfirmed。一个长期不变化的 Route 只保留一个跨小时区间，不产生 744 份重复快照。

`account_capability_health_deadlines` 持久化正向证据过期和 soft avoid 到期，唯一身份为 `stats_contract_epoch_id + account_runtime_key + scope_id + routeDefinitionRevision + attemptDefinitionRevision + membership incarnations + deadline_kind + deadline_at + scope_policy_revision`；账户 publication 双 revision 只作 provenance。deadline owner 按 epoch + partition 持租约；到期时在 PostgreSQL 事务中再次校验 Attempt 当前 definition / incarnation / deadline，锁定分区 sequence，写入具有确定性 event ID 的 `positive_evidence_expired` 或 `soft_avoid_elapsed` durable stats outbox 事件，事件的 `observed_at / state_effective_at` 都固定为权威 `deadline_at`。stale deadline 只终结自身，不改变 Route。deadline 在事件已连续 ingest 并完成 publication 前不得按 TTL 删除；进程崩溃后按 due cursor 补跑，因此没有新 circuit 事件时也会推进状态区间和 publication。

### 5.4 Route 事件预聚合

`account_capability_health_route_event_projection` 保存 API 可读的 versioned Route 事件：`stats_contract_epoch_id`、运行账户与 Route 身份、publication 双 revision、route membership incarnation / definition revision、`stat_hour`、`route_event_id`、`event_effective_at`、`event_type`、`outcome`、变更前后的 `route_aggregate / capability_schedulable / evidence_unconfirmed_count`、`source_first_partition_seq / source_last_partition_seq`、`aggregation_version`、`superseded_at_aggregation_version` 和 `retain_until`。它与 Route 区间和摘要在同一个 staging build 中生成。导致 Route aggregate、capability schedulable 或 scoped evidence 变化的 transition 每次独立成行；不改变 Route 结论的 credential-only observation 按 UTC 固定 5 分钟桶及 `accountRuntimeKey + Route definition + outcome` 合并为至多一行，只保留 first / last source sequence，不保存精确重复次数。合并桶在 publication 后不可追加；迟到 observation 通过新 aggregation version 替代该桶。

授权实例的小时事件接口只能读取这张预聚合投影，不能在请求时扫描 Attempt 事件后过滤或 GROUP BY。管理员也默认读取该 Route 投影；需要 Attempt 级诊断时走主设计中受权限保护的 capability trace 资源。投影按 `event_effective_at + route_scope_id + route_event_id` 稳定排序，所有行受 publication as-of 版本约束，避免合并组跨 raw cursor 边界。

### 5.5 账户小时摘要

account_capability_health_hourly_summary 保存：

~~~text
stats_contract_epoch_id
system_account_id
runtime_account_id
account_runtime_key
effective_dispatch_revision
capability_universe_revision
stat_hour
revision_active_from
revision_active_until
summary_evaluation_end_at
routable_route_count
capability_schedulable_route_count
healthy_route_count
unknown_route_count
confirming_route_count
partially_unavailable_route_count
blocked_route_count
recovering_route_count
scoped_unconfirmed_attempt_count
scoped_unconfirmed_route_count
has_scoped_unconfirmed
data_status
data_status_reason
aggregate
last_observed_at
aggregation_version
superseded_at_aggregation_version
published_partition_seq
computed_at
semantics_version = 2
~~~

逻辑身份覆盖 `stats_contract_epoch_id + system_account_id + account_runtime_key + effective_dispatch_revision + capability_universe_revision + stat_hour`；物理唯一键追加 `aggregation_version`，并以 `superseded_at_aggregation_version` 支持短期 as-of 读取。账户摘要按小时稠密保存，一小时、一个运行账户和一个双 revision 在同一 publication 快照只有一行。`revision_active_from / revision_active_until` 是与统计小时相交后的半开适用区间；零长度 revision 段不生成摘要或 API 项。摘要必须把同一双 revision 的 CapabilityScopeCatalog 和 credential membership baseline 作为全集；只扫描 incident 行无法计算 unknown，也不得宣称 healthy。revision 中途变化时旧桶保留诊断，新桶从新 Catalog / credential baseline 独立计算。

每个 revision 段的评价边界固定为 `summary_evaluation_end_at = min(stat_hour_end, revision_active_until, current_hour_history_generated_at)`，不存在的上界忽略；摘要取该 revision 段内、有效时间严格小于该半开边界的最后 Route 状态。已结束旧 revision 因此在自己的 `revision_active_until` 前评价，不要求覆盖整个小时结束时刻。已完成小时的 `displaySegment` 选择 `revision_active_from < stat_hour_end AND (revision_active_until IS NULL OR revision_active_until >= stat_hour_end)` 的段；当前小时把 `stat_hour_end` 换成 `historyGeneratedAt`。恰在边界开始的新 revision 属于下一个评价区间，不能抢占前一小时的 displaySegment。revision 子资源返回 `summaryEvaluationEndAt`，使旧桶的 aggregate 可解释。

`data_status` 与管理 API 统一使用 `ready / unconfirmed / rebuilding`，并保存同一受限 `data_status_reason=null|deployment_capability_barrier|observation_handoff_unconfirmed|projection_gap|capacity_exceeded|catalog_unconfirmed|credential_baseline_unconfirmed|rebuild_in_progress` 枚举。active deployment-wide barrier 固定映射为 `deployment_capability_barrier` 并拥有最高优先级，使所有账户 current / 小时摘要 fail-closed；barrier 解除后只恢复各账户原有原因，不批量写 healthy。除此以外，只有 account-wide hold、Catalog / credential baseline 缺口、整个账户 projection gap 或 rebuild 才能令账户 data_status unconfirmed / rebuilding；已知 scope 的 handoff delivery / quarantine / fanout hold 只更新 Attempt evidence 与上述 scoped-unconfirmed 计数，顶层仍 ready。producer gap 若 affected set 已精确解析同样不得升级整账户；无法确定账户 affected set 才由全局 barrier 让所有 capability fail-closed。这些 data status 只说明投影尚不能给出权威账户结论，不属于 unknown 能力状态；统计 API 不暴露 producer、分区、spool 路径或内部 backlog 明细。

### 5.6 原子发布清单

`account_capability_health_publications` 是**运行账户级 build generation**，不是逐小时或逐 revision 的独立 publication。每行固定保存 `publication_id / build_id`、`stats_contract_epoch_id`、运行账户身份、全账户单调 `aggregation_version`、`stats_partition_id`、`cutover_partition_seq`、`published_partition_seq`、`published_at`、`superseded_at`、`retain_until` 和发布状态。`account_capability_health_publication_members` 以 `publication_id + stat_hour + effective_dispatch_revision + capability_universe_revision` 唯一列出该 build 对每个受影响小时完整可见的 revision 段集合及其 `summary_evaluation_end_at / event_projection_retain_until`。publication 的事实内容 / 水位和 member 在发布后不可变；只有 `superseded_at`、单调延长的 `retain_until` 等生命周期元数据可用 CAS 更新。成员清单包含未变化但仍应在该小时快照可见的 revision，业务事实行本身不复制。

`account_capability_health_current_pointers` 的唯一身份固定为 `stats_contract_epoch_id + account_runtime_key + stat_hour`，值为该小时当前 `publication_id + aggregation_version`；append-only `account_capability_health_pointer_history` 保存每次替换的 `valid_from / superseded_at`。stats owner 先按唯一 build_id 在 API 不可见的 staging 完整计算全部受影响的 Route 状态区间、Route 事件投影、稀疏小时事实、账户摘要 delta 和 publication members；确认本分区从上一已发布水位到 `published_partition_seq` 连续无缺口、全部 deadline 已处理到目标评价边界且 member 清单完整后，在同一 PostgreSQL 事务中校验旧 pointer、应用 versioned delta、插入 publication / members、封闭受影响小时的旧 pointer history，再 CAS 把所有受影响小时 pointer 指向同一个 publication。账户级 publication 只有在更新后已无任何 current pointer 引用时才设置 `superseded_at`；未受影响历史小时仍指向旧 build 时，该 build 继续是这些小时的 current snapshot，不能提前清理。任一步失败都不暴露 staging 或推进 pointer；孤儿 staging 按 build lease 回收，不能删除 durable event / deadline。

`cutover_partition_seq` 是小时关闭或 rebuild 开始时，在同一 epoch + partition sequence 行上捕获的已提交 event outbox head；`published_partition_seq` 是该 build 已连续 ingest、聚合并纳入 publication 的上界，必须大于等于 cutover。`aggregation_version` 是运行账户级 as-of 版本，不能为每小时各自分配；跨小时 Route 区间和多个受影响小时因此可在一个一致版本内发布。

列表从 `(accountRuntimeKey, statHour)` current pointer 读取 publication 时才签发 `hourRef / revisionsRef`。两种引用都固定 `stats_contract_epoch_id + publication_id + aggregation_version + published_partition_seq + reference_expires_at + 当前权限视图`；`revisionsRef` 读取该 publication 的完整 member 集合，每个 member 生成的 `hourRef` 仍绑定同一 publication。引用默认从签发时起有效 10 分钟，publication 创建时间不能充当引用 TTL。API 每次请求在只读 REPEATABLE READ 事务中校验 publication、member、retain_until 和权限，再按 validity 区间读取同一 as-of 版本。current pointer 后续推进不使 TTL 内引用失效；引用过期、版本已按保留契约清理或 revision 不再可解释时返回 `409 stale_view`。

reader 只有在 publication、小时 / Route 业务行和 Catalog / credential baseline 的 `retain_until >= reference_expires_at + safety_margin` 时才能签发引用；不满足时该小时先退出可引用窗口，不能签发注定在 TTL 内失效的 ref。Route 事件投影若仍声明可读，其 `event_projection_retain_until` 也必须覆盖引用 TTL；若事件保留已结束，member 固定返回 `eventsExpired=true`，但不阻止继续签发用于 Route / revision 摘要的 hourRef。被替代 publication 和业务行从 `superseded_at` 起至少保留 15 分钟；仍 current 但接近 31 天历史边界的行同样保留到最后可签发时刻加 10 分钟 TTL 与安全余量。繁忙当前小时因此可以稳定翻页。迟到事件走相同 staged publish，不能在 publication 事务外原地改写 API 可见行。

`account_capability_health_hour_close_cursors` 按 `stats_contract_epoch_id + stats_partition_id` 持久化 `next_hour_to_close`。所有统计小时使用统计时区映射后的唯一 UTC 半开区间 `[stat_hour_start, stat_hour_end)`；DST 重复小时必须有不同 UTC identity。整点 owner 先在同一 close lease 下驱动 deadline cursor 处理并提交所有 `deadline_at < stat_hour_end` 的 durable event，再锁定 partition sequence 捕获包含这些事件的 `cutover_partition_seq`；随后等待 ingest / stats cursor 连续追平该 head，才为该分区内本小时存在 v2 语义的每个运行账户和每个非零 revision 段生成稠密摘要，即使没有新事件也不能省略。崩溃重启按 `next_hour_to_close` 逐小时幂等追赶；未关闭小时保持 `unconfirmed / stale`，不能补造 unknown。晚到但仍在允许窗口内的事件通过新 aggregation version 修正已关闭小时。

### 5.7 账户列表快照投影

账户列表不能在每次 GET 时把所有可见账户复制到临时表。`account_capability_health_list_rows` 按运行账户只保存冻结排序字段、目录身份、owner identity 和 `valid_from_list_version / superseded_at_list_version`；名称、质量热度、last-used、账户增加 / 删除时只追加该账户的新版本行。实时 control summary 不进入 list row，避免 10 分钟分页快照冻结旧“可调度”结论。`account_capability_health_list_publications` 保存全局单调 `list_version`、stats contract epoch、目录 revision、质量排序 publication、stats cutoff watermark vector、v1 hourly cutoff watermark vector、`history_generated_at`、build 状态、created / retain until 和 manifest digest。授权关系不复制进列表行；publication 固定 authorization revision，读取时重新鉴权，revision 变化使旧 viewer 快照失效。

list projection coordinator 只消费目录 / 质量 / stats pointer / v1 hourly pointer 的 durable dirty outbox。它选择各 stats / v1 分区的连续水位向量，在不可见 staging 计算增量；确认所有向量无 gap 后，以单事务插入 publication、封闭旧 row validity 并 CAS current list pointer。publication 不是伪全局 event seq，向量中每个 epoch + partition 水位独立保留。旧 list row / publication 及其 v1 version / pointer history 依赖至少保留到最后签发引用的 10 分钟 TTL + safety；清理前还要证明没有有效 `accountListSnapshotRef`。control summary 变化只推进 capability current view / invalidation，不触发列表排序 publication。

第一页只选择当前 ready publication 并签名引用，不执行列表快照写入。若 current publication 未 ready，接口保留上一个 ready publication 并返回 `listStale=true / generatedAt`；从未有 ready publication 时返回 `503 projection_unavailable`，不能退化到 mutable offset 查询。单次 build 的 row delta 上限为 100,000；超限时按同一 buildId 写不可见 staging batch，全部完成后一次发布。列表 publication owner、延迟、delta 数、retain backlog 和 gap 都进入 readyz / 监控。

## 6. v2 聚合 owner 与优先级

- gateway / control worker 在 circuit ledger 事务内写 durable stats event outbox 和有界事件，不直接推进 stats cursor。
- ingest owner 按 `stats_contract_epoch_id + stats_partition_id + partition_seq` 连续持久化 account_capability_health_events，并以同事务 ACK 推进 ingest cursor。
- deadline owner 按 epoch + partition 持久化并触发证据过期 / soft avoid deadline，仍通过同一 durable stats outbox 分配 sequence。
- stats owner 按自己持有的 epoch + partition cursor 连续增量维护 Attempt 投影，再写 Route 状态区间、Route 事件投影、稀疏小时事实和账户小时摘要，并通过账户级 publication build 原子发布。
- hour close owner 持久化每分区整点 cursor；无事件小时也生成账户摘要，重启后逐小时追赶。
- list projection coordinator 按 durable dirty outbox 发布 versioned 账户排序行和跨分区水位向量；API 只选择 ready publication，不在 GET 中物化快照。
- 管理 API 只读清单已发布的预聚合版本，不扫描 usage、audit、运行日志或完整 ledger 临时 GROUP BY。

### 6.1 Attempt 到 Route 的确定性聚合

事件是精确 Attempt / credential scope 事实，`phase_after` 不能直接复制成 Route 的 `last_state`。stats owner 必须：

1. 按 `accountRuntimeKey + routeScopeId + 账户双 revision` 加载同代 Catalog 与固定 credential membership baseline；每行再携带 route / attempt definition revisions。没有 incident 的 Attempt 初始化为 unknown。只有 Catalog、credential membership、账户级 affected set 或整个 capability runtime projection 未 ready 时才写账户级 `data_status` 并停止聚合；已知 scope 的 handoff hold 写 Attempt `evidence_data_status=unconfirmed`，不得把模型 B 的局部缺口提升成整账户 data_status。Key owner 的当前冷却 / 禁用运行态不进入 capability 历史颜色。
2. 按 `stats_contract_epoch_id + stats_partition_id + partition_seq` 连续消费事件；同一运行账户在 epoch 内固定属于一个分区。每个 scope 只用更高 generation / ledger revision 推进当前 phase，同时允许匹配 definition revisions 的幂等 deadline / hold 更新派生 freshness、soft avoid 与 evidence status；全部事件按 `state_effective_at` 重放历史区间，并保存该 Attempt 的当前状态、evidence status / reason、正向证据过期时间、soft avoid 截止和 policy revision。
3. 每次 Attempt 变化后，用事件所属账户 publication 的 credential membership baseline 中**全部凭据槽位**重算 Route；realtime current 只使用当前 ready publication。Key 删除或集合变化进入新的 capabilityUniverseRevision 桶，但配置事务必须把 scopeId + definition revisions 未变的 incident / hold 确定性 carry-forward；不能把未变化 Key-2 + B 的 OPEN 重置为 unknown，也不能用查询时的当前 Key 目录改写旧桶分母。
4. Route 在观察时刻至少有一个 evidence ready 且为 `available`、`unknown` 或 soft avoid 已到期的 `suspect` Attempt 时为 `capability_schedulable=true`。evidence unconfirmed Attempt 一律不调度；只有全部当前 Attempt 都是 unconfirmed、未到期 suspect、blocked、half-open 或 recovering 时 capability 才不可调度。该值不代表对应 Key owner 当前可执行。
5. Route 使用独立 `route_aggregate`，不能把任意一个 Attempt 的 state 填入 Route。当前 credential slot 数为 0 时固定为 `unknown` 且 capability 不可调度，最终阻断原因交给 Key owner。其余聚合优先级固定为：至少一个 Attempt capability 可调度且另有 blocked / half-open / recovering 时为 `partially_unavailable`；capability 不可调度且存在 half-open / recovering 时为 `recovering`；capability 不可调度、所有 Attempt evidence ready 且都是 confirmed blocked 时为 `blocked`；同时存在 confirmed blocked 与 suspect / evidence unconfirmed 时为 `partially_unavailable`，即使暂不可调度；此前未命中且存在 suspect / evidence unconfirmed 时为 `confirming`；此前未命中且存在 ready unknown 时为 `unknown`；否则为 `healthy`。`last_soft_blocked=true` 只表示 Route 的全部 ready capability 当前都在 suspect soft-avoid 窗口内，不等同 confirmed blocked。
6. Key-1 的 capability blocked、Key-2 的 capability available 时，Route 必须是 `partially_unavailable` 且 capability 可调度，同时凭据 capability 计数保留局部阻断事实；不能因为 Key-1 是最后到达的事件把整个 Route 写成 blocked。Key-2 自身是否被 Key owner 冷却由当前 effectiveAvailability 另行求交，不回写历史能力颜色。授权实例 API 隐藏这些凭据计数，只返回 Route 聚合。

`verification_backpressure_notice` 与 `cost_verification_notice` 都是 realtime current 的软提示，不进入 v2 小时 phase / outcome 历史。前者虽由 durable receipt / due 投影重建，后者仅是可丢失 Redis TTL；两者可让 current Route 展示 confirming / notice，但不得改写已发布历史 segment、计数或 aggregate。若未来要求历史审计，必须先定义独立 durable notice event / deadline contract，不能从 Redis 当前值补造过去。

Route 状态区间是跨小时连续状态事实，稀疏小时表只保存发生过活动的 outcome 与转换；账户摘要仍每小时生成一行。某小时没有新事件时，stats owner 按第 5.5 节的 `summary_evaluation_end_at` 选择该 revision 段评价边界前的最后状态，不能把“没有新事件”重置为 unknown，也不能为全部 Route 复制整点快照。小时 Route 明细先按所选双 revision 的保留 Catalog 稳定分页，再以索引等值 / 区间条件关联该评价边界的已发布状态区间，并左联本小时稀疏事实；这是预计算投影的有界合成，不允许回扫原始事件或临时 GROUP BY。

迟到事件的 outcome 按 `observed_at` 所属统计小时、phase 按 `state_effective_at` 幂等重算受影响区间、稀疏事实、Route 事件投影和账户摘要，直到下一次同 scope 状态转换或当前小时，不能以到达顺序覆盖历史。重算必须在 staging 使用新的 `aggregation_version`，只有所属 epoch + partition 连续水位、deadline 水位与全部受影响窗口都完整后，才通过账户级 publication build 原子推进可见版本；不能在 publication 事务外原地改写 API 可见行。

### 6.2 Route 到账户摘要

账户摘要只消费账户级 `data_status=ready`、同一 publication revision 且在该桶 `summary_evaluation_end_at` 前最后有效的已发布 Route 状态区间。`healthy / unknown / confirming / partially_unavailable / recovering / blocked` 六类 Route 计数必须互斥并满足 Route 总数守恒；另返回 `scopedUnconfirmedAttemptCount / scopedUnconfirmedRouteCount / hasScopedUnconfirmed`，但局部 hold 不改变顶层 data_status。账户级投影未确认或重建中时只返回 `data_status`，`aggregate=null`，不能计算健康结论。ready 时账户 aggregate 优先级固定为：

1. `data_status=ready` 但 Route 总数为 0：`no_routable_capability`；Catalog / projection 未 ready 已在前置门禁返回空 aggregate，不能把未配置 Route 与 unknown Route 混为一谈。
2. `capability_schedulable_route_count = 0` 且存在 recovering Route：recovering。
3. `blocked_route_count = routable_route_count` 且大于 0：all_configured_capabilities_blocked。
4. partially-unavailable / blocked / recovering Route 大于 0：partially_unavailable。
5. confirming Route 大于 0：confirming。
6. unknown Route 大于 0：no_confirmed_unavailability。
7. 全部 Route 都有新鲜正向事实：healthy。

`aggregate` 只描述能力状态构成，绝不蕴含“当前可调度”。例如一个 Route 同时含 confirmed blocked 与未到期 SUSPECT 时，Route 和账户 aggregate 都可以是 `partially_unavailable`，但 `capability_schedulable_route_count` 仍为 0。当前页面只有在实时 `effectiveSchedulableRouteCount > 0` 时才允许出现“可调度”；若 capability schedulable 为 0 且 aggregate 为 partially unavailable，固定显示“当前无可调度模型能力 · 部分能力待确认”，进入既有有界等待，但不得冒充 `all_configured_capabilities_blocked`。若 capability 仍有候选但 Key × capability 实时交集为 0，则继续使用 `all_keys_unavailable / instance_no_effective_route`，不能从历史 aggregate 推断。

同一小时 revision 变化时，旧 revision 事件与桶保留诊断，但只返回当前请求明确选择的单一 revision；不能把旧 Route 数并入新 revision。纯 SUSPECT、零凭据 unknown、blocked + unknown 均不满足全部 confirmed blocked：它们分别按 confirming、no_confirmed_unavailability、partially_unavailable 展示。stats 聚合滞后时返回 `stale / generatedAt`，不回退用 v1 冒充 v2。

v2 小时表只记录 capability 事实，不历史化 Key owner 冷却。当前账户卡片 / 能力详情从 Key owner 快照另算 `effectiveSchedulable`；若 all_keys_unavailable 或 Key × capability 交集为空，最终 presentation 使用 Key / mixed blocker，不能篡改 route_aggregate。

v2 不计算“能力成功率”。available / unavailable observation 的比例只表示探针结果分布，不能代表请求成功率或供应商 SLA。

## 7. API

### 7.1 列表

管理端：

~~~text
GET /__aisys__/api/stats/ai-health
~~~

用户端：

~~~text
GET /__aisys__/api/my-stats/ai-health
~~~

查询参数：

| 参数 | 默认值 | 边界 |
| --- | --- | --- |
| hours | 168 | 1..744 |
| keyword | 空 | 账户名搜索 |
| limit | 20 | 10..50 |
| cursor | 空 | 仅后续页使用的签名 keyset cursor |

每个账户返回：

~~~text
sentinel
  semanticsVersion = 1
  successCount
  failureCount
  unknownCount
  availabilityRate
  hourly[]

capability
  semanticsVersion = 2
  statsContractEpochId
  current
    source = realtime_control_plane
    currentViewVersion
    appliedControlOutboxSeq
    effectiveDispatchRevision
    capabilityUniverseRevision
    dataStatus
    dataStatusReason
    summary
    generatedAt
  hourly[]
    statHour
    semanticsAvailable
    displaySegment
      effectiveDispatchRevision
      capabilityUniverseRevision
      revisionActiveFrom
      revisionActiveUntil
      summaryEvaluationEndAt
      aggregationVersion
      dataStatus
      aggregate
      hourRef
    revisionSegmentCount
    hasRevisionChanges
    revisionsRef
  historyGeneratedAt
  historyStale
~~~

列表响应根级还必须返回 `accountListSnapshotRef / nextCursor / hasMore / generatedAt / listStale / listStaleSince`，不再返回可在持续写入下重复或漏项的 `page / pageSize` offset 分页。第一页选择第 5.7 节当前 ready publication 并签发 10 分钟只读引用；它固定 `listVersion`、manifest digest、`statsContractEpochId`、账户目录 revision、授权可见性 revision、质量排序 publication、stats cutoff watermark vector、v1 hourly cutoff watermark vector、`historyGeneratedAt`、规范化 keyword / hours、权限视图和过期时间，**不固定 capability current control version**。引用不复制凭据、小时数组或全量候选清单；其依赖的 versioned 目录、质量分、v1 / v2 pointer history 和 immutable hourly versions 必须保留到引用 TTL 与安全余量结束。`accountListSnapshotRef` 不能把跨分区水位压成一个伪全局 seq，也不能触发按 viewer 写一份临时快照。

账户 cursor purpose 固定为 `account_health_list`，绑定 `accountListSnapshotRef`、limit 和最后一个冻结排序 tuple。第一页与后续页都从 manifest 指定的同一版本读取账户身份、按 v1 cutoff 选择的哨兵小时 version 和截至同一 stats cutoff 的 v2 小时 pointer；排序 publication 或 v1 / v2 current pointer 后续变化不影响旧快照。每个返回行再按其 runtime identity 批量水合**请求时最新** capability current view，并签发不绑定 current version 的短期 `capabilityCurrentRef`；control 状态在两页之间变化可以让 current 不同，但不会改变冻结排序、重复或漏掉账户。cursor 格式、签名、purpose、limit 或 filter 不匹配返回 `400 invalid_cursor`；快照过期或历史依赖版本已合法清理返回 `409 stale_view`；权限已撤销或跨 viewer 复用返回 `404`。刷新创建新快照，不能把旧 cursor 拼到新响应。

`capability.current` 每次从最新 control-plane view 读取，不从最近小时 publication 或 accountListSnapshotRef 反推。若 active deployment-wide barrier 存在，必须优先返回 `dataStatus=unconfirmed + dataStatusReason=deployment_capability_barrier`；若最新 Catalog / credential baseline 或账户级 current pointer 未 ready，或存在 account-wide hold，则返回对应 unconfirmed / rebuilding 原因，均不能回退到列表签发时的旧 healthy / schedulable summary。已知 scope 的 handoff / projection hold 则返回最新 Route / Attempt 的 `evidenceDataStatus=unconfirmed`、scoped counts 和精确不可调度结论，不能笼统提升账户 dataStatus。`historyGeneratedAt / historyStale` 只描述 stats 历史投影；页面不能用历史 aggregate 覆盖实时 Key owner / effective schedulable 结论，也不能把当前 Key 冷却回写到历史能力颜色。`statsContractEpochId` 标识本响应的历史投影 epoch；v2 尚未启用时 capability 为 null，不补造全绿小时点。时间范围跨过切换点时，切换前小时仍返回槽位，但 `semanticsAvailable=false`、`displaySegment=null`、`revisionSegmentCount=0`；切换后已启用但账户级 projection 未确认 / 重建中时返回对应 `dataStatus`，aggregate=null，页面不能回退成 unknown 或 healthy。

可见行 current 使用独立批量刷新资源：

~~~text
POST /__aisys__/api/stats/ai-health/capability-current:batch
POST /__aisys__/api/my-stats/ai-health/capability-current:batch
~~~

body 只接受列表行返回的最多 50 个 `capabilityCurrentRef`。引用绑定 viewer、runtime identity 与 10 分钟 TTL，但不绑定 currentViewVersion；接口逐项重新鉴权并返回最新 current summary / version / generatedAt，不读取历史小时或改变排序。control outbox invalidation 到达时页面立即批量刷新当前可见行；失去 invalidation 通道时，前台可见页面最多每 5 秒一次批量轮询，不得逐账户 N+1。引用过期刷新列表，单项权限撤销按 opaque ref 返回 notFound，不阻塞其他项。

列表仍固定每账户最多 744 个 statHour 槽，不因同小时多次配置变更扩张。一个小时存在多个双 revision 时，`displaySegment` 按第 5.5 节的半开区间规则选择小时评价边界前有效的 revision；当前未结束小时使用 `historyGeneratedAt` 作为评价边界。它返回 `summaryEvaluationEndAt` 和对应签名 `hourRef`，同时返回 `revisionSegmentCount / hasRevisionChanges / revisionsRef`。`hourRef` 绑定 stats epoch、account / runtime identity、statHour、revision active interval、评价边界、双 revision、账户级 publication snapshot 和当前权限视图；`revisionsRef` 绑定同一 publication member 清单中该小时全部 revision 段、权限视图和快照 TTL。旧 revision 通过下述 revisions 子资源可达，不能仅凭 statHour 猜测。

### 7.2 小时 revision 段

~~~text
GET /__aisys__/api/stats/ai-health/accounts/:accountId/capability-hours/:statHour/revisions
GET /__aisys__/api/my-stats/ai-health/accounts/:accountId/capability-hours/:statHour/revisions
~~~

仅当 `revisionSegmentCount > 1` 时页面才请求，首次请求的 query 必须携带列表返回的 `revisionsRef`；后续使用 `revisionCursor`。参数 limit 默认 20、最大 100，cursor purpose=`hour_revision`，按 `revisionActiveFrom + 双 revision` 稳定分页，并绑定 stats epoch、publication member 清单、当前权限视图和最后排序键；每项返回 active from / until、`summaryEvaluationEndAt`、双 revision、dataStatus、aggregate、aggregationVersion 和可用于 Route / event 明细的 hourRef。`revisionsRef` 格式 / 签名错误返回 `400 invalid_revisions_ref`，cursor 格式 / purpose 错误返回 `400 invalid_cursor`，快照到期或保留版本已清理返回 `409 stale_view`，资源或权限不可见返回 `404`。该资源只读同一账户级 publication 的 immutable member 与小时摘要投影，不扫描事件；极端配置抖动也不会把 744 小时列表变成无界响应。

### 7.3 小时能力明细

~~~text
GET /__aisys__/api/stats/ai-health/accounts/:accountId/capability-hours/:statHour
GET /__aisys__/api/my-stats/ai-health/accounts/:accountId/capability-hours/:statHour
~~~

首次请求的 query 必须携带列表小时点返回的 `hourRef`；后续使用 `routeCursor`。接口按 `hourRef` 指定的 Catalog 分页，并关联同一已发布版本、在 `summaryEvaluationEndAt` 前最后有效的 Route 状态区间与本小时稀疏事实。参数 `limit` 默认 50、范围 1 到 100，使用独立签名 cursor 和稳定 Route 排序；响应返回 stats epoch、双 revision、`summaryEvaluationEndAt`、`aggregationVersion`、`items`、`nextRouteCursor`、`generatedAt` 和 `stale`，不在同一分页中夹带事件。

`routeCursor` 带独立 `route_hour` purpose，并绑定 `hourRef` 的全部声明、规范化筛选和最后 Route 排序键。`hourRef` 格式 / 签名错误返回 `400 invalid_hour_ref`，cursor 格式 / 签名 / purpose 错误返回 `400 invalid_cursor`；引用 TTL 到期、保留 publication 已清理或双 revision 无法解释返回 `409 stale_view`，客户端必须重载小时列表取得新 `hourRef`；资源不可见、权限视图变化或跨运行实例统一返回 `404`。current pointer 更新本身不得使未过期 hourRef 失效。

### 7.4 小时事件

~~~text
GET /__aisys__/api/stats/ai-health/accounts/:accountId/capability-hours/:statHour/events
GET /__aisys__/api/my-stats/ai-health/accounts/:accountId/capability-hours/:statHour/events
~~~

事件首次请求的 query 同样必须携带 `hourRef`，后续使用 `eventCursor`；支持可选 `routeScopeId`，后端必须确认该 Route 属于 hourRef 的可见 Catalog。接口只读 `account_capability_health_route_event_projection`，按 `eventEffectiveAt + routeScopeId + routeEventId` 稳定分页，并以 hourRef 的 stats epoch、publication aggregation version 和 `publishedPartitionSeq` 为不可变上界；不得把 raw Attempt `partitionSeq` 直接作为对外分页键。`eventCursor` 带独立 `route_event` purpose，并绑定 `hourRef` 的全部声明、Route 过滤和最后投影排序键，不能与 `routeCursor` 互换。`hourRef`、stale view 和资源不可见错误与 Route 明细一致。

响应只返回脱敏的 Route transition / outcome 预聚合投影，不返回原始凭据、fingerprint、用户 payload、上游正文、内部 outbox sequence 或精确重复次数。物理所有者 / 管理员可在账户能力详情按权限查看 Attempt trace。授权实例不能读取 raw Attempt 事件，只保留导致 Route `routeAggregate`、`capabilitySchedulable` 或通用 outcome 展示变化的投影。授权实例仍可看到 `partially_unavailable`，这只解释 Route 存在混合能力事实，不承诺该 Route 当前可调度；是否可调度必须读取同项 `capabilitySchedulable`，当前最终可执行性再读取 realtime effective presentation。`credentialTopologyHidden=true` 只承诺隐藏精确数量、身份和事件频次，不把该信号伪装成 healthy。事件仍在保留期时返回 `items / nextEventCursor / eventsExpired=false`；对应事件投影已按保留契约清理时返回空 `items`、`eventsExpired=true`，仍保留 Route 小时摘要，不扫描 usage / audit / 运行日志补造。

管理路径仅管理员。my-* 只使用当前 session；授权实例用户只能看自己的聚合和脱敏 Route，不能看到来源 Key、其他实例或 trace。不可见资源返回 404。

### 7.5 账户分页和排序

后端先按冻结的权限、账户名搜索和质量排序 publication 做 keyset 分页，再按 publication 的 v1 / v2 cutoff 批量读取当前页账户的小时结果。冻结排序 tuple 固定为 `recent_request_count DESC + last_used_at DESC NULLS LAST + normalized_name ASC + account_runtime_key ASC`；每一项都来自 `accountListSnapshotRef` 指向的同一 publication / 目录版本，v1 不得回读 mutable `account_health_hourly`，后续页也不能重新读取 mutable `account_quality_scores` 或账户名称参与排序。禁止 offset、逐账户查询和请求时全量 `COUNT(*)`；`hasMore` 由 `limit + 1` 判断。

## 8. 页面

菜单保留：

- 管理入口：/ai-health
- 用户入口：/my-ai-health

页面使用账户卡片列表：

- 工具栏提供账户名搜索、时间范围和刷新。
- v1 图例显示“哨兵可用、哨兵不可用、无记录”。
- v2 图例显示“能力可用、未知、确认中、部分不可用、恢复中、全部阻断”。
- 每个账户先展示统一 effectiveAvailability / availabilityPresentation，再展示哨兵和能力两条独立摘要。
- active + partially_unavailable 只有在 realtime `effectiveSchedulableRouteCount > 0` 时显示“可调度 · 部分能力不可用”；若 `capabilitySchedulableRouteCount = 0`，显示“当前无可调度模型能力 · 部分能力待确认”，不得从 aggregate 推断可调度或冒充全部 confirmed blocked；若 capability 尚有候选但实时 Key × capability 交集为 0，显示 Key / mixed blocker。
- active + all_configured_capabilities_blocked 显示“当前无可调度模型能力”，不能继续只显示绿色“可调度”。
- quality_isolated、disabled、error、用户显式 temporary_unavailable 等硬状态优先。
- 小时状态使用 Canvas，31 天不创建 744 个 DOM 节点。
- 点击 v1 槽查看哨兵诊断；点击 v2 槽时若存在多个 revision 段，先分页选择目标 revision，再携带其 `hourRef` 用 `routeCursor` 查看模型、endpoint 和转换路径，并按需用独立 `eventCursor` 查看通用 outcome。三个分页状态互不覆盖；任一接口返回 `stale_view` 时关闭旧明细并重载小时列表，不能沿用旧 `hourRef`。
- v2 事件过期后仍展示小时摘要，并注明明细已过保留期。

页面不显示内部时区、generation、lease、原始 fingerprint 或来源账户秘密。

页面状态决策固定如下，刷新不能用瞬时空值覆盖已经成功显示的数据：

| 数据条件 | 页面行为 |
| --- | --- |
| 首次 loading | 显示固定高度骨架，Canvas 和摘要不抖动 |
| 后台 refresh | 保留旧数据并显示轻量刷新态；新响应完整后一次替换 |
| 请求失败且已有数据 | 保留旧数据、标记“刷新失败”并提供重试；不得清空成无记录 |
| 请求失败且无数据 | 显示错误态和重试，不显示业务空态 |
| `capability=null` | 显示“能力历史尚未启用”，仅展示 v1 |
| `semanticsAvailable=false` | 该小时显示 v2 未启用，不绘制 unknown 色块 |
| `dataStatus=unconfirmed` | 显示“能力数据待确认”，aggregate 保持空 |
| `dataStatus=rebuilding` | 显示“能力数据重建中”，aggregate 保持空 |
| ready 且 Route 总数为 0 | 显示“未配置可路由模型能力”，与 unknown Route 区分 |
| ready + unknown / no_confirmed_unavailability | 灰色“能力未确认”，不得显示绿色 |
| ready + partially_unavailable 且 `effectiveSchedulableRouteCount > 0` | 显示“可调度 · 部分能力不可用” |
| ready + partially_unavailable 且 `capabilitySchedulableRouteCount = 0` | 显示“当前无可调度模型能力 · 部分能力待确认”，不得显示全部 confirmed blocked |
| ready 且 capability 有候选、但 `effectiveSchedulableRouteCount = 0` | 按实时事实显示 `all_keys_unavailable` 或 `instance_no_effective_route`，不得沿用历史“可调度” |
| `listStale=true` | 保留冻结排序和历史，根级显示“列表历史更新延迟”及 `listStaleSince`；不得把它套到 realtime current，也不得隐藏 current 的 blocked / unconfirmed |
| `historyStale=true` | 保留历史并显示聚合延迟时间；不回退到 v1 填充 |
| `eventsExpired=true` | 保留小时与 Route 摘要，事件区显示“明细已过保留期” |
| 409 stale_view | 关闭旧明细、清理三个 cursor、刷新列表；不得无限自动重试 |

账户列表、能力详情和本页都调用主设计定义的 `CapabilityHealthPresentation`。Canvas 的 v2 六色映射对应 Route 聚合；账户级 `no_confirmed_unavailability` 与 Route unknown 共用 neutral 色，但 tooltip 必须使用账户级文案，不能把两种枚举混为一个后端值。

## 9. 历史切换

- capabilityHealthSemanticsStartedAt 记录 v2 启用时间。
- 启用前的 account_health_hourly 只按 v1 展示，不迁移成 v2。
- 启用后的 v1 仍继续记录哨兵，不被 v2 覆盖。
- v2 聚合不存在时显示“能力数据待聚合”或“尚无能力观察”，不能用 v1 success 填充。
- 回滚 v2 时保留 v1；v2 表可只读保留或按迁移契约移除。

## 10. 性能与容量

- 单次最多返回 50 个账户、每账户最多 744 个 v1 / v2 小时摘要；账户列表使用 10 分钟 snapshot keyset cursor，不使用 mutable offset 排序。
- 当前页账户 ID 分块批量查询；禁止 N+1。
- Route 明细按需加载，默认 50、最大 100。
- revision、Route 和事件使用三个独立分页请求；关闭小时详情时取消三类在途请求并丢弃旧 cursor，不能把一个 cursor 用于另一资源。
- 每个物理 AI 账户最多 50 个 current credential slots，授权运行实例继承同一受限目录。创建、编辑、导入或重授权若会超过 50 必须在配置事务提交前拒绝并返回明确容量错误，不能静默截断；迁移时已超限的账户保持 capability projection `unconfirmed` 并进入治理清单，直到降到边界内才允许 ready。
- 每个 `accountRuntimeKey + statHour` 最多 128 个非零双 revision 段。配置 owner 在生成第 129 段前拒绝该次配置变更，返回 `429 capability_revision_rate_limited` 与跨到下一统计小时的 `Retry-After`；不得合并 revision、覆盖旧桶或让 stats owner 接受后再丢弃。该限制与 8192 Route 保存上限共同构成配置 admission。
- 账户摘要按小时稠密保存；Route 只保存 Catalog 初始状态、状态有效区间和有活动的稀疏小时事实。禁止按“Route 数 × 744 小时”物化全量快照。以单账户 8192 Route 上限计算，旧方案会产生 6,094,848 行 / 31 天，不能作为默认存储模型。
- 普通增量 publication build 的 versioned delta 硬上限为 100,000 行，计入 Route segments、Route 稀疏小时事实、Route 事件投影、账户摘要和 publication members。达到上限时停止扩展该 build，对目标账户 / partition 施加 backpressure，后续事件保留在 durable outbox，不能丢弃、ACK 到未物化位置或推进 publication cursor。一个不可分割 repair 预计超过 100,000 行时，账户先发布 `data_status=rebuilding`，再用同一 rebuild_id 分多个不可见 staging batch 写入，每批不超过 100,000 行；全部 batch、member manifest 和水位校验完成后只用一次 pointer 事务原子发布，禁止部分小时提前可见。
- stats owner 使用运行账户级 `aggregation_version` 和 epoch + partition 连续 watermark 的 staged 写入，通过账户级 publication build / member / pointer CAS 原子发布；API 不读取未发布版本，也不在请求中聚合 raw Attempt 事件。被替代版本和旧 publication 至少覆盖签名快照 TTL，保证当前小时稳定翻页。
- Canvas 绘制必须有固定尺寸和命中索引，不为每个小时点创建组件或监听器。
- 外部 source observation 的最大允许迟到窗口固定为 7 天：outcome 用数据库 `created_at - observed_at`，phase 用 `created_at - state_effective_at` 判断。超窗 outcome 不回写旧小时计数；超出 31 天 `history_repair_floor` 的状态区间不重开已退出保留窗的 publication，只记录有界审计 / 指标并以 ledger rebuild 校准 current。已在窗口内 durable commit 的 outbox 事件，以及由权威 deadline 生成的确定性内部事件，不会因 ingest / stats 内部 lag 后续超过 7 天而被丢弃，仍须连续消费；内部 lag 只会保持 stale / not-ready。
- capability raw event 和 Route 事件投影的名义保留截止为 `max(observed_at, state_effective_at) + 7 天`，但实际 `retain_until` 还必须覆盖已签发引用 TTL 与安全余量。清理必须同时满足 epoch + partition ingest ACK、stats cursor 和所有受影响 publication 窗口均越过该事件、对应 publication 已完成、outbox 可清理且 retain_until 已到；stats lag、partition gap 或有效 ref 存在时不得删除。`eventsExpired=true` 只在对应投影已合法退出保留窗时返回，不能仅按 statHour 年龄猜测。
- Route 状态区间、稀疏小时事实和账户摘要默认历史窗口为 31 天；Route 事件 projection 仍按上一条 7 天事件保留契约清理。开放区间、当前 pointer 可达区间以及任何与 `[history_repair_floor, now]` 相交的封闭区间都不得按 `segment_start_at` 删除；封闭区间只有 `segment_end_at < history_repair_floor`、已被 publication 替代且 snapshot TTL / retain_until 均结束后才能清理。长期不变化的开放区间即使开始早于 31 天也必须保留。
- 用于旧小时分页和脱敏展示的 Catalog revision、Route 描述、credential membership baseline 元数据至少保留 31 天加 7 天迟到安全期，再叠加引用 TTL 与安全余量；只清凭据秘密，不得先清模型、endpoint、adapter route 或 revision 基线导致历史行无法解释。current publication 在退出 31 天列表窗口后仍须经过“停止签发引用 + 最后 TTL + safety”才能清理。清理任务按 epoch + partition 独立有界运行。

## 11. Mockdata

Mockdata 必须通过真实事件 / 聚合 owner 生成：

- 超过一页的账户。
- v1 success / failure / unknown。
- v2 六种 scope 状态，以及 no_routable_capability、healthy、no_confirmed_unavailability、confirming、partially_unavailable、recovering、all blocked 全部账户聚合。
- A 可用、B 阻断的多模型账户。
- Key-1 + B 阻断、Key-2 + B 可用的多 Key 账户。
- 授权实例凭据拓扑隐藏、零 Route、零凭据 unknown、纯 SUSPECT、blocked + unknown、投影 unconfirmed / rebuilding 的样本。
- active deployment-wide barrier 的样本：所有账户 current / 小时摘要均为 `unconfirmed + deployment_capability_barrier`，解除后恢复各自原状态而不是全量 healthy。
- 单 Route 同时 blocked + 未到期 SUSPECT，aggregate 为 partially unavailable 但 capability / effective schedulable 均为 0 的样本。
- 切换日前只有 v1、切换日后 v2 聚合滞后、同小时多 revision、旧 revision 在半小时结束、繁忙当前小时连续 publication、事件已过期的样本。
- stats epoch 重分区、outbox gap、ingest ACK 丢失、无新事件 deadline 到期、跨整点崩溃追赶和 7 天迟到边界样本。
- 50 credential slots、每小时 128 revision 段和 100,000 行 build 边界内外的容量样本。

禁止前端硬编码状态。

## 12. 明确不做

- 不判断失败是谁的责任。
- 不维护错误码白名单、黑名单或动态错误分类。
- 不把 v1 哨兵结果扩散为全部模型结论。
- 不根据页面查询触发全模型扫描。
- 不把健康监控变成生产重检入口。
- 不把 unknown 计为健康或失败。
- 不用模型能力结果修改 accounts.status。

## 13. 验收标准

- v1 三态、可用率和历史行为保持兼容，文案明确是哨兵；无 snapshot 的兼容 reader 可继续读取 current 表，带 `accountListSnapshotRef` 的列表只能按 publication 的 v1 cutoff 读取 immutable version。
- 第一页签发 `accountListSnapshotRef` 后，同小时新哨兵结果、迟到记录或全量重算即使推进 v1 current pointer，旧 snapshot 的后续页仍返回原 v1 version；刷新到新 publication 后才看到新结果。版本插入、history 封闭、current CAS、兼容表更新和 dirty outbox 前后逐点崩溃均不得暴露半成品，引用 TTL 内也不得清理旧 version。
- `accountListSnapshotRef` 只冻结账户排序与小时历史，不冻结 `capability.current`。模型 B 在翻页期间被确认 blocked / handoff unconfirmed 后，同页重取或 current batch 刷新必须返回更高 currentViewVersion 和最新不可调度 / 待确认结论；旧历史 cursor 仍无重复漏项，不能继续显示十分钟旧“可调度”。
- current invalidation 丢失时可见页 5 秒批量轮询兜底，50 行只产生一次请求；`listStale=true` 明确显示排序 / 历史延迟，不改变或遮盖 current 状态。
- A 成功、B 失败时 v1 与 v2 可以同时呈现不同结论，页面不互相覆盖。
- A 成功、B/C 未观察时 v2 不是 healthy。
- 全部能力阻断时统一状态显示无可调度能力，但 accounts.status 未变化。
- v1 / v2 切换日前后、聚合滞后和事件过期均有稳定显示。
- 管理 / my-* 权限、授权实例脱敏和 404 边界正确。
- Key-1 blocked、Key-2 available 的 Route 保持可调度，迟到 credential 事件不能把 Route 或账户摘要覆盖成 blocked。
- blocked + 未到期 SUSPECT 的单 Route 可得到 partially unavailable 且 capability schedulable 为 0；当前页面不得显示“可调度”，也不得把它升级成全部 confirmed blocked。
- 同小时双 revision 由 `revisionsRef / hourRef` 精确选择；10:30 结束的旧 revision 按自己的半开评价边界生成可解释 aggregate，小时末 displaySegment 只选边界前有效的新 revision。revision / Route / event 独立 cursor 不能交叉复用。
- 一个账户级 publication build 可同时更新多个小时并以完整 member manifest 原子切换对应 pointers；未受影响小时继续引用旧 build。current pointer 推进时未过期快照仍可稳定翻页，快照过期后的 stale view 必须重新加载。
- circuit 事务提交 event outbox 后、ingest insert 前、ingest insert 后 ACK 前、stats staging 中、pointer CAS 前后逐点注入崩溃；sequence gap 可由 durable outbox 补齐，任何半成品都不可见且 cursor 不越过缺口。
- 旧 generation 迟到事件只修正其 `state_effective_at` 历史区间，不覆盖 current；outcome 使用 `observed_at` 入桶。同一 effective time 的稳定 tie-break、超 7 天 source lateness 和超 31 天 repair floor 均有确定结果。
- 没有新 circuit 事件时，positive evidence expiry / soft avoid deadline 仍产生幂等 stats event 并发布新区间；deadline claim 前后崩溃可恢复，不重复转换。
- stats owner 在整点前后崩溃仍按 durable hour close cursor 补齐无事件账户摘要；只有 ingest cursor 追过 cutover、deadline cursor 追过 hour end 后才标 ready。
- stats epoch 改变 partitionCount 时，新旧 `(epoch, partition, seq)` 不碰撞；旧 cursor 不能跨 epoch 使用，新 epoch 未全量 rebuild ready 前不得切换。
- 开放超过 31 天的 Route segment 不被清理；在历史窗口最后 10 分钟签发的 ref 可完整存活 TTL。event、projection、Catalog baseline 和 superseded publication 都按 retain_until 验证后清理。
- 授权实例面对同 Route、同小时的大量 credential-only observation 只分页读取预聚合 Route event，不扫描 raw Attempt 事件，不泄漏精确次数，cursor 不跨合并边界重复或漏项。
- 第 51 个 credential slot 和同账户同小时第 129 个 revision 在配置事务前被拒绝且不产生半成品；100,000 行增量 build 达到上限后 durable backpressure、不推进 cursor，超大 repair 分批 staging 后只原子发布一次。
- 8192 Route、31 天容量基准只产生初始状态区间、实际状态变化和稀疏事实，不产生 6,094,848 行 Route 整点快照；历史小时仍可从保留 Catalog 和区间投影稳定分页。
- 31 天列表只读预聚合，当前页批量查询，无 N+1、raw scan 和 744 DOM 节点。
- Browser 回归覆盖管理员、物理所有者、授权实例，全部 aggregate、硬状态优先、加载 / 空态 / 错误 / stale、三类分页、事件过期，并在 901、900 和 390px 宽度验证 Canvas 非空、命中与文字不重叠。
