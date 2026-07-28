# 部署文档目录

> 面向 AI 与维护者。
> 这里集中放发布包构建、部署场景、三端部署差异、Docker、高性能模式、网络代理、HTTPS 证书、状态检测、常驻运行、反向代理、项目/业务备份、迁移和排障相关文档。

## 先选部署场景

部署时不要先通读所有文档。先按入口环境选择场景：

| 场景 | 入口文档 | 后续只看 |
| --- | --- | --- |
| 云服务器、VPS、独立服务器、公司内网服务器 | [服务器部署方案](scenarios/服务器部署方案.md) | 对应平台文档、HTTPS、代理 |
| 家里电脑、家用小主机、NAS、家庭宽带入口 | [家庭宽带反向代理方案](scenarios/家庭宽带反向代理方案.md) | 对应平台文档、HTTPS、代理 |
| 公网 Edge 回源、多人高并发、SSE 长连接 | [反向代理与高并发隧道部署指南](反向代理与高并发隧道部署指南.md) | WireGuard、Caddy/Nginx、系统参数、切换回滚 |
| 不确定怎么选 | [部署场景选择示例](scenarios/部署场景选择示例.md) | 按示例跳转 |

`部署指南.md` 只保留发布包启动、环境变量、验证、常驻以及项目备份和业务备份这些通用基线，不再作为部署方式选择入口。默认旧模式的两类备份分别只保留最近 3 次，日志、审计 payload、usage 和 Redis 不备份；普通统计仍按旧排除规则处理。capability v2 是强制例外：`juhe_stats` 必须与 `juhe_business` 进入同一个 PostgreSQL 一致快照，`juhe_rollback_compat` 存在时也进入同一 dump，且 compat 非空期间必须保留可恢复的 PITR/WAL 范围，不能套用“统计不备份”或固定三份轮换规则。

## 文档索引

- [跨平台构建文档](构建指南.md)：构建环境检测、构建命令、产物说明、构建参数和构建后检查。
- [部署场景目录](scenarios/README.md)：服务器部署、家庭宽带反代和场景选择示例。
- [跨平台部署基线](部署指南.md)：发布包兼容矩阵、解压配置、启动验证、常驻运行、项目/业务备份、迁移和常见排障。
- [部署流程示例](部署流程示例.md)：一次从选择场景、构建、选择平台文档、配置 HTTPS / 代理到验证的完整示例。
- [Docker 部署指南](Docker部署指南.md)：单容器镜像构建、默认配置、启动、验证和清理。
- [高性能模式部署指南](高性能模式部署指南.md)：当前 Node 阶段的 PostgreSQL、PgBouncer、Redis cache/state/queue、同机多网关与独立 worker、初始化、设置、验证和备份；Go 迁移完成后 PostgreSQL + Redis 将成为唯一正式模式。
- [反向代理与高并发隧道部署指南](反向代理与高并发隧道部署指南.md)：公网 Edge L4、WireGuard、PROXY v2、Caddy/Nginx、Linux/macOS 参数、受控切换、回滚和容量门禁。
- [Go 渐进减法迁移开发构建部署调整](../migration/开发构建部署调整.md)：后端迁移到 Go 期间的构建、发布包、Docker、服务化和回滚目标。
- [Linux 部署目录](linux/README.md)：Linux 发布包、Docker、systemd、防火墙和代理访问差异。
- [Windows 部署目录](windows/README.md)：Windows 发布包、PowerShell、服务化、Docker Desktop 和代理访问差异。
- [macOS 部署目录](macos/README.md)：macOS 发布包、launchd、Docker Desktop 和代理访问差异。
- [网络代理部署目录](proxy/README.md)：sing-box 安装、本机 mixed 代理端口和 juhe-ai 后台代理绑定。
- [HTTPS 证书部署目录](https/README.md)：Caddy 自动 HTTPS、免费证书自动续期、Docker / 裸机入口和 Nginx + Certbot 备选。
- [状态检测与自动恢复目录](watchdog/README.md)：可选的外部 health 探针和自动恢复策略；默认常驻由服务管理器负责，外部探针只告警。

## 适用边界

- 构建指南回答“如何从源码生成可部署发布包，以及构建后如何检查产物”。
- 跨平台部署基线回答“发布包在目标机器上如何启动、配置、验证、常驻和迁移”，不负责选择服务器或家庭宽带入口方案。
- Docker 部署指南回答“如何直接用 Docker 镜像和 Compose 运行项目”。
- 高性能模式部署指南当前回答“如何用 Docker 或非 Docker 方式部署 PostgreSQL + Redis 中间件、初始化 PostgreSQL schema / 默认数据、记录生产凭据，以及如何配置 performance 模式”；Go 迁移完成后不再保留 standalone / performance 两套模式，部署文档需要收敛为 PostgreSQL + Redis 默认运行方式。
- 部署场景目录回答“这次部署到底是服务器入口还是家庭宽带反代入口，以及该读哪些后续文档”。
- 反向代理与高并发隧道指南回答“公网 Edge 如何通过 WireGuard 安全、高并发地回源，以及系统参数、切换和容量如何验收”。
- 三端子目录回答“Windows、macOS、Linux 在启动脚本、服务化、Docker 访问宿主机代理、防火墙和反向代理上有什么差异”。
- 网络代理目录回答“服务器无法直连上游 API 时，如何部署 sing-box 并把本地代理绑定到 AI 账户”。
- HTTPS 证书目录回答“公网域名如何用免费证书提供 HTTPS 入口，并让证书自动续期”。
- 状态检测与自动恢复目录回答“确有无人值守需求时如何增加外部探针”；它不是默认部署项，也不能与发布流程并发重启应用。
- 压测、性能分析和容量结论统一放入 `docs/reports/`，不要混入部署操作手册。
- 影响本地开发启动或测试验证的内容应优先更新 `docs/develop/`，不要混入部署文档。
- 影响环境变量、数据目录、加密密钥或发布包结构时，需要同时确认构建指南和部署指南。

## Capability v2 平台门禁

模型能力健康 v2 不是“多启动一个 worker”即可启用。Docker、Linux systemd、Windows Service / PowerShell、macOS launchd 和高性能拓扑各自都必须有可执行 runbook，明确 gateway、每个 gateway host 独立 capability-handoff-replay / quarantine、control projector / reconciler / due scheduler、Asynq consumer、stats partition / hour-close、deployment coordinator 的实例数，active / prepared epoch，host volume / producer inventory / replay fencing lease、shard / cursor / advisory lock 域，readyz，停止顺序和最长 drain。gateway ingress 先停，replay 必须继续到 ACK drain；单个 exported PostgreSQL snapshot 覆盖 `public.goose_db_version + juhe_business + juhe_stats`，compat 存在时同 dump 覆盖它，并携带同 barrier 的 handoff / quarantine 证据和 PITR。对应平台 runbook 和演练报告缺失时，manifest preflight 必须拒绝 capability v2；不能借用另一个平台的命令或只凭 HTTP 200 放行。
