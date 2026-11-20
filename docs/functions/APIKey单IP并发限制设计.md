# API Key 单 IP 并发限制设计

> 设计状态：**已实施并上线（2026-11-19 代码与受影响范围测试完成；2026-11-20 随 `v2026.11.20` 生产发布，schema revision 25 已 applied、revision 26 合并上线，见运维手册发布记录）**。本文是「单 IP 并发限制」从分组调度策略迁移到 API Key 的唯一目标契约。2026-11-19 同日修订：「不限制」统一为 `-1`，`0` 非法，与分组阈值语义对齐。
>
> 关联裁决：2026-11-19 用户确认「单 IP 并发限制」不应在分组里配置，应在 API Key 维度配置；「最大单账户排队阈值」保留在分组。同日追加裁决：「不限制」统一使用 `-1`（与分组 `defaultSoftConcurrency` 一致），`0` 不再是合法配置值。本次只迁移配置归属，**不改变**计数作用域 `(system_account_id, group_id, api_key_id, client_ip)`。

## 1. 背景与决策

2026-11-09 高并发分组退役后，普通分组的 `scheduling_policy_json` 保留两个可选键：

- `defaultSoftConcurrency`：最大单账户排队阈值，软排序策略；
- `clientIpConcurrencyLimit`：单 IP 并发上限，硬拒绝策略。

两者性质不同：

- 软阈值作用于**分组内账户池**的候选排序，让账户使用更平均，不拒绝请求、不占配额；分组是它的自然作用域。
- IP 并发限制的计数键本来就包含 `api_key_id`，实际效果是「每 Key + 每 IP」的并发上限；配置放在分组上属于历史遗留的位置错配，且该键参与超级分组成员调度指纹，改一个客户端保护参数会触发池一致性校验。

因此本次把 `clientIpConcurrencyLimit` 的**配置归属**迁移到 API Key，分组只保留 `defaultSoftConcurrency`。

### 目标

1. API Key 增加 `clientIpConcurrencyLimit` 配置，`-1` 不限制，`1..1000000` 生效，超限固定 `429`；`0` 不是合法值，避免与分组阈值的 `-1` 语义分叉。
2. 删除 `groups.scheduling_policy_json.clientIpConcurrencyLimit`；普通分组策略只允许 `defaultSoftConcurrency` 一个键。
3. 计数作用域保持 `(system_account_id, group_id, api_key_id, client_ip)` 不变；现有单分组 Key 的运行行为不变。
4. 超级分组调度指纹只按 `group_type + defaultSoftConcurrency` 计算，IP 限制不再参与分组指纹。
5. 提供一次性迁移命令与 schema revision 25，把存量分组值映射到可达 API Key。

### 非目标

1. 不改变计数作用域，不实现「同一 Key 跨多个分组共享一个 IP 槽」。
2. 不变更「最大单账户排队阈值」的配置位置与语义，不做 per-key 阈值覆盖。
3. 不实现真正的「整 IP 跨多个 Key 总并发」；如需要，另立账号级或全局 IP 配额设计。
4. 不改变 realtime WS 长连接边界：WS 仍不经过本闸口，继续受 `realtimeMaxConnectionsPerApiKey` 约束。
5. 不改变账户硬并发、RPM、分组整池等待、错误语义、熔断/冷却/探针规则。

## 2. 字段与作用域契约

### 2.1 API Key 字段

`api_keys` 增加：

```sql
client_ip_concurrency_limit integer NOT NULL DEFAULT -1
```

| 字段 | 类型 | 默认 | 合法范围 | 语义 |
| --- | --- | --- | --- | --- |
| `clientIpConcurrencyLimit` | 整数 | `-1` | `-1` 或 `1..1000000` | 单 IP 并发上限；`-1` 不限制；超限立即 429 |

- JSON 字段名为 `clientIpConcurrencyLimit`，列名为 `client_ip_concurrency_limit`。
- `-1` 是「不限制」的唯一配置取值，与分组 `defaultSoftConcurrency` 的关闭语义一致；`0` 不是合法值。
- 创建缺省或显式 `null` → 持久化 `-1`；PATCH 缺省 → 保持原值；PATCH 显式 `-1` → 不限制。
- 显式 `0`、小于 `-1`、超过 `1000000` 或非整数按字段校验错误拒绝（400），不静默截断、映射或取整。
- 该字段与 API Key 的 `quotaLimits`、`availabilitySchedule`、`securityPolicyId`、`schedulingPreferenceId` 同属 Key 级配置，管理面与个人面均可读写；写入口与现有 API Key 更新一致，受 owner 校验与 `expectedRevision` CAS 约束。

