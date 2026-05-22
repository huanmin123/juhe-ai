# Mockdata 造数设计

> 面向本地演示、测试联调和页面验收。
> Mockdata 是可重复执行的离线造数能力，不进入后端运行请求链路。

## 1. 目标

- 一条命令给本地 `admin` 用户生成完整业务闭环数据。
- 覆盖系统账户、团队、授权、代理、错误策略、AI 账户、分组、API Key、公告、使用记录、审计日志、操作日志、运行日志、账号质量、用量统计、系统监控和表空间监控。
- 默认生成近 31 天数据，并重建所有用量统计窗口、排行窗口、额度窗口、授权统计、账号质量缓存、运行日志分面和系统监控窗口。
- 脚本可重复执行；每次先清理上一批 `造数-` / `mockdata_` 数据，再重新插入。

## 2. 职责边界

Mockdata 是项目里“可复用本地造数”的唯一职责入口：

- 本地演示、页面验收、联调排障、空库补数据、压测临时网关数据和后续新增的通用测试数据，都应扩展 `backend/src/scripts/maintenance/mockdata.ts` 或 `backend/src/scripts/maintenance/mockdata-fixtures.ts`。
- 不再新增独立的 `seed-*`、`demo-*`、`sample-*`、`fixture-*` 造数脚本；如果某段造数逻辑会被多个脚本、页面验收或人工联调用到，必须收口到 Mockdata。
- `seedDefaults()` 只负责系统启动所需的最小默认数据，例如默认管理员、OpenAI 供应商、默认分组和系统设置；它不是业务演示 / 测试造数入口。
- 回归脚本内部为了断言某个 bug 的最小私有 fixture 可以保留在对应脚本内，但不能被文档、人工联调或其他脚本当作通用造数方案；一旦需要复用，就移动到 Mockdata。
- Mockdata 写出的数据必须带稳定清理标识：业务名称使用 `造数-` 前缀，数据集库 ID / trace 使用 `mockdata_` / `mockdata-` 前缀，配套用户使用 `mockdata_` 用户名前缀。

当前已收口的散落入口：

- `pnpm test:perf` 的临时压测分组、OpenAI API Key 类型账户和本地网关 API Key 由 `mockdata-fixtures.ts` 生成。
- `pnpm test:smoke` 在空库且未指定真实账户时，使用 `mockdata-fixtures.ts` 生成临时 mock OpenAI 账户、分组和本地网关 Key，再接入烟测自己的本机 mock 上游。

## 3. 命令

在项目根目录执行：

```powershell
pnpm mockdata
```

可调整时间跨度和每天使用记录数量：

```powershell
pnpm mockdata -- --days 31 --daily-requests 80
```

参数边界：

| 参数 | 默认值 | 范围 | 说明 |
| --- | --- | --- | --- |
| `--days` | `31` | `1` 到 `90` | 生成最近多少天的明细和监控样本 |
| `--daily-requests` | `80` | `1` 到 `500` | 每天生成多少条使用记录 |

脚本会在业务库所在目录写入 `mockdata-summary.json`，记录本次生成的资源 ID、本地网关 API Key 明文和统计数量。默认本地路径是 `backend/data/mockdata-summary.json`。

## 4. 数据边界

- admin 拥有核心业务资源：AI 账户、分组、代理、错误策略、团队、授权、公告和主要 API Key。
- 脚本会创建若干 `mockdata_*` 普通用户，用于团队成员、授权调用方、公告已读和操作日志可见性；这些用户是配套数据。
- 本地网关 Key、上游 API Key、OAuth Token 和代理密码均为模拟值，不会真实请求 OpenAI。
- 统计数据来自脚本写入的 `usage_records`，再通过现有聚合器重建预聚合表；页面读取路径仍然和真实数据一致。

## 5. 清理策略

默认只清理以下数据：

- 名称以 `造数-` 开头的业务数据。
- ID 或 trace 前缀为 `mockdata_` / `mockdata-` 的数据集库记录。
- 用户名以 `mockdata_` 开头的配套系统用户。

清理后会重建全量用量统计缓存。系统监控小时缓存会从现有 `system_metrics_samples` 和本次 Mockdata 样本重新聚合，避免重复执行导致小时指标累加。

## 6. 验证点

执行完成后建议检查：

- 管理后台首页、账号、分组、API Key、授权、团队、公告均有 `造数-` 数据。
- 使用记录、审计日志、操作日志、运行日志均可按 `mockdata` 或 `造数` 检索。
- 用量统计、AI 性能监控、授权用量、API Key 额度窗口、系统指标趋势和表空间监控均有近 31 天数据。
- `backend/data/mockdata-summary.json` 中的 active API Key 可用于本地网关请求验证。
