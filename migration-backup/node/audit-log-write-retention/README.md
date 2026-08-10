# Node 审计写入与保留基线归档

这里保存 F3 接管前 Node 审计 writer、队列、transport、SQLite/PG retention、热搜索和关联回归脚本的原始基线。

这些文件仅用于迁移对照、回滚取证和行为差异分析，不进入 `backend/tsconfig.json`，不作为运行时或测试默认入口，也不代表 Node 仍拥有审计写入。F3 完成后，Node 只负责生成一次性 loopback 输入并通过独立 read-only adapter 提供管理查询；Go 是持久化、热搜索和 retention owner。

归档不对应单一 Git 来源基线；`779ab71fc` 仅是 F3 输入接线前的整体回滚基线。实际归档内容以同目录 `manifest.json` 中每个文件的路径和 SHA-256 为唯一依据，必须逐项复算。
