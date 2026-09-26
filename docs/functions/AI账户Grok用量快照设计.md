# AI 账户 Grok 用量快照设计

- 状态：设计定稿（2026-09-26，端点已实测）
- 实现归属：`backend-go/projects/jobs`（刷新任务族）、`backend-go/projects/gateway`（读取投影）、`backend-go/projects/maintenance`（schema）、`frontend/src`（显示）
- 关联：`AI账户上游余额查询设计.md`（J2，中转余额，本设计不复用其 adapter 体系）

## 1. 背景与目标

GPT（OpenAI Codex OAuth）账户在前端展示窗口用量（`oauthUsage`：5 小时窗/周窗百分比），数据来自网关转发时上游响应头的被动采集（`gatewayupstream/usageheaders.go` → `account_usage_snapshots` 表 kind=`openai_codex`）。

xAI/Grok OAuth 账户（SuperGrok 订阅，上游 `https://cli-chat-proxy.grok.com/v1`）没有等价的响应头采集通道，但提供可主动查询的用量端点（2026-09-26 用生产账户 acc_f6a60b47564ab148 实测，HTTP 200）。

**目标**：为 `providerCode=xai` 且 `type=oauth` 的账户建立周期性用量快照，前端在账户视图文案中显示套餐名、额度已用百分比、当前周期与重置时间。

**非目标**：
- 不做 5 小时会话窗（社区无干净查询端点，billing 只给订阅周期窗）；
- 不做 grok.com gRPC-web fallback（需要浏览器 WKE 密钥，超出 OAuth token 能力）；
- 不做限额重置券（`GetRemainingResets`）查询；
- 不改 Codex 快照的被动采集链路。

## 2. 上游端点（实测）

### 2.1 GET /v1/billing?format=credits

请求头：`Authorization: Bearer <access_token>`、`x-xai-token-auth: xai-grok-cli`、`accept: application/json`。出站走账户绑定代理（与聊天同域）。

实测响应（节选，2026-09-26）：

```json
{"config":{
  "currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-09-21T02:34:03.052517+00:00","end":"2026-09-28T02:34:03.052517+00:00"},
  "creditUsagePercent":14.0,
  "onDemandCap":{"val":0},"onDemandUsed":{"val":0},
  "productUsage":[{"product":"GrokBuild","usagePercent":13.0},{"product":"GrokChat","usagePercent":1.0},{"product":"GrokImagine"}],
  "isUnifiedBillingUser":true,
  "billingPeriodStart":"2026-09-21T...","billingPeriodEnd":"2026-09-28T..."
}}
```

字段取舍：
- 主用量：`config.creditUsagePercent`（float，0-100）；
- 周期：`config.currentPeriod.{type,start,end}`；`end` 缺失时回退 `config.billingPeriodEnd`；
- 分产品：`config.productUsage[]` 原样存 JSON（可选展示）；
- `onDemandUsed/onDemandCap`：两者 `val` 均 >0 时存备用百分比（`onDemandUsed/onDemandCap*100`），否则不存。

### 2.2 GET /v1/settings

同请求头。实测取 `subscription_tier_display`（如 `"SuperGrok Heavy"`）；缺失或字段为空则快照不写套餐名。

## 3. 数据模型

复用 stats 库 `account_usage_snapshots`（PK `(system_account_id, account_id, kind)`，含 `snapshot_json/refresh_status/last_attempt_at/last_success_at/next_refresh_after/last_error_message` 完整刷新状态列）。

- kind 新增 `'xai_grok'`：
  - maintenance schema（`pg_schema_stats.go` / `sqlite_schema_stats.go`）CHECK 同步扩展；
  - 存量库受控变更（加法式）：`ALTER TABLE <stats>.account_usage_snapshots DROP CONSTRAINT account_usage_snapshots_kind_check; ADD CHECK (kind IN ('openai_codex','relay_balance','xai_grok'))`。
- `snapshot_json` 字段（全部 `grok_` 前缀，对齐 `codex_*` 惯例）：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `grok_credit_used_percent` | number | 额度已用百分比 |
| `grok_period_type` | string | 如 `USAGE_PERIOD_TYPE_WEEKLY`（存原文） |
| `grok_period_start` / `grok_period_end` | string(RFC3339) | 当前周期起止 |
| `grok_subscription_tier` | string | 套餐名（settings） |
| `grok_product_usage_json` | string | `productUsage[]` 原样 JSON（可选） |
| `grok_on_demand_used_percent` | number | 按量池百分比（可选） |

- `source` 固定 `xai_grok_billing`。

## 4. 刷新任务族（jobs）

