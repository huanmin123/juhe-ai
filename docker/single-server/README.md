# 单机 Docker 部署（国内服务器，go-only）

国内单台服务器（Debian 13）上以 Docker Compose 运行全套 juhe-ai：PostgreSQL + Redis + gateway/jobs/maintenance（本地交叉编译的二进制）+ Caddy 入口。这是替代 K3s 混合形态的 go-only 目标部署形态；服务器地址、资产、密码与运行事实记录在 `.local/project-resources/prod/`（私有，不入库——BUG-0225）。

## 拓扑

```
:80 / :443 Caddy ──> gateway:3000（管理 SPA /__aisys__/ + OpenAI 兼容 /v1）
gateway ─┬─ postgres:5432（单库 juhe_ai，8 schema，见下）
         └─ redis:6379（DB0=cache，DB1=state，DB2=J3b circuit，namespace=prod）
jobs ─────┘（与 gateway 共用同一 PG/Redis/namespace）
maintenance：compose --profile tool 一次性容器（幂等 CLI）
Prometheus（infra 机，经既有 SSH 隧道）──> 生产机宿主回环 127.0.0.1:3306（gateway GET /__aisys__/metrics）
                                     └──> 生产机宿主回环 127.0.0.1:3305（jobs 健康监听指标）

一次性迁移命令（2026-09-28 AI 对话账户唯一绑定）：`juhe-ai-maintenance -migrate-chat-account-only-binding -driver postgres -dsn "$JUHE_AI_POSTGRES_URL"` —— 在 `--ensure-schema` 之后执行；幂等可重入（重复运行报 `alreadyMigrated: true`）；归档存量旧模式会话、备份并删除 `juhe_chat.chat_conversations` 的 `bind_mode`/`bind_group_id`/`bind_group_name_snapshot` 三列（PG 用 `DROP COLUMN IF EXISTS`）；stdout JSON 报告含 `rollbackSql`（回滚 = 先恢复旧版二进制，再执行报告中的恢复 SQL）。

一次性回填命令（2026-10-12 成功成本历史回填）：`juhe-ai-maintenance --apply-success-cost-backfill [--dry-run] --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"`（SQLite 形态 `--driver sqlite --paths "stats=<stats 库文件>,..."`）—— 六表 success_cost_usd 历史回填的唯一执行者，先 `--ensure-schema` 建好 `juhe_stats.backfill_progress` 标记表再执行（表缺失时显式报错不建表；ensure 后目标 marker 缺失或非 done 时在结果摘要提示待回填表，不自动迁移）；幂等可重入（done 表零触碰、重复执行零批秒完），中断从持久化主键游标续跑；stdout 单行 JSON 报告六表批数/命中行数。仅限维护窗口（`docker compose stop jobs` 后）执行，完整窗口序列见下"更新发布"节。

一次性迁移命令（AI 账户能力域拆分 M1，存量库赋域 + 声明裁剪）：`juhe-ai-maintenance --migrate-account-capability-domain --account-capability-domain-stage=m1 --node-stopped --go-stopped --backup-confirmed --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"` —— `accounts.capability_domain` 赋域的唯一执行者；赋域同时把每行声明（`credentials_encrypted` 内 `supported_endpoint_modes`）裁剪至落域 lane 集（`declared ∩ 落域 lane 集`，仅混合声明行回写凭据密文，同批次事务执行），stdout 报告逐行列明被裁值及其建议归属域，操作者据报告为被裁能力同凭据另建归属域账户承接（未承接前该能力无候选账户）；加列本身随例行 `--ensure-schema` 幂等生效（只加列不赋域），赋域必须在停写窗口（`docker compose stop gateway jobs` + 已验证可恢复备份）执行，三确认门禁缺一拒绝；失败清单非空零写入 exit 3，不得上线。只读核验 `juhe-ai-maintenance --verify-account-capability-domain --account-capability-domain-stage=m1 --account-capability-domain-check=preflight|postflight ...`。顺序契约：先迁移并核验通过，后启动依赖该列的新版 gateway/jobs。完整窗口序列见下"更新发布"节。

一次性迁移命令（2026-10-20 超级分组 M1，`merge` 策略→超级分组）：`juhe-ai-maintenance --migrate-merge-super-groups --migration-batch-id <id> --dry-run --manifest-out <path> --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"`（SQLite 形态 `--driver sqlite --paths "business=<业务库文件>"`，只强制 `business=` 键）—— 存量 `route_strategies.mode='merge'` 的唯一迁移执行者；dry-run 只扫描并写出可复核 manifest（允许 blocked 非零，exit 0）；正式执行去掉 `--dry-run` 并带 `--node-stopped --go-stopped --backup-confirmed`（三确认缺一 exit 2），在单个业务库事务内把每条策略改写为绑定新生成超级分组的 `normal` 策略并替换绑定，任一校验失败整笔回滚；apply 后 `blocked>0` exit 1，不得带 blocked 上线。PG 与 SQLite 共用同一迁移器与事务语义。manifest 记录源策略/绑定、目标超级分组与成员、`target_route_mode=normal` 和阻断原因。旧 `merge` 运行链已于 2026-10-21 删除，运行时只识别四种路由模式；未迁移的存量 `merge` 策略在新版 gateway 加载时按「模式不受支持，请先执行迁移」fail-fast，因此迁移必须先于新版进程启动。

