# 发布包快速运行说明

> 这是发布包根目录内置的快速说明。构建发布包见 `docs/deploy/构建指南.md`。完整部署先按场景选择：服务器看 `docs/deploy/scenarios/服务器部署方案.md`，家庭宽带反代看 `docs/deploy/scenarios/家庭宽带反向代理方案.md`；通用启动、HTTPS 证书、状态检测、常驻运行、反向代理、备份迁移和排障再看 `docs/deploy/部署指南.md`。

## 启动脚本

| 目标平台 | 启动命令 |
| --- | --- |
| Windows | `pwsh ./start.ps1` |
| macOS | `bash ./start.sh` |
| Linux | `bash ./start.sh` |

### 部署模式（go-only 终态）

发布包是 go-only 形态，只有一条启动路径：`juhe-ai-go-gateway` 以 `JUHE_AI_GATEWAY_SYSTEM_API_ENABLED=true` 绑定 `JUHE_AI_HOST:JUHE_AI_PORT` 成为主入口，`juhe-ai-go-jobs` 承载 F1/F2。Node Web/API 已归档，不再提供 `hybrid` / `node` 部署模式；`JUHE_AI_DEPLOY_MODE` 出现历史值时启动脚本会拒绝启动，唯一合法值是 `go`（缺省即 go）。

可选预检：`JUHE_AI_GO_MAINTENANCE_BOOTSTRAP=true` 启动前执行幂等的 `backend-go/juhe-ai-maintenance --ensure-schema`（SQLite 按 `backend/.env` 六库路径或 PostgreSQL `--dsn`），`JUHE_AI_GO_MAINTENANCE_SEED=true` 追加 `--seed`。`JUHE_AI_OWNER_LOCK_ENABLED=true` 会被拒绝（owner lock 尚无 Go server 包装）。

可选运行变量（均有缺省值，常规单机部署无需设置）：

- `JUHE_AI_JOBS_INTERNAL_URL`：gateway 进程回调 jobs internal-api 派发面的 loopback origin，默认 `http://127.0.0.1:3305`（与 jobs 健康监听缺省端口一致）。手动账户测试派发（`POST /__aiinternal__/v1/account-test/dispatch`）、请求失败链账户健康检查派发（`POST /__aiinternal__/v1/account-health-check/dispatch`）与账户余额健康裁决都经过它；jobs 健康监听端口变更时必须同步修改。
- `JUHE_AI_BLUE_GREEN_OWNER_MODE`：Go 进程蓝绿 owner 模式，合法值 `active` / `standby` / `drain`（大小写不敏感），缺省 `active`。gateway 与 jobs 启动时各自校验，非法值启动失败；仅 `active` 持有 owner 工作，`standby` / `drain` 供蓝绿切换窗口把候补/下线槽排除出 owner 判定（含账户余额健康对对端 ownerMode 的裁决）。

详见 `docs/migration/部署go-only双轨开关.md`（源码仓库内）。


发布包可以来自 Windows、macOS 或 Linux 任一打包平台。发布包包含两个可独立部署的常驻 Go 二进制：`backend-go/juhe-ai-jobs`（Windows 为 `.exe`）承载 F1 运行日志索引与 F2 表监控，`backend-go/juhe-ai-gateway` 承载主入口、F3 审计与 F4 操作日志；`backend-go/juhe-ai-maintenance` 只用于一次性维护命令。日志搜索（Go 原生 grep）要求目标机器提供系统 `rg`，或配置 `JUHE_AI_RG_PATH`。

## 部署前检查

- Node.js LTS（22.x LTS >=22.13.0 或 24.x LTS >=24.11.0）仍需安装：启动脚本用 `node` 运行 `scripts/start-go-project.mjs` 启动器与健康探测；不再需要 pnpm、`node_modules` 或 `backend/dist`。
- 默认端口 `3000` 没有冲突；冲突时修改 `backend/.env` 的 `JUHE_AI_PORT`。
- 当前目录有写入权限和足够磁盘空间。

## 配置

go-only 发布包不再携带 `backend/.env.example`（X02 裁剪）。可以直接手工创建并编辑 `backend/.env`（启动脚本也会在缺失时创建空文件）：

```powershell
notepad .\backend\.env
```

