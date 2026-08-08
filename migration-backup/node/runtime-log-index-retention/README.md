# F1 Node 源码归档

本目录保存“运行日志索引与保留”在直接 Go 接管时的 Node 源码对照快照，用于遗漏排查和人工比较。`manifest.json` 的 `sourceSnapshotSha256` 校验的是这些归档字节，不代表某个更早 Git 提交的 blob。

## 恢复边界

可运行的 Node F1 owner 基线是提交 `ba0fc6aa5`，它仍包含完整的 importer、scheduler、retention、schema 与 Node 启动路径。恢复必须在专用回滚提交中整体采用该基线，并在恢复前停止 Go indexer。

不要把本归档目录的文件覆盖到 `ba0fc6aa5`：这些文件是直接切换期间的对照快照，包含该基线不存在的抽取文件和 `indexOwner` gate，叠加后不能保证可编译或单 owner。

恢复验证最低要求：在隔离 worktree 检出 `ba0fc6aa5`，运行 Node typecheck，并确认 `worker.ts` 重新注册 F1 importer 与 `background-jobs.ts` 重新注册 `runtime-log-index-maintenance`。恢复完成后，Go indexer 必须保持停止。
