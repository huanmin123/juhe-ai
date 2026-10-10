# BUG-0303 退出登录时 sessionStorage 访问异常阻断本地注销

## 基本信息

- 状态：已修复（2026-10-10，待发布验证——生效需随下次前端重建发布）
- 严重程度：P3（有条件的前端状态不一致）
- 发现时间：2026-10-09
- 模块：前端 / 认证 / 聊天本地状态
- 关联：[全面审查复核与残留问题报告](../reports/全面审查复核与残留问题报告-2026-10-09.md)

## 现象与范围

浏览器策略禁用存储、读取 `window.sessionStorage` 的 getter 抛出 `SecurityError` 时，用户点击退出，服务端注销已成功，但前端仍保留 `currentUser`、提示退出失败并留在当前受保护页面。

这是本地 UI 与服务端会话状态不一致，不是服务端仍允许旧会话访问。刷新或后续 401 可能清理本地状态，本轮没有在真实浏览器中验证具体恢复时机。

## 根因与调用链

1. `frontend/src/composables/useAuth.ts:64–72` 先等待 `api.auth.logout()`，再等待 `clearCurrentAccountChatState()`，最后调用 `clearAuthState()`。
2. 清理函数第 82 行直接求值 `window.sessionStorage`，然后传给 `clearChatPendingSubmission`。后者内部 try/catch 只保护 `removeItem`，不能捕获发生在实参求值阶段的 getter 异常。
3. 异常使 logout 提前 reject，本地 `clearAuthState` 未执行。
4. `frontend/src/layouts/AppLayout.vue:397–407` 捕获异常时仍看到 `currentUser`，只提示错误，不跳转登录页。
5. 后端 `internal/authsys/routes.go:155` 的正常注销路径已经撤销 token 并清 cookie。不能把该前端失败解释为后端注销失败。

`getDefaultChatLocalCache().clearAccount` 经 `ChatLocalCache.call` 将 IndexedDB 错误转换为结果；本问题不泛化为所有缓存清理失败。

## 本轮验证

用 Node VM 在内存中回放实际 `useAuth.ts` 的相关函数，Mock API 成功、聊天依赖正常，将 `window.sessionStorage` getter 设为抛 `SecurityError`。不写入仓库测试、不使用真实凭据。实际结果：

```json
{"serverLogout":true,"currentUser":{"id":"u1"},"cleared":false,"error":"SecurityError: session storage disabled","result":"cleanup-rejected-before-clearAuthState"}
```

现有 `useAuth.test.ts` 的 logout 用例只覆盖清理成功。此实验验证函数控制流，不等同于真实浏览器策略端到端验证。

## 待修复与验收

服务端成功注销后，本地认证态必须完成注销，同时保留清理错误的可观察性。修复应保留当前 authStateVersion 的并发身份保护：旧 logout 完成不得清掉期间已登录的新用户。

测试覆盖正常注销、sessionStorage getter 失败、服务端注销失败、注销期间切换用户。不要用静默吞掉所有清理错误的方式让测试通过。实施记录见下节。

## 修复记录（2026-10-10）

- 实施：`useAuth.ts` 的 `logout()` 对 `await clearCurrentAccountChatState(systemAccountId)` 包 try/catch——清理失败（含 sessionStorage getter 在实参求值阶段抛出的 `SecurityError`）经 `console.error` 保留可观察性后继续执行后续版本守卫与 `clearAuthState()`，服务端注销成功后本地认证态必清。`authStateVersion` 并发身份保护保持恰 2 处守卫、`clearAuthState()` 仍在 `api.auth.logout()` 之后的既有顺序；静态回归脚本 `auth-transient-session-regression.ts`（锁定函数结构与 AppLayout 分支）保持绿。`AppLayout.vue` 与 `chatPendingSubmissionStorage.ts` 未改；getter 异常根因点（原 82 行）保留原状，由 logout 边界 catch 承接，未把清理错误静默化。
- 测试：`useAuth.test.ts` 新增三用例——getter 抛 `SecurityError`（`Object.defineProperty` 替换并保存/恢复原描述符）→ `logout()` resolves、本地态清空、`console.error` 收到原 error；服务端注销失败 → rejects 且登录态保留；注销期间切换用户 → 新身份保留且聊天清理未被调用（第一处版本守卫提前 return）。连同既有正常注销用例覆盖档案要求的四场景。
- 验证：前端全量单测 960/960 绿；`test:auth-transient-session` 静态回归绿；独立复审确认三用例断言真实依赖修复行为（清理 mock 未调用、原描述符恢复）。