### 2.2 计数作用域

沿用现行实现：

```text
systemAccountId + groupId + apiKeyId + clientIp
```

说明：

- 同一 Key 在同一分组、同一 IP 下的并发共享一个计数；
- 不同 Key、不同分组、不同 IP、不同系统账户互不影响；
- 一个 Key 经策略路由到达多个分组时，每个分组各自计数；
- 无 API Key 的内部请求（内部身份通道）视为 `limit=-1`，不启用闸口；
- `clientIp` 为空时不启用闸口（沿用现行行为）。

### 2.3 闸口行为

- `limit = -1`：不限制，完全不启用闸口，零额外开销。
- `limit > 0`：在分组解析完成、账号准入/派发前，按作用域获取一个 IP 槽；达到上限立即返回 `429`，不占账号并发、不进入任何等待队列、不写账号状态。
- IP 槽在响应结束（成功/失败/流中断/取消）释放；客户端断开同样释放。
- 路由计划还有后续分组时，按既有跨组回退逻辑处理；IP 限制不改变跨组回退语义。
- 审计继续记录 `client_ip_concurrency` 元数据（enabled/acquired/current/limit/reason）；错误归因保持 `gateway_capacity`，错误码保持既有 IP 并发拒绝契约。

### 2.4 分组策略

`groups.scheduling_policy_json` 收敛为单键：

| 键 | 类型 | 默认 | 合法范围 | 语义 |
| --- | --- | --- | --- | --- |
| `defaultSoftConcurrency` | 整数 | `-1` | `-1` 或 `1..1000000` | 最大单账户排队阈值；`-1` 关闭 |

- 非空策略必须只包含该键；缺键、未知键、越界或非整数一律 400。
- `NULL` 等价于 `defaultSoftConcurrency=-1`。
- 读取兼容：迁移前后，旧两键 JSON 中的 `clientIpConcurrencyLimit` 作为 legacy 键读取并忽略；迁移完成后 postflight 必须确认分组策略中不再出现该键；新写入只允许单键。
- 分组 API 的 `schedulingPolicy` 响应只返回 `{ defaultSoftConcurrency }` 或 `null`。

### 2.5 超级分组指纹

成员调度指纹输入收敛为：

```text
SHA256(canonical_json({
  "group_type": group.group_type,
  "scheduling_policy_json": normalized_single_key_policy
}))
```

- 算法版本升至 `group-dispatch-fingerprint:v2`；指纹不持久化，无数据迁移。
- IP 限制不再参与成员一致性校验；`super_group_member_dispatch_policy_mismatch` / `super_group_member_dispatch_policy_conflict` 只由 `group_type` 与 `defaultSoftConcurrency` 决定。
- 迁移命令必须重写普通分组的策略 JSON，并按首成员重算超级分组自身行；空超级分组收敛为 `personal + NULL`。

## 3. API 与前端契约

### 3.1 管理面与个人面

- `POST /__aisys__/api/api-keys`、`POST /__aisys__/api/my-api-keys`：接受可选的 `clientIpConcurrencyLimit`，缺省 `0`。
- `PATCH /__aisys__/api/api-keys/:id`、`PATCH /__aisys__/api/my-api-keys/:id`：只在字段实际变化时提交；成功响应 `changedFields` 包含 `clientIpConcurrencyLimit`，`rowPatch` 携带新值。
- `GET /__aisys__/api/api-keys`、`GET /__aisys__/api/my-api-keys`：列表项返回 `clientIpConcurrencyLimit`。
- 单条详情/编辑种子复用列表行；不新增详情接口。
- 操作日志变更标签新增「单 IP 并发限制」。
- 分组创建/PATCH 的 `schedulingPolicy` 只接受单键；`clientIpConcurrencyLimit` 作为未知键拒绝（400），不再静默忽略。

外部集成 API（`/__aipublic__/*`）若现有 API Key 推送字段白名单包含 Key 级调度配置，则同步暴露该字段；若白名单未覆盖，则不新增外部字段，保持现有契约。

### 3.2 前端

- 分组创建/编辑表单删除「单 IP 并发限制」输入与校验；分组列表/详情摘要删除对应文案，只保留「单账户排队阈值」。
- API Key 创建/编辑表单增加「单 IP 并发限制」整数输入：默认 `-1`，帮助文案挂在字段 label 的问号提示上（「-1 表示不限制；设置后同一 IP 超过并发立即返回 429」），不设独立区块标题。
- API Key 列表按「列表只做概览」原则决定是否展示摘要；至少编辑弹窗必须可回填与保存。
- 更新相关类型、表单模型、mutation payload、回归脚本与 smoke 断言。

## 4. 数据迁移