一次性迁移命令（2026-10-12 调度偏好独立实体 P2，存量库绑定回填）：`juhe-ai-maintenance --migrate-scheduling-preferences --node-stopped --go-stopped --backup-confirmed --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"`（SQLite 存储形态 `--driver sqlite --paths "business=<业务库文件>"`，可复用六库完整 `--paths`，只强制 `business=` 键）—— 普通键调度绑定回填与等值偏好生成的唯一执行者，须在停写窗口（`docker compose stop gateway jobs` + 已验证可恢复备份）执行，三确认门禁缺一拒绝（exit 2）；行为：`speed_first` 且带有效配置的策略生成等值偏好（name=`{策略名}·速度优先`，owner 内重名自动加序号 `-2/-3`），`cost_first`/缺省策略不建偏好（NULL 普通 Key 回填内置 ID `schpref-builtin-chat-cost-first`）；**仅更新 `scheduling_preference_id IS NULL AND purpose='general'` 的 Key**；显式绑定 Key 一律不动，chat Key 不回填（绑定列恒 NULL）；存量 studio Key 在退场清理命令（`--apply-studio-retirement`）执行前保留——迁移排除、绑定列恒 NULL，预检/执行报告单独计数 `apiKeysStudioExcluded`，清理命令执行后随退场删除；只读 `route_strategies.config_json`（不回写，作回滚 oracle）；速度运行态不迁移（P2 设计门裁决：瞬态自愈）。断点恢复 = 同命令重跑（NULL 判定天然幂等，确定性偏好 ID 复用）；完成记录写 `global_settings` key `scheduling_preferences_migration`（`{"stage":"completed","ruleVersion":1,...}`），后检有残留时拒绝写记录 exit 3。只读核验 `juhe-ai-maintenance --verify-scheduling-preferences --scheduling-preferences-check=preflight|postflight ...`（preflight=迁移前报告，退出码恒 0；postflight=完成态核验，未就绪 exit 3）。顺序契约：先迁移并核验通过，后启动 P2 版 gateway/jobs。完整窗口序列见下"更新发布"节。
```

- Redis `queue` 是 Node 时代残留概念，Go 代码不读 `JUHE_AI_REDIS_QUEUE_URL`，无需第三个实例。
- 8 个 PG schema：6 个由 `maintenance --ensure-schema` 建（business/usage/stats/chat/dataset/codex_context）；`juhe_jobs`、`juhe_j3b` 需手工 `CREATE SCHEMA AUTHORIZATION juhe_ai` 后由 maintenance `--apply-j3a-proxy-latency-postgres` / `--apply-j3b-model-check-postgres` / `--apply-account-balance-postgres`（J2 account_balance 四表，2026-10-02 起取代手工 SQL）建表（完整序列见 `.local/project-resources/prod/runbooks/国内单机Docker部署与运维.md`）。
- `--ensure-schema` 幂等契约（2026-10-14 起）：普通 ensure **恢复幂等加法**——schema 收敛只剩建表/补列/建索引/内置行补种等加法动作，**studio 退场清理已移出普通 ensure**（studio 行/列/表改由一次性显式命令 `--apply-studio-retirement` 清理，见下「更新发布」节）；仍随 ensure 执行的破坏性收敛以「更新发布」节维护窗口为准（如调度偏好 `DROP COLUMN enabled`），**存量库上跑 ensure 前必须先停写并完成可恢复备份**——写者运行时跑 ensure 会打断仍在运行的旧版进程，ensure 之后才备份会让备份失去回滚价值。
- chat 库加表随例行 `--ensure-schema` 幂等生效，无一次性迁移：如 2026-10-02 的 `juhe_chat.chat_user_tool_preferences`（AI 对话工具默认绑定，见 docs/functions/AI问答工具体系与主子模型设计.md §7）。
- 模型检测题库内置题（2026-10-09 起）：`model_check_question_bank` 新增 `is_builtin` 列（`--ensure-schema` 幂等补列，SQLite PRAGMA 守卫 / PG `ADD COLUMN IF NOT EXISTS`），8 道系统内置公开题（草莓字母计数、9.11 vs 9.9、糖果多步算术、牛奶稀释等）由 seed 幂等写入（固定 id 条件 upsert：新行插入、存量内置行题面随 seed 版本恢复权威值）。生效方式按存储模式区分：**SQLite（开发/单机直跑）gateway 每次启动的 storage preflight 自动 ensure+seed，内置题自动出现**；**PG（生产）需在发版后跑 `maintenance --ensure-schema --seed`**——只跑 ensure-schema 会补列但不写内置题数据；seed 幂等可重入，对用户自拟题无副作用。
- 账户"特供"恢复标记（2026-10-10 起）：业务库加法式两列——`accounts.expedited_recovery_enabled`（integer 0/1，DEFAULT 0）与 `system_accounts.expedited_account_limit`（可空 integer 0–100，NULL 读侧默认 3），随发版执行既有 `--ensure-schema` 幂等补列生效（SQLite 两个 ensure 守卫 / PG 两侧均 `ADD COLUMN IF NOT EXISTS`——`accounts` 侧守卫为 BUG-0305 发布事故后补齐，2026-10-10 发布窗口曾因缺它崩溃循环约 11 分钟，生产库已手工 ALTER 恢复），无额外命令、无新增环境变量；回滚上一版二进制无需处理该列（加法式变更，旧代码不读）。契约见 docs/functions/AI账户特供快速恢复通道设计.md。
- 模型检测题库提交门禁（2026-10-11 起）：业务库加法式一表两列——新表 `model_check_question_tests`（题目可用性测试记录，含 `(created_by, created_at)` 与 `draft_hash` 索引）与 `model_check_question_bank` 的 `last_test_id`/`last_test_json` 两列，随例行 `--ensure-schema` 幂等建表/补列/建索引生效（加法式 schema，无需手工迁移），无额外命令、无新增环境变量；回滚上一版二进制无需处理（加法式变更，旧代码不读）。契约见 docs/functions/模型检测设计.md §5.7。
- 成功成本历史回填治理（2026-10-12 起）：`juhe_stats` 新增 `backfill_progress` 回填标记表（table_name 主键 + status/cursor_json/updated_at，PG 与 SQLite stats 同构），随例行 `--ensure-schema` 幂等建表；同批**卸载**原随每次 ensure 全表扫六表的六条 `usage_stats_*` 回填 UPDATE statements（PG StatementCount 711→706，本批次净 −5；与其他 schema 批次叠加时的绝对值以当次 `--ensure-schema` 输出为准）与 SQLite ensure 内的同谓词回填循环——历史回填唯一执行者收敛到 `--apply-success-cost-backfill` 一次性子命令（维护窗口，见下"更新发布"节）；发布后迟到符合谓词的行由扣减事务内 CASE 补正兜住。回滚可保留 marker 表；旧代码仍执行全表回填，旧写者运行后再次升级须重置六表完成态并重跑。契约见 docs/plans/计划-20261011T225418-成功成本历史回填治理.md。
- 账户能力域列（AI 账户能力域拆分 M1）：业务库 `accounts` 新增 `capability_domain`（`TEXT NOT NULL DEFAULT 'chat'` + CHECK 四值 `chat`/`video`/`audio`/`image`，SQLite/PG 同形）。新库建表含列；存量库随例行 `--ensure-schema` 幂等补列（SQLite PRAGMA 守卫 / PG `ADD COLUMN IF NOT EXISTS`，列已存在时仍做类型/NOT NULL/DEFAULT/CHECK 四值形状校验），**只加列不赋域**；赋域与存量声明裁剪只由 `--migrate-account-capability-domain` 停写窗口执行（赋域同时把声明裁剪至落域 lane 集，仅混合声明行回写 `credentials_encrypted`，报告列明被裁值与建议归属域；见"拓扑"节命令与下"更新发布"节窗口序列），阶段记录存 `global_settings` key `account_capability_domain_migration`。新版 gateway/jobs 启动对该列做只读形状校验，缺失 / 形状不符 fail-fast，只校验不 ALTER。**M2 启动阶段门禁（2026-10-12 起）**：形状校验之上还要求阶段记录已封存 `m2_finalized`——记录缺失 / 停在中间阶段 / ruleVersion 不符即 fail-fast，错误信息直接给出迁移命令序列；全新空库由 `--ensure-schema`（ensure 臂）与 SQLite preflight 的 bootstrap 种子路径**自动封存**该记录，`accounts` 非空的存量库一律不自动写记录，升级 M2 版本前必须按下"更新发布"节窗口序列显式迁移。契约见 docs/plans/计划-20261011T141004000Z-AI账户能力域拆分.md。
- 调度偏好独立实体（2026-10-12 起，调度偏好独立实体与 API Key 绑定 P1）：业务库加法式一表一列一内置行——新表 `scheduling_preferences`（`id` text 主键、`system_account_id` 可空（内置行恒 NULL）、`name` NOT NULL、`chat_mode` CHECK 两值 `cost_first`/`speed_first`、`chat_speed_first_config_json` 可空、`built_in` integer 0/1 词表（默认 0；v2 用户裁决修订起 `enabled` 列退役——P1 曾交付 `enabled` integer 0/1 默认 1，新库建表即 8 列无 `enabled`，存量库由 `--ensure-schema` 幂等 `DROP COLUMN enabled` 收敛）、`created_at`/`updated_at` NOT NULL；索引：`idx_scheduling_preferences_owner_name_unique[_lower]`（`(system_account_id, name)` 精确 + lower 归一，均挂 `WHERE built_in = 0`）、`idx_scheduling_preferences_builtin_id_unique`（`UNIQUE(id) WHERE built_in = 1`）、`idx_scheduling_preferences_owner`，PG 与 SQLite 同构）、`api_keys.scheduling_preference_id` 绑定列（text 可空，SQLite 建表内联 / PG `ADD COLUMN IF NOT EXISTS` 幂等补列，均带列级 `REFERENCES scheduling_preferences(id)` 无 ON DELETE 子句仅作 belt——SQLite 运行时 PRAGMA 不保证执行，存在性/owner 校验以事务内校验为主）与固定 id 内置行 `schpref-builtin-chat-cost-first`（`默认调度偏好`，`built_in=1`/`chat_mode='cost_first'`/owner NULL；P1 名称「对话·成本优先」随 v2 更名退役，seed / ensure 臂幂等恢复权威名）。**内置行落库通道**：`--seed` 链在系统内置资源顺序位恢复式幂等 upsert；`--ensure-schema` ensure 臂同源补种（固定 id 冲突时仅在目标行仍是 `built_in=1` 时恢复权威字段，`updated_at` 不在更新列、重复执行不漂移；固定 id 被自定义行占用时不触碰）——**存量库升级 = 跑 `--ensure-schema` 即得内置行**。**新版 gateway/jobs 启动对其做只读四臂校验（臂 1：表形状 8 列 + `chat_mode`/`built_in` 两组 CHECK，v2 起无 `enabled`；臂 2：api_keys 绑定列；臂 3：内置行存在且 `built_in=1`、`chat_mode` 合法；臂 4：P2 起要求 `global_settings` key `scheduling_preferences_migration` 存在且 `stage=completed`），缺失或不符 fail-fast，只校验不 ALTER 不补种，错误信息给出 `juhe-ai-maintenance --ensure-schema` 与 `juhe-ai-maintenance --migrate-scheduling-preferences` 运维动作**。升级顺序：存量库升级 = 先进入停写窗口（`docker compose stop gateway jobs` + 已验证可恢复备份），窗口内先 `--ensure-schema`（幂等，得内置行；v2 起含 `DROP COLUMN enabled` 删列，必须在写者停止后执行）再跑 P2 迁移命令并以 postflight 核验通过（见下"更新发布"节窗口序列），最后启动新版 gateway/jobs；全新空库由 `--seed` 链建表并自动封存完成记录；SQLite 开发/单机直跑形态经 gateway preflight 的 bootstrap 种子路径自动满足（含完成记录封存）。回滚上一版二进制不再是无条件免处理：v2 起 `--ensure-schema` 含破坏性幂等删列 `DROP COLUMN enabled`，从 P2 及之后版本回滚到 P1 旧二进制不能仅换二进制（P1 代码对 `scheduling_preferences` 做 9 列形状校验并读写 `enabled`，删列后旧二进制启动即失败），恢复路径见下「更新发布」节。契约见 docs/functions/接口契约与权限矩阵.md「调度偏好」章。
- gateway 启动硬性要求 J3b 运行态索引 ready：新库必须先跑 `docker compose run --rm gateway -init-account-circuit-runtime-index`。
- 管理前端由 gateway 从镜像内 `/app/frontend/dist` 提供（必须显式 `JUHE_AI_FRONTEND_DIST_PATH`，默认空不挂 SPA）；根路径 `/` 由 Caddy 301 到 `/__aisys__/`。
- PG/Redis 不对宿主机发布端口。入口为 `https://aijh.huanmin.top`（Caddy ACME 自动续期，80 常驻 308 升级 HTTPS，443/udp HTTP/3）。
- 指标暴露（2026-10-14 起，替代已下线的系统指标统计/表监控自维护采样）：gateway 健康监听 `JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS=0.0.0.0:3306`（承载 `GET /__aisys__/metrics`）与 jobs 健康监听 `JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS=0.0.0.0:3305` 经宿主回环端口映射发布（`"127.0.0.1:3306:3306"` / `"127.0.0.1:3305:3305"`）——**仅宿主回环可达，部署层必须拒绝公网入口、只允许受限 Prometheus 抓取**（infra 机经既有 SSH 隧道抓取，60s 采样预算；抓取片段、面板导入与告警迁移见 `monitoring/README.md`）。
- postgres 服务经 compose `command` 预置内存调优（2026-09-30 起部署契约）：`shared_buffers=2GB`、`effective_cache_size=4GB`（后者为优化器估值不占内存）。此前镜像默认 `shared_buffers=128MB` 在 8GB 单机上是隐性瓶颈（两库共享 128MB 缓存导致命中率 0.85~0.95、iowait ~9%、checkpoint 5 分钟一轮）。**新建实例勿删此 command**，改参数需同步本节与 `.local` 运维手册。
- postgres 服务 `command` 另预置 `shared_preload_libraries=pg_stat_statements`（2026-10-08 起，慢 SQL 可追溯契约）：实例需执行一次 `CREATE EXTENSION pg_stat_statements`（juhe_ai 库，幂等报错即已存在）后即可查询累计语句统计（Top-N 采样口径与命令见 `.local/project-resources/prod/runbooks/监控与告警运维.md`）。**实例级日志参数不在 compose 内、也不在镜像默认里**：固化在数据卷内 `postgresql.auto.conf`（随容器重建保留、随数据卷删除丢失），2026-10-17 巡检盘点实证生产共四项——**新建实例/全新数据卷必须照下述清单重放**（reload 级，逐条 `ALTER SYSTEM` 后 `SELECT pg_reload_conf();` 生效）：

  ```sql
  ALTER SYSTEM SET log_min_duration_statement = '500ms';  -- >500ms 语句逐条进容器 json-file 日志
  ALTER SYSTEM SET log_line_prefix = '%m [%p] app=%a ';   -- 慢语句/活动日志带 app= 来源标签（与 application_name 契约配套，缺失则慢语句无法区分来源进程）
  ALTER SYSTEM SET log_connections = 'on';                -- 连接建立留痕
  ALTER SYSTEM SET log_lock_waits = 'on';                 -- 超过 deadlock_timeout 的锁等待逐条留痕（锁取证/死锁现场依赖，监控手册 5.8）
  SELECT pg_reload_conf();
  ```

  该日志不进 Loki（alloy 只采应用容器），追溯窗口受 docker `daemon.json` 日志轮转（20MB×3）约束，**容器重建会丢日志，重建前先备份**（运维手册 5.8 节同规）。可直接复制的整套重放命令在 `.local` 运维手册「首次初始化序列」第 1 步。
