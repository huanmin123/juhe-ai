# Codex Responses 双向协议防护验证报告（2026-07-28）

## 1. 结论

- 生产故障由双向缺口共同造成：错误 `item_*` 响应 identity 没有被实际改写，又被客户端带入下一轮请求；账户切换发生过，但污染在后续轮次持续存在。
- 修复后的请求侧覆盖解析态和最终出站两个检查点；响应侧可以在先前 reasoning 已提交后修复尚未暴露的新 identity，同时保持提交后禁止换号的流一致性边界。
- 性能门禁通过。检查为 O(n) 增量/单次扫描，量级远低于真实模型与网络延迟；原生 clean 请求不会固定增加一次 JSON 解析。

## 2. 范围

- 测试对象：Node 网关 Codex Responses 历史 sanitizer、SSE/JSON guard、strict/safe 策略和账户切换决策。
- 覆盖链路：原生 Buffer、OAuth 风格字符串、适配器重引入、reasoning 后 custom tool、identity 生命周期、JSON/SSE 性能。
- 不覆盖内容：生产部署、真实账户写操作、对模型物理身份的鉴定、上游自身 503/429/无流量超时修复。

## 3. 环境

| 项目 | 内容 |
| --- | --- |
| 日期 | 2026-07-28 |
| 机器 / 环境 | Windows 本地开发工作区；macOS 生产仅只读审计 |
| 运行模式 | Node 后端专项 regression / performance |
| 命令或入口 | `backend` package 中 Codex Responses 专项脚本 |
| 原始产物 | 本轮命令输出；未保存敏感 payload |

## 4. 性能结果

| 场景 | p95 / 内存结果 |
| --- | --- |
| 请求历史 100 items | 0.0035 ms |
| 请求历史 1000 dirty items | 0.0391 ms |
| 请求历史 10000 dirty items | 0.4294 ms |
| JSON safe repair clean 1000 items | 0.1397 ms |
| JSON safe repair dirty 1000 items | 0.6627 ms |
| JSON safe repair clean 10000 items | 2.4843 ms |
| JSON safe repair dirty 10000 items | 4.6986 ms |
| SSE safe repair clean 1000 identities | 1.0599 ms |
| SSE safe repair clean 10000 identities | 14.7209 ms，heap 约 4.63 MB |
| SSE 同一 identity 100000 delta | retained heap 约 14 KB |

以上数字是本机专项微基准，不代表生产端到端延迟。它们用于证明算法增长和预算；相较模型首 token、工具执行和网络耗时，新增开销不是本故障链路的主要延迟来源。

## 5. 行为验证

- 历史 `custom_tool_call.id=item_*` 在真实账户派发前被删除，`call_id` 和 input 保持不变，输入对象不被原地修改。
- 适配器在最终 outbound Buffer 或字符串 body 中重新引入坏 ID 时，第二检查点仍会删除。
- reasoning 事件先提交后，新出现的 `item_*` custom tool identity 在 added、input delta/done、output done 和 completed 中使用同一个 `ctc_*` 映射。
- identity 若曾在禁止修复状态下被观察，后续不能中途建立新映射。
- strict 只在语义提交前允许 retry/account switch；提交后只拦截当前流，避免混合两个账户的内容。

## 6. 边界

- `reasoning unavailable` 可能是客户端对隐藏/加密推理不可见的渲染，不能单独作为模型异常证据。可可靠检测的是 ID 契约、工具调用生命周期、事件阶段、模型字段/指纹和输出结构一致性。
- 默认安全修复只处理确定性、局部、语义不变的 R0 问题；未知事件、截断或无法证明的语义异常只观察，不自动换号。
- 生产发布后需要用目标用户、目标账户和真实 Codex 客户端复验；本轮没有自动部署。

## 7. 关联文档

- [实施计划](../plans/计划-20260728T071315383Z-CodexResponses双向协议防护修复.md)
- [问题记录](../bug/问题-0138-CodexResponses双向ID防护失效.md)
- [长期设计](../functions/Responses协议防火墙与历史会话自愈设计.md)

