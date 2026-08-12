# macOS 部署目录

> 面向 macOS 本机或小型服务器部署。
> 这里记录 macOS 发布包、launchd、Docker Desktop、HTTPS 和本机代理的差异。跨平台通用流程仍以 [部署指南](../部署指南.md) 为准。

## 文档索引

- [macOS 部署指南](macOS部署指南.md)：macOS 发布包、launchd、Docker Desktop、HTTPS、反向代理和代理配置入口。
- [macOS 部署流程示例](macOS部署流程示例.md)：一次从 tar 包到 launchd 和代理绑定的示例流程。
- [反向代理与高并发隧道部署指南](../反向代理与高并发隧道部署指南.md)：macOS 作为 WireGuard 回源节点时的 Caddy/Nginx、launchd、系统参数和切换回滚。
- [macOS 运维脚本](operations/README.md)：包含 Redis cache/state/queue 三进程安装、只读角色验证、主服务 launchd 与临时切流模板。

## 适用边界

- macOS 默认适合个人或小团队轻量部署；作为高并发回源节点时，必须使用独立公网 Edge、WireGuard、显式资源上限和分档容量验证。
- Docker 部署依赖 Docker Desktop 或等价容器环境。
- 公网 HTTPS 默认优先用 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md)。
- 长期运行默认由 launchd 负责常驻，外部探针只告警；确有无人值守自动恢复需求时再看 [状态检测与自动恢复指南](../watchdog/状态检测与自动恢复指南.md)。
- 当前高性能 `install-performance-topology.sh` 已编排唯一 Go sidecar 内的 F1/F2/F3。routine release 从最终冻结 release 启动独立 candidate Node 槽，使用 `--go-sidecar-mode reuse --quick` 复用正式 Go owner，再用 `quick-performance-cutover.sh` 原子切 route。完整 temporary 预演、功能读回和 handover controller 只用于首次新拓扑、事故调查或异常回切。
- 高性能模式在 macOS 固定使用 main `6379/6380/6381` 三个 system LaunchDaemon；临时接管使用独立 `16379/16380/16381`，namespace 不能替代物理 PID 隔离。
- 如果上游 API 需要代理访问，先看 [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md)。
