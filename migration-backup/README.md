# 迁移源码归档区

本目录仅保存完成 Go 接管后退出活跃 Node 运行路径的完整功能源码。归档规则、目录结构、`manifest.json` 字段、恢复方式和禁止项见 [完整功能接管与 Node 归档迁移规则](../docs/migration/完整功能接管与Node归档迁移规则.md)。

当前已归档 F1“运行日志索引与保留”：[`node/runtime-log-index-retention`](node/runtime-log-index-retention)。其中不包含仍在 Node 运行的 JSONL 日志生产、grep 或只读查询。

归档文件必须保留原相对路径，不得包含密钥、环境文件、数据库、日志、依赖目录或构建产物。manifest schema 按归档批次声明：旧批次可使用 `files`/`sourceSnapshotSha256` 与嵌套 `rollback.mode`；Node J3b 及当前模板使用 `originalFiles`/`sha256` 与顶层 `cutoverCommit`、`rollbackCommit`。维护脚本必须按 schema 版本或字段形状路由，不能把两种格式混读。恢复前用 SHA-256 重算 `archivePath`，不匹配即停止。该哈希描述归档快照，不得误称为更早 Git 提交的 blob 哈希。

这里的代码不参与构建、测试、部署或运行时 import；恢复只能通过明确提交恢复整个功能 owner。若 manifest 指定 `rollback.mode: exact-git-baseline`，该 Git 基线是唯一可执行恢复源，归档目录仅用于对照，不得将其文件叠加覆盖到基线。