- redis 服务 `command` 预置 `--slowlog-max-len 1024`（2026-10-08 起，慢命令可追溯契约；阈值维持默认 `slowlog-log-slower-than=10ms`——生产慢命令实测集中在 10~15ms 的 Lua 脚本，收紧到 1ms 会用常规 eval 刷满环形缓冲，丧失巡检价值）。持久留痕由宿主机 `/opt/juhe-monitoring/redis-slowlog/` 快照 cron（每 5 分钟 id 增量导出 jsonl + node-exporter textfile 指标）负责，安装与口径见 `.local` 监控手册；slowlog 本身在 Redis 重启后清空，快照文件不受影响。
- **连接串 `application_name` 契约（2026-10-08 起）**：PG 慢语句日志与 `pg_stat_activity` 靠 `app=%a` 区分来源，三进程共用主库 URL 无法在 `.env` 层区分，故 gateway/jobs/maintenance 在 compose `environment` 对 `JUHE_AI_POSTGRES_URL` 追加 `?application_name=juhe-ai-gateway|juhe-ai-jobs|juhe-ai-maintenance`；子系统专用 URL（`RUNTIME_LOG`/`ACCOUNT_HEALTH_INPUT`/`MAINTENANCE_J3A`/`MAINTENANCE_J3B` 各 `*_POSTGRES_URL`）在 `.env` 值内自带 `?application_name=juhe-ai-<子系统>`。当前实例各 URL 均无其他查询参数，追加符恒为 `?`；若未来某个 URL 值自带参数，需把该处追加符改为 `&` 并同步本节。（原 `TABLE_MONITOR`/`GO_RUNTIME_METRICS` 专用 URL 随两子系统 2026-10-14 下线删除。）
- caddy 服务声明 `init: true`（2026-09-29 起强制）：healthcheck 的 busybox wget 走 https 每次探测泄漏 1 个 `ssl_client` 子进程，caddy 自身作为容器 PID 1 不回收，曾连续堆积 9470 个 zombie 导致容器 docker exec 全败（`procReady not received`，实例级损坏）；docker 内置 init（tini）负责收割，移除该字段即回归。
- 同机共存：聚合AI公益站（juhe-pw 栈，`gyai.huanmin.top`）以 external 方式加入本栈网络并复用本栈 PG/Redis/Caddy；`Caddyfile` 为两栈共享文件（含公益站反代块），划分与修改纪律见 `.local/project-resources/prod/assets/` 服务器资产台账「同机共存」节。

## 目录

```
docker/single-server/
├── compose.yml          # Compose 拓扑（本目录即 Compose 项目目录；gateway/jobs 必须 working_dir: /app/backend，见下）
├── Caddyfile            # 共享入口配置：aijh.huanmin.top（本项目）+ gyai.huanmin.top（同机公益站 juhe-pw 栈）；ACME 自动 HTTPS，:80 常驻 308
├── Dockerfile.runtime   # alpine + 预编译二进制 + 前端 dist（未设 WORKDIR，cwd 锚定靠 compose）
├── deploy.sh            # 发布脚本（防呆流程，见下「更新发布」节）
├── verify-release.sh    # 发布后验证（spool 同源 / drain 接线 / 内存限制断言，随发布自动上传执行）
├── host-init.sh         # 主机层优化自检（swap/sysctl/journald，幂等；deploy.sh 每次发布自检，新机初始化单独执行）
├── monitoring/          # Prometheus/Grafana 监控产物（面板 JSON、postgres_exporter 增补查询、抓取与告警迁移说明）
├── .env                 # 密钥、连接串与部署变量（gitignore；服务器同路径放置，见下".env 契约"）
└── build/               # 本机构建产物（gitignore）：bin/ + frontend-dist/
```

## 运行时数据目录契约（working_dir 必须锚定 /app/backend）

gateway/jobs 的全部"路径类"env 未配置时按 datadir 约定派生为 `<DATA_DIR>/<固定名>`，而 `JUHE_AI_DATA_DIR` 缺省是 `./data`——**相对进程 cwd**（gateway/jobs 的 `internal/datadir` 包注释即此约定）；文件日志等同族运行时路径同样相对 cwd。`Dockerfile.runtime` 只创建 `/app/backend/{data,logs}` 目录但**未设 `WORKDIR`**（Alpine 默认 cwd 为 `/`），因此：

- **compose 必须为 gateway/jobs 保持 `working_dir: /app/backend`**（2026-09-26 起，BUG-0193）。丢了这一行，`data/`、`logs/` 会解析到容器私有可写层 `/data`、`/logs`：用量记录、文件日志、chat-assets、account-health-input 等全部写进临时层，容器重建即静默丢失。
- **gateway↔jobs 同源要求**：用量记录经 `usage-record-spool` 文件目录交接（网关写 JSON 文件，jobs 的 usage spool drain 消费后写 PG `juhe_usage.usage_records`），两侧目录必须解析到**同一个宿主机路径**（当前形态 = `/app/backend/data/usage-record-spool`，挂载 `./data/app/data`）。任何一侧不一致，记录永不到库且无报错——统计全空。
- 非 compose 部署（systemd/裸进程直跑）同理：必须让进程 cwd 锚定到含 `data/` 的工作目录，或显式配置绝对路径（`JUHE_AI_DATA_DIR`，必要时 `JUHE_AI_USAGE_SPOOL_DIRECTORY`，现行名；旧名 `JUHE_AI_USAGE_SPOOL_DIR` 兼容回落）。
- 症状速查（BUG-0193）：统计页面全空但接口 200 → 先查 `SELECT count(*) FROM juhe_usage.usage_records;` 是否 0 行，再对比 gateway/jobs 两容器内 `data/usage-record-spool` 是否为同一挂载目录（`docker inspect` 挂载 + `docker exec ls` 实际内容）。


## 构建与部署（本机 Windows → 服务器）

> **红线：严禁在服务器上构建/打包**（Go 编译、前端 pnpm build 等一切编译型打包只能在本地 Windows 机执行）。
> 4C8G 服务器跑构建会直接把机器拖死（OOM/卡死，整机不可用）。服务器上只允许做两件轻量事：
> `docker compose build`（仅把本地产物的二进制/dst 组装进 alpine 镜像，无编译）和 `docker compose up -d`。

```sh
# 1. 本地构建（Go 交叉编译 + 前端）
for p in gateway jobs maintenance; do
  (cd backend-go/projects/$p && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o ../../../docker/single-server/build/bin/juhe-ai-$p ./cmd/juhe-ai-$p)
done
(cd frontend && pnpm build && rm -rf ../docker/single-server/build/frontend-dist && cp -r dist ../docker/single-server/build/frontend-dist)

# 2. 上传（服务器目录约定 /opt/juhe-ai；<PROD_SERVER_IP> = 生产服务器公网 IP，
#    见 .local/project-resources/prod/assets/，日常发布用 deploy.sh + JUHE_AI_DEPLOY_SERVER）
ssh root@<PROD_SERVER_IP> 'mkdir -p /opt/juhe-ai'
tar czf - -C docker/single-server compose.yml Caddyfile Dockerfile.runtime .env build \
  | ssh root@<PROD_SERVER_IP> 'tar xzf - -C /opt/juhe-ai && chmod 0755 /opt/juhe-ai/build/bin/*'

# 3. 服务器上构建镜像 + 初始化 + 启动（完整序列见运维手册）
cd /opt/juhe-ai
docker compose build          # BASE_IMAGE 已指向 docker.m.daocloud.io（BuildKit 解析 daemon 级 mirror 不可靠）
docker compose up -d postgres redis
# … maintenance 初始化与预置序列（见运维手册）…
docker compose up -d
```