如果没有手动创建，`start.ps1` / `start.sh` 首次启动会自动从 example 创建 `backend/.env`，生成稳定随机 `JUHE_AI_SECRET` 写回文件，并填入本机默认 `JUHE_AI_ALLOWED_ORIGINS`。这不会生成 F2/F3/F4 owner ID 或 F3/F4 input secret；任一组件必填项缺失时启动会明确失败。因此首次启动前仍应手工编辑 `backend/.env`。公网 IP、域名或反向代理部署后，仍要把 `JUHE_AI_ALLOWED_ORIGINS` 改成实际后台访问 Origin，并备份 `backend/.env`。公网 HTTPS 默认优先使用 Caddy 自动申请和续期免费证书，详见 `docs/deploy/https/Caddy自动HTTPS部署指南.md`。

最低配置：

```env
JUHE_AI_HOST=127.0.0.1
JUHE_AI_PORT=3000
JUHE_AI_DATABASE_PATH=./data/juhe-ai.sqlite3
JUHE_AI_CHAT_DATABASE_PATH=./data/juhe-ai-chat.sqlite3
JUHE_AI_DATASET_DATABASE_PATH=./data/juhe-ai-dataset.sqlite3
JUHE_AI_RUNTIME_LOG_DATABASE_PATH=./data/juhe-ai-runtime-log.sqlite3
JUHE_AI_USAGE_CATALOG_DATABASE_PATH=./data/juhe-ai-usage-catalog.sqlite3
JUHE_AI_STATS_DATABASE_PATH=./data/juhe-ai-stats.sqlite3
JUHE_AI_TABLE_MONITOR_DATABASE_PATH=./data/juhe-ai-table-monitor.sqlite3
JUHE_AI_RUNTIME_LOG_INSTANCE_ID=juhe-ai-go-jobs-runtime-log
JUHE_AI_RUNTIME_LOG_POSTGRES_URL=
JUHE_AI_TABLE_MONITOR_INSTANCE_ID=juhe-ai-go-jobs-table-monitor
JUHE_AI_TABLE_MONITOR_POSTGRES_URL=
JUHE_AI_AUDIT_LOG_INSTANCE_ID=juhe-ai-go-gateway-audit-log
JUHE_AI_AUDIT_LOG_STORE=sqlite
JUHE_AI_AUDIT_LOG_DATABASE_PATH=./data/juhe-ai-audit-log.sqlite3
JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY=./data/audit-payload-blobs
JUHE_AI_AUDIT_LOG_HOT_SEARCH_DIRECTORY=./data/audit-hot-search
JUHE_AI_AUDIT_LOG_POSTGRES_URL=
JUHE_AI_AUDIT_LOG_INPUT_LISTEN_ADDRESS=127.0.0.1:3303
JUHE_AI_AUDIT_LOG_INPUT_URL=http://127.0.0.1:3303
JUHE_AI_AUDIT_LOG_INPUT_SECRET=替换为稳定的高熵密钥，production 至少 32 位
JUHE_AI_AUDIT_LOG_INPUT_TIMEOUT_MS=7000
JUHE_AI_OPERATION_LOG_INSTANCE_ID=juhe-ai-go-gateway-operation-log
JUHE_AI_OPERATION_LOG_STORE=sqlite
JUHE_AI_OPERATION_LOG_DATABASE_PATH=./data/juhe-ai-operation-log.sqlite3
JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH=./data/juhe-ai.sqlite3
JUHE_AI_OPERATION_LOG_POSTGRES_URL=
JUHE_AI_OPERATION_LOG_INPUT_LISTEN_ADDRESS=127.0.0.1:3304
JUHE_AI_OPERATION_LOG_INPUT_URL=http://127.0.0.1:3304
JUHE_AI_OPERATION_LOG_INPUT_SECRET=替换为另一把稳定的高熵密钥，production 至少 32 位
JUHE_AI_OPERATION_LOG_INPUT_TIMEOUT_MS=7000
JUHE_AI_USAGE_SHARD_ROOT=./data/usage-shards
JUHE_AI_USAGE_SHARD_COUNT=16
JUHE_AI_SECRET=可留空由启动脚本首次生成，或换成自己保存的强随机密钥
JUHE_AI_ALLOWED_ORIGINS=http://localhost:3000,http://127.0.0.1:3000
JUHE_AI_OAUTH_PROXY_URL=
```

