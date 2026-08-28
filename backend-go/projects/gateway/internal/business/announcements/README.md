# `announcements` Business owner

本目录是公告 transaction group 的独立 Go store/service 原语。调用者传入
`Actor`，由本包执行 `OwnerGate`、公开已发布读取、已读去重、管理员 CRUD 和
revision CAS；本包不接入 Gateway main、不创建 schema，不调用 Node、IPC、queue
或外部服务。

`HTTPHandler` 是可选、独立挂载的 HTTP adapter：调用者必须注入 `Port` 和
`ActorResolver`，以 Node 的十个公告端点返回 `{ "data": ... }`。公开列表/详情可由
resolver 返回 `ErrUnauthenticated` 或零值 actor 匿名读取；已读和全部管理操作必须认证，管理操作还在
adapter 与 Service 两层要求 admin。未接线依赖或未分类错误一律返回 `503`；不实现
session touch、dedupe 或 operation-log。

已冻结语义：

- 公开列表/详情只允许 `published` 且 `published_at` 非空；列表默认最多 30 条，
  已读 marker 按 `(announcement_id, system_account_id)` 幂等写入。
- 管理列表默认 page 1/pageSize 50，最大 100，按 `updated_at,created_at,id`
  倒序使用 pageSize+1 progressive window；详情只返回编辑投影。
- 标题/正文 trim 后分别限制 120/5000 个 UTF-16 code unit；level/status 和
  JSON 字段未知值 fail-closed；空 PATCH 不产生 DML 或 revision。
- revision 是精确字符串 CAS。进入 `published` 刷新 `published_at` 并清理已读；
  `publish` 同样清理已读；`unpublish` 变为 `archived` 但保留发布时间；delete
  通过 `DeleteResult.Deleted` 明确表达未来 HTTP 204 判定。
- `AfterCommitPort` 只提供提交后的副作用 seam，事件仅携带公告身份/状态元数据，
  不含正文；副作用错误不会撤销已提交事务，也不宣称已投递。

`NewStore`/`NewService` 不执行 DDL；`CheckContract` 只读验证既有
`announcements`、`announcement_reads`、`system_accounts` relations。Postgres
模式会校验 schema identifier、限定表名并将 `?` 转换为 `$n`。

验证：

```text
gofmt -w internal/business/announcements/*.go
go test -race ./internal/business/announcements
go vet ./internal/business/announcements
git diff --check -- backend-go/projects/gateway/internal/business/announcements
```