> **非 root 容器（2026-09-28 起，BUG-0225）**：`Dockerfile.runtime` 以固定 UID/GID 1000 的非特权用户 `app` 运行，gateway/jobs/maintenance 三进程不再以 root 跑。compose 无需 `user:` 字段（镜像 `USER app` 已生效；bind mount 属主由宿主机目录决定，加 `user:` 也解决不了挂载目录写权限）。**首次启用的一次性前置**：宿主机挂载根授权 `chown -R 1000:1000 /opt/juhe-ai/data/app`（data 与 logs 两个子目录；回滚 = `chown -R root:root`），否则容器以 app 身份无权写 data/logs。**chown 时序（BUG-0227 部署侧成因）**：root→app 过渡发布时，chown 必须在旧容器完全停止之后执行——仍在运行的旧 root 容器会继续写入 root 属主新文件（0600），app 侧进程无权读取导致 spool drain 失败；若发布序列无法保证该时序，发布完成后必须复查 `data/app/data/usage-record-spool` 属主并对 root 属主残留文件补 chown。caddy/PG/Redis 容器不受影响（镜像各自管理用户，caddy 仍需绑 80/443）。除属主切换发布的 chown 时序外，**发布、更新与回滚流程与命令均不变**。

## 更新发布

**推荐用发布脚本**（`docker/single-server/deploy.sh`，防呆流程）：

```sh
bash docker/single-server/deploy.sh all          # gateway + jobs + maintenance 一同发布（默认）
bash docker/single-server/deploy.sh gateway      # 只发布 gateway / jobs / maintenance 同理
```

脚本固定执行：**前端漏发门禁**（2026-10-08 起，见下）→ **无条件全量重编译**（不信任 `build/bin` 既有产物，防止 shared 模块修复后旧产物上线）→ 上传 → **md5 三点闭环校验**（本地新编译 = 服务器 build/bin = 容器内运行二进制）→ **部署配置同步**（2026-10-14 起：compose.yml / Caddyfile / Dockerfile.runtime 与远端逐文件 md5 比对，漂移即自动 tar 上传并复核——远端 compose 资源契约不再依赖人工记忆补传）→ **迁移前置核验**（2026-10-14 起：只读 verify 核验 capability domain `m2_finalized` 与调度偏好迁移 `stage=completed` 两条新版启动门禁，未就绪立即中止并指向维护窗口章节；maintenance 单独发布除外——维护窗口先更镜像、后迁移，门禁未就绪属预期）→ `docker compose build` + `up -d`（maintenance 为一次性 CLI 只重建镜像不 up，`up -d` 会等其入口退出而挂起）→ 逐容器等待 healthy → 公网健康检查 → **发布后验证**（`verify-release.sh`，见下；2026-10-14 起含 gateway GOMEMLIMIT/mem_limit 内存限制断言）。任一环节失败立即退出并给出回滚提示——服务器侧 `docker compose build`/`up -d` 失败同样使发布立即中止（远端命令显式 pipefail，构建失败不会继续用旧镜像滚动更新）。**all 含 maintenance（2026-10-03 起）**：schema/seed 变更必须随发版落地——否则发布后 `--ensure-schema` 用旧清单"幂等成功"却漏建新表（2026-10-02 与 10-03 两次同款事故：旧二进制 623/624 语句"一致"掩盖 chat/j3b 迁移未落地，功能静默不可用）；ensure 后应核对 `StatementCount` 与本地清单期望一致。

**maintenance 单独发布（2026-10-12）**：只更新该 CLI 镜像，校验本地二进制、服务器产物与镜像内二进制 MD5 一致。镜像校验通过 `compose run --no-deps --entrypoint md5sum` 完成，不执行 maintenance 业务入口，也不启动 gateway/jobs；此次目标没有常驻进程，因此不执行迁移前置核验（维护窗口的既定顺序就是先更 CLI 镜像、后跑迁移，门禁未就绪属预期）、公网健康检查或 `verify-release.sh`。这允许 jobs 保持停止直到回填完成。恢复新版 gateway/jobs 的发布仍必须通过原有健康与 spool/drain 验证；镜像校验失败立即中止，不启动写者。

**前端漏发门禁（2026-10-08 起，deploy.sh 第 [0/6] 步；仅当本次发布包含 gateway 时执行）**：前端产物由 `Dockerfile.runtime` 的 `COPY build/frontend-dist` 烤进镜像，而脚本此前只编译上传 Go 二进制——本地 `build/frontend-dist` 未重建时，后端发版会把旧前端再次烤进新镜像（2026-10-07/10-08 多次漏发的成因：使用记录「媒体并入用量列」等已提交的前端改动在生产不可见）。门禁规则：

- **判据**：读取线上正在服务的前端版本（gateway 容器内 `/app/frontend/dist/build-info.json` 的 `buildId`），与本地 `frontend/` 做差异比对——线上版本以来已提交的差异、已跟踪文件的未提交修改、未跟踪新文件三段并集。任一非空即判定"前端有改动"。读取失败或该 commit 在本地仓库不存在时直接中止发布（版本未知不得放行）。
- **有改动时自动补齐**：`pnpm build`（产物 `buildId` 必须等于当前 HEAD，且构建期间不得再有前端源文件改动写入，否则中止；`frontend/tsconfig.tsbuildinfo` 为 vue-tsc 增量构建缓存（gitignored 构建产物），不计入“源文件改动”判定——2026-10-08 首次触发误报修复）→ 服务器旧目录备份为 `build/frontend-dist.bak-<时间戳>` → 上传 → 服务器产物 `buildId` 复核。新前端随本次 gateway 镜像重建生效，发布后额外校验容器内前端 `buildId` 与 HEAD 一致，防止"目录更新了但镜像没重烤"。
- **无改动时**：沿用服务器现有前端产物，不重建。`jobs` / `maintenance` 单独发布不触碰前端，不执行本门禁。
- 前置：`frontend/node_modules` 必须已安装（缺失即中止并提示 `pnpm install`）。回滚前端 = 恢复服务器 `frontend-dist.bak-*` 后重新 `docker compose build gateway && docker compose up -d gateway`。

手动流程（等价于脚本内部步骤，仅排障时用）：构建（见上节命令）→ 上传 `build/` 与配置三件（compose.yml / Caddyfile / Dockerfile.runtime，脚本发布时自动同步）→ `docker compose build gateway jobs maintenance` → `docker compose up -d`。**手动流程不含前端漏发门禁**，发布前必须自行确认 `frontend/` 相对线上前端版本无差异，或已重新 `pnpm build` 并上传 `build/frontend-dist`；也**不含迁移前置核验**——新版启动门禁涉及的迁移（capability domain / 调度偏好）必须先按下文维护窗口完成，否则 `up -d` 后新版进程 fail-fast 循环。ensure 已恢复幂等加法（studio 退场走显式命令），发布后跑一次 `--ensure-schema` 应用加法式 schema；含删列等破坏性收敛的版本必须先走维护窗口，勿在写者运行时直接跑。回滚 = 上传上一个版本的 build/ 并重新 build+up。

**发布后验证（`verify-release.sh`，2026-10-02 起 deploy.sh 第 [6/6] 步自动上传并在服务器运行；也可手动 `cd /opt/juhe-ai && bash verify-release.sh`）**。强制断言（无凭据）：① gateway 与 jobs 的 `/app/backend/data` 挂载解析到同一宿主目录且两侧 `usage-record-spool` 目录存在——不同源时接口照常 200 但用量永不到库（BUG-0193）；② jobs 近 10 分钟日志无 `usage_record_spool_drain_unwired`；③ gateway 容器 env 含 `GOMEMLIMIT` 且 `mem_limit` 非 0（BUG-0313，2026-10-14 起——双限缺一会让流量高峰 RSS 无封顶膨胀拖死整机，只断言已配置、不锁定数值）。容器内端口拓扑（实测 2026-10-03）：3000=业务链路（`/v1/*`），3306=health+metrics 专用（无 `/v1` 路由）；gateway 3306 / jobs 3305 已宿主回环发布供 Prometheus 抓取（见"拓扑"节与 `monitoring/README.md`）。发布后冒核（生产机宿主执行）：`curl -sf http://127.0.0.1:3306/__aisys__/metrics | head -n 5` 与 `curl -sf http://127.0.0.1:3305/health`。~~可选闭环（`/v1` 请求 → 审计+用量落库 trace 级门禁）~~ **已于 2026-10-03 按用户裁定移除**：闭环依赖具体账户/模型的实时可用性，无法保证稳定，作为门禁会随上游波动误报；本实例曾配置并实测 PASS 一次（trace 双落库、新修复字段活体实证），随后移除凭据与该项，链路落库正确性改由发布后巡检（`audit_logs`/`usage_records` 新行字段核验）覆盖。

**数据库约束迁移（2026-10-01 起，模型质量检测批次，两步）**：本次升级的两处 CHECK 约束分属两条迁移通道，发布检查必须两步分别执行——（历史批次注：本批约束放宽向后兼容、写者运行时执行安全；现行契约下 ensure 内的破坏性收敛——如删列、退场清理——必须在「更新发布」节停写窗口内执行）。
1. `maintenance --ensure-schema`（业务库，例行幂等）：迁移 `model_quality_schedules.interval_minutes`（`10..10080` → `1..10080`，对应定时检查间隔下限放宽到 1 分钟）。实现方式：PG 在 ensure-schema 内用 DO 块替换约束；SQLite 业务库重建 `model_quality_schedules` 表迁移（数据保持；SQLite 无法原位改列约束）。
2. `maintenance --apply-j3b-model-check-postgres`（j3b 库，读 env `JUHE_AI_MAINTENANCE_J3B_POSTGRES_URL`，幂等可重入，与 ensure-schema 相互独立）：迁移 `juhe_j3b.model_check_runs.trigger_kind`（取值域加 `schedule_now`，计划立即执行）。`--ensure-schema` 不包含该迁移；未执行时存量三值 CHECK 不变，首个 `schedule_now` run 插入会因 CHECK 约束写入失败（用户侧表现为"已发起"后静默无记录）。

升级后验证（PG，`docker compose exec postgres psql -U juhe_ai juhe_ai`）：

