# 部署文档目录

> 以**当前唯一生产形态**为主线：国内单台云服务器 + Docker Compose（PostgreSQL + Redis + gateway/jobs/maintenance + 入口代理，go-only）。主文档只保留部署流程、门禁与关键细节；主机、配置、入口、监控、备份、排障、迁移等组件内容拆到独立文档。文档已按 2026-11-05 现行状态整理，不保留历史形态的部署说明。

## 文档索引

| 内容 | 入口 |
| --- | --- |
| **从零部署主流程** | [部署指南](部署指南.md)：拓扑、前置条件、首次部署、验证、升级与回滚、安全清单；按顺序执行即可 |
| 主机初始化 | [host/README.md](host/README.md)：服务器规格、Docker 安装、防火墙、`host-init.sh`、目录属主 |
| 配置参考 | [config/README.md](config/README.md)：`.env` 全量语义与模板、Redis Cluster 部署/迁移/影响、Caddyfile、权限 |
| Nginx 入口 | [nginx/README.md](nginx/README.md)：替代内置 Caddy 的完整安装、TLS、SSE/WebSocket、超时、真实 IP、排障与回退 |
| HTTPS / 反向代理 | [https/README.md](https/README.md)：默认 Caddy 形态；外部 TLS 的通用要求与外部 Caddy 示例 |
| 监控接入 | [monitoring/README.md](monitoring/README.md)：应用指标端点、采集端/监控栈安装、SSH 隧道、`juhe-ai 应用总览`、告警基线、集群差异 |
| 监控交付物 | [`docker/single-server/monitoring/`](../../docker/single-server/monitoring/)：`collector/`、`stack/`、`dashboards/`、查询与告警规则 |
| 备份与恢复 | [backup/README.md](backup/README.md)：策略、手工备份、恢复、不备份边界、与升级/迁移的关系 |
| 排障速查 | [troubleshooting/README.md](troubleshooting/README.md)：入口、容器、PG/Redis、业务链路、监控 |
| 迁移到新服务器 | [migration/README.md](migration/README.md)：搬迁内容、启动、验证、DNS 切流、回退 |
| 非 Docker 直跑 | [linux/README.md](linux/README.md)：systemd/裸进程差异、cwd/数据目录契约、升级等价操作 |
| 发布包构建（非生产主流程） | [构建指南](构建指南.md)：跨平台构建 Go 三项目 + 前端，生成 go-only 发布包 |
| 出海网络代理（AI 上游） | [proxy/README.md](proxy/README.md) → [sing-box 指南](proxy/sing-box网络代理部署指南.md) |
| 多节点部署与不停机发布（实施中） | [functions/多节点部署与不停机发布设计.md](../functions/多节点部署与不停机发布设计.md)：多机 Docker + 外部 LB + expand/contract 滚动发布；P1 门禁/账本/在线 DDL、P2 gateway 无状态、P3 jobs 全局 leader 已落地并通过 dev 演练，**尚未达到多节点生产就绪**（共享文件/前端/LB/发布脚本未完成），当前生产仍以单机流程为准 |
| 集群上线与开发兼容（实施中） | [集群上线与开发兼容指南](集群上线与开发兼容指南.md)：集群 expand/switch/contract、单机停写窗口、开发需求评估表与禁止项；`rolling/contract` 在剩余 P3 完成后可用 |

## 权威位置

- 部署配置源文件：仓库 [`docker/single-server/`](../../docker/single-server/)（`compose.yml` / `Caddyfile` / `Dockerfile.runtime` / `deploy.sh` / `verify-release.sh` / `host-init.sh`）。
- 配置契约（`.env` 变量、运行时目录、更新发布、监控端口、schema 升级门禁）：[`docker/single-server/README.md`](../../docker/single-server/README.md)。
- 监控交付物（采集端 compose/脚本、监控栈 compose、Grafana 面板 JSON、postgres_exporter 查询、告警基线）：[`docker/single-server/monitoring/`](../../docker/single-server/monitoring/)；接入、复用与验收说明见 [monitoring/README.md](monitoring/README.md)。
- 生产环境的服务器地址、密钥、备份账号等实例事实：私有目录 `.local/project-resources/prod/`，不进入本目录。

## 适用边界

- 本文档面向“拿到仓库即可部署”的维护者：主文档走流程，组件细节按上表进入对应文档。
- 内网生产演练变体不在 `docs/deploy/` 下单独成文：复用外部 PostgreSQL/Redis 的 compose、生产语义配置、备份与边界统一由 [`docker/dev-server/README.md`](../../docker/dev-server/README.md) 维护；它不是生产实例，也不替代生产部署。
- 不覆盖已退役形态：K3s/K8s、Harbor/Jenkins、家庭宽带反代、Edge/WireGuard 回源、macOS/Windows 本机部署、SQLite/内存存储模式、Node 后端。看到旧文档或旧命令时以本目录为准。
- 修改部署行为（compose / Caddyfile / Dockerfile / 环境变量契约 / 运行时目录 / 初始化与升级序列）时，必须同批更新本目录、对应组件文档与 `docker/single-server/README.md`。
