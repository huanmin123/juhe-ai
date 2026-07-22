# GPT-5.6 模型检测现状与样本审计报告（2026-07-13）

## 1. 结论

- 当前项目已经具备协议一致性、响应模型字段、通用行为题、长上下文、稳定性、辅助模型对照和可选可信账户对照，不是只有“你是什么模型”这类弱检测。
- 当前项目还不能可靠识别“上游把 Sol / Terra / Luna 都映射到同一个模型，同时伪造正确 `model` 字段”的高级替换。现有行为题主要是通用能力检查，不是经过样本校准的 GPT-5.6 子版本指纹。
- 当前项目没有实现 Token padding 检测。`usage_shape` 只检查 usage 数字字段是否存在；本地粗略 Token 估算只在流式 usage 缺失时兜底，没有比较 reported / local delta、比例、斜率或异常阈值。
- 生产模型检测历史只有 `16` 次，且 GPT-5.6 完成样本主要集中在 Sol，Luna 没有完成样本，不能用来训练或验证 Sol / Terra / Luna 指纹。
- 生产用量聚合有大量自然流量，但这些请求受提示词、工具、缓存、参数、路由和客户端影响，没有真实模型标签，不能直接作为模型身份基线。
- 最合理的增强方向是：受控隐藏探针 observation、同账号跨模型塌缩检测、按独立上游桶构建抗污染群体基线，以及差分 Token 斜率探针。无官方直连样本时只能输出可信度和异常证据，不能输出“已证明是假模型”。

## 2. 审计范围与环境

| 项目 | 内容 |
| --- | --- |
| 审计日期 | 2026-07-13 |
| 本地代码 | `F:\sub2api-lite` 当前工作区，审计开始时工作树干净 |
| 生产资料 | `F:\服务部署\juhe-ai` 中的线上文档和既有连接资料 |
| 生产主机 | 线上 macOS 主机，只读检查 |
| 当前 release | `/Users/huanmin/juhe-ai-lite/releases/20260713-1312-upstream-capability-ac5f8587c/juhe-ai-release` |
| 生产进程 | launchd 运行中；本次未重启、未写库、未发起额外真实模型探针 |
| 原始产物 | 无；本次为只读代码、文档、聚合 SQL 和回归输出审计 |

不覆盖：

- 没有官方 OpenAI 直连 Sol / Terra / Luna 对照账户。
- 没有向生产账号追加高成本长上下文或多轮行为探针。
- 没有用普通用户请求正文训练或反推模型身份。
- 没有核对上游实际账单，因此本报告不能证明生产账号发生 Token 灌水。

## 3. 当前实现能力

### 3.1 官方模型差异

2026-07-13 的 OpenAI 官方模型页显示：