```sql
-- interval_minutes 相关约束定义应显示 PG 规范化形态 ((interval_minutes >= 1) AND (interval_minutes <= 10080))
SELECT conname, pg_get_constraintdef(oid) FROM pg_constraint
 WHERE conrelid = 'juhe_business.model_quality_schedules'::regclass;
-- trigger_kind 约束应包含 schedule_now
SELECT pg_get_constraintdef(oid) FROM pg_constraint
 WHERE conrelid = 'juhe_j3b.model_check_runs'::regclass
   AND pg_get_constraintdef(oid) LIKE '%trigger_kind%';
```

也可直接经管理面创建/编辑一条 `intervalMinutes=5` 的定时检查计划做写入面验证（管理面写入下限自 BUG-0309 起为 5 分钟；DB CHECK 约束仍放宽到 1..10080，1 仅存量兼容——存量 `intervalMinutes=1` 计划照常运行，新建/编辑一律不得低于 5）。回滚提示：约束放宽向后兼容（旧二进制接受的取值是新约束的子集），回滚上一版二进制时**无需回滚约束**，直接按常规流程回滚即可。

**成功成本历史回填维护窗口（2026-10-12，一次性命令）**：`--ensure-schema` 不再回填六表 success_cost_usd。必须先停 jobs 写者，保持停写直到回填完成；迁移前禁止用 `deploy.sh all/jobs` 启动新版 jobs。gateway 可持续将用量交付持久 spool，已落盘记录在 jobs 恢复后追平；进程内尚未落盘记录仍受原有优雅停机/崩溃边界约束，不能承诺进程崩溃时零丢失。窗口只运行一个回填实例，并避开 04:00–07:00 备份窗口。

```sh
cd /opt/juhe-ai
# 1) 目标服务器先停聚合/扣减/清理写者；完成本次数据库备份
docker compose stop jobs
# 2) 本机构建发布新版 maintenance（deploy.sh maintenance，只更新镜像，不启动 jobs）
# 回到目标服务器 /opt/juhe-ai，用宿主 .env 的应用 DSN，先 ensure 建 marker
docker compose --profile tool run --rm maintenance --ensure-schema --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"
# 3) 只读预检；行数以实际报告为准，不预设零
docker compose --profile tool run --rm maintenance --apply-success-cost-backfill --dry-run --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"
# 4) 一次性回填（stdout 单行 JSON 报告六表批数/命中行数；重复执行零批秒完）
docker compose --profile tool run --rm maintenance --apply-success-cost-backfill --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"
# 5) 先复核六行全 done；再执行一次命令，应零批完成
docker compose exec postgres psql -U juhe_ai juhe_ai -c "SELECT table_name, status FROM juhe_stats.backfill_progress;"
# 6) 本机再按发布流程更新 gateway/jobs（deploy.sh all），确认 jobs 为新版健康容器
# 若本版还有其他前置迁移，先完成对应迁移，再恢复新版 jobs；随后检查 spool 追平
```

SQLite 形态使用 `--driver sqlite --paths "stats=<stats 库文件>,..."`，同样先停 jobs，再 ensure/回填，完成后启动新版；dry-run/probe 只读打开既有主库，不创建主库或父目录、不更改 journal、不写事实或 marker，并正常读取已提交 WAL；SQLite 自身可创建、更新或清理必要的 WAL/SHM 协调文件，本命令不主动 checkpoint。任何步骤失败都保持 jobs 停止，修复原始错误后从持久游标重跑。

回退旧二进制可保留 marker 表，但旧版仍会执行原全表回填，性能问题会恢复；旧 jobs 的扣减也可能重新产生待补行。经历旧写者运行后再次升级，须先停 jobs 并备份，在窗口将六目标 marker 重置为 `status='running', cursor_json='{}'`，然后完整回填和核验，最后启动新版 jobs。重置只针对本子命令的六个 `usage_stats_*` 目标，不删除 marker 表或其他状态。

```sql
UPDATE juhe_stats.backfill_progress SET status='running', cursor_json='{}', updated_at=CURRENT_TIMESTAMP::text
WHERE table_name IN ('usage_stats_totals','usage_stats_minute','usage_stats_hourly','usage_stats_daily','usage_stats_weekly','usage_stats_monthly');
```

**账户能力域迁移维护窗口（AI 账户能力域拆分 M1，一次性赋域；M2 升级沿用同一停写窗口）**：`--ensure-schema` 只对存量库幂等补 `accounts.capability_domain` 列，不赋域；赋域必须在停写窗口按下述序列执行（顺序契约：**先停写、完成并验证可恢复备份，再在窗口内跑 ensure 与迁移并核验通过，最后启动依赖该列的新版 gateway/jobs**——新版启动对该列做只读形状校验，缺列 / 形状不符 fail-fast，且 M2 起还要求阶段记录已封存 `m2_finalized`；停写不能用"低峰"或未观察到请求代替；窗口前置：服务器 maintenance 镜像已含本迁移命令，本机 `deploy.sh maintenance` 只更镜像、不启动写者）：

```sh
cd /opt/juhe-ai
# 1) 停写窗口第一步：停止旧版写者（schema 变更与迁移一律不在写者运行时执行）
docker compose stop gateway jobs
# 2) 完成并验证可恢复备份（必须先于 ensure：ensure 之后才备份，备份就失去了回滚价值）
# 3) 例行 ensure-schema（幂等补列 + 形状校验；只加列不赋域）
docker compose run --rm maintenance --ensure-schema --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"
# 4) 只读预检（可选；域列尚不存在时也允许执行，报告预计赋域账户数）
docker compose run --rm maintenance --verify-account-capability-domain --account-capability-domain-stage=m1 --account-capability-domain-check=preflight --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"
# 5) 执行迁移（--node-stopped --go-stopped --backup-confirmed 三确认缺一拒绝；分批事务写域列、被裁剪行的凭据声明与阶段记录，报告列明被裁值与建议归属域；失败清单非空零写入 exit 3，保持停写修复后同阶段重跑）
docker compose run --rm maintenance --migrate-account-capability-domain --account-capability-domain-stage=m1 --node-stopped --go-stopped --backup-confirmed --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"
# 6) 停写核验（要求域列存在、全表域值合法、授权实例与源账户域一致、失败清单清零）
docker compose run --rm maintenance --verify-account-capability-domain --account-capability-domain-stage=m1 --account-capability-domain-check=postflight --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"
# 7) 核验通过后才启动新版 gateway/jobs，恢复写入
docker compose start gateway jobs
```

阶段记录存 `global_settings` key `account_capability_domain_migration`，已 `m1_complete` 的重复执行只报告完成、不再重算；失败清单非空不得上线（部分成功不满足上线门禁）。迁移报告含声明裁剪明细（账户 × 被裁值 × 建议归属域）：被裁能力需由操作者同凭据另建归属域账户承接，未承接前该能力无候选账户。**M2 切换（升级 M2 版本前，同一停写窗口）**：M1 步骤 5/6 完成后，在同一停写窗口继续执行 `docker compose run --rm maintenance --migrate-account-capability-domain --account-capability-domain-stage=m2-cutover --node-stopped --go-stopped --backup-confirmed ...`（停写全量重算）并以 `--account-capability-domain-check=postflight` 核验，通过后才启动新版 gateway/jobs——M2 版启动门禁要求已封存 `m2_finalized`；全新空库由 `--ensure-schema` 自动封存，非空库一律不自动写记录。回滚 = 上传上一版 build/ 重新 build+up（加式列与阶段记录保留，回滚保留 `m2_finalized`，旧代码不读；不执行删列）。若迁移已裁剪混合声明行，回滚不恢复被裁值（凭据已收敛域内），被裁能力需按报告另建归属域账户承接。契约见 docs/plans/计划-20261011T141004000Z-AI账户能力域拆分.md。

**调度偏好迁移维护窗口（2026-10-12 起，调度偏好独立实体 P2；P1 建表与 P2 迁移同一停写窗口）**：新版 gateway/jobs 启动对 `scheduling_preferences` 表形状 / `api_keys.scheduling_preference_id` 绑定列 / 内置行 `schpref-builtin-chat-cost-first` 做只读校验，并**要求 `global_settings` key `scheduling_preferences_migration` 已完成（`stage=completed`）**——任一不符 fail-fast，错误信息给出 `juhe-ai-maintenance --ensure-schema` 与 `juhe-ai-maintenance --migrate-scheduling-preferences`（三确认门禁）运维动作。升级顺序（在 M1/M2 能力域窗口之后、启动新版 gateway/jobs 之前；窗口前置：服务器 maintenance 镜像已含本迁移命令，本机 `deploy.sh maintenance` 只更镜像、不启动写者）。**停写与备份必须先于 ensure**：v2 起 ensure 含破坏性幂等删列 `DROP COLUMN enabled`，P1 旧二进制仍在运行时执行会打断旧版写者；ensure 之后才备份会让备份失去 P1 回滚价值：

```bash
# 1) 停写窗口第一步：停 gateway/jobs（schema 变更与迁移一律不在写者运行时执行）
docker compose stop gateway jobs
# 2) 完成并验证可恢复备份（先于 ensure，保住 P1 回滚价值）
# 3) P1/P2 schema：例行 ensure-schema（幂等建表/补列/删列，ensure 臂恢复式补种内置行）
docker compose run --rm maintenance --ensure-schema --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"
# 4) 预检（只读报告，退出码恒 0；Blocking>0 表示存在阻断迁移的坏数据，先修复）
docker compose run --rm maintenance --verify-scheduling-preferences --scheduling-preferences-check=preflight --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"
# 5) 迁移（三确认门禁缺一 exit 2；失败清单非空零写入 exit 3，不得上线；断点恢复=同命令重跑）
docker compose run --rm maintenance --migrate-scheduling-preferences --node-stopped --go-stopped --backup-confirmed --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"
# 6) 完成态核验（未就绪 exit 3）
docker compose run --rm maintenance --verify-scheduling-preferences --scheduling-preferences-check=postflight --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"
# 7) 核验通过后才启动新版 gateway/jobs，恢复写入
docker compose start gateway jobs
```

