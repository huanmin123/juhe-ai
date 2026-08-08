# Node 完整功能归档

每个已完成 F4 的功能在本目录创建 `<feature-id>/`，并从 [`manifest.template.json`](manifest.template.json) 复制出该功能的 `manifest.json`。

归档前必须先完成 Go 唯一 owner 验收和 Node 活跃路径清零；归档后文件只用于对照与整功能回滚，不能被构建、测试、部署或运行时加载。规则见[完整功能接管与 Node 归档迁移规则](../../docs/migration/完整功能接管与Node归档迁移规则.md)。
