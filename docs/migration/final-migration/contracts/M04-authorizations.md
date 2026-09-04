# M04 authorizations 契约规格（2026-09-04 子代理提取，Go 实现规格源）

> 完整报告由子代理生成并存会话；本文件为持久化摘要。实现以本文件 + Node 源码为准。

## 关键语义速查

- 单 router 双挂载：/authorizations（requireAdmin，query.systemAccountId 代操作必填于 POST）+ /my-authorizations（forceSelfAccessScope：清 systemAccountId、role 强制 user）。成功 {data,message}，错误 {message}。
- 三张表：resource_authorizations（运行时，rauth_）、resource_authorization_sources（来源）、resource_authorization_grants（管理面，路由 :id = grant.id，rauthgrant_）。
- 状态机：active|paused|expired|revoked|returned（grant+runtime 共用）；source: active|superseded|revoked。
- 版本：nextResourceAuthorizationVersion = current>=now ? current+1ms : now；乐观锁 WHERE id=? AND updated_at=?；409 {message:'授权配置已被其他操作更新，请刷新后重试', currentUpdatedAt}。
- effective source 优先级：active team source（team grant active 未过期）→ active；paused team → paused；active manual → manual；无 active source → terminal（revoked/returned，preserveExpired=true 时 expires 已过 → expired+authorization_expired）。
- return：仅 system_account 直授 + runtime 有 active manual source；grantee 本人；204 无 body；team source 不动。
- revoke：任何 grant；manual+team source 都 revoked；200 {id,status:'revoked',updatedAt}。
- 到期扫描：expires_at<=now 且 status IN(active,paused) → expired（batch 20，PG FOR UPDATE SKIP LOCKED）。
- expired→active：必须同时给新 expiresAt，否则 '到期授权恢复时请同时调整过期时间'；新 expiresAt 仍过去 → 直接 expired。
- 创建：admin 必须带 ?systemAccountId（'管理员新增授权时必须指定授权人'）；account+system_account 必须 targetGroupId（'授权 AI 账户给个人时必须选择目标分组'）；反向 '只有授权 AI 账户给个人时可以指定目标分组'；重复 active → '该资源已授权给该用户/团队，请勿重复授权'；team 成员>20 / active grants>20 上限；grantee=owner → '不能授权给资源所有者自己'；账户实例克隆 + group_accounts 绑定 + 默认分组缺失 → '目标用户缺少启用的默认分组…'。
- usage 端点读 juhe_stats 的 authorization_team/user_usage_range_windows + usage_scope_range_windows（J5 域）。
- 上限：team members 20 / team active grants 20 / sweep batch 20 / usage window 1001 / usage page 200。

## 实现顺序（本切片内）
① 三表 DDL+读列表/详情（keyword 前缀上界=codePoint+1，direction/sourceType/status 过滤）
② create 事务（grant upsert + per-member runtime upsert + source + effective source）
③ patch/revoke/expire sweep
④ return
⑤ usage 端点（接口先行，查询后接）
⑥ 操作日志/幂等/缓存失效钩子

## 待人工复核（依赖服务）
- provider_model_catalog 批量 upsert（依赖定价服务，属 C03）
- 账户实例命名去重需 accounts 域支持（M08）
- 小时额度窗口绑定（quota 域 G07/J-F）
