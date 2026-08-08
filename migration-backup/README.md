# 迁移源码归档区

本目录仅保存完成 Go 接管后退出活跃 Node 运行路径的完整功能源码。归档规则、目录结构、`manifest.json` 字段、恢复方式和禁止项见 [完整功能接管与 Node 归档迁移规则](../docs/migration/完整功能接管与Node归档迁移规则.md)。

当前已归档 F1“运行日志索引与保留”：[`node/runtime-log-index-retention`](node/runtime-log-index-retention)。其中不包含仍在 Node 运行的 JSONL 日志生产、grep 或只读查询。

归档文件必须保留原相对路径，不得包含密钥、环境文件、数据库、日志、依赖目录或构建产物。每个 `manifest.json` 的 `files` 必须逐项列出 `originalPath`、`archivePath` 和归档时源码快照的 `sourceSnapshotSha256`；恢复前用 SHA-256 重算 `archivePath`，不匹配即停止。该哈希描述归档快照，不得误称为更早 Git 提交的 blob 哈希。

这里的代码不参与构建、测试、部署或运行时 import；恢复只能通过明确提交恢复整个功能 owner。
