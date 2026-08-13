# F4 操作日志 Node 源码归档

此目录保存 F4 Go 接管前退出活跃 Node 路径的队列、IPC、Redis、SQLite/PostgreSQL repository、保留清理和专项回归源码快照。它不参与构建、测试、发布或运行时 import。

当前状态是 `pre_cutover_source_archive`：Go 实现、Node direct RPC adapter 与本地双模式验证已经完成，但还没有生产 candidate 切流提交或回滚提交。因此不得把本目录或 `manifest.json` 当作 F4 已完成 L3/L4 生产接管的证据。

恢复只能在明确的回滚提交中恢复整个 F4 Node owner；不得从这里挑选单个文件或函数重新接入运行链路。逐文件的原路径、归档路径和 SHA-256 见 `manifest.json`。