### 4.1 Schema

- `api_keys` 加法列 `client_ip_concurrency_limit integer NOT NULL DEFAULT -1`；`BusinessSchemaVersion` 升至 `business-postgres-gateway-v24`。
- `SchemaRevision` 升至 25（当前 24）；注册 `migrate-key-client-ip-limit` 数据步骤，标记 Destructive，只能在停写窗口执行。
- DDL 在 upgrade 的加法相幂等执行；数据步骤与 DDL 同批，不拆到 `--ensure-schema`。

### 4.2 迁移命令

命令：

```text
juhe-ai-maintenance migrate-key-client-ip-limit \
  [--dry-run] \
  [--manifest-out <path>] \
  [--conflict-policy=block|min] \
  [--check=preflight|postflight] \
  [--stop-write-confirmed --backup-confirmed] \
  --dsn "$JUHE_AI_POSTGRES_URL"
```

默认 `--conflict-policy=block`。`--check`、`--dry-run` 全程只读；`--dry-run` 必须提供 `--manifest-out`（唯一写入是 manifest 文件），正式 apply 可选提供 `--manifest-out` 留档。正式 apply 必须同时带 `--stop-write-confirmed --backup-confirmed`（缺一 exit 2），命令本身不负责停进程。独立 apply 的前提是 revision 22 已完成（`groups.group_type` 已全部为 `personal`）；正常发布序列由 `upgrade` 按 revision 顺序执行，不单独先跑本命令。

映射规则：

1. 可达分组按**配置拓扑全量**解析：`route_strategy_groups` 的全部绑定行（不过滤策略/绑定状态、分组 enabled、授权有效期与绑定数量上限）；超级分组展开全部 `super_group_members`（不过滤成员 enabled）；合并路由按绑定的全部分组。manifest 对每个可达分组标注配置可达性（`runtimeActive` = 绑定 `active` + 分组/成员 `enabled`，不含授权有效期、授权开关与策略状态），供操作者核对差异，但过滤与否不改变迁移取值。迁移语义是搬配置，不丢 disabled 分组上的旧值。**分组 owner 与 Key owner 不同不视为异常**：被授权人可用自己的策略绑定授权分组，绑定行 owner 是 grantee、分组行 owner 是 grantor；只有 Key 与策略/绑定 owner 不一致这类非法拓扑才进入 `needs_review`。
2. 对每个 API Key，收集可达分组旧 `clientIpConcurrencyLimit` 的正值。
3. 只对当前 `client_ip_concurrency_limit = -1` 的 Key 写入推导值：唯一正值 → 写入；无正值 → 保持 `-1`（不产生 UPDATE）。revision 25 未发布版本曾使用 `0` 作为临时关闭值，迁移按未设置处理并归一为 `-1`（无观测值时产生一次归一 UPDATE），避免 `0` 残留在新契约。
4. 当前值已 `>0` 的 Key 视为 `preexisting`，**不覆盖**：若该值等于可达分组观测到的任一正值，视为人工裁决已生效（保留并继续）；若观测集非空且与全部观测值都不同，记 `existing_key_limit_conflict` 进入 `needs_review`，`min` 模式也不改写已有值，只在 manifest 记录冲突；若观测集为空（旧分组本来没有 IP 值，或迁移完成后的幂等重跑），保留当前值且不记冲突。这样人工把冲突 Key 设为某个候选值后重跑可以收敛，既不会被清空也不会再次阻断。当前值小于 `-1` 或大于 `1000000` 属于非法存量，记 `invalid_key_limit_value` 进入 `needs_review`。
5. 多个不同正值（针对当前值为 `-1` 的 Key）：
   - `block`（默认）：该 Key 进入 `needs_review`，apply 以退出码 3 零写入阻断，由人工确认后用 `min` 重跑或手工设置；
   - `min`：取正值集合的最小值写入，并在 manifest 中记录 `conflict` 行与原值集合。
6. 分组策略 JSON 重写为 `{"defaultSoftConcurrency":X}`；`X=-1` 时写 `NULL`。未知键/损坏 JSON 进入 `needs_review`。单键 soft 值必须原样读取，不得按旧 `globalMax` 规则二次映射。
7. 超级分组自身行：先检查全部成员（不论 enabled）的 `defaultSoftConcurrency` 是否一致；不一致进入 `needs_review` 阻断，不静默选边；一致但自身行不一致时按首成员重算。空超级分组写 `personal + NULL`。
8. 不更新 `groups.updated_at` 与 `api_keys.updated_at`，避免前端乐观锁 churn；发布流程重启 gateway/jobs 刷新运行时缓存。
9. 迁移必须可重跑：首次成功后重跑零写入（分组已单键、Key 已为正值或 `-1`），不得把已迁移值清回 `-1`。

