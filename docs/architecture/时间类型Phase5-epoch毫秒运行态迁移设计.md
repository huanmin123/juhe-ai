# 时间类型 Phase 5：epoch 毫秒运行态迁移设计

- 状态：草稿，待评审；Revision 32 未实现。
- 关联：[时间类型与统计口径统一改造](../plans/计划-20261111T075030000Z-时间类型与统计口径统一改造.md) Phase 5。
- 目标：把锁/熔断/投影运行态的 epoch 毫秒瞬时列迁到 `timestamptz`；时长、桶边界和外部协议中的毫秒值保持数值。

## 1. 范围

### 1.1 迁 `timestamptz(6)`

| 表 | 列 |
| --- | --- |
| `account_lock_states` | `next_retry_at_ms`、`lease_until_ms` |
| `account_circuit_incidents` | `open_until_ms`、`next_transition_at_ms`、`lease_until_ms`、`attempt_started_at_ms`、`attempt_hard_deadline_ms`、`retained_until_ms`、`created_at_ms`、`updated_at_ms` |
| `account_circuit_outbox` | `available_at_ms`、`claim_until_ms`、`acknowledged_at_ms`、`created_at_ms`、`updated_at_ms` |
| `account_list_availability_dirty` | `available_at_ms`、`claim_until_ms`、`created_at_ms`、`updated_at_ms` |

精度统一 `(6)`：这些列参与 CAS 与租约比较，毫秒截断会改变“同一毫秒内谁先赢”的语义。

### 1.2 保持数值

- `duration_ms`、`latency_ms`、`first_token_ms`、`bucket_upper_bound_ms`、`first_token_ms_*`、`duration_ms_*` 是时长或桶上界，不是瞬时。
- Redis / Lua 协议中的 `now_ms`、`cooldownUntilMsByRuntimeKey` 是跨进程协议字段，保持毫秒整数；只在 PG 写入/读取边界转换。
- API 响应中的 `*Ms` 字段如需保留，按既有契约输出毫秒，不因数据库类型改变。

## 2. 类型与转换规则

- 写入：`time.UnixMilli(v).UTC()` → `timestamptz(6)`；SQL 参数使用 `time.Time`，禁止再拼数字或文本。
- 读取：扫描为 `time.Time`；需要毫秒整数时显式 `UnixMilli()`。
- NULL 保留：所有可空列保持 `NULL` 语义，禁止用 `0` 表示“无租约/无截止时间”。
- 会话时区固定 UTC（Phase 1 已落地），SQL 不再写 `AT TIME ZONE`。
- 所有边界转换集中到 `dbtime` 或本域单一 helper，禁止在 SQL 中 `to_timestamp(col/1000.0)` 散落转换。

## 3. CAS 与租约算法

### 3.1 租约获取

原语义：`claim_until_ms <= now_ms` 才可抢占。
新语义：`claim_until_ms <= $now`，其中 `$now time.Time`；比较仍在 SQL 中完成，避免 Go 侧读-改-写竞争。

```sql
UPDATE account_circuit_outbox
SET status='processing', claim_token=$1, claimed_by=$2,
    claim_until_ms=$3, attempt_count=attempt_count+1, updated_at_ms=$4
WHERE event_id=$5
  AND ((status='pending' AND available_at_ms <= $4)
    OR (status='processing' AND claim_until_ms <= $4))
```

### 3.2 幂等 CAS

- `updated_at_ms` 比较条件改为 `time.Time` 参数；不再依赖 epoch 整数相等。
- `attempt_started_at_ms`/`attempt_hard_deadline_ms` 的“同时存在”不变量保留，改为 `IS NULL`/`IS NOT NULL`。
- 需要毫秒整数返回给 Redis/调用方时，读取后 `UnixMilli()`，不把 `timestamptz` 文本透出。

### 3.3 索引

- 现有索引列名保持不变，仅列类型变化。
- 表达式索引若含 `*_ms` 算术（如 `updated_at_ms + ...`），先列出并改为 `timestamptz + interval`；无表达式的普通索引由 `ALTER COLUMN TYPE` 自动重建。
- 迁移前用 `pg_depend`/`pg_indexes` 盘点表达式索引与 CHECK；有依赖时在同一事务内先 drop、迁类型、再重建。

### 3.4 代码适配面盘点

- `gateway/cmd/juhe-ai-gateway/chain_account_locks.go`：账户锁租约/下一次重试的读取、CAS 与清扫；重点 `lease_until_ms > ?`、`next_retry_at_ms = next_retry_at_ms + ?`。
- `gateway/internal/business/circuit_control_plane/store.go`：incident/outbox 列集与 CAS，`incidentColumns`/`outboxColumns` 直接拼接列名。
- `jobs/internal/circuitstore/controlplane.go` 与 `listavailability_rows.go`：incident CAS、锁状态清理、列表可用性读取。
- `jobs/internal/opsjobs`：`LeaseUntilMS`/`NextRetryAtMS` 等是内存状态与 JSON 协议边界（`circuitmemorystore`/`circuitprojection`），保留毫秒整数；只有 `circuitstore` 的 PG 读写层做转换。
- `jobs/internal/accounthealth/projection.go`：outbox 插入的 `available_at_ms/created_at_ms/updated_at_ms`。
- 测试夹具与 mockdata 的 `*_ms` 数值需按目标类型改写；`bucket_upper_bound_ms` 等时长列不动。

## 4. 迁移步骤（Revision 32）

1. 预检：四表逐列 `pg_input_is_valid(col::text, 'timestamptz')` 不适用（整数），改为范围校验：`col IS NULL OR col BETWEEN 0 AND 4102444800000`（2100-01-01）。
2. 停写窗口内单事务：
   - 临时 drop 依赖的表达式索引/CHECK；
   - `ALTER COLUMN TYPE timestamptz(6) USING to_timestamp(col / 1000.0)`；
   - 重建索引与 CHECK；
   - 校验行数不变。
3. 更新 fresh schema 与 `BusinessSchemaVersion`/`SchemaRevision`。
4. 代码同批切换：`circuit_control_plane`、`circuitstore`、`accounthealth` 投影 outbox、mockdata 与索引 DDL。
5. 回滚边界：旧二进制因 revision 门禁不可回退；只能恢复停写前备份。

## 5. 验收

- 单元：毫秒→`time.Time`→毫秒往返、NULL、边界 0/2100 年。
- 真实 PG：租约抢占、CAS 冲突、outbox 重试、dirty 投影消费各一条；`EXPLAIN` 命中原索引。
- 静态检查：生产代码不得对 `*_ms` 瞬时列做 `Scan(&int64)` 后直接写回；Redis 协议字段允许整数。
- 全链路：换表后 gateway 写入、jobs 投影消费、清理任务无类型错误。

## 6. 非目标

- 不迁移时长/桶列，不改 API 的 `*Ms` 字段契约。
- 不把 Redis 协议改成 `timestamptz`。
- 不在本阶段清理历史 `isoMillis` 拷贝；Phase 5 收尾统一处理。
