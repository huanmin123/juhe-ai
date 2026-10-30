# Changelog

本文件自 v1.0.0 起维护（《升级兼容与迁移账本设计》§16 发布契约）。每个发布版本固定四节：`Breaking` / `Upgrade notes` / `Rollback` / `Pre-1.0 behavior notes`；无内容的节写「无」。

- `Upgrade notes` 必须回答：是否需要停写、是否需要备份、`maintenance upgrade` 会自动执行哪些步骤、是否有必须手工执行的独立命令。
- `Rollback` 二值：`binary-only`（换回上一版二进制即可）或 `backup-required`（只能恢复升级前备份）。
- `Pre-1.0 behavior notes` 承载首个 tag 之前历史批次的可见行为变更披露与核验 SQL（自 `docs/deploy/部署指南.md` §4 流水账迁入）。

## Unreleased

### Breaking

无

### Upgrade notes

（发布时填写）

### Rollback

（发布时填写：binary-only / backup-required）

### Pre-1.0 behavior notes

（历史批次披露迁入处；首个正式 tag 发布时从部署指南 §4 收敛）
