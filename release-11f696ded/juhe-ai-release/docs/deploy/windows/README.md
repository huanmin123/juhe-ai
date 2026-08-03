# Windows 部署目录

> 面向 Windows Server 或 Windows 桌面部署。
> 这里记录 PowerShell、Windows 服务、Docker Desktop、HTTPS 和本机代理的差异。跨平台通用流程仍以 [部署指南](../部署指南.md) 为准。

## 文档索引

- [Windows 部署指南](Windows部署指南.md)：Windows 发布包、PowerShell、服务化、Docker Desktop、HTTPS 和代理配置入口。
- [Windows 部署流程示例](Windows部署流程示例.md)：一次从 zip 发布包到后台代理绑定的示例流程。

## 适用边界

- Windows 示例默认使用 PowerShell 7。
- Docker 部署依赖 Docker Desktop 或等价容器环境。
- 公网 HTTPS 默认优先用 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md)。
- 长期运行默认由 Windows Service / NSSM 负责常驻，外部探针只告警；确有无人值守自动恢复需求时再看 [状态检测与自动恢复指南](../watchdog/状态检测与自动恢复指南.md)。
- 如果上游 API 需要代理访问，先看 [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md)。
