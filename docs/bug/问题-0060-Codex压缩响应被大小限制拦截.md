# BUG-0060 Codex 压缩响应被大小限制拦截

## 现象

新版 Codex 在普通 `/responses` 请求中加入 `compaction_trigger` 后，客户端长时间停留在自动压缩或思考状态。服务端最终可能记录 HTTP 200，但请求耗时达到十几分钟。

## 触发条件

- 请求被识别为 Codex Responses Remote Compaction V2。
- 上游在单个 `response.output_item.done` SSE 事件中返回较大的 `encrypted_content`。
- 单事件超过响应检查缓冲区的通用 `256 KB` 解析上限，或压缩完成前累计暂存超过 `1 MB`。

## 根因

响应检查缓冲区先应用普通 SSE 单事件解析上限，再执行 Codex 压缩契约检查。生产合法压缩事件实际达到约 `487-496 KB`，被误判为 `codex_compaction_contract_mismatch`，随后触发服务端换账户和重试。

## 修复

- Codex 压缩契约检查不再因单个 SSE 事件大小拒绝响应。
- 移除 Codex 压缩完成前的累计暂存大小拒绝。
- 保留请求体硬上限和对应 `413`。
- 保留压缩输出数量、结构及 `response.completed` 完整性检查。
- 普通超大 SSE 仍采用停止深度解析并原样透传，不因大小拒绝客户端。

## 验证

- 新增约 `1.2 MB` 的合法 compaction 单事件回归，确认收到 `response.completed` 后完整释放。
- 响应检查策略、Codex 客户端策略、OpenAI API Key 透传、请求体硬上限和 TypeScript 类型检查通过。
- 响应检查网关 E2E 的业务流程执行完成，但 Windows 清理临时 SQLite 文件时存在既有 `EBUSY`，需单独修复测试清理逻辑。
- 生产 release `20260712-0103-codex-compaction-f55cf6274` 已通过临时服务接管、主服务直连检查、Nginx `main` 回源和 30 秒稳定门禁。
- 生产保持 `Caddy -> Nginx:3099 -> 3001/3002`，上线后目标 release 日志未再出现 `codex_compaction_contract_mismatch`。

## 关联 bug

- `BUG-0032`：compact 摘要子请求被误改写为流式。
- `BUG-0045`：服务端与客户端重试预算叠加。
