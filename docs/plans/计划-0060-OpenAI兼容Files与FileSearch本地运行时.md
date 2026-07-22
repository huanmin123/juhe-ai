# PLAN-0060 OpenAI 兼容 Files 与 File Search 本地运行时

## 基本信息

- 编号：PLAN-0060
- 状态：已完成
- 创建时间：2026-06-24
- 更新时间：2026-06-24
- 需求来源：用户对话
- 执行者：AI
- 关联模块：后端 / 网关 / OpenAI 兼容接口 / Anthropic bridge / 文件存储 / Vector Store / 本地检索 / 审计 / 文档 / 验证

## 需求目标

- 背景：OpenAI 到 Anthropic Messages 桥接已经支持基础四入口和部分高兼容能力，但 `file_id` 和 `file_search` 仍只能受控失败。用户要求继续做到尽可能客户端无感知，上传文件、图片引用和 RAG 检索也应按 OpenAI 客户端习惯工作。
- 目标：新增 OpenAI 兼容 Files / Vector Store / File Search 本地运行时，让 Chat / Responses 中的 `file_id` 能解析为 Anthropic image / document，让 Responses `file_search` 能由本地检索模拟并按 OpenAI Responses 形态返回。
- 交付物：长期设计文档、计划记录、后端本地 Files / resolver / Vector Store / retrieval 实现、mock 回归、真实账户联调、凭据和文件内容不落文档检查。

## 范围边界

### 本次包含

- [x] 新增长期设计文档，明确 OpenAI Files、Vector Stores、`file_id` resolver 和 `file_search` 的本地承接边界。
- [x] 新增 OpenAI 兼容 Files API 首批接口：上传、列表、获取、下载、删除。
- [x] 新增文件元数据和本地对象流式存储，不在运行路径一次性读写大文件。
- [x] 新增 bridge `file_id` resolver 抽象、注入点和未配置时受控失败。
- [x] 将 resolver 接入本地 Files API 的 API Key 归属、MIME 和受控大小校验。
- [x] bridge 层支持 Chat / Responses 中图片、PDF、text 文件的 `file_id` 转换。
- [x] 新增 Vector Store 基础元数据、文件绑定和首批 chunk 索引；本轮先覆盖文本类文件的 keyword retrieval。
- [x] 实现 Responses `file_search` 本地预检索模拟，返回 `file_search_call`、annotations 和可选 `file_search_call.results`。
- [x] 补齐真实上游 E2E、凭据扫描和文件内容日志边界验证。

### 本次不包含

- 不承诺 OpenAI 托管 File Search 的语义完全等价；首批是本地预检索模拟。
- 不实现 `/v1/uploads` 大文件分片上传；首批只做 `/v1/files` multipart 上传。
- 不引入 Redis、Kafka、外部对象存储或外部向量数据库作为必需依赖。
- 不让 Anthropic 上游直接看到 OpenAI `file_id` 或本地文件路径。
- 不把文件正文写入文档、默认日志、测试快照或普通审计摘要。

## 关联文档

- 高兼容矩阵：`docs/functions/OpenAI到Anthropic高兼容能力矩阵.md`
- 基础桥接设计：`docs/functions/OpenAI到Anthropic协议桥接设计.md`
- Files / File Search 运行时设计：`docs/functions/OpenAI兼容Files与FileSearch本地运行时设计.md`
- 请求处理分层：`docs/functions/请求处理分层设计.md`
- 安全与日志：`docs/functions/安全与日志策略.md`
- SQLite 存储说明：`docs/functions/SQLite存储说明.md`
- 验证手册：`docs/develop/测试与验证说明.md`

## 方案概述

- 方案原则：先实现本地 Files resolver，保证 `file_id` 不再是桥接硬缺口；再实现 Vector Store / File Search，避免把检索运行时和文件上传存储混在一起。
- 数据变化：新增 OpenAI 兼容文件、vector store、vector store 文件和 chunk 元数据表；运行路径只维护当前 schema，不写旧结构兼容分支。
- 接口变化：新增或拦截 `/v1/files`、`/v1/vector_stores` 相关 OpenAI 兼容入口；Chat / Responses 下游接口不新增客户端必填字段。
- 前端变化：首批不改前端；后续如需要管理文件库和索引状态，再单独立前端计划。
- 后端变化：新增 Files / Vector Store 本地模块、resolver 抽象、bridge 注入点和 retrieval 服务。
- 数据处理策略：文件上传和下载使用 stream；索引按文件 / chunk 增量处理；检索只读取 topN chunk，不扫描全库拼接。

## 执行拆解

