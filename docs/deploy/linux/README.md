# Linux 部署目录

> 面向 Linux 服务器部署和运维。
> 这里记录 Linux 下发布包、Docker、systemd、防火墙、HTTPS 和代理访问的差异。跨平台通用流程以 [部署指南](../部署指南.md)（国内单机 Docker，唯一生产形态）为准。

## 文档索引

- [Linux 部署指南](Linux部署指南.md)：Linux 服务器发布包直跑、systemd、HTTPS、防火墙和上游网络代理配置入口。
- [Linux 部署流程示例](Linux部署流程示例.md)：一次从上传发布包到 systemd 常驻和代理绑定的示例流程。

## 适用边界

- Linux 是生产服务器优先部署平台，推荐 `systemd` 或 Docker Compose。
- 公网 HTTPS 默认优先用 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md)。
- 长期运行默认由 systemd 负责常驻，外部探针只告警；确有无人值守自动恢复需求时再看 [状态检测与自动恢复指南](../watchdog/状态检测与自动恢复指南.md)。
- Docker 生产形态以 [部署指南](../部署指南.md) 与 `docker/single-server/` 为准；本文只说明 Linux 平台差异。
- **发布包直跑（systemd/裸进程）必须锚定进程 cwd**：gateway/jobs 的 `data/`、`logs/` 等相对路径以 cwd 为基准，且用量记录经 `usage-record-spool` 目录在 gateway↔jobs 间交接，两侧必须解析到同一路径。systemd 单元请设 `WorkingDirectory=` 到含 `data/` 的目录，或显式 `JUHE_AI_DATA_DIR` 绝对路径；否则统计全空且数据随进程/容器重建丢失（BUG-0193，契约细节见 `docker/single-server/README.md`"运行时数据目录契约"）。
- 如果上游 API 需要代理访问，先看 [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md)。
