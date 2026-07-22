# PLAN-0087 GPT 服务档位与思考 Token 计费修复

## 基本信息

- 编号：PLAN-0087
- 状态：已完成
- 创建时间：2026-07-11
- 需求来源：计费链路审查与用户要求修复
- 执行者：AI / 维护者
- 关联模块：模型目录 / 网关 usage / 使用记录 / 额度 / 统计 / 文档 / 验证

## 需求目标

- GPT `priority`、`flex` 请求按实际服务档位价格结算，不再统一使用标准价格。
- GPT-5.6 Sol、Terra、Luna 使用完整 standard / priority / flex / 长上下文价格。
- 统一思考 token 口径，避免漏算或重复计费。

## 核心契约

- `ParsedUsage.outputTokens` 表示上游可计费输出总量，包含隐藏思考 token；`thinkingTokens` 只是输出中的可观测子集，成本公式不重复相加。
- OpenAI 与 Anthropic 的输出总量保持上游 usage 口径；Gemini 的 `candidatesTokenCount` 不含 `thoughtsTokenCount`，解析时合并为可计费输出总量。
- OpenAI JSON / SSE 从响应实际 `service_tier` 读取 `default|priority|flex`；成本按实际档位计算，缺失时使用标准价。
- Priority / Flex 只使用模型目录的精确档位价格；模型声明支持档位但缺少对应价格时，成本保持未定价，不允许按标准价倍率猜测。
- 超过模型长上下文阈值时，输入、缓存和输出分别应用目录声明的倍率。
- Priority 与 Flex 平级且只精确匹配，不互相降级。
- 系统设置不提供 Priority/Flex 通用倍率；价格由模型目录统一维护，避免配置倍率与真实上游价格漂移。

## 范围边界

- 本次不新增用户倍率、充值或支付系统。
- 不把思考 token 再单独加到已包含它的 OpenAI / Anthropic 输出 token。
- 不为能力未知的模型猜测服务档位价格。
- 不保留旧计费口径兼容分支。

## 执行拆解

- [x] 扩展模型价格类型和 GPT-5.6 档位价格。
- [x] 服务档位进入 usage 解析和成本计算。
- [x] 修正 Gemini 思考 token 输出归一化。
- [x] 增加 standard / priority / flex / 长上下文 / reasoning 回归。
- [x] 更新模型目录、计费与测试文档。
- [x] 补全价格源明确声明的 Flex 模型能力，并拆分模型目录服务等级 / 思考级别 Tag 列。
- [x] 类型检查、构建和生产验证。
- [x] 删除 Node / Go / 前端通用倍率设置契约，并通过新 migration 清理线上设置行。

## 2026-07-14 口径收敛

- OAuth 与 bridge 不再静默删除客户端合法的 `service_tier` 和 `reasoning` 字段；只在目标协议无法等价表达时按协议边界明确拒绝。
- Priority / Flex 缺少精确目录价格时返回未定价，禁止使用系统倍率或默认倍率生成成本快照。
- 历史 migration `000034` 保留为发布历史；`000043` 删除现行两项倍率设置，Node、Go 和前端白名单同步移除。

## 验收标准

- 同一 GPT-5.6 用量在 priority 下高于 standard，在 flex 下低于 standard，金额匹配目录专用价格。
- 超过 272K 输入时输入/缓存与输出倍率正确。
- OpenAI reasoning token 不重复计费，Gemini thoughts 不漏计。
- 实际服务档位可进入使用记录计费输入和成本明细。