- [x] 新增 `docs/functions/OpenAI兼容Files与FileSearch本地运行时设计.md`。
- [x] 创建 PLAN-0060 并纳入计划索引。
- [x] 更新高兼容能力矩阵和桥接设计中的 `file_id` / `file_search` 边界。
- [x] 梳理网关路由拦截点，确定 Files API 需要在 `express.raw` 前分流并使用流式 multipart 解析。
- [x] 设计并实现文件元数据 repository 和本地对象流式存储。
- [x] 实现 `/v1/files` 首批接口和 OpenAI 兼容错误。
- [x] 实现 bridge resolver 注入和 Chat / Responses `file_id` 转换。
- [x] 实现 Vector Store 基础接口、文件绑定、文件详情、解绑和内容查看。
- [x] 实现 text / markdown / json / code 类文件的 chunk / keyword retrieval 首批版本。
- [x] 实现 Responses `file_search` 预检索模拟，并在 JSON / SSE 输出中插入 `file_search_call`。
- [x] 补齐 vector store file `in_progress` / `completed` / `failed` 生命周期，让 OpenAI SDK 可按状态轮询。
- [x] 补充真实 E2E、凭据扫描和文档验证记录。
- [x] 更新完成总结。

## 测试项

| 测试类型 | 测试项 | 验证方式 / 命令 | 预期结果 | 状态 | 实际结果或备注 |
| --- | --- | --- | --- | --- | --- |
| 命令类验证 | 后端类型检查 | `pnpm --dir backend typecheck` | 后端 TypeScript 类型检查通过 | 通过 | 2026-06-24 通过 |
| 命令类验证 | 前端类型检查 | `pnpm --dir frontend typecheck` | 前端不因共享类型变化回归 | 通过 | 2026-06-24 通过 |
| Mock 回归 | OpenAI Files API | `pnpm --dir backend test:protocol-boundary-openai-anthropic` | 上传、列表、获取、下载、删除均为 OpenAI 兼容形态 | 通过 | 2026-06-24 通过 |
| Mock 回归 | `file_id` resolver | `pnpm --dir backend test:protocol-boundary-openai-anthropic` | Chat file、Responses image/file `file_id` 成功；未知 file_id 本地 404；上游不收到 OpenAI file id | 通过 | 2026-06-24 通过；上传后 Chat document 和 Responses image 均已覆盖 |
| Mock 回归 | Vector Store / search | `pnpm --dir backend test:protocol-boundary-openai-anthropic` | 创建 store、绑定文本文件、轮询到 completed、索引 chunk、搜索 topN 成功 | 通过 | 2026-06-24 通过；覆盖创建、绑定、列表、内容查看、search 和删除 |
| Mock 回归 | Vector Store 文件生命周期 | `pnpm --dir backend test:protocol-boundary-openai-anthropic` | 创建先返回 `in_progress`；成功后为 `completed`；不支持 MIME / 超限文件最终为 `failed` 且带 `last_error`；未就绪 file_search 本地失败 | 通过 | 2026-06-24 通过 |
| Mock 回归 | Responses `file_search` | `pnpm --dir backend test:protocol-boundary-openai-anthropic` | JSON / SSE 返回 `file_search_call`、annotations、可选 results | 通过 | 2026-06-24 通过；覆盖 Responses JSON/SSE 与 Chat file_search 预检索 |
| Mock 回归 | Files / Vector Store 边界 | `pnpm --dir backend test:protocol-boundary-openai-anthropic` | 跨 API Key 不能访问文件 / vector store，未知 vector store 本地失败，不支持 MIME 和文本索引超限落 `failed + last_error` | 通过 | 2026-06-24 通过；边界错误均为 OpenAI 形态且不触发 Anthropic 上游 |
| 回归场景 | 既有高兼容能力 | `pnpm --dir backend test:protocol-boundary-openai-anthropic` | inline 文件、web_search、structured output、thinking 不回归 | 通过 | 2026-06-24 通过 |
| 真实联调 | 真实 Anthropic 上游 | `pnpm --dir backend test:anthropic-real-gateway-e2e` | 核心四入口通过；本地 file_search 真实上游探针通过 | 通过 | 2026-06-24 通过；`responses_file_search_local:passed`，可选 `chat_image_data_url` 仍因请求 abort 失败 |
| 安全检查 | 凭据与文件内容扫描 | `rg` 固定 key 前缀和测试文件正文特征 | 仓库无真实 key；文档 / 运行模块无文件正文泄露 | 通过 | 2026-06-24 通过；测试样本文本仅存在回归脚本 |

## 进度记录