| 模型 | 定位 | Context | Max input | Max output | Knowledge cutoff |
| --- | --- | ---: | ---: | ---: | --- |
| [`gpt-5.6-sol`](https://developers.openai.com/api/docs/models/gpt-5.6-sol) | frontier / 复杂专业工作 | 1,050,000 | 922,000 | 128,000 | 2026-02-16 |
| [`gpt-5.6-terra`](https://developers.openai.com/api/docs/models/gpt-5.6-terra) | 平衡智能与成本 | 1,050,000 | 922,000 | 128,000 | 2026-02-16 |
| [`gpt-5.6-luna`](https://developers.openai.com/api/docs/models/gpt-5.6-luna) | 成本敏感 / 高吞吐 | 1,050,000 | 922,000 | 128,000 | 2026-02-16 |
| [`gpt-5.5`](https://developers.openai.com/api/docs/models/gpt-5.5) | 上一代复杂专业工作模型 | 1,050,000 | 官方页未单列 | 128,000 | 2025-12-01 |

Sol / Terra / Luna 的公开端点和核心功能也相同，区别主要是质量 / 成本定位。因此端点、上下文和知识截止日期只能验证系列硬边界，不能区分三个 5.6 子版本；知识时间窗最多是低权重特征。

本地模型目录当前仍为三个 5.6 保存 `max_input_tokens = 372000`，对应回归也固定断言 `372000`。这与最新官方模型页存在漂移，必须在实现动态长上下文探针前先修正；本次文档审计没有修改运行代码。

### 3.2 已有检测项

当前强诊断由 `backend/src/modules/model-checks/model-checks.service.ts` 组织，包含：

- 基础非流式响应。
- SSE 流式响应。
- 结构化输出。
- 工具调用。
- usage 字段形态。
- 8 类通用行为约束。
- `8k / 20k / 60k` 长上下文。
- 3 轮稳定性。
- 辅助模型对照。
- 用户显式选择可信账户时的分布相似度对照。

GPT 首批配对关系由 `backend/src/modules/model-checks/model-checks.profiles.ts` 定义：

| 目标模型 | paired model |
| --- | --- |
| `gpt-5.6-sol` | `gpt-5.6-terra` |
| `gpt-5.6-terra` | `gpt-5.6-sol` |
| `gpt-5.6-luna` | `gpt-5.6-terra` |
| `gpt-5.5` | `gpt-5.4` |

这些能力可以侦查：

- 响应对象直接声明了不同模型。
- Responses / Chat / Messages / Gemini 协议字段明显不符。
- 工具、结构化输出、SSE 或 usage 形态明显伪造或缺失。
- 通用能力和约束题大面积失败。
- 长上下文在较高窗口稳定截断或失败。
- 可信对比开启时，目标账号与用户选择的对比账号分布明显不同。

### 3.3 当前不能可靠侦查的情况

- 中转站把请求模型映射到低版本，同时把响应 `model` 字段改回请求值。
- Sol、Terra、Luna 都由同一个模型生成，但上游针对当前固定通用探针保持正确格式。
- 5.5 映射到 5.4，而两者在当前通用题上的输出都能通过 deterministic checker。
- 轻微 Token 灌水，尤其是 5%-15% 比例放大、固定增加或按固定区间取整。
- 只有套餐权益差异的能力。Pro / Max / Ultra、思考档位、多智能体、service tier 等不能作为模型身份强证据。

### 3.4 模型映射语义

账号配置中的显式模型映射会在检测前解析，报告保留 requested / upstream / mapping 字段。这意味着：

- 请求 Sol、配置明确映射 Luna，是透明路由事实，不应描述为系统“侦查到欺诈”。
- 请求 Sol、没有配置映射、响应字段返回 Luna，属于硬冲突。
- 请求 Sol、没有配置映射、响应仍写 Sol，但行为接近 Luna，当前只能形成弱异常，尚无模型专属基线支持稳定结论。

## 4. 生产样本快照

以下数据来自 2026-07-13 只读聚合查询，只代表该时点快照。

### 4.1 模型检测历史

`model_check_runs` 总数为 `16`：

| 模型 | 检测次数 | 已完成 |
| --- | ---: | ---: |
| `gpt-5.6-sol` | 11 | 8 |
| `gpt-5.6-terra` | 3 | 1 |
| `gpt-5.6-luna` | 1 | 0 |
| 其他模型 | 1 | 未单独统计 |

已完成的 5.6 检测没有发现响应模型字段不匹配或账号模型映射命中，但该样本量和模型分布远不足以验证子版本指纹准确率。

### 4.2 自然流量模型聚合

2026-07-12 至 2026-07-13 的预聚合 `usage_model_daily` 包含：

| 模型 | 聚合请求数 |
| --- | ---: |
| `gpt-5.6-sol` | 171,240 |
| `gpt-5.6-terra` | 6,160 |
| `gpt-5.6-luna` | 1,694 |
| `gpt-5.5` | 100,832 |

这些数量说明项目拥有足够大的真实调用面，可以安排分层受控探针，但不能把自然流量数量或自然输出直接当作真假标签。Sol 样本量远高于 Terra / Luna，如果按账号或请求简单多数投票，基线会严重偏斜。

### 4.3 显式映射事实

审计聚合中可见的代表性映射包括：

| 下游请求模型 | 实际上游模型 | 次数 |
| --- | --- | ---: |
| `gpt-5.5` | `gpt-5.6-sol` | 1,635 |
| `gpt-5.5` | `gpt-5.4` | 9 |

这些字段来自已记录的映射 / 路由事实，只能证明项目实际发生了什么配置后模型改写，不能证明实际上游物理模型与字段一致或不一致。

## 5. 回归验证现状

本次执行结果：

| 命令 | 结果 | 说明 |
| --- | --- | --- |
| `pnpm test:model-check-protocol-profiles` | 通过 | 协议 profile 静态回归正常 |
| `pnpm test:model-check-full-profile` | 失败 | 在执行真实探针前无法选出可调度 fixture 账号 |
| `pnpm test:model-check-strict-model-match` | 失败 | 同上 |
| `pnpm test:model-check-cross-model-paired-mismatch` | 失败 | 同上 |
| `pnpm test:model-check-distribution-similarity` | 失败 | 同上 |
| `pnpm test:model-check-long-context-routing` | 失败 | 同上 |

失败根因：

- 共享 fixture `backend/src/scripts/maintenance/mockdata/fixtures.ts` 创建账号时传入 `status: 'active'`。
- 当前 `backend/src/storage/repositories.ts` 账户创建规则会把除 disabled 外的新账号统一置为 `pending_test`。
- 模型检测固定账号选择器排除 `pending_test`，所以测试在真正运行模型探针前失败。

这不是上述五类探针已经被证明实现错误，而是当前回归门禁失效；在修复 fixture 激活流程并重跑前，不能声称完整检测链路当前全部通过。

## 6. 准确率提升建议

推荐优先级：

1. 先恢复现有模型检测全链路回归，保证每种探针确实执行到 mock 上游。
2. 把模型身份、显式映射、Token 诚信和证据充分度拆成独立结果，避免总分误导。
3. 用版本化生成式隐藏探针记录结构化 observation，不采集普通用户正文。
4. 在同一账号上随机交错请求 Sol / Terra / Luna 和 5.5 / 5.4，检测多模型长期异常同质。
5. 按上游 origin 的 HMAC 桶限制权重，由 `stats-worker` 构建 leave-one-upstream-out 群体基线。
6. 使用精确 tokenizer 和 P0 / P1 / P2 差分斜率检测 input Token 比例灌水；输出 Token 在隐藏 reasoning 无法拆分时明确标为不支持。
7. 先在 mock 中注入 5%、10%、固定增加和分桶取整，再决定产品阈值；未完成校准前不宣传可稳定识别轻微灌水。

限制：如果多数独立上游桶都做相同模型替换，无官方对照的群体基线仍可能被整体误导。第一版更适合识别跨模型塌缩和离群，不能承诺准确点名物理模型。

长期设计见 [模型检测设计](../functions/模型检测设计.md)，推进计划见 [PLAN-0095](../plans/计划-0095-模型指纹与Token用量可信度检测.md)。

## 7. 2026-07-14 实施后记

本报告第 3.1 节和第 5 节记录的是 2026-07-13 审计快照。2026-07-14 已同步 GPT-5.6 三模型目录边界、通过正式健康检查成功路径修复共享 fixture，并恢复报告所列五条全链路回归。当前还只完成五维报告最小闭环；精确 Token 差分、observation / cohort 窗口和生产校准仍未实现，不能据此修改 usage、成本、额度或账户状态。
