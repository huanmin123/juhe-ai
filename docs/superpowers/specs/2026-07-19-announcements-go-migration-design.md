# 公告接口 Go 迁移设计

## 目标

在不提前接管其他 Node 模块和 AI Chat 的前提下，将公告公开读取、已读记录、管理端维护、页面数据失效和操作日志完整迁移到 Go opt-in 路径，为后续真实 listener smoke 和 owner 切换提供可验证边界。

## 现状与边界

Node 当前提供两张业务表：`announcements` 和 `announcement_reads`。公开接口按当前登录系统账户返回最多 30 条已发布公告并带 `readAt`；管理接口提供分页、详情、创建、更新、发布、下线和删除。已发布公告的新增、编辑、发布、下线和删除会发布 `announcements.public` 页面数据变化。

本批不迁移：公告前端页面删除、Node 路由删除、生产反向代理切流、AI Chat、其他未迁移后台 worker。Go 先以默认关闭的 management/public opt-in 方式提供完整接口，Node 继续作为默认 owner。

## Go 分层

- `internal/modules/announcements`：领域类型、服务、输入校验、公开/管理查询和状态转换。
- `internal/store/port`：不泄露 pgx/sqlc 类型的公告读写接口。
- `internal/store/postgres`：公告查询、事务写入、已读 upsert、稳定分页和 `announcement_reads` 清理。
- `internal/httpapi`：公开公告和管理公告 handler，复用现有 session、admin、限流、JSON body 上限、no-store 和错误脱敏边界。
- `internal/app`：统一装配、operation-log 写入口和 `accountpagedata`/Redis publisher 注入。

## 行为契约

- 公开列表只返回 `status='published' AND published_at IS NOT NULL`，排序为 `published_at DESC, created_at DESC, id DESC`，limit 默认 30、上限 30。
- 已读请求严格接受 `announcementIds` 数组，去空白、去重、最多 30 个；只写当前已发布公告，重复写入幂等并返回 `{ readAt, count }`。
- 管理列表默认 page 1、pageSize 50，pageSize 最大 100；使用 `updated_at DESC, created_at DESC, id DESC` 和 progressive pagination 上界，不做无界精确 count。
- 管理详情、写接口只允许 `admin` / `super_admin`，不存在返回 404 `公告不存在`。
- 标题 1-120、内容 1-5000；level 为 `critical|warning|info|normal`；status 为 `draft|published|archived`；未知字段拒绝。
- 从非 published 进入 published 时设置当前 `published_at` 并清理该公告已读记录；发布动作也清理已读记录。下线使用 archived，保留公告记录。
- 已发布公告内容/级别/标题更新发布 `upsert`；进入 published 发布 `upsert`；下线和删除发布 `delete`。草稿之间变更不发布公开域事件。
- 业务事务提交后再执行 operation log 与 page-data 发布；副作用失败不回滚已经提交的公告写入，由现有 page-data dirty recovery 记录和恢复。

## 数据与安全

新增 Goose migration 只描述当前 Go schema，不写运行时旧结构兼容。外键删除公告时级联已读记录。公开 DTO 不返回内部维护信息；管理 DTO 只返回 Node 当前摘要字段。公告正文不是凭据，但沿用 operation-log 当前原文/容量边界，不新增字段名脱敏逻辑。

## 验证

每个增量先写失败测试再实现。最终至少覆盖：新库 migration、公开列表排序/limit/已读幂等、权限/严格字段/状态转换/404、事务失败无半写、operation log 和 page-data 事件、目标包 race、Go 全量、真实 PostgreSQL/Redis/Asynq smoke、真实 Go listener Cookie smoke 和公告前端 API 回归。所有真实资源使用隔离数据库/Redis，结果不代表生产接管。