| 日期 | 状态 | 记录人 | 进展 / 决策 / 阻塞 |
| --- | --- | --- | --- |
| 2026-06-24 | 进行中 | AI | 用户要求继续做完整长期兼容；确认 `file_id` 和 `file_search` 需要本地 Files / Vector Store 运行时，已新增设计文档和本计划。 |
| 2026-06-24 | 进行中 | AI | 已同步高兼容能力矩阵、基础桥接设计和 PLAN-0059，明确 `file_id` / `file_search` 后续归 PLAN-0060 本地运行时承接。 |
| 2026-06-24 | 进行中 | AI | 已实现 bridge `file_id` resolver 抽象、测试注入点和 Chat / Responses 图片 / 文档转换；mock 回归覆盖 Responses `input_image.file_id`、Responses `input_file.file_id`、Chat `file.file_id` 成功路径；生产 driver 接入本地 Files resolver 后，未知 `file_id` 返回本地 404。 |
| 2026-06-24 | 进行中 | AI | 已确认完整 `/v1/files` multipart 上传不能走现有公开网关的全量 `express.raw` 请求体链路，后续 Files API 入口需要在 raw parser 前分流并使用流式解析。 |
| 2026-06-24 | 进行中 | AI | 已实现 OpenAI 兼容 `/v1/files` 首批本地入口：上传、列表、获取、内容下载和删除；上传使用 `busboy` 流式 multipart 解析，元数据经 db-service 写入，文件对象落本地目录。 |
| 2026-06-24 | 进行中 | AI | 已将 Anthropic bridge resolver 接入本地 Files API；mock 回归覆盖上传文本文件后 Chat `file.file_id` 转 Anthropic document、上传图片后 Responses `input_image.file_id` 转 Anthropic image，且 Anthropic 上游不接触 OpenAI `file_id`。 |
| 2026-06-24 | 进行中 | AI | 已按 OpenAI 当前文档修正 Vector Store search 为 `POST /v1/vector_stores/{vector_store_id}/search`；首批实现范围限定为文本类文件 keyword retrieval，PDF / Office 深度解析后续由 extractor 插件承接。 |
| 2026-06-24 | 进行中 | AI | 已实现 OpenAI 兼容 `/v1/vector_stores` 首批本地入口、文本 chunk 索引、keyword search，以及 Chat / Responses `file_search` 本地预检索到 Anthropic system context 的桥接；mock 回归覆盖 JSON / SSE 输出 `file_search_call`、`file_citation` annotations 和 include results。 |
| 2026-06-24 | 进行中 | AI | 已将真实 E2E 脚本改为生产一致路由链，补充本地 Files / Vector Store / Responses `file_search` 真实上游探针；真实上游返回 `responses_file_search_local:passed`。 |
| 2026-06-24 | 进行中 | AI | 本轮继续补边界专项：跨 API Key 授权、未知 vector store、unsupported MIME、文本索引超限，要求全部走 OpenAI 形态本地错误且不触发 Anthropic 上游。 |
| 2026-06-24 | 进行中 | AI | 已补 Files / Vector Store 边界回归：同系统内不同 API Key 不能读取 owner file / vector store，Responses `file_search` 引用他人 vector store 返回本地 404；后续 MIME / 超限语义已升级为 vector store file `failed + last_error`。 |
| 2026-06-24 | 进行中 | AI | 继续按官方 File Search 指南补齐 vector store file 轮询生命周期：创建返回 `in_progress`，索引异步更新 `completed` / `failed`，`file_search` 遇到未就绪或失败 store 时返回本地可诊断错误。 |
| 2026-06-24 | 进行中 | AI | 已实现轻量异步索引生命周期：`POST /v1/vector_stores/{id}/files` 先返回 `in_progress`，后台索引完成后更新 `completed`，unsupported MIME / 文本索引超限更新 `failed + last_error`；mock 和真实 E2E 均通过。 |

## 决策记录

| 日期 | 决策 | 原因 | 影响 |
| --- | --- | --- | --- |
| 2026-06-24 | `file_id` 先做本地 Files resolver，不转发给 Anthropic | OpenAI `file_id` 是 OpenAI 文件存储引用，Anthropic Messages 无法解析 | bridge 只发送 image / document 内容块，上游不接触 OpenAI file id |
| 2026-06-24 | `file_search` 首批做预检索模拟，不做完整托管工具循环 | Anthropic Messages 没有 OpenAI `file_search_call` 托管语义，完整多轮工具循环需要更多运行时 | 客户端可获得 OpenAI 形态结果，但审计中标记为 local emulation |
| 2026-06-24 | 文件运行时必须流式处理 | OpenAI 单文件上限高，项目规范禁止大文件运行路径全量读入内存 | 上传、下载、索引、resolver 都要有大小上限和 stream 边界 |

## 验收标准