行为要点：`speed_first` 且带有效配置的策略生成等值偏好（name=`{策略名}·速度优先`，owner 内重名自动加序号 `-2/-3`）；`cost_first`/缺省策略不建偏好（NULL 普通 Key 回填内置 ID）；**仅更新 `scheduling_preference_id IS NULL AND purpose='general'` 的 Key**；显式绑定 Key 一律不动，chat Key 不回填（绑定列恒 NULL）；存量 studio Key 在退场清理命令（`--apply-studio-retirement`）执行前保留——迁移排除、绑定列恒 NULL，报告单独计数 `apiKeysStudioExcluded`，清理命令执行后随退场删除；`route_strategies.config_json` 只读不回写（回滚 oracle）；速度运行态不迁移（降级 TTL 与慢样本停写后瞬态自愈，属明示行为变化）。全新空库由 `--seed` 链自动封存完成记录（counts 全零），存量库一律由上述迁移命令写记录；已 `completed` 的重复执行只报告完成。升级后验证（PG）：`SELECT id, name, chat_mode, built_in FROM juhe_business.scheduling_preferences WHERE id = 'schpref-builtin-chat-cost-first';` 应返回一行 `默认调度偏好 / cost_first / 1`（v2 起 8 列无 `enabled`，期望名随 v2 更名）；`SELECT COUNT(*) FROM juhe_business.api_keys WHERE scheduling_preference_id IS NULL AND purpose = 'general';` 应为 0；`SELECT value_json FROM juhe_business.global_settings WHERE key = 'scheduling_preferences_migration';` 的 `stage` 应为 `completed`。**回滚（v2 起有破坏性变更，与 P1 时期不同）**：v2 起 `--ensure-schema` 含幂等 `ALTER TABLE scheduling_preferences DROP COLUMN enabled`（SQLite/PG 同执行），从 P2 及之后版本回滚到 P1 旧二进制**不能仅换二进制重新 build+up**——P1 旧代码对 `scheduling_preferences` 做 9 列形状校验并读写 `enabled` 列，删列后旧二进制启动即失败。回滚须二选一：恢复迁移前备份，或手工补列后回滚——SQLite `ALTER TABLE scheduling_preferences ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1;`；PG 同形 `ALTER TABLE juhe_business.scheduling_preferences ADD COLUMN IF NOT EXISTS enabled integer NOT NULL DEFAULT 1;`（两方言同形 `integer NOT NULL DEFAULT 1`，0/1 整数承载布尔语义，非 PG `boolean`；补列值恒 1 与 P1 种子 DEFAULT 一致，无数据损失）。契约见 docs/functions/接口契约与权限矩阵.md「调度偏好」章。

**studio 退场清理维护窗口（2026-10-14 起，一次性命令）**：`--ensure-schema` 已恢复幂等加法，普通 ensure 不再携带任何破坏性 studio 退场语句（studio 四表已从建表清单移除、`idx_api_keys_studio_purpose_unique` 不在创建清单）；存量库的 studio 遗留——4 张 studio 表（`studio_assets` / `studio_tasks` / `studio_operations` / `studio_user_asset_usage`）、`api_keys` 中 `purpose='studio'` 的 Key 行与 `studio_domains_json` 列、遗留索引 `idx_api_keys_studio_purpose_unique`——由一次性显式命令统一清理（PG 与 SQLite 双方言同构执行）：

```sh
cd /opt/juhe-ai
# 1) 停写窗口：停 gateway/jobs，完成并验证可恢复备份（回滚唯一依赖就是该备份——
#    清理动作不可逆，ensure 的幂等加法不覆盖本命令的删除面）
docker compose stop gateway jobs
# 2) 显式退场清理（--node-stopped --go-stopped --backup-confirmed 三确认缺一拒绝；
#    完成后 stdout 报告执行前/后的对象计数；若 studio Key 仍关联 OpenAI-compatible
#    文件或向量资源，会在任何写入前拒绝并在 stderr 给出计数。命令清理 4 张 studio
#    表 + purpose='studio' Key 行 + studio_domains_json 列 + 遗留索引；幂等可重入）
docker compose run --rm maintenance --apply-studio-retirement --node-stopped --go-stopped --backup-confirmed --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"
# 3) 命令成功后核对执行报告，再启动新版 gateway/jobs
docker compose start gateway jobs
```

窗口纪律与能力域/调度偏好窗口相同：先停写、后备份、再执行。命令会在写入前统计 studio Key 和其关联的 OpenAI-compatible files、vector stores、vector store files、chunks；关联资源任一非零即拒绝清理，先由维护者迁移或单独处理这些资源，再重试。因 stdout 报告在执行后输出，其中 preflight 字段是执行前计数，可供留档但不是 dry-run 审批材料。命令由多个幂等语句组成，不是全命令原子事务；中途失败后可在修复原因后重跑收敛，无法原地回滚的删除仍只能恢复发布前备份。

`purpose` CHECK 在清理前允许保留历史形状：新库三值；PostgreSQL ensure 会把旧二值扩为三值；SQLite 普通 ensure 不重建 `api_keys`，旧二值 CHECK 保持不变。应用写路径已拒绝新建 studio Key，显式清理后 studio Key 行随之删除。两方言在旧库上的 CHECK 形状可以不同。回滚 = 恢复发布前备份。

**退役监控表清退维护窗口（2026-10-14 起，一次性命令）**：系统指标统计与表监控子系统下线后，12 张退役监控表由一次性显式命令 `--drop-retired-monitoring-tables` 幂等清退（探测 + `DROP TABLE IF EXISTS`）：统计库 9 张（`system_metrics_*` / `process_event_loop_*` 6 张 + `go_runtime_metrics_*` 3 张）+ 表监控库 3 张（`database_storage_snapshots`、`table_storage_snapshots`、`table_monitor_owner_leases`）。命令本身不负责停进程，三确认门禁缺一拒绝（exit 2）；退出码 0 含重跑的「只报告已清退」，1 为运行失败（任一表失败或目标库不可达，stdout 报告始终列出已成功与已缺席清单）。

```sh
cd /opt/juhe-ai
# 1) 停写窗口：停 gateway/jobs，完成并验证可恢复备份（DROP 不可逆，
#    无备份表可回填，回滚唯一路径是整体恢复该备份）
docker compose stop gateway jobs
# 2) 清退（--node-stopped --go-stopped --backup-confirmed 三确认缺一拒绝；
#    PG 单 --dsn 覆盖 juhe_stats schema 全部 12 张，历史独立 metrics 库
#    以该库为 --dsn 重跑本命令即可，缺席表是良性 no-op；幂等可重入）
docker compose run --rm maintenance --drop-retired-monitoring-tables --node-stopped --go-stopped --backup-confirmed --driver postgres --dsn "$JUHE_AI_POSTGRES_URL"
# 3) 命令成功后核对 stdout 报告，再启动 gateway/jobs
docker compose start gateway jobs
```

SQLite 存储形态走 `--driver sqlite --paths`，必填 `stats=` / `go-runtime-metrics=` / `table-monitor=` 三个键（其余键接受并忽略，可复用 `--ensure-schema` 的完整 `--paths`）。窗口纪律与 studio 退场窗口相同：先停写、后备份、再执行。stdout 保持单条 Report JSON，回滚指引同时落 stderr；回滚 = 用 `--backup-confirmed` 验证过的外部备份整体恢复对应数据库。生产执行需用户另行授权。

## .env 契约（当前实例全集见服务器 /opt/juhe-ai/.env）

### Compose 与 deploy 脚本级必含变量（BUG-0225 实例事实变量化）

- 服务器 `/opt/juhe-ai/.env`：除下述业务变量外，必须包含 `POSTGRES_PASSWORD`、`REDIS_PASSWORD`（既有必填）与 **`JUHE_AI_PROXY_IP_A` / `JUHE_AI_PROXY_IP_B`**——出海代理隧道两条域名（`data.aijh.huanmin.top` / `egress.aijh.huanmin.top`）的公网 IP，注入 gateway/jobs/maintenance 三服务的 `extra_hosts`；缺失时 `docker compose config` 阶段即报错（`:?required` 模式，与 `POSTGRES_PASSWORD` 一致）。真实取值属实例事实，只在 `.local/project-resources/prod/` 私有留档。
- 本地发布机 `docker/single-server/.env`（gitignore，不入库）：须含 `JUHE_AI_DEPLOY_SERVER=<user>@<服务器IP>`——`deploy.sh` 的 ssh 目标，环境变量同名导出优先，`.env` 次之，均缺时脚本 fail-fast 并提示取值位置。

### 容器内存治理（BUG-0313，2026-10-14）

- `GOMEMLIMIT=2300MiB`：compose gateway `environment` 固定值（**非 .env 变量**），Go 堆软限制——接近时 GC 激进回收，RSS 实际封顶约 2.5GiB。背景：流量高峰下 gateway RSS 曾冲 3.56GiB（goroutines 全程平坦无泄漏，纯堆膨胀），与 PG（~3GiB）合计挤光 8G 整机页缓存引发 I/O 活锁僵死（10-14 11:51-13:48）。调大前必须复核整机内存预算：`gateway RSS ≈ GOMEMLIMIT + ~200MiB 运行时开销`，须与 PG ~3GiB、系统与其余容器共存后仍留 ≥1.5GiB 页缓存。
- `mem_limit`（cgroup 硬限，最后防线）：gateway `3g`、jobs `1g`——超限 OOM kill 只波及容器自身（restart: unless-stopped 自动拉起），不再拖死整机。PG **有意不设限**：PG 被 OOM 强杀的恢复代价远大于保护收益，其 RSS 由 shared_buffers/work_mem 自控（历史稳定 3.0-3.2GiB）。
- 主机层配套：4G swapfile（`/swapfile`，fstab 持久，`vm.swappiness=10` 仅作耗尽兜底）+ `vm.min_free_kbytes=262144`、`vm.watermark_scale_factor=125`（提前启动回收防 D 状态活锁）。三项主机层治理（swap/sysctl/journald 上限）已固化为幂等脚本 **`host-init.sh`**（已纳入版本控制，见上「目录」节；swap 激活与 fstab 持久化、sysctl 配置文件与运行值分别核对，漂移自动重写并重新应用）——`deploy.sh` 每次发布自动自检（单项失败仅告警不阻塞发布），新机初始化按 `docs/deploy/部署指南.md` 第 1 节执行。

