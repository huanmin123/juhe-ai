# W6 管理端表监控只读 Schema 共存记录

## 范围

- Go 只读取 `GET /__aisys__/api/table-monitor/overview`、`GET /__aisys__/api/table-monitor/history` 和 `GET /__aisys__/api/table-monitor/database-history` 所需的 PostgreSQL 快照。
- 共存期 `juhe_stats.database_storage_snapshots` 和 `juhe_stats.table_storage_snapshots` 的采样、保留清理和数据质量仍由 Node 单一 writer 负责。
- 本切片不迁移 `POST /__aisys__/api/table-monitor/non-business-data/cleanup`，不启动 Go sampler，不补默认行，也不把缺失快照伪装成空结果。

## Schema 决策

`000070_w2_gateway_model_catalog_chat_snapshot_variants.sql`、`000071_w2_gpt_codex_auto_review_default.sql` 和 `000072_w1_account_circuit_control_plane.sql` 已正式发布；page-data 退场随后新增 `000073_w7_drop_page_data_dirty_domains.sql`，权威 Goose catalog 当前连续到 `000073`。本表监控切片没有新增 migration；后续工作必须从真实 catalog 派生下一连续版本，不能复用或改写已发布版本。

因此本轮选择运行时只读能力门禁，而不是抢占 migration 版本：

1. 每次表监控 PostgreSQL 读取前，通过 `information_schema.tables` / `columns` 检查两张 Node-owned 对象确为 base table、当前 Go reader 使用的全部列及 Node PostgreSQL `text` / `integer` / `bigint` 类型，并确认运行账号具备 `SELECT` 权限。
2. 门禁不执行 DDL/DML，不创建表，不修补列，不写快照。
3. 表或列缺失时返回 typed unavailable 错误，并在任何快照查询前停止；调用链不能返回伪造的空数组。
4. catalog 查询失败保持原始 infrastructure error 链，不能误报成 schema 缺失。
5. capability check 与实际查询之间若并发发生 drop / alter / revoke，reader 把 PostgreSQL `42P01`、`42703`、`42501` 统一映射回 typed unavailable；不为低频管理读开启跨多条查询的长事务。

这允许 fresh Go schema 73 正常启动并与 Node 共存。部署层只有在 Node 初始化过上述 PostgreSQL schema 且门禁通过后，才能把三条精确 GET 路径 opt-in 到 Go；否则这些路径继续由 Node 持有。

## 后续 Schema Owner 门禁

- 如减法阶段决定由 Go 接管表监控 schema，先从届时权威 catalog 派生下一连续版本，再新增两张快照表及索引；不能回填或改写已发布 migration。
- 新 migration 只能建立兼容 Node writer 的结构，不能顺带启动 Go writer；writer owner 切换必须另行设计、切流和验证。
- 生产切流前仍需真实 PostgreSQL 执行部署前 schema contract preflight、Node writer -> Go reader smoke 和目标索引 `EXPLAIN`。静态门禁通过不等于这些证据已经完成。

## 本轮验证

```powershell
Set-Location backend-go
go test ./internal/store/postgres -run 'TestManagementTableMonitor' -count=1
go vet ./internal/store/postgres
```

覆盖：只读 capability SQL、base table / SELECT / 列类型契约、缺表前置拒绝、无数据查询、schema race SQLSTATE 映射、infrastructure error 保真，以及既有 overview/history 映射与有界查询契约。
