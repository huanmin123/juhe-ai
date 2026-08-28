# Gateway system accounts

该包是 Gateway Business 的系统账户管理事务原语，当前只覆盖 `List`、`Options`、`Create` 和按 `updated_at` 的 `PatchCAS`。它不接 HTTP、main、Node、IPC、队列、缓存或 schema DDL。

所有写操作要求 `OwnerGate{Confirmed, SchemaReady, NodeWriterStopped}` 三项均为真。SQLite 与 PostgreSQL 共用业务规则；PostgreSQL 使用 `juhe_business` 表限定符、`$n` 占位符、行锁和事务级 advisory lock，SQLite 依赖单写事务。缺少既有关系、默认资源表或加密 capability 时失败关闭，不伪造成功。

`Create` 只接受已生成的 `PasswordHash`，并在同一事务内创建账户、8 个默认分组、7 个非 hybrid 默认普通路由、路由分组绑定、7 个默认 API Key 和 1 个 chat API Key。完整 API Key 仅经显式 `SecretCipher` 加密后落库；该接口接收原始生成密钥，具体 Node-compatible envelope 由注入适配器负责。响应模型不携带密码哈希或密钥明文。

`PatchCAS` 支持资料、角色、状态、改密、图像权限、AI 账户限制和请求限制 JSON 的组合修改。密码实际变更或状态转为停用时删除目标账户全部会话；角色 / 状态变更在事务级锁下检查并保持至少一个启用 `super_admin`。
