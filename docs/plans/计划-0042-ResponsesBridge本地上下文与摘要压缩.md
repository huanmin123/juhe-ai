# PLAN-0042 Responses Bridge 本地上下文与摘要压缩

## 基本信息

- 编号：PLAN-0042
- 状态：已关闭
- 创建时间：2026-06-09
- 更新时间：2026-06-09
- 需求来源：用户对话
- 执行者：AI
- 关联模块：后端 / 存储 / 网关 / API Key / 审计 / 使用记录 / 后台任务 / 文档 / 验证

## 关闭说明

2026-06-09 方向调整：该计划关闭，不进入实现。原因是为了让 Responses -> Chat Completions 支持完整长会话，需要本地 `previous_response_id` 账本、payload 文件、SQLite 元数据、热缓存、dirty flush、后台清理、摘要 compact、错误恢复和审计 / 使用记录联动，复杂度和成本超过轻量中转边界。后续不再推进 Chat bridge 的完整 Responses 仿真；需要长会话、compact 或稳定上下文的场景优先使用原生 Responses 上游。

## 原需求目标（已关闭，不进入实现）

以下内容是该关闭计划的原始目标，用于解释为什么最终放弃，不代表当前实现承诺。

在 `chat_completions_bridge` 模式下补齐本地 `previous_response_id` 续链和内置摘要 compact，使 Responses 客户端可以在 Chat-only 上游上完成长会话：

- 本地保存短期可回放上下文，支持用 `previous_response_id` 还原 Chat `messages`。
- 完整 payload 落本地文件，SQLite 只保存会话索引、顺序关系、大小元数据和文件引用。
- 热路径优先命中进程内 hot session cache，SQLite 只做冷加载和 dirty session 批量落表。
- `/responses/compact`、`compaction_trigger` 和高水位自动压缩触发本地摘要 compact。
- 摘要调用使用当前 bridge 会话同一账号和同一模型，本期不做专用摘要模型、不开放用户配置和自定义摘要 prompt。

## 原范围边界（已关闭）

### 原计划包含（未执行）

- [ ] 新增 bridge context session / response index / turn payload 元数据表。
- [ ] 新增本地 payload 文件存储，支持普通 turn 和 `summary_snapshot`。
- [ ] 新增 hot session cache、dirty session 批量 flush 和可选 journal 恢复。
- [ ] bridge 模式收到本地 `previous_response_id` 时恢复历史 replay messages。
- [ ] 内置摘要 compact：显式 `/responses/compact`、`compaction_trigger` 和续链高水位触发。
- [ ] 摘要只压缩旧历史前缀，保留最近原始 turns、当前 input、未闭合 tool call 和有效 instructions。
- [ ] 摘要调用接入现有账号并发、错误处理、使用记录和审计 metadata。
- [ ] 后台 cleanup job 按 TTL、状态和 `storage_key` 小批次清理 payload 文件与墓碑。

### 原计划不包含

- 专用摘要模型配置、摘要模型池或跨账号摘要。
- 用户自定义摘要 prompt、任意 body patch 或脚本化压缩。
- 完整 Responses 服务端状态、conversation、文件、MCP、computer use 或后台任务仿真。
- Redis、Kafka、对象存储或外部分布式会话状态。
- 原生 Responses passthrough 的服务端会话托管；passthrough 仍交给上游处理。

## 关联文档

- 功能方案：`docs/functions/Responses转ChatCompletions账户适配方案.md`
- 压缩方案：`docs/functions/Responses上下文压缩落地方案.md`
- 存储说明：`docs/functions/SQLite存储说明.md`
- 架构总览：`docs/architecture/架构总览.md`
- 第一版 bridge 计划：`docs/plans/计划-0039-Responses转ChatCompletions账户适配.md`

## 原执行拆解（未执行）

| 步骤 | 任务 | 验收标准 |
| --- | --- | --- |
| 1 | 存储结构与 payload store | 表结构、索引、文件原子写入、有界读取和 cleanup 引用完整 |
| 2 | hot session cache 与批量落表 | 连续请求命中缓存，dirty session 短间隔批量写 SQLite |
| 3 | 本地 `previous_response_id` 续链 | 找到、过期、作用域不匹配、超限和 payload 缺失都有明确错误 |
| 4 | 内置摘要 compact | 同账号同模型摘要，生成 summary snapshot，后续续链从压缩窗口继续 |
| 5 | 使用记录与审计 | 摘要调用记录真实 usage，metadata 标记 `responses_bridge_summary_compact` |
| 6 | 后台清理 | TTL、超限和 compacted payload 文件按 `storage_key` 小批次清理 |
| 7 | 回归验证 | 转换、续链、摘要、流式失败、类型检查和大文件边界通过 |

## 原决策记录（已废止）

以下决策随本计划关闭而废止，不再作为当前开发依据。

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-09 | 摘要 compact 作为 bridge 内置能力默认启用 | Responses -> Chat bridge 要打通长会话，减少用户配置 | 不新增前端配置；实现需固定保守默认 |
| 2026-06-09 | 本期只用当前账号和当前模型摘要 | 权限、隐私、成本和账号边界最清楚 | 不做专用摘要模型；摘要成本计入同一调用链 |
| 2026-06-09 | 摘要写成 `summary_snapshot`，不改写旧 payload | 方便审计、恢复和后台清理 | 请求路径不删除大文件，cleanup job 后续处理 |
| 2026-06-09 | 摘要内容不写入 system / developer instructions | 避免把用户历史提升为高优先级指令 | replay 时作为普通历史摘要 message |

## 原验证计划（未执行）

| 类型 | 测试项 | 预期 |
| --- | --- | --- |
| 单元测试 | 本地 response index 解析 | 正确命中 session、sequence、作用域和 TTL |
| 单元测试 | replay messages 组装 | summary snapshot + 最近原始 turns + 当前 input 顺序正确 |
| 单元测试 | `/responses/compact` bridge | 返回新的本地 response id，后续可续链 |
| 单元测试 | `compaction_trigger` bridge | 触发摘要，不请求上游 `/responses/compact` |
| 单元测试 | 高水位自动摘要 | 最多摘要一次，摘要后仍超限返回明确错误 |
| 回归测试 | 流式响应持久化失败 | 不输出 `response.completed` / `response.incomplete` 伪成功 |
| 性能测试 | 大 payload 和长会话 | 判断大小先用元数据，不扫描 payload 目录，不全量读无关文件 |

## 风险与注意事项

- 摘要是有损压缩，可能丢失早期细节；必须保留最近原始 turns。
- 摘要调用会增加一次上游请求成本和延迟；需要在使用记录和审计中透明标记。
- SQLite 是单 writer，dirty session flush 必须短事务、小批次、可退避。
- 流式场景下持久化失败不能返回可续链成功状态，否则客户端会拿到无法继续的 response id。