- `NODE_ENV=production`：触发全部生产校验（SECRET 强度、CORS 白名单必填、禁 dev 自动登录）。
- `JUHE_AI_SECRET`：≥32 位强随机；账号凭据/内建 API Key 加密封套，**有加密数据后不可更换**，务必留档。所有共享 Redis 会话亲和绑定的 Gateway 实例必须使用相同 secret：会话绑定携带上游 Key 的 HMAC 指纹（账户内 Key 会话黏性，见 `docs/functions/账户内APIKey会话亲和设计.md`），组级会话绑定键同样由该 secret 派生（独立 HMAC purpose `group-affinity:v1`，见 `docs/functions/策略路由分组会话亲和设计.md`），不同 secret 实例混跑不是受支持的稳定形态；secret 协调轮换时先统一切换全部实例再恢复流量，旧指纹自然失配并在下一次协议成功后重绑。
- `JUHE_AI_DATABASE_DRIVER=postgres` + `JUHE_AI_POSTGRES_URL`；audit/operation/account-health 子系统缺省跟随主库驱动与主 URL。
- `JUHE_AI_RUNTIME_LOG_POSTGRES_URL`：**必须显式**（F1 运行日志索引无主 URL 回退）。（原并列表的 `JUHE_AI_TABLE_MONITOR_POSTGRES_URL` 随表监控子系统 2026-10-14 下线删除，见下"部署时需同步从 .env 移除的行"。）
- `JUHE_AI_LOG_DIR`（compose 已显式配 `/app/backend/logs`，对应宿主 `data/app/logs`；显式配置行为不变）：运行日志文件三方同目录契约——gateway 文件日志写侧与 grep 扫描面、jobs F1 索引器与轮转文件保留清理都按它取目录。**2026-09-28 默认开启整改起，gateway 未配置时同样派生 `<DATA_DIR>/logs`（与 jobs 侧派生对称，写侧 NewFileSink 代建目录），文件日志与 grep 面默认可用**；`disabled` 字面量（大小写不敏感）= 显式关闭文件日志写侧与 grep 面（启动事件 `runtime_log_file_sink_disabled`）。双进程要共享同一目录（compose 挂载卷内）时仍建议显式同值，避免 `JUHE_AI_DATA_DIR` 分叉造成 gateway/jobs 目录不一致。gateway 文件日志为 BUG-0195 修复（2026-09-28）：slog 同时写 stdout 与 `juhe-ai.log`（按大小轮转 `<base>.<YYYYMMDDTHHMMSSZ>.<uuid>.log`），`slog.Default()` 一并接管——HTTP 访问日志自此为 JSON 且进文件；文件清理由 jobs 保留清理负责（只删已完整索引的 rotated 文件）。
- `JUHE_AI_LOG_MAX_FILE_MB`（gateway 写侧轮转阈值，2026-09-28 随 BUG-0195 恢复——Node 既有 env，同名同默认同范围）：默认 `100`，合法 `1..1024`，越界启动失败；另有 `JUHE_AI_LOG_MAX_FILES`（默认 `500`）/ `JUHE_AI_LOG_RETENTION_DAYS`（默认 `30`，1..30）为轮转文件保留参数，由 jobs 索引器按此清理 rotated 文件（**jobs 长期停机期间 gateway 持续写不受 500×30 约束，目录会继续增长，需关注磁盘**）。注意区分：索引**行**保留是独立参数 `JUHE_AI_RUNTIME_LOG_RETENTION_DAYS`（默认 `14`，1..90，可被 system_settings `runtimeLogIndexRetentionDays` 覆盖），索引查询读窗默认最近 3 天——500×30 只约束文件，不约束索引行。
- `JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS`（BUG-0226，2026-09-28；**2026-10-14 起仅 jobs 使用**）：jobs 健康监听地址，compose 显式配 `0.0.0.0:3305`（代码默认 `127.0.0.1:3305`，容器形态必须显式覆盖为 0.0.0.0 才能经 ports 的宿主回环映射 `"127.0.0.1:3305:3305"` 对宿主可达）——现供 infra 机 Prometheus 经 SSH 隧道抓取（见 `monitoring/README.md`）；值非法（主机名/坏端口）会启动失败。原 gateway 侧同名变量（系统指标页 jobs 健康段抓取地址，曾配容器名视角 `juhe-ai-go-jobs:3305`）随「系统指标统计」子系统下线从 compose gateway 服务删除，`.env` 中若有该变量整行移除（见下"部署时需同步从 .env 移除的行"）。
- `JUHE_AI_GATEWAY_HEALTH_LISTEN_ADDRESS`（2026-10-14 起 compose 显式配置）：gateway 健康监听地址，compose 配 `0.0.0.0:3306`（代码默认 `127.0.0.1:3306`；compose healthcheck 仍走容器内 `127.0.0.1:3306/health` 不受影响）。绑全接口只为让 ports 的宿主回环映射 `"127.0.0.1:3306:3306"` 可达，承载 `GET /__aisys__/metrics` 供 infra 机 Prometheus 抓取；端口仅发布到宿主回环，**部署层必须拒绝公网入口、只允许受限 Prometheus 抓取**（接入细节见 `monitoring/README.md`）。
- `JUHE_AI_GATEWAY_HEALTH_ALLOW_NON_LOOPBACK`（2026-10-15 起 compose 显式配置 `"true"`）：健康监听 fail-closed 校验的放行开关。代码默认只允许 localhost/回环地址，`0.0.0.0:3306` 会启动失败（2026-10-15 发布实测重启循环）；设 `true` 放行私网（RFC1918）与未指定地址（`0.0.0.0`），**公网地址任何情况下仍拒绝**（见 gateway `main.go` `validateHealthListenHost`）。jobs 侧无该校验、无对应变量。
- `JUHE_AI_CHAT_*`（AI 问答子系统，gateway/jobs 双进程，越界均启动失败）：
  - `JUHE_AI_CHAT_RETENTION_DAYS`：默认 `3`，合法 `1..365`；gateway 读取保留窗口与 jobs retention 清理按它取值，**双进程必须同值**，否则清理窗口与读取保留不一致（消息先被读不到或超期残留）。
  - `JUHE_AI_CHAT_ASSETS_ROOT`：chat 资产对象根目录，未配置时按 `JUHE_AI_DATA_DIR`（缺省 `./data`）派生 `<数据根>/chat-assets`；gateway 资产写入与 jobs 资产清理**双进程必须同值**（同卷同目录）。
  - `JUHE_AI_CHAT_DATABASE_PATH`：仅 sqlite 模式生效的 chat 库文件（默认 `<数据根>/chat.sqlite3`），生产 PG 模式（`juhe_chat` schema 跟随主库）不配置。
  - `JUHE_AI_CHAT_MAX_TURNS_PER_CONVERSATION`：单会话最大轮次，默认 `50`，合法 `1..1000`。
  - `JUHE_AI_CHAT_MAX_CONVERSATIONS_PER_USER`：单用户最大会话数，默认 `50`，合法 `1..1000`。
  - `JUHE_AI_CHAT_UPSTREAM_SSE_MAX_EVENTS`：单轮上游 SSE 事件预算，默认 `65536`，合法 `2048..262144`（调高只为容纳合法长输出，不解除单事件/累计内容等 DoS 防护）。
  - `JUHE_AI_CHAT_WEB_SEARCH_TIMEOUT_MS`（2026-10-14 起，gateway 单进程）：联网搜索子调用整体超时，默认 `120000`（120 秒），合法 `1000..600000`；非整数或越界启动失败。仅 gateway 消费，jobs 不读。