新部署可以使用启动脚本生成的 `JUHE_AI_SECRET`，也可以改成自己保存的强随机值；如上线窗口已离线处理并保留当前 schema 数据，必须沿用原 `JUHE_AI_SECRET` 解密敏感字段。`JUHE_AI_AUDIT_LOG_INPUT_SECRET` 与 `JUHE_AI_OPERATION_LOG_INPUT_SECRET` 是两把不同密钥，不能留示例文字、不能相互复用或回退到 `JUHE_AI_SECRET`，并须在安全的密码管理或受限 env 中保存。项目运行时不承担旧数据迁移或旧结构兼容。`JUHE_AI_DATABASE_PATH` 保存业务配置和资源关系；公开接口日志、模型检测和清理目标在数据集目录库；Go F1 运行日志索引在 `JUHE_AI_RUNTIME_LOG_DATABASE_PATH`；usage shard 注册表、列表筛选目录和账号 / API Key scope catalog 在使用记录目录库；新写入的使用记录保存在 usage shard 目录；统计缓存和窗口表保存在统计结果库；Go F2 表监控快照在 `JUHE_AI_TABLE_MONITOR_DATABASE_PATH`；Go F3 原始审计事实、payload/blob 与 hot-search 分别使用上述 F3 专用路径；Go F4 操作日志事实库在 `JUHE_AI_OPERATION_LOG_DATABASE_PATH`，只读业务设置来自 `JUHE_AI_OPERATION_LOG_BUSINESS_SETTINGS_PATH`。八个 SQLite 文件路径必须互不相同，usage shard 根目录也要与这些文件区分。原始审计正文捕获固定开启，不再通过环境变量关闭。

发布包启动器会为 Go 子进程设置 `TZ=UTC`。所有 API、异步任务、日志事件和回执中的绝对时间只接受或输出 RFC3339 的 `Z` 或明确数字 offset，绝不以本机时区或上海裸时间表达。管理页面按浏览器本地时区展示；排班、可用时段和统计日界线等业务日历只使用显式 IANA timezone（在管理后台「系统设置」中配置，持久化于 `system_settings`）。

启动脚本先启动 `juhe-ai-go-gateway` 并等待 owner health `3306 /health` 返回 `200`，再确认 F3 `3303 /__aiinternal__/health` 与 F4 `3304 /__aiinternal__/v1/operation-logs/health` 返回 `204`，然后确认业务端口 `/__aisys__/api/health` 返回 `200`；随后启动 `juhe-ai-go-jobs` 并确认 `3305 /health` 返回 `200`。F1、F2、F3、F4 各自拥有 Store、schema 和 owner lease，且 owner ID 必须在 `backend/.env` 或更高优先级环境中显式配置且稳定；启动脚本绝不生成或改写这些标识。普通单条/单轮错误只记录并交给下一轮处理；租约丢失、启动预检失败、外部停止或 OOM/runtime fatal 等不可恢复故障只结束所属 Go 项目并交由服务管理器恢复。PID 与日志分别位于 `backend/runtime/juhe-ai-{gateway,jobs}.pid`（Windows 为 `juhe-ai-go-{gateway,jobs}.pid`）和 `backend/logs/juhe-ai-{gateway,jobs}.log`。

F1 是运行日志索引、cursor、facet 与保留清理的唯一 writer；F2 是表监控采样、快照写入和保留清理的唯一 owner；F3 是原始审计持久化、payload/blob、hot-search 与保留的唯一 writer；F4 是操作日志写入、读取、摘要索引与保留的唯一 owner。gateway 的 system API 组合根直接读写业务库，是业务库唯一 writer。SQLite 的 F1/F2/F3/F4 路径必须物理隔离；PostgreSQL 下各功能优先使用各自 `JUHE_AI_*_POSTGRES_URL`，留空时才回退 `JUHE_AI_POSTGRES_URL`。

`JUHE_AI_AUDIT_LOG_INPUT_SECRET` 与 `JUHE_AI_OPERATION_LOG_INPUT_SECRET` 分别是 gateway 进程内 F3、F4 loopback HMAC 输入端点的显式密钥，不能省略、互用或回退到 `JUHE_AI_SECRET`，且在 production 至少 32 位。两组 `*_INPUT_LISTEN_ADDRESS` 与 `*_INPUT_URL` 必须各自指向同一 loopback 端口。F3/F4 输入 RPC 的默认超时均为 `7000ms`，但业务提交后的 producer 采用 fire-and-forget，不等待 `204`；可分别用对应 `*_INPUT_TIMEOUT_MS` 在 `1000..60000` 毫秒内调整。

