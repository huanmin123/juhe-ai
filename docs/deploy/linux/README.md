# Linux 部署目录

> 面向 Linux 服务器部署和运维。
> 这里记录 Linux 下发布包、Docker、systemd、防火墙、HTTPS 和代理访问的差异。跨平台通用流程仍以 [部署指南](../部署指南.md) 为准。

## 文档索引

- [Linux 部署指南](Linux部署指南.md)：Linux 裸机发布包、Docker、systemd、HTTPS、反向代理、防火墙和上游网络代理配置入口。
- [Linux 部署流程示例](Linux部署流程示例.md)：一次从上传发布包到 systemd 常驻和代理绑定的示例流程。

## 适用边界

- Linux 是生产服务器优先部署平台，推荐 `systemd` 或 Docker Compose。
- 公网 HTTPS 默认优先用 [Caddy 自动 HTTPS 部署指南](../https/Caddy自动HTTPS部署指南.md)。
- 长期运行需要状态检测和自动恢复，先看 [状态检测与自动恢复指南](../watchdog/状态检测与自动恢复指南.md)。
- Docker 高性能模式仍以 [高性能模式部署指南](../高性能模式部署指南.md) 为主；本文只说明 Linux 平台差异。
- 如果上游 API 需要代理访问，先看 [sing-box 网络代理部署指南](../proxy/sing-box网络代理部署指南.md)。
