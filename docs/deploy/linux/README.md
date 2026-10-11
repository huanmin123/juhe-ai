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
- **一次性维护窗口的直跑等价操作（2026-10-12 成功成本历史回填）**：直跑形态没有 compose，窗口序列中的 `docker compose stop/start jobs` 等价替换为停/启 jobs 的 systemd 单元（如 `systemctl stop juhe-ai-jobs && systemctl start juhe-ai-jobs`）；maintenance 子命令直接跑二进制——PG 形态 `juhe-ai-maintenance --apply-success-cost-backfill [--dry-run] --driver postgres --dsn ...`，SQLite 存储形态 `--driver sqlite --paths "stats=<stats 库文件>,..."`（键集同六库 bootstrap 词汇，只强制 `stats=`）。marker 表前置 `--ensure-schema`、复核 `backfill_progress` 六行 done 与 spool 归零口径以[部署指南](../部署指南.md)§4 与 `docker/single-server/README.md`"更新发布"节为准，不在此复制。
- **账户能力域迁移的直跑等价操作（AI 账户能力域拆分 M1）**：加列随例行 `--ensure-schema` 幂等生效（只加列不赋域），赋域/核验子命令直接跑二进制——PG 形态 `juhe-ai-maintenance --migrate-account-capability-domain --account-capability-domain-stage=m1 --node-stopped --go-stopped --backup-confirmed --driver postgres --dsn ...` 与 `juhe-ai-maintenance --verify-account-capability-domain --account-capability-domain-stage=m1 --account-capability-domain-check=preflight|postflight ...`，SQLite 存储形态 `--driver sqlite --paths "business=<业务库文件>"`（能力域命令只读写业务库，paths 只强制 `business=` 键，可复用六库完整 `--paths`）；窗口序列中的 `docker compose stop/start gateway jobs` 等价替换为停/启 gateway 与 jobs 的 systemd 单元。停写备份确认门禁、"先迁移后启动新版 gateway/jobs"顺序契约、失败清单非空不得上线门禁与完整窗口序列以[部署指南](../部署指南.md)§4 与 `docker/single-server/README.md`"更新发布"节为准，不在此复制。
- 如果上游 API 需要代理访问，先看 [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md)。