### 空 PostgreSQL 库的首次初始化

`start.sh` / `start.ps1` 默认**不**初始化 PostgreSQL schema。对新建、可销毁且确认为空的 PostgreSQL 库，设置 `JUHE_AI_POSTGRES_URL` 后使用 maintenance 预检完成 ensure-schema 与种子，再启动 release：

```bash
JUHE_AI_GO_MAINTENANCE_BOOTSTRAP=true JUHE_AI_GO_MAINTENANCE_SEED=true bash ./start.sh
```

它写入当前 schema 与默认种子数据，F1/F2/F3/F4 依赖的 `juhe_business.system_settings` 也在其中；命令幂等，可重复执行。只适用于空库或明确可重建的测试库。已有业务库先完成项目/业务备份和临时预演，只按 [高性能模式部署指南](../docs/deploy/高性能模式部署指南.md) 的既有库流程做 `schema-only` 校验或受控迁移，禁止把完整初始化当作普通升级命令。

## 启动与验证

Windows：

```powershell
pwsh .\start.ps1
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/health
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/api/health
Invoke-WebRequest http://127.0.0.1:3000/__aisys__/
Invoke-WebRequest http://127.0.0.1:3303/__aiinternal__/health
Invoke-WebRequest http://127.0.0.1:3304/__aiinternal__/v1/operation-logs/health
Get-Content .\backend\logs\juhe-ai-go-gateway.log -Tail 100
Get-Content .\backend\logs\juhe-ai-go-jobs.log -Tail 100
```

macOS/Linux：

```bash
bash ./start.sh
curl -i http://127.0.0.1:3000/__aisys__/health
curl -i http://127.0.0.1:3000/__aisys__/api/health
curl -I http://127.0.0.1:3000/__aisys__/
curl -i http://127.0.0.1:3303/__aiinternal__/health
curl -i http://127.0.0.1:3304/__aiinternal__/v1/operation-logs/health
tail -n 100 ./backend/logs/juhe-ai-go-gateway.log
tail -n 100 ./backend/logs/juhe-ai-go-jobs.log
```

上例使用默认 F3/F4 端口；如变更对应 `*_INPUT_LISTEN_ADDRESS`，health URL 也必须相应变更。上述 HTTP health 只证明 listener 可用，发布验收仍须确认 F1/F2 数据新鲜，并发起一次可审计业务请求后在管理员审计与操作日志详情中读回同一记录，证明业务提交 -> F3/F4 -> 管理读面的完整链路。

## 备份

备份固定分为两类，并分别只保留最近 3 次：

- `project-*`：升级前当前运行 release 的干净可部署压缩包、清单和校验值；目标迁移脚本只能作为附加证据。它必须与同一时间戳的业务备份组成恢复点，不得用待上线新包替代当前代码快照；不含真实 env、`data/`、日志、数据库、`node_modules` 或链接。源码历史由 Git 保存，项目备份保存的是可部署产物。
- `business-*`：核心业务库、恢复加密凭据所需的有效 `JUHE_AI_SECRET` / `backend.env`、schema / 清单和校验值。

不得备份数据集目录库、使用记录目录库、usage shard、统计结果库、Codex context、审计 payload、日志或 Redis。同一时间戳的项目备份与业务备份都完成完整性校验并原子发布后，立即分别删除同类第 4 份及更早目录。完整规则见 `docs/deploy/部署指南.md`。

部署方式先看 `docs/deploy/scenarios/`。状态检测和自动恢复看 `docs/deploy/watchdog/README.md`。HTTPS 证书、常驻运行、反向代理、端口开放、数据迁移和常见排障请继续查看 `docs/deploy/部署指南.md`。

## 统计重建

统计重建不是每次上线动作。只有统计表、统计口径、授权消耗规则或额度窗口规则变化时才执行。go-only 发布包不再携带历史 Node 重建脚本（`backend/dist/scripts/maintenance/rebuild-usage-stats.js` 已随 Node backend 于 X02 归档）；Go 侧等价重建工具落地前，统计重建属未收口能力，执行前先以 `backend-go/juhe-ai-maintenance --help` 核对当前可用维护命令，不要复用归档脚本处理 go-only 的统计库。
