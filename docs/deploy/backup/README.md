# 备份与恢复

> 本文是生产数据的备份、验证与恢复手册；部署与升级流程见[部署指南](../部署指南.md)，升级窗口的停写顺序见[部署指南 §9](../部署指南.md)。备份只承担业务数据恢复点，不承担审计/日志/运行态的恢复职责。

## 1. 策略

按实际环境二选一或并用：

| 方式 | 做法 | 适用 |
| --- | --- | --- |
| 备份机定时拉取（当前生产采用） | 备份机通过 SSH 到服务器，在 PG 容器内执行 `pg_dump`，流式落盘，保留最近 7 份 | 不想占用生产磁盘；已有独立备份机 |
| 服务器本地 cron | 每日低峰 `pg_dump -Fc` 到 `/opt/juhe-ai/backup/`，再同步到异机并清理过期文件 | 没有独立备份机；需自行控制磁盘 |

当前生产只备份业务数据：日志/审计/遥测域仅排除表数据（`--exclude-table-data`，DDL 仍保留在 dump 内），`data/app` 文件域整体不备份。

## 2. 窗口纪律

- 自动备份通常安排在凌晨（当前生产为 04:10）；发版/重启容器避开备份窗口——整库读快照会持表锁，窗口约 1–2 小时。
- 备份必须在停写之后、升级之前完成并验证可读；不能只看文件大小。
- 恢复或升级回滚都在停写窗口内执行，先停 gateway/jobs，再动数据库。

## 3. 手工备份

```sh
cd /opt/juhe-ai
DUMP=backup/juhe_ai-$(date +%Y%m%d-%H%M).dump
docker exec juhe-ai-postgres pg_dump -U juhe_ai -Fc juhe_ai > "$DUMP"
docker exec -i juhe-ai-postgres pg_restore -l >/dev/null < "$DUMP" && echo backup-ok
```

- `pg_restore -l` 只验证归档可读，不验证数据完整性；需要更强验证时在隔离实例 `pg_restore` 后抽查关键表行数。
- dump 文件落在服务器 `/opt/juhe-ai/backup/`；备份机拉取时直接流式写到备份机，不在服务器落盘。
- dump 内含 `.env` 之外的业务数据，按密钥同等敏感级别保管。

## 4. 恢复

停写窗口内执行：

```sh
cd /opt/juhe-ai
docker compose stop gateway jobs
docker exec -i juhe-ai-postgres psql -U juhe_ai -d postgres -c 'DROP DATABASE IF EXISTS juhe_ai;'
docker exec -i juhe-ai-postgres psql -U juhe_ai -d postgres -c 'CREATE DATABASE juhe_ai OWNER juhe_ai;'
docker exec -i juhe-ai-postgres pg_restore -U juhe_ai -d juhe_ai --no-owner < backup/<dump>
docker compose up -d
```

恢复后按[部署指南 §8](../部署指南.md) 验证：容器 healthy、公网健康、登录、一次真实 `/v1` 调用、`usage_records` 增长。

若只恢复 PG dump（未搬 `data/` 目录），Redis 运行态为空，必须在 gateway 首次启动前重建电路索引：

```sh
docker compose run --rm gateway -init-account-circuit-runtime-index
```

gateway 已启动时先 `docker compose stop gateway`，跑完再 `start`（一次性容器会占用固定 IP）。

## 5. 不备份的内容

按当前生产口径不备份：

- Redis（运行态可重建；AOF/RDB 不作为恢复点）；
- `juhe_dataset` 与 `juhe_jobs`/`juhe_stats` 遥测表的**表数据**（结构仍在 dump 内）；
- `data/app` 内的审计 blob、热缓存、瞬态队列。

升级回滚只依赖业务数据；审计/日志域不承担恢复点职责。

## 6. 与升级、迁移的关系

- 升级前备份是硬门禁：`maintenance upgrade` / `deploy.sh upgrade` 都要求确认已备份。
- 迁移到新服务器时，如果整体搬 `data/`（PG/Redis 数据 + app 文件），不需要恢复 dump；只搬 PG dump 时按[迁移文档](../migration/README.md)完整初始化。
- 回滚旧二进制时，如果 schema 已经前进（例如 revision 17 的 CHECK），旧二进制会被启动门禁拒绝；唯一恢复路径是升级前整库备份。

## 7. 关联文档

- [部署指南](../部署指南.md)：首次部署、升级窗口、验证。
- [`docker/single-server/README.md`](../../../docker/single-server/README.md)：PG 容器、数据卷与运行时目录契约。
- [迁移到新服务器](../migration/README.md)。