- [x] 文档明确说明 `file_id` / `file_search` 为什么必须本地运行时承接。
- [x] `/v1/files` 首批接口可被 OpenAI SDK 常规上传 / 下载路径调用。
- [x] bridge 层在 resolver 已注入时，Chat / Responses 使用 `file_id` 能生成正确 image / document block。
- [x] resolver 接入本地 Files API 后，Chat / Responses 使用上传得到的本地 `file_id` 能生成正确 image / document block。
- [x] Responses `file_search` 在本地 vector store 就绪时返回 OpenAI 形态结果。
- [x] 跨 API Key 未授权访问和未找到 vector store 返回稳定 OpenAI 错误。
- [x] Vector store file 支持 `in_progress` / `completed` / `failed` 状态轮询；MIME 不支持和文本索引超限落 `failed + last_error`，`file_search` 未就绪时本地失败。
- [x] `/v1/files` 上传和下载路径不出现全量读入内存的实现。
- [x] Vector Store 索引和 `file_search` 路径不出现无上限全量读入内存的实现；文本索引首批只在 2 MB 上限内读取并生成受控 chunk。
- [x] mock 回归、真实 E2E、凭据扫描和文件内容泄露检查完成或明确未验证原因。

## 验证记录

- 类型检查：2026-06-24 执行 `pnpm --dir backend typecheck`，通过。
- 前端类型检查：2026-06-24 执行 `pnpm --dir frontend typecheck`，通过。
- Mock 回归：2026-06-24 执行 `pnpm --dir backend test:protocol-boundary-openai-anthropic`，通过；覆盖 `/v1/files` 上传 / 列表 / 下载 / 删除、上传后 Chat `file_id`、上传后 Responses 图片 `file_id`、未知 `file_id` 本地 404、`/v1/vector_stores` 创建 / 绑定 / 轮询 / 列表 / 内容查看 / search / 删除、Responses JSON / SSE `file_search_call`、Chat `file_search` 本地预检索，以及跨 API Key、未知 vector store、未就绪 vector store、unsupported MIME failed、文本索引超限 failed 边界。
- 真实联调：2026-06-24 执行 `pnpm --dir backend test:anthropic-real-gateway-e2e`，通过；核心 `chat_json`、`chat_sse`、`responses_json`、`responses_sse`、未知 vector store 本地 404 通过，可选 `responses_file_search_local:passed`；可选 `chat_image_data_url` 因请求 abort 失败。
- 凭据检查：2026-06-24 执行固定真实 key 前缀扫描，未命中；测试样本文本未出现在 docs 或运行模块。
- 未验证项：PDF / Office extractor、embedding / hybrid search、跨进程索引恢复和重试尚未实现。

## 风险与注意事项

- 文件上传引入新的磁盘容量风险，需要配额、过期和清理策略。
- PDF / Office 文本抽取质量会影响 `file_search` 回答质量，不能把检索质量宣称为 OpenAI 托管等价。
- 文件内容属于敏感数据，默认不写普通日志和文档；测试样本必须使用无敏感内容。
- 发布异常处理：如本地 Files / Vector Store 运行时异常，先关闭 `file_id` resolver 和 `file_search` local emulation，保持 inline 文件、web_search、基础四入口 bridge 可用。

## 完成总结

- 完成时间：2026-06-24
- 实际完成内容：已完成 OpenAI 兼容 Files API 首批入口、本地 Files resolver、Chat / Responses `file_id` 图片和文档桥接、Vector Store 基础接口、文本 chunk / keyword retrieval、Responses `file_search_call` 渲染、Chat `message.annotations`、vector store file `in_progress` / `completed` / `failed` 生命周期，以及跨 API Key / 未知资源 / 未就绪索引 / unsupported MIME / 超限文本的本地 OpenAI 形态错误。
- 主要改动位置：`backend/src/modules/openai-compatible-files/`、`backend/src/modules/openai-compatible-vector-stores/`、`backend/src/storage/openai-compatible-files.repository.ts`、`backend/src/storage/openai-compatible-vector-stores.repository.ts`、`backend/src/modules/providers/drivers/_shared/openai-anthropic-bridge.ts`、`backend/src/modules/providers/drivers/anthropic/driver.ts`、`backend/src/scripts/regression/openai-anthropic-bridge-mock-regression.ts`、`backend/src/scripts/regression/openai-anthropic-bridge-real-e2e.ts`。
- 验证结果：`pnpm --dir backend typecheck`、`pnpm --dir backend test:protocol-boundary-openai-anthropic` 和 `pnpm --dir backend test:anthropic-real-gateway-e2e` 已通过；真实联调包含 `responses_file_search_local:passed`；凭据扫描无真实 key 命中，测试样本文本未进入 docs 或运行模块。
- 后续建议：PDF / Office extractor、embedding / hybrid search、跨进程索引恢复、过期清理和容量配额作为后续增强计划处理，不影响首批 OpenAI 客户端 `file_id` / `file_search` 协议闭环。