- `JUHE_AI_ACCOUNT_BALANCE_DATABASE_PATH`（余额手动刷新租约库，2026-10-04 起双方言装配）：仅 sqlite 模式生效——gateway 进程内余额手动刷新执行器的租约四表文件，未配置时按 `JUHE_AI_DATA_DIR`（缺省 `./data`）派生 `<数据根>/account-balance.sqlite3`；gateway 独占、启动期幂等自建四表，无需预处理；生产 PG 模式（同构四表在 `juhe_jobs` schema）不配置、不打开该文件。路径不得与其他 SQLite 库文件共用。
- `JUHE_AI_CODEX_CONTEXT_ROOT`（Codex Responses↔Chat 桥 segments 根，gateway/jobs 双进程；2026-10-03 起 gateway 组合根装配该状态，gateway 现亦消费本 env）：可选，未配置时按 `JUHE_AI_DATA_DIR`（缺省 `./data`）派生 `<数据根>/codex-context`。gateway 桥 segment 写入与 jobs retention segment 文件清理**双进程必须同值**（同卷同目录）——根不同源时清理按「文件不存在=成功」落账而文件永不删除（存储泄漏）。PG 模式另要求 `juhe_codex_context` schema（`juhe-ai-maintenance --ensure-schema` 预置六个 schema 之一）：缺失时 gateway 启动期 schema 探针 fail-fast（错误含 `codex context postgres schema 探针失败`），属部署错误，先补 schema 再启动，无静默降级。
- `JUHE_AI_REDIS_STATE_URL`：Redis 运行态键空间连接串（cache/state/queue 三实例约定中的 state 实例）。**依赖门禁非开关**：未配置时 jobs 的账户恢复探针、速度优先运行态、电路控制面 reconcile 等族按 disabled 登记（`/health` 的 `workerWired`/`workerJobs` 与任务登记可见缺席），配置后自动启用；gateway 主链运行态也按它归一。生产 compose 三容器共享同值。
- `JUHE_AI_J3B_CIRCUIT_REDIS_URL`：J3b 电路运行态专用 Redis。**必须显式且不得与 `JUHE_AI_REDIS_STATE_URL` 相同键空间**（gateway 启动强校验——主链 state URL 回退会让两套 Lua 状态机并发写同一 states hash，语义不兼容；现用同实例 DB2）。未配置/无 Redis 时 J3b 电路回退**进程内 memory 状态机（重启即失，跨进程不共享）**——功能性可用但持久性降级，生产应显式配置独立 Redis URL。
- `JUHE_AI_PROXY_LATENCY_MANAGEMENT_LISTEN_ADDRESS`（jobs 侧，J3a 代理延迟管理面）：`POST /__aisys__/api/proxies/{id}/test` 手动探测入口的监听地址，默认 `127.0.0.1:0`（随机回环端口，每次启动变化；`/health` payload 报告实际监听地址）。需稳定入口或跨容器访问时显式配置 `host:port`（端口 0 = 随机）；非法 host/端口启动失败。
- `JUHE_AI_AUDIT_LOG_BLOB_DIRECTORY`、`JUHE_AI_ACCOUNT_HEALTH_INPUT_SOURCE=postgres`（+ INPUT_POSTGRES_URL）：PG 模式按运维手册显式化。
- `JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE`（**2026-09-28 默认开启整改起默认 `1`**——成功正文默认全量，原默认 0.1 采样已废除；显式 `0..1` 仍生效，`0` 须与 `JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS=0` 联动）与 `JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS`（默认 1）：成功请求正文长期采样率与热保留窗口（失败/问题请求恒全量保留 7 天，成功正文长期保留 3 天）。当前生产显式配 `1`——与新的出厂默认一致（BUG-0198）。
- `JUHE_AI_MAINTENANCE_J3A/J3B/ACCOUNT_BALANCE_POSTGRES_URL`：`--apply-*` 预置命令的 maintenance 专用 URL（`ACCOUNT_BALANCE` 为 2026-10-02 新增成员，值通常与 J3A 相同——目标都是 `juhe_ai` 库且当前 role 拥有 `juhe_jobs` schema）。
- `JUHE_AI_OWNER_LEASE_ACQUIRE_WAIT`（compose 为 gateway 显式配 `45s`，BUG-0228，2026-09-28 起）：F3 audit 与 F4 operation-log 两个启动获取点共用的 owner 租约有界等待——被前任进程持有时按 1s 间隔重试到 deadline，超时仍维持 fail-fast 原文案退出（不静默排队活 owner 并存场景）。作用：发布 recreate 窗口旧容器被 SIGKILL 截断 `defer` 租约释放时，新 gateway 在启动期等待 DB 租约 TTL（默认 30s）过期后接管，替代原来的 fail-fast 重启循环。默认空/0 = 既有 fail-fast 契约不变；生产取 `45s`（> 30s TTL）。gateway healthcheck `start_period` 相应配 `75s`（等待发生在 health server 监听之前：45s 等待 + 启动余量）。jobs 无启动 fail-fast 租约（F1/F2/J1/J3a 均为运行期 supervisor 重试），不配置该变量。2026-10-15 起 F3 与 F4 获取冲突分支均有同 owner 快速路径：未过期租约的 owner_id（进程身份，默认主机名）与获取方相同时直接接管并自增 fence_token，不消耗等待预算——同 owner 的再获取只可能来自本实例自己的重启/失租恢复，旧世代写入由 fence 挡在外面；跨 owner 仍必须等过期（BUG-0228 契约不变）。失租后的 keeper 不再发放旧 owner/fence 快照，采集路径不能把已丢失的行续活。
- `JUHE_AI_GATEWAY_VOLCENGINE_SPEECH_BASE_URL`（**测试 seam，生产不配置**；2026-10-04 M6 火山语音批引入）：火山豆包 TTS 出站服务根覆盖（缺省恒官方 `https://openspeech.bytedance.com`，与账户 ark base_url 无关的 URL 构造特例）。仅验收 E2E 把 gateway 子进程出站指回本机 mock 上游时注入；**不是功能开关**（语音能力恒生效），生产/compose/直跑均不配置，误配置会把语音流量指向错误上游。
- `JUHE_AI_ALLOWED_ORIGINS`：生产必填、逗号分隔、拒绝 `*`；当前为 `https://aijh.huanmin.top` 加 `http://<生产服务器公网 IP>`（实例值见服务器 `/opt/juhe-ai/.env` 与 `.local` 资产）。
- `JUHE_AI_COOKIE_SECURE=true`（HTTPS 已启用）；`JUHE_AI_TRUST_PROXY=true`（经 Caddy）。
- `JUHE_AI_REDIS_NAMESPACE=prod`：redis 驱动下必填非空。
- 管理后台账号沿用老生产 `system_accounts`（154 个），无默认密码残留；seed 的 `sys_admin`（admin/admin）仅在全新部署或重置 schema 后出现，且 `must_change_password=1` 首登强制改密（BUG-0224），现有实例行不被 seed 重跑改写。

### 部署时需同步从 .env 移除的行（2026-10-14 系统指标统计/表监控下线）

随「系统指标统计」与「表监控」两子系统整体下线，发布新版前需从服务器 `/opt/juhe-ai/.env` 删除以下行（只列变量名，值属实例事实不入任何文档；`.env` 属密钥文件，手工编辑前先备份，禁止把内容粘贴进仓库或文档）：

- `JUHE_AI_GO_RUNTIME_METRICS_STORE`（及同家族若存在：`JUHE_AI_GO_RUNTIME_METRICS_POSTGRES_URL` / `JUHE_AI_GO_RUNTIME_METRICS_INTERVAL` / `JUHE_AI_GO_RUNTIME_METRICS_RETENTION_DAYS` / `JUHE_AI_GO_RUNTIME_METRICS_SERVICE` / `JUHE_AI_GO_RUNTIME_METRICS_DATABASE_PATH`）
- `JUHE_AI_TABLE_MONITOR_POSTGRES_URL`（及同家族若存在：`JUHE_AI_TABLE_MONITOR_INTERVAL` / `JUHE_AI_TABLE_MONITOR_RETENTION_DAYS` / `JUHE_AI_TABLE_MONITOR_MAX_TABLES` / `JUHE_AI_TABLE_MONITOR_RUN_TIMEOUT` / `JUHE_AI_TABLE_MONITOR_OWNER_LEASE` / `JUHE_AI_TABLE_MONITOR_MAX_CONCURRENT_SOURCES` / `JUHE_AI_TABLE_MONITOR_RETENTION_BATCH_SIZE` / `JUHE_AI_TABLE_MONITOR_RETENTION_MAX_BATCHES` / `JUHE_AI_TABLE_MONITOR_INSTANCE_ID` / `JUHE_AI_TABLE_MONITOR_STORE`）
- `JUHE_AI_JOBS_HEALTH_LISTEN_ADDRESS`（gateway 跨容器抓取用途已删除；jobs 侧监听地址由 compose 显式固定为 `0.0.0.0:3305`，该变量在 `.env` 中整行移除）

## 系统设置键（DB `system_settings`，非 env；2026-10-04 M2 媒体批次新增）

- `audioFirstResponseTimeoutSeconds`：默认 `120`（单位秒），音频车道（`/v1/audio/*` 同步 TTS/STT）单次 attempt 首响应超时；`videoCreateTimeoutSeconds`：默认 `60`（单位秒），`POST /v1/videos` 创建请求超时。两键合法区间均为 `10..3600` 整数，归属管理面系统设置 `gateway-core` section（与其他 `*TimeoutSeconds` 车道键同组）。
- **存量库自动补齐**：键行由 maintenance `--seed` 播种（`pgSeedSystemSettings`）；存量库在例行发布 `--ensure-schema --seed` 前缺行时，gateway（`compatibleSystemSettingDefaults` 兜底）与 jobs（`jobssettings.DefaultSystemSettings`）按同默认值（120/60）运行，行为一致、无需手工补行；重新播种后键行自动落库。非环境变量，compose 与直跑形态同源。

## 非承诺功能面登记（2026-09-28 默认开启整改一并登记）

以下面均为**已文档化的降级面**，不属于"未配置即禁用"的反模式整改范围（它们缺失的根因是 Node IPC 对等体消失，而非配置缺失），不承诺恢复：

- **系统指标页的运行时快照 / 队列端点**：Node 网关进程内运行时（IPC 对等体）消失后的既定降级面，系统指标-运行时快照与队列端点按降级契约响应（原降级契约文档已随该子系统下线删除）。**2026-10-14 起「系统指标统计」子系统整体下线，本条降级面随功能删除消亡；运行时可观测改由 Prometheus 指标承接（`monitoring/README.md`）**。
- **gateway `/__aisys__/api/health` 的 `proxyLatency` 字段已随整改移除**（2026-09-28）：原为 Node 契约冻结 stub（恒 `{enabled:false, ready:true}`，无任何代码消费方——前端只读 jobs `/health` payload 的 `proxyLatencyEnabled/Ready/OwnerHeld` 扁平字段）。J3a 真实状态以 jobs `/health` 为准（宿主回环 `127.0.0.1:3305` 直取；原 health-snapshot jobs 段随系统指标统计下线删除）。
