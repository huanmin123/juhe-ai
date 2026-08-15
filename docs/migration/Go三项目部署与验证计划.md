# Go 三项目部署与验证计划

> 状态：2026-08-15 的部署基线与验收计划。未完成 candidate handover 前，本文件不授权替换现网 Node 网关或停止现有 owner。

## 1. 当前结论

当前源码已经拆为 `jobs`（F1/F2）、`gateway`（F3/F4）和 `maintenance`（一次性命令），但代码拓扑不等于任一环境已经切换。现网若仍运行历史 unified sidecar，不能直接执行当前 split `start.sh`、`start.ps1` 或 Compose 来“升级”；那会同时引入新的 `jobs`、`gateway` owner 与端口，需要独立 candidate、drain 和回滚证据。

本次开发基线已验证：

- `jobs` 与 `gateway` 各自 `go test ./...` 通过。
- F1、F2、F3、F4 都在可销毁 PostgreSQL/PgBouncer 子库通过 adapter/lease/schema/retention smoke；F4 performance producer 在独立 PostgreSQL/Redis namespace 通过。
- F3 SQLite 的真实 Node 输入 → Go 持久化 → Node 只读查询 → 重启回归通过；F4 的 Node→Go producer（普通、System API、OAuth、worker）smoke 通过。

这些证据只覆盖开发资源和临时文件；不代表生产服务、历史数据迁移、owner switch、回滚或网关业务已验收。

## 2. 项目与启动边界

| 项目 | 常驻职责 | 健康证据 | 当前限制 |
| --- | --- | --- | --- |
| Node | 对外网关、管理 API、主业务 | Node API health 与业务读回 | 仍是业务/网关 owner，不能被 Go jobs 依赖 |
| `juhe-ai-jobs` | F1/F2 与后续完整后台任务 | `/health` 同时反映 F1/F2 owner/readiness | 未完成 handover 前不在生产启动新的 F1/F2 owner |
| `juhe-ai-gateway` | F3/F4 数据 listener/owner；未来才是 Go gateway | `/health`、F3/F4 loopback input health | 当前不接管公开 AI 网关或管理 API |
| `juhe-ai-maintenance` | 一次性 schema/导入/回填/诊断 | 退出码与报告 | 不作为常驻 worker 或 scheduler |

每个项目必须独立二进制、环境变量、PID/日志目录、健康端点、重启策略和停止顺序。项目之间没有 import、进程看护或运行时调用依赖；特别是 `jobs` 不调用 Node 或 `gateway`。

## 3. 发布分层

### A. 现网兼容层（当前可执行）

1. 保持现有 Node 与其当前唯一 owner 拓扑，不启动 split `jobs`/`gateway`。
2. 只核对现有 Node health/API、现有 sidecar 的实际进程与输入健康、F3 读回和日志；不把 `3305/3306`、F4 input 或新二进制健康当作旧拓扑必须存在的证据。
3. F4 仍按其切换前状态处理；没有历史迁移、candidate、rollback commit 与生产 readback 时不得启用 F4 新 owner。

### B. 隔离开发层（已可继续扩展）

1. 使用独立 SQLite 文件或严格命名的可销毁 PostgreSQL 子库及独立 Redis namespace；不得写主开发库、主 namespace 或私有生产资料。
2. 为 `jobs` 配置 F1/F2 的独立 owner ID、Store、健康端口；为 `gateway` 配置 F3/F4 的独立 owner ID、input secret、Store、健康端口。缺配置必须 fail-fast，不能自动生成或 fallback。
3. 验证 Node health、`jobs`/`gateway` project health、F3/F4 204、F1/F2 freshness 与 Node→Go→Node readback。单条失败不得使无关项目退出。

### C. Docker 层（发布前门）

1. 先执行 Compose config、release launcher、project boundary、Docker isolation 和 release package 回归。
2. 在专门的隔离槽执行 `docker compose ... up -d --build --wait`；不复用用户已运行的容器、端口、volume 或数据库。
3. standalone 验证 SQLite 单 writer/物理隔离；performance 验证 PgBouncer、cache/state/queue Redis、F1–F4 health 与读回。`JUHE_AI_POSTGRES_JIT_ENABLED=false` 必须通过事务内 `SET LOCAL jit = off` 生效，URL 不得携带 PgBouncer 会拒绝的 startup `options` 参数。
4. Docker 健康成功只是 candidate 前提，不是 owner handover。

### D. 后续 jobs-only candidate（后续任务，不在本轮执行）

1. 只选择一个完成 L1/L2 的后台功能，例如 F1/F2 或将来的 J1；不启动 Go public gateway。
2. 记录旧 Node/unified owner 的进程、配置、数据、回滚包和停止步骤；candidate 证明 lease、freshness、结果 readback、日志、metrics、drain 和 restart。
3. 通过受控 handover 后停止该功能的旧 Node owner；F3/F4 与 Node 网关保持原 owner。失败时按已验证的回滚路径恢复旧 owner，不做双写。

## 4. 开发 PostgreSQL/PgBouncer 与 Redis 规则

- 真实 smoke 只能使用 `juhe_ai_sub2api_dev_<name>` 格式的临时数据库、`juhe-ai:dev:<name>` namespace 与 `.local/project-resources/dev/env/scratch/` 下的短生命周期 profile。
- F3 destructive smoke 还要求专用 `juhe_ai_sub2api_dev_f3_smoke_<name>` 数据库和明确 destructive token；测试结束后验证表清空、关闭会话、删除子库、SCAN/UNLINK 三套 Redis DB 9 的本次 namespace，并删除 scratch profile/runtime。
- PostgreSQL statement/lock/idle transaction timeout 必须由连接/事务配置验证；JIT 关闭只能使用事务内 `SET LOCAL jit = off`，不得将 `options=-c jit=off` 放入 PgBouncer startup URL。
- 任何环境变量、URL、密码、token、数据库内容或生产资产不得写入 Git、公开文档、测试输出或报告。

## 5. 发布前检查清单

1. 三个项目分别构建、`go test ./...`、版本/boundary 检查通过。
2. F1/F2/F3/F4 的紧贴模块测试、SQLite smoke、PostgreSQL/PgBouncer smoke、Node→Go RPC 回归通过；未配置或未运行项明确列为未验证。
3. `test:dev-go-project-env`、`test:release-go-project-launchers`、`test:docker-go-project-isolation`、`test:macos-operations`、`test:release-package`、owner manifest 回归通过。
4. release package 包含三个常规二进制、启动脚本、Node dist 和前端产物；不含 `.env`、数据库、日志、私有资料或符号链接。
5. candidate 环境的健康、F3/F4 input、F1/F2 freshness、真实读回、日志与 rollback 演练通过；若任一项缺失，不进入 production handover。
