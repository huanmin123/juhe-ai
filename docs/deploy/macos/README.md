# macOS 部署目录

> 面向 macOS 本机或小型服务器部署。
> 这里记录 macOS 发布包、launchd、Docker Desktop、HTTPS 和本机代理的差异。跨平台通用流程仍以 [部署指南](../部署指南.md) 为准。

## 文档索引

- [macOS 部署指南](macOS部署指南.md)：macOS 发布包、launchd、Docker Desktop、HTTPS、反向代理和代理配置入口。
- [macOS 部署流程示例](macOS部署流程示例.md)：一次从 tar 包到 launchd 和代理绑定的示例流程。
- [macOS 运维脚本](operations/README.md)：launchd 主服务、sing-box、只读诊断和临时接管回滚门禁。

## 适用边界

- macOS 适合个人或小团队轻量部署，不建议作为高并发生产主机。
- Docker 部署依赖 Docker Desktop 或等价容器环境。
- 公网 HTTPS 默认优先用 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md)。
- 长期运行需要状态检测和自动恢复，先看 [状态检测与自动恢复指南](../watchdog/状态检测与自动恢复指南.md)。
- 如果上游 API 需要代理访问，先看 [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md)。
