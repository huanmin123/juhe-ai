# AI 长会话精简 Artifact 与退避设计

## 目标

降低 50 轮真实验收中 artifact checkpoint 随累计需求增长导致的长流连接失败，同时保持 50 项评分、15 个 checkpoint 和最多 3 次尝试不变。

## Artifact 合同

- artifact checkpoint 仍返回完整可运行 HTML，不降低 requirements、anchors、forbidden 或 continuity 评分。
- 输出只能包含一个 `html` fenced code block；禁止解释文字、HTML/CSS 注释、重复示例和冗余内容。
- artifact UTF-8 上限统一为 `32 KiB`。fixture perfect artifact 必须低于该值；超限样例必须失败。
- runner 在收到 completed artifact 后、进入评分和账本推进前检查实际字节数。超限转换为稳定 deterministic quality failure，不按连接异常重试。
- manifest 合同保持现有严格 JSON 与摘要上限。
- 不向上游请求注入未经现有合同确认的 `max_output_tokens`。

## 重试合同

- transient 总尝试数保持 3 次。
- 第 2 次尝试前等待 2 秒，第 3 次尝试前等待 5 秒。
- 等待由调用方注入，并复用真实 runner 的 `ChatLongSessionRunBudget.sleep`，因此总 deadline 或中断可以终止 backoff。
- attempt metric 新增 `delayMs`；首轮为 0，后续分别为 2000、5000。不得记录错误正文或其他新敏感数据。

## 验证

- fixture prompt 固定紧凑输出属性和 `32 KiB` 文案。
- perfect artifact 全部在上限内且完整评分为 1。
- oversize artifact 被 deterministic quality gate 拒绝。
- transient 恢复、retry exhausted 都验证 delay 序列和总尝试数 3；deterministic 首次失败不等待。
- local preflight、typecheck、build、diff-check 和独立 review 通过后，才允许下一次真实网络验收。
