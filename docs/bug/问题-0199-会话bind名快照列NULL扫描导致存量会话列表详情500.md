# BUG-0199：会话 bind 名快照列 NULL 扫描导致存量会话列表/详情 500

## 现象

2026-09-27 会话绑定模式（`chat_conversations` 五列 bind 快照）上线后验收实测发现：`GET /__aisys__/api/my-chat/conversations`（会话列表）与 `GET /conversations/{id}`（详情）对**含存量会话**的账户稳定返回 500 `internal_generation_failed`（「生成任务异常结束，请重新发送」），新建会话的账户不受影响。逐行定位：`?limit=1/2/3` 返回 200、`limit=4`（触达存量行）返回 500，单条 GET 存量行同样 500——行级读取失败。

## 根因

五列 bind 快照经 `maintenance --ensure-schema` 幂等加列交付：仅 `bind_mode` 是 `NOT NULL DEFAULT 'api_key'`，`bind_group_id / bind_group_name_snapshot / bind_account_id / bind_account_name_snapshot` 四列为**可空且无默认**。存量行（加列前创建）的后四列为 NULL；而 `conversationRow` 扫描结构体只给 id 列配了 `sql.NullString`，`bind_group_name_snapshot / bind_account_name_snapshot` 两个声明为普通 `string`——PostgreSQL 驱动扫描 NULL 到 `string` 直接报错（`converting NULL to string is unsupported`），查询即败。

新建会话不受影响：创建路径对未选绑定对象写入的是空串 `''` 而非 NULL；SQLite 测试 fixture 同样只造空串行，因此包测试全绿、上线前未暴露。

影响面：任何拥有"五列交付前创建的会话"的用户，其会话列表/详情整体不可用（分页扫描到该行即全页失败）。上线当日生产 4 行会话中 1 行存量行受影响（1 个用户）。

## 修复

`backend-go/projects/gateway/internal/chat/conversations.go`：

- `conversationRow.bindGroupNameSnapshot / bindAccountNameSnapshot` 改为 `sql.NullString`（与 `bindGroupID / bindAccountID` 同款）；
- `mapConversation` 取 `.String`（NULL 归一为空串，`Conversation` 投影字段仍为 `string` + `omitempty`，JSON 形状不变：旧会话不输出 `bindGroupName / bindAccountName`）。

回归测试：`chat_store_test.go` 新增 `TestListAndGetTolerateLegacyNullBindNameSnapshots`——存量 NULL 行 Get/List 必须成功且快照读为空串。已验证该测试在未修复状态下 FAIL、修复后 PASS。

## 验证

- `go test ./internal/chat/ -count=1` 全绿；`go build ./...` 通过。
- 生产验收：修复发布后，含存量行的账户会话列表/详情恢复 200，三种绑定模式会话创建与流式对话不受影响（同日验收记录见交付说明）。