- 注册名 `xai-grok-usage-refresh`，lane `external-account-maintenance`，interval 10 分钟（用量百分比变化粒度低，10 分钟足够；timeout 60s，OverlapCoalesce 同 J2 惯例）。
- 每轮：查 `juhe_business.accounts` 中 `provider_code='xai' AND type='oauth' AND status='active' AND deleted_at IS NULL` 的活跃账户（disabled/expired 账户 token 多半已失效，跳过以免白耗出站超时）→ 逐个（并发 ≤4）：
  1. 解密 `credentials_encrypted` 取 `access_token`（复用 shared 解密封装）；
  2. 调 billing + settings（出站走账户绑定代理，未绑定走默认出海代理链）；
  3. UPSERT 快照（成功写 `refresh_status='ok'`+`last_success_at`，失败写 `'failed'`+`last_error_message`，沿用表列）；
  4. token 401 时**不**主动刷新 token（留给既有凭据轮换体系），记失败。
- 端点不标注幂等副作用（纯读）；单账户失败不影响其他账户。
- 实现落点（2026-09-26 交付）：jobs 组合根 `backend-go/projects/jobs/cmd/juhe-ai-jobs/worker_xai_grok_usage.go`（`wireXAIGrokUsageFamily`，schedule/registry 同名条目 `xai-grok-usage-refresh`）；每轮全量扫描（无游标/候选租约），单账户失败不影响其他账户且一轮内不重试；成功轮全量替换快照并清空 `last_error_message`，失败轮只推进状态列（`refresh_status/last_attempt_at/next_refresh_after/last_error_message/updated_at`）并保留最近一次成功 `snapshot_json` 与 `last_success_at`；**`snapshot_json` 列 NOT NULL，首次即失败落 `'{}'` 占位**（2026-09-27 修正：NULL 违反真实表 NOT NULL 约束）；`last_error_message` rune 截断 500；settings 不可用只降级不写套餐名，不推翻 billing 快照；两种状态均写 `next_refresh_after=now+10min`，`created_at` 不更新。

## 5. 读取投影（gateway）

`accounts/accountsbalance` 包新增 `LoadXAIGrokUsageSnapshots`（镜像 `LoadOpenAICodexUsageSnapshots`，kind 过滤换 `xai_grok`），投影出 `OAuthUsageSnapshot{Kind:"xai_grok", Windows:[{Label:"credit", UsedPercent, PeriodType, PeriodStart, PeriodEnd}], SubscriptionTier, ProductUsage}`。账户列表/详情 API 的 `oauthUsage` 字段按 provider 注入：gpt → codex 快照，xai → grok 快照。

## 6. 前端显示

- `types/domain/accounts.ts`：`oauthUsage` 为判别联合，新增 `xai_grok` 形态（usedPercent/periodType/periodStart/periodEnd/subscriptionTier/productUsage）；列表 DTO `AccountListItem` 携带网关注入的 `oauthUsage` 只读投影（gpt→openai_codex，xai→xai_grok）。
- 显示位置与 gpt 用量条一致：账户列表"用量"单元格（`AccountUsageCell.vue`，桌面表格与移动卡片共用）。**渲染形态为与 GPT 窗口条同构的进度条行**（2026-09-27 按用户反馈由文本行改为条形）：网格列依次为周期徽章（WEEKLY→周、MONTHLY→月、其他→期）、进度条（配色阈值与 GPT 一致：≥80% 警告、≥100% 危险）、百分比、重置相对时间（`periodEnd` 相对现在，如 `2d 1h`）；整行 title 提示 `套餐 · 本周已用 N% · MM-DD HH:mm 重置 · 分产品明细`（各段缺失则不渲染）。`usedPercent` 缺失时不渲染 Grok 条。
- 数据获取不新增请求：网关账户列表接口已按 provider 注入 `oauthUsage`（`hydrateOAuthUsageSnapshots`），前端跟随现有链路渲染。

## 7. 验收标准

1. 单测：billing/settings 响应解析（Mock 响应体：完整、缺 creditUsagePercent、缺 currentPeriod 回退 billingPeriodEnd、401、非 200）；快照 UPSERT 幂等；投影分 provider 注入。
2. maintenance：全新库 `--ensure-schema` 后 CHECK 含 `xai_grok`。
3. 生产验收（acc_f6a60b47564ab148）：任务族跑一轮后 `account_usage_snapshots` 出现 kind=`xai_grok` 行，`snapshot_json.grok_credit_used_percent=14`、`grok_subscription_tier='SuperGrok Heavy'`；前端账户视图显示用量与套餐。
4. 文档：本文件随实现同交付；`docs/functions/README.md` 索引更新。
