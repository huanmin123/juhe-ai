# 问题-0146：macOS 网络切换导致 WireGuard 假活与 root wrapper 权限缺陷

状态：已实现，待生产验证

## 现象与影响

macOS 网络配置发生变化后，8 条 WireGuard system job 仍显示运行，但真实 `utun` 映射的握手停止。公网 Edge 到回源端的 TCP/TLS 连接超时，业务入口不可达；本机 Node、Caddy、Nginx 和数据库仍可正常响应本地检查。

旧配置中部分 root LaunchDaemon 直接执行服务用户可写发布目录中的 WireGuard wrapper，root 进程可能执行被服务用户替换的脚本。

## 根因边界

已确认的故障边界是 macOS 网络变化后 WireGuard 数据面与 launchd `running` 状态脱节，不是已证实的整机重启，也不是应用服务重启或客户端主动断开。仅凭公网 HTTP 失败无法继续细分，因为它还可能来自 DNS、Edge、证书、Caddy 或 Node。

## 修复

- 新增 root-only WireGuard wrapper/config 迁移器，私有 manifest 按 SHA-256、精确 plist `ProgramArguments` 和 8 条 allowlist 校验后，将来源配置原子复制到 `/usr/local/libexec/juhe-ai/wireguard-config/<逻辑接口>.conf`，目录为 `root:wheel 0700`、配置为 `root:wheel 0600`，并在 root-only libexec 固定生成 wrapper。`/usr/local/etc/wireguard` 仅为迁移输入，不修改其父目录所有权。来源 wrapper 只保留哈希、精确 plist 绑定和回滚元数据，允许兼容旧两参数生命周期实现及其服务用户可写父目录，但绝不复制或执行；配置含 WireGuard shell hook 仍拒绝。固定 wrapper 不读取 `WG_*` 覆盖，完成 down/up、ready 等待、监控和退出清理；bootstrap 失败回滚已修改 job。
- 新增单次 root WireGuard reconciler 和安装器。恢复仅接受全 Edge 双次且不跨过样本过期间隔的陈旧、稳定网络、sleep/wake 宽限结束、锁与预算允许、203 probe 非未知的组合条件；canary 在确认旧映射已清理后重新观察映射、连续握手、传输增量和独立 TLS nonce probe 成功。canary 后任一 Edge 失败立即停止。macOS 可以复用 `utun` 编号，因此不以编号变化判定成功。
- 203 collector 仍只监听回环；adapter 使用 root-only 专用 SSH key、固定 `known_hosts` 和受限 forced-command 读取其只读文本，不开放 HTTP 指标。私有 mapping 必须与 8 条 runtime manifest Edge 精确一致，并为每条 Edge 固定唯一的 `node + public_ip` 指标序列；不可读、重复、陈旧或非唯一样本统一归为 unknown。
- 移除恢复器只卸载恢复器本身，保留 root 化后的 WireGuard job 与本地审计状态，避免回退到用户可写 wrapper。

## 验证与剩余风险

仓库验证覆盖 shell 语法、dry-run、私有值泄露门禁、SSH probe mapping 的缺失/重复/额外记录和非 canary 失败停批约束，以及固定 wrapper 的真实 down/up、ready、运行期退出清理、`WG_*` 覆盖无效、up 失败和 ready 超时路径。真实生产仍须先对私有 manifest、203 adapter、8 个 plist、配置所有权、固定 wrapper 语法、203 SSH key/host fingerprint/forced-command 和 release lock 做预检，再进行受控 apply 与逐 Edge TLS nonce 验收。

共享 Wi-Fi/WAN 仍是物理单点：软件只能在网络恢复后受控重建 WireGuard，不能修复上游网络本身；应单独推进有线链路和固定 DHCP 租约。
