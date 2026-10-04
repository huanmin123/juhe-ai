# 问题-0284：J1 隔离状态行阻止 pending_test 激活成功投影，账户永久待检查

- 编号：BUG-0284
- 状态：已修复（2026-10-04 登记并修复代码，随当日第二批 jobs 发布）
- 影响面：凡 J1 `account_health_current_state` 存在 direct_input_invalid 隔离基线（`account_status=''`）的 `pending_test` 账户，其激活成功永远无法落业务行——生产 12 个 `profile_gemini_openai_chat_v1beta` 账户中 2 个已实测命中（探针 `complete_success` 200，业务行健康字段全空、状态仍 pending_test）。

## 现象

BUG-0283 修复发布后探针首次真实运行，2026-10-04 12:02 两个 Gemini 渠道（mad.myddns.me_gemini-vip、sub.vcnovb.cn_gemini-cheap）探针返回 200 `complete_success`，但：

- 业务行 `last_health_check_at`/`last_health_success_at` 全空、`status` 仍 `pending_test`；
- J1 `account_health_current_state` 仍停留在 2026-09-26 的 `probe_task_failure` 隔离基线（`account_status=''`，BUG-0262 时代批量 `direct_input_invalid` 写入的 retry 元数据行）。

同因适用所有带隔离基线的 pending_test 账户：这类行由 BUG-0262/0282 时代"探针从未能构建输入"的失败批量产生。

## 根因（状态 CAS 逃生门缺失 activation epoch）

`store.go` `upsertCurrentStateTx` 的同 epoch 状态推进 CAS 在 `Projection != nil` 时附加状态围栏：

```sql
AND (state.account_status = $expected
  OR ($expected = 'active' AND state.account_status <> 'active'))
```

`active` 有越权逃生门（允许权威业务状态为 active 时收敛各类陈旧 jobs 行），但 `pending_test` 没有：隔离基线 `account_status=''` 与期望 `pending_test` 既不相等、也不满足 active 特例 → CAS 永久拒绝 → outcome 落库但状态与业务投影双双不推进。冷却路径不受影响（走 `updateCooldownCurrentStateTx`，其已有同款隔离复水臂：`account_status='' AND error_code='direct_input_invalid' AND 冷却三字段 NULL`，BUG-0282 修复后的 15 个冷却账户正是经此恢复）。

## 修复

- `upsertCurrentStateTx` PG/SQLite 两臂的 CAS 增加第三逃生门：`$expected='pending_test' AND state.account_status='' AND state.error_code='direct_input_invalid'`——精确镜像冷却路径的隔离复水谓词（仅 direct_input_invalid 隔离行可被激活成功推进；对 `request_deadline_elapsed` 等其它空白行、以及任何非空白权威状态行仍不越权）。
- 回归测试 `TestSQLiteHealthCASAdvancesDirectInputQuarantineForPendingTest`：①隔离基线 + pending_test 成功必须推进（红绿验证：移除逃生门必红）；②pending_test 成功不得推进 `temporary_unavailable` 权威状态行（收窄锁定）。

## 关联

- BUG-0262（隔离行源头）/ BUG-0283（探针通路修复后成功首次出现、本缺陷暴露）/ BUG-0282（冷却路径同族，已由冷却复水臂覆盖）。

## 存量数据修复

代码修复发布后，存量 10 个 pending_test 账户的探针仍被冻结的隔离基线去重阻断（next_due 停在 09-26 → scheduled request_id 恒定 → HasRequest 永久去重；冷却账户不受影响因其 due 每轮取自业务行 cooldown_until）。处置：备份表 `juhe_jobs.account_health_current_state_backup_b0284`（10 行）后删除这批隔离 state 行——无状态行走 `!found` 全新开始路径（health due=IssuedAt，id 全新）。发布后实测：mad.myddns.me_gemini-vip 与 sub.vcnovb.cn_gemini-cheap 探针 200 complete_success 即刻激活 active，其余 8 渠道恢复逐周期真实探测（503/404/403 为上游真实状态）。

## 回归验证

- `go test -count=1 ./projects/jobs/internal/accounthealth/` 全包绿（含修复后的 w1cover 覆盖库上全部 PG 族用例）。
