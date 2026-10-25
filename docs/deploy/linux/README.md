# Linux 部署目录

> 面向 Linux 服务器部署和运维。
> 这里记录 Linux 下发布包、Docker、systemd、防火墙、HTTPS 和代理访问的差异。跨平台通用流程以 [部署指南](../部署指南.md)（国内单机 Docker，唯一生产形态）为准。

## 文档索引

- [Linux 部署指南](Linux部署指南.md)：Linux 服务器发布包直跑、systemd、HTTPS、防火墙和上游网络代理配置入口。
- [Linux 部署流程示例](Linux部署流程示例.md)：一次从上传发布包到 systemd 常驻和代理绑定的示例流程。

## 适用边界

- Linux 是生产服务器优先部署平台，推荐 `systemd` 或 Docker Compose。
- 公网 HTTPS 默认优先用 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md)。
- 长期运行默认由 systemd 负责常驻，外部探针只告警；确有无人值守自动恢复需求时，须单独评审防抖、限频和发布互斥（原 watchdog 指南已随旧形态文档清理删除）。
- Docker 生产形态以 [部署指南](../部署指南.md) 与 `docker/single-server/` 为准；本文只说明 Linux 平台差异。
- **发布包直跑（systemd/裸进程）必须锚定进程 cwd**：gateway/jobs 的 `data/`、`logs/` 等相对路径以 cwd 为基准，且用量记录经 `usage-record-spool` 目录在 gateway↔jobs 间交接，两侧必须解析到同一路径。systemd 单元请设 `WorkingDirectory=` 到含 `data/` 的目录，或显式 `JUHE_AI_DATA_DIR` 绝对路径；否则统计全空且数据随进程/容器重建丢失（BUG-0193，契约细节见 `docker/single-server/README.md`"运行时数据目录契约"）。
- 系统运行设置键（DB `system_settings`，如 2026-10-04 M2 媒体超时键 `audioFirstResponseTimeoutSeconds`/`videoCreateTimeoutSeconds`）随 maintenance `--ensure-schema --seed` 落库，非环境变量；直跑与 Docker Compose 同源，无平台差异（存量库缺行时进程按默认值兜底）。
- **一次性维护窗口的直跑等价操作（2026-10-12 成功成本历史回填）**：直跑形态没有 compose，先执行 `systemctl stop juhe-ai-jobs`，再安装新版 maintenance 并完成 ensure、dry-run、apply 和六表完成态核验；随后安装同版 jobs，最后执行 `systemctl start juhe-ai-jobs`，失败时保持 jobs 停止；maintenance 子命令直接跑二进制 `juhe-ai-maintenance --apply-success-cost-backfill [--dry-run] --dsn ...`（`--dsn` 单形态；`--driver`/`--paths` 旗标已随 2026-10-24/25 SQLite 退役删除）。marker 表前置 `--ensure-schema`、复核 `backfill_progress` 六行 done 与 spool 归零口径以[部署指南](../部署指南.md)§4 与 `docker/single-server/README.md`"更新发布"节为准，不在此复制。
- **账户能力域迁移的直跑等价操作（AI 账户能力域拆分 M1）**：加列随例行 `--ensure-schema` 幂等生效（只加列不赋域），赋域/核验子命令直接跑二进制 `juhe-ai-maintenance --migrate-account-capability-domain --account-capability-domain-stage=m1 --node-stopped --go-stopped --backup-confirmed --dsn ...` 与 `juhe-ai-maintenance --verify-account-capability-domain --account-capability-domain-stage=m1 --account-capability-domain-check=preflight|postflight ...`（`--dsn` 单形态）；窗口序列中的 `docker compose stop/start gateway jobs` 等价替换为停/启 gateway 与 jobs 的 systemd 单元。停写备份确认门禁、"先迁移后启动新版 gateway/jobs"顺序契约、失败清单非空不得上线门禁与完整窗口序列以[部署指南](../部署指南.md)§4 与 `docker/single-server/README.md`"更新发布"节为准，不在此复制。
- **调度偏好迁移的直跑等价操作（2026-10-12，调度偏好独立实体 P1+P2）**：新版 gateway/jobs 启动对 `scheduling_preferences` 表 / `api_keys.scheduling_preference_id` 绑定列 / 内置行做只读校验，并要求 `global_settings` key `scheduling_preferences_migration` 已完成（`stage=completed`），任一不符 fail-fast。直跑序列（在能力域 M1/M2 窗口之后、启动新版 gateway/jobs 的 systemd 单元之前并入同一停写窗口）：先 `juhe-ai-maintenance --ensure-schema`（幂等建表/补列并补种内置调度偏好行）；停写（`systemctl stop juhe-ai-gateway juhe-ai-jobs`）并完成可恢复备份后，依次执行 `juhe-ai-maintenance --verify-scheduling-preferences --scheduling-preferences-check=preflight`（只读预检，退出码恒 0）、`juhe-ai-maintenance --migrate-scheduling-preferences --node-stopped --go-stopped --backup-confirmed`（一次性迁移，三确认门禁缺一 exit 2，失败清单非空零写入 exit 3，断点恢复=同命令重跑）、`juhe-ai-maintenance --verify-scheduling-preferences --scheduling-preferences-check=postflight`（完成态核验，未就绪 exit 3），全部通过后再启动 gateway/jobs 单元。存储统一 `--dsn ...` 单形态（`--driver`/`--paths` 旗标已随 SQLite 退役删除）。迁移行为要点、门禁纪律与升级后验证 SQL 以 `docker/single-server/README.md`"更新发布"节为准，不在此复制。
- **studio 退场清理的直跑等价操作（2026-10-14 起，一次性命令）**：`--ensure-schema` 已恢复幂等加法，存量库的 studio 遗留（4 张 studio 表、`purpose='studio'` Key 行与 `studio_domains_json` 列、遗留索引 `idx_api_keys_studio_purpose_unique`）由一次性显式命令清理。直跑序列：`systemctl stop juhe-ai-gateway juhe-ai-jobs` 并完成可恢复备份后，直接跑二进制 `juhe-ai-maintenance --apply-studio-retirement --node-stopped --go-stopped --backup-confirmed`（三确认缺一 exit 2；studio Key 仍关联 OpenAI-compatible 文件或向量资源时在任何写入前拒绝并在 stderr 给出计数），命令成功后再 `systemctl start juhe-ai-gateway juhe-ai-jobs`。存储统一 `--dsn ...` 单形态。清理动作不可逆、非全命令原子事务，回滚唯一路径是恢复发布前备份；窗口纪律与完整口径以[部署指南](../部署指南.md)§4 与 `docker/single-server/README.md`"更新发布"节为准，不在此复制。
- **退役监控表清退的直跑等价操作（2026-10-14 起，一次性命令）**：系统指标统计与表监控子系统下线后，12 张退役监控表（统计库 9 张 `system_metrics_*` / `process_event_loop_*` / `go_runtime_metrics_*` + 表监控库 3 张 `database_storage_snapshots` / `table_storage_snapshots` / `table_monitor_owner_leases`）由一次性显式命令幂等清退（探测 + `DROP TABLE IF EXISTS`）。直跑序列：`systemctl stop juhe-ai-gateway juhe-ai-jobs` 并完成可恢复备份后，直接跑二进制 `juhe-ai-maintenance --drop-retired-monitoring-tables --node-stopped --go-stopped --backup-confirmed`（三确认缺一 exit 2；退出码 0 含重跑的「只报告已清退」，1 为运行失败且 stdout 报告始终列出已成功与已缺席清单），命令成功后再启动两个单元。`--dsn ...`（单 `--dsn` 覆盖 `juhe_stats` schema 全部 12 张，历史独立 metrics 库以该库为 `--dsn` 重跑，缺席表是良性 no-op）。DROP 不可逆、无备份表可回填，回滚 = 用验证过的外部备份整体恢复对应数据库；窗口纪律与完整口径以[部署指南](../部署指南.md)§4 与 `docker/single-server/README.md`"更新发布"节为准，不在此复制。
- **provider 维度持久列退役的直跑等价操作（2026-10-23 起，B4① 存储迁移批）**：`--ensure-schema` 含 9 条幂等 `DROP COLUMN IF EXISTS`（codex 三表 + business 六列）——破坏性收敛，直跑序列：**先安装新版 gateway/jobs/maintenance 二进制但保持旧进程运行**，然后 `systemctl stop juhe-ai-gateway juhe-ai-jobs` 并完成可恢复备份（先于 ensure：收列后旧二进制读写该批列报错，回滚必须「先回代码、再回 schema」），再直接跑二进制 `juhe-ai-maintenance --ensure-schema --dsn ...`，核验通过后 `systemctl start juhe-ai-gateway juhe-ai-jobs`。媒体任务 `providerCode` 值改显 vendor（gpt 账户由 `"openai"` 变 `"gpt"`）。窗口纪律与核验 SQL 以[部署指南](../部署指南.md)§4 与 `docker/single-server/README.md`"更新发布"节为准，不在此复制。
- 如果上游 API 需要代理访问，先看 [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md)。

Docker 维护窗口只发布 maintenance 时，发布脚本校验 CLI 镜像内二进制后结束，不要求 gateway/jobs 正在运行；回填完成并恢复常驻进程后，原健康与 spool/drain 发布验证仍是必经步骤。直跑形态按上文停写、替换 maintenance、回填、再恢复新版 jobs 的顺序执行。
