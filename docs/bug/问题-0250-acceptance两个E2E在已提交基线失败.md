# BUG-0250 acceptance 两个 E2E 在已提交基线失败（会话模型列表缺 gpt-5.6-sol / 操作日志未持久化 announcements.create）

## 基本信息

- 编号：BUG-0250
- 状态：已修复（2026-09-30，工作区未提交，随批次一并交付）
- 严重程度：P1（验收链门禁失败：chat flow 与 management backbone 两个核心 E2E 红灯）
- 发现时间：2026-09-30
- 发现方式：全面审查收尾批次全量验证
- 模块：后端验收夹具（acceptance chat_flow / management_backbone）
- 关联 bug：无直接关联；两症状均为**验收夹具未跟上已提交行为契约**，非产品代码缺陷
- 责任人：主代理（当日批次收尾）

## 问题概述

- 现象 1：`TestAcceptanceChatFlow` 失败 `conversation models never listed gpt-5.6-sol`（GET /my-chat/conversations/{id}/models 返回 200 但恒为空列表，10 秒轮询超时）。
- 现象 2：`TestAcceptanceManagementBackbone/operation_logs` 间歇失败（约 1/8 概率）`management operation logs never persisted the announcements.create entry`（15 秒轮询超时）。
- 期望：夹具按当前行为契约驱动验收链，两个 E2E 稳定绿。

## 复现步骤

1. 修复前：`cd backend-go && go test ./projects/gateway/cmd/juhe-ai-gateway/acceptance/ -count=1` → 现象 1 稳定失败；现象 2 需多次运行（`-count=3` 以上可观察到间歇失败）。

## 环境信息

- 分支 / 版本：master（工作区改动经 `git stash` 二分验证：干净 HEAD 同样失败，确认非当日批次引入）
- 是否稳定复现：现象 1 稳定；现象 2 间歇（时序依赖）

## 根因分析

提交二分（f42c5f42b PASS → 逐提交前移）将现象 1 定位到 **a6f424bcb**（feat(chat): 实现AI问答会话绑定模式调度覆盖功能）；该三绑定模式契约随后被 2026-09-28《AI问答会话账户唯一绑定设计》取代为**会话唯一绑定 AI 账户**。产品代码正确实现了现行契约，验收夹具停留在旧契约：

- 现象 1：现行契约下 `POST /my-chat/conversations` 免请求体创建空会话（历史字段兼容忽略），**未选账户的会话模型列表为空、不能发送**（设计 §5）；`listConversationModels`（generation_deps.go）对 `conversation.BindAccountID == nil` 返回空列表是正确行为。夹具创建会话只传 `apiKeyId`（已被兼容忽略）且从不绑定账户 → 模型列表恒空。中间提交 a6f424bcb 时代创建曾直接被拒（`请选择会话绑定方式`），后续提交放宽为免请求体创建后演变为「创建成功但列表恒空」。
- 现象 2：测试全程 35+ 个管理面写操作（首个条目还是 announcements 之前的 `POST /system-accounts`），而 `/api/operation-logs` 默认 `pageSize=20` 且按 `created_at DESC` 排序（store.go List）。announcements.create 是最早的条目，全部条目异步落库后**恒在第 2 页之后**；轮询裸查询仅当「首次采样早于其余条目落库」的时序碰巧命中时通过——异步落库顺序不利时 15 秒轮询永远看不到目标条目。F4 写入器三条丢弃路径（队列满/租约续期失败/提交失败）均有 WARN 日志，失败运行中均未出现，排除生产侧丢条。

## 修复方案

纯验收夹具修复，产品代码零改动：

1. `acceptance/gateway_chain_test.go`：`chainFixture` 增加 `accountID` 字段并在夹具中填充 mock 上游账户 ID。
2. `acceptance/chat_flow_test.go`：会话创建改为免请求体 POST（契约 §5）；新增 `PATCH /my-chat/conversations/{id}` 携带 `{"accountId": chain.accountID}` 绑定账户后轮询模型列表；移除废弃的 `chatKeyIDOf`（apiKeyId 已被兼容忽略，鉴权主体由服务端 `EnsureChatAPIKey` 自动复用）；头注释与链路注释同步为现行契约。
3. `acceptance/management_backbone_test.go`：operation_logs 轮询查询加 `?module=announcements&action=create` 过滤（store 层精确匹配），分页不再受全量条目数影响。

## 验证结果

- `TestAcceptanceChatFlow` 单跑通过（10.7s，修复前 17s 失败）。
- `TestAcceptanceManagementBackbone -count=10` 十连跑全绿（50.8s；修复前 `-count=3` 内即可观察到失败）。
- acceptance 整包 `-count=1` 全绿（87.6s，覆盖 chat flow、management backbone、fullchain 系列、log/read faces 等）。
- `go vet` + `gofmt -l` acceptance 包零告警。

## 防回归与观测性备注

- 本立案与当日批次解耦的证据保留：stash 二分证明干净 HEAD 同样失败；提交二分证明引入点为已提交的 a6f424bcb 及其后续契约演进，非当日批次。
- 会话模型列表断言现在隐式验证「绑定前空列表 → 绑定后非空」的前半段（绑定前不查询）；若后续契约再变（如恢复免绑定可见性），此测试会第一时间红灯。
- operation_logs 轮询的过滤式查询是分页安全写法范本：任何「在列表里找特定条目」的验收轮询都必须带过滤参数或显式翻页，禁止依赖默认页恰好包含目标条目。
