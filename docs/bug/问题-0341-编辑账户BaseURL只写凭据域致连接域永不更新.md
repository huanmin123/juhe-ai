# BUG-0341 编辑账户 Base URL 只写凭据域致连接域永不更新

## 基本信息

- 编号：BUG-0341
- 状态：已修复（2026-11-02 本机交付，待发布与生产验证）
- 严重程度：P1（编辑 Base URL 静默不生效，引发生产账户反复误判打死）
- 发现时间：2026-11-01（生产排查 moonup 账户反复「临时不可调用」）
- 发现方式：生产取证（用户报障）
- 模块：前端（账户编辑保存链）
- 关联计划：无
- 关联 bug：BUG-0331（过渡态来源）、BUG-0333/0332（同期账户编辑链路缺陷族）
- 关联待办：T94（`docs/待办与阻塞事项.md`，含生产数据修复与全量扫描记录）
- 责任人：待定

## 问题概述

- 现象：编辑抽屉修改 Base URL 并保存「成功」后，前端回显新值，但网关与健康检测继续拨号旧地址，全程无任何报错；上游旧域名失效时账户被健康检测/冷却复测反复打成 `temporary_unavailable`（清理运行态后分钟级复挂），且因该账户缺席调度产生 503「未分配账号」。
- 期望：编辑 Base URL 保存后，网关拨号事实源 `juhe_business.account_connections.base_url`（连接域）同步更新。
- 实际：编辑保存只更新凭据域 `credentials_encrypted` 内的 `base_url` 键（编辑框回显来源），连接域自创建后永不变更。
- 影响范围：全部 api_key 类型 AI 账户的编辑保存（`PATCH /accounts/{id}` 与用户侧 `my-accounts` 同源接口）；上游换域名即触发账户不可自愈失效。2026-11-01 生产全量扫描 1381 账户，14 个凭据域/连接域不一致，其中 5 个连接域指向死域名或拒绝存量 key（当晚已受控修复，见 T94）。

## 复现步骤

1. 编辑任一 api_key 账户，把 Base URL 从 A 改为 B，保存成功。
2. 查库 `account_connections.base_url` 仍为 A；编辑框回显 B（凭据域已更新）。
3. 网关/健康检测继续拨 A。

## 环境信息

- 分支 / 版本：2026-11-01 生产基线（前端含 BUG-0331 修复）。
- 数据状态：`juhe_business.account_connections` 目标 schema。
- 是否稳定复现：是（凭 trace `81e57d82-88bb-4cea-b43f-c17869e019bc` 实锤：网关实拨 `https://motomoto.lol/v1/responses` 报 x509 证书不匹配，而编辑框显示 moonup.dev）。

## 根因分析

- 表象：保存成功但拨号地址不变。
- 真实根因：编辑保存链 `buildAccountUpdatePayload`（`frontend/src/views/accounts/accountSavePayload.ts`）的更新载荷不包含 `connections` 字段——`buildAccountSavePayload` 在编辑时已组装连接集合，但 `buildAccountUpdatePayload` 挑字段时丢弃，基线差量（`buildAccountEditPatch.ts` `buildAccountAdvancedUpdatePatch`）两侧均无该键，PATCH 永不携带。基础编辑链 `buildAccountBasicEditSnapshot` 同病（快照 values 无 connections，仅凭据 `base_url` 进 `credentialsPatch`）。
- 为什么会发生：BUG-0331（2026-10-22）修复前端连接契约迁移时明确保留「`connections` 只在创建载荷出现，PATCH 仍不整集合替换」的过渡态；后端 PATCH 契约（`patch.go` `case "connections"` 全集合替换，文档《供应商与上游协议面重构设计》「更新时 n 全集合替换」）早已就绪，前端迟迟未跟进。base_url 在系统中存两份（凭据域=历史形态、连接域=重构后权威拨号事实源），编辑写面只覆盖凭据域，形成「编辑框显示 A、实际拨号 B」的静默分叉。终态契约为凭据域不存 `base_url`（同文档 §2），过渡态下两域必须由编辑链同源同步。

## 修复方案

- 修改点：
  - `frontend/src/views/accounts/accountSavePayload.ts`：`buildAccountUpdatePayload` 透传 `connections`（与创建链同构）。
  - `frontend/src/views/accounts/accountEditPatch.ts`：`buildAccountBasicEditSnapshot` 快照 values 补连接集合（连接 preset 独立于健康检查三件套求值）；`buildAccountUpdateDelta` 增加 `connections` 仅在 current 侧存在时参与差量的规则（OAuth 清空 Base URL 等场景不得回发 `connections: null`，后端全集合替换不接受空集合）。
  - `frontend/src/api/domains/accounts.ts`：`AccountUpdatePayload` 声明 `connections` 内联结构类型。
  - 回归：`accountSavePayload.test.ts` 新增 3 例；`account-edit-save-flow-regression.ts` 新增创建→编辑透传、基础编辑改 URL 双域同源、高级编辑改 URL 整集合提交与 null 防护 4 组断言。
- 行为影响：编辑 api_key 账户 Base URL/请求形态保存后连接域同步更新（后端全集合替换幂等，未变化不落写）；OAuth 清空 Base URL 的编辑行为不变；`healthCheckConnectionPresetCode` 未变时三件套不误发。凭据域 `base_url` 键继续存在（终态退役属 B4 收尾批次，见《供应商与上游协议面重构设计》§2 与 T94）。
- 回滚：回退前端构建即可（后端零改动）。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 类型检查 | 代码类型检查 | `npm run typecheck` | 通过 | 通过 | 已执行 |
| 单元测试 | 定向三文件 | `npx vitest run src/views/accounts/accountSavePayload.test.ts accountMediaFormBlocks.test.ts accountCredentials.test.ts` | 全绿 | 3 文件 44 例通过（含新增 3 例） | 已执行 |
| 单元测试 | 前端全量 | `npm run test:unit` | 全绿 | 94 文件 1167 例通过 | 已执行 |
| 回归验证 | 编辑保存载荷 | `npm run test:account-edit-save-flow` | 连接集合断言通过 | 通过 | 已执行 |
| 静态检查 | lint 改动文件 | `npx eslint <5 文件>` | 0 error | 0 error | 已执行 |
| 功能验证 | 生产编辑 Base URL 后连接域更新 | 发布后生产编辑任一账户 Base URL 保存并查库 | `account_connections.base_url` 同步更新 | 待发布后执行 | 待执行 |