### 4.3 兼容与回滚

- 新二进制在迁移前可读取旧两键分组策略：解析时忽略 `clientIpConcurrencyLimit`，按单键策略运行；写面从新二进制启动起只接受单键。
- revision 22 的 `groupspolicymigration` 完成态谓词同步接受单键（`defaultSoftConcurrency` 单键或 NULL）：否则 revision 25 之后的 `upgrade --phase=all` postflight 会把单键判为未完成，独立重跑 `migrate-group-policy` 还会把单键写回两键。
- `mergesupermigration` 等历史独立迁移命令在 revision 25 之后不得再产出两键策略；要么按单键守卫，要么在文档中约束为升级序列专用。
- `legacybusinessimport`（旧库回迁）在 revision 25 之前运行时**保留旧两键历史形状**（含 `clientIpConcurrencyLimit`），由回迁后 `upgrade` 链的 revision 25 步骤统一映射到 API Key 并把分组策略单键化；导入命令自身不写 `api_keys.client_ip_concurrency_limit`，也不直接写单键，避免旧 IP 值在迁移前被丢弃。该命令的导入目标是空库，必须随后执行 maintenance upgrade，不做向后兼容承诺。
- 迁移完成后 postflight 必须确认：
  - `groups.scheduling_policy_json` 不含 `clientIpConcurrencyLimit`；
  - 所有 API Key 的 `client_ip_concurrency_limit` 非空且为 `-1` 或 `1..1000000`；
  - 超级分组自身行与首成员策略一致。
- 回滚限制：旧二进制不认识单键分组策略与 API Key 新列，回滚只能恢复升级前备份；新旧行为差异必须在发布说明中列明。
- 新 Key 默认 `-1`（不限制）；升级后新建 Key 不会自动继承旧分组值，需按需配置。

## 5. 验收标准

1. **配置归属**：API Key 创建/编辑可设置 `clientIpConcurrencyLimit`，分组创建/编辑不再接受该字段；分组策略只剩 `defaultSoftConcurrency`。
2. **运行行为**：`limit=-1` 零开销；`limit>0` 超限 429、不占账号并发；不同 Key/分组/IP/系统账户隔离；响应结束与取消/断流释放。
3. **行为保持**：现有单分组 Key 在迁移前后并发上限行为一致；同一 Key 跨多个分组仍按分组各自计数。
4. **指纹**：超级分组指纹只由 `group_type + defaultSoftConcurrency` 决定；改 API Key 的 IP 限制不影响超级分组一致性。
5. **迁移**：dry-run 报告完整；冲突 Key 默认阻断；`min` 模式可收敛；迁移幂等；旧两键 JSON 可读、写面拒绝；postflight 通过。
6. **前端**：分组表单/摘要无 IP 字段；API Key 表单可回填、校验和保存；移动端与列表摘要一致。
7. **测试**：受影响包单测与功能基线通过；迁移包真实 PG 场景通过；不使用全量测试替代影响范围验证。

## 6. 风险与未决项

1. **多分组 Key 的旧值冲突**：一个 Key 可达多个分组且旧值不同，无法无损映射；默认阻断，避免静默改变保护强度。
2. **新 Key 默认关闭**：升级后新建 Key 不再自动继承分组保护；需在发布说明与前端帮助文案中明确。
3. **超级分组旧数据**：成员策略不一致的存量数据在迁移中进入 `needs_review`，不自动选边。
4. **回滚**：单键分组 JSON 与 `business-postgres-gateway-v24` 使旧二进制无法直接启动；回滚依赖升级前备份。
5. **内部请求**：无 API Key 的内部通道不启用 IP 闸口；与现行行为一致，但需在测试中固定。

## 7. 关联文档

- [普通分组可选调度策略设计](普通分组可选调度策略设计.md)（软阈值与分组策略单键化）
- [IP级错误熔断与恶意请求保护设计](IP级错误熔断与恶意请求保护设计.md)（IP 作用域与错误熔断分层）
- [超级分组设计](超级分组设计.md)（成员调度指纹）
- [合并路由设计](合并路由设计.md)（窗口组作用域）
- [核心功能设计](核心功能设计.md)（分组与 API Key 字段）
- [接口契约与权限矩阵](接口契约与权限矩阵.md)（API Key 写读契约）
- [升级兼容与迁移账本设计](升级兼容与迁移账本设计.md)（revision 25 与停写窗口）
- [部署指南](../deploy/部署指南.md)、[单机部署配置](../../docker/single-server/README.md)（发布与回滚）
