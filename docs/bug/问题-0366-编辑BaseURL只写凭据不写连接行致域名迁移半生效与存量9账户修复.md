# BUG-0366 编辑 Base URL 只写凭据不写连接行——域名迁移"半生效"存量 9 账户数据修复（生产）

## 基本信息

- 编号：BUG-0366
- 状态：已修复已修复数据（2026-11-17 生产数据修复；无代码改动——用户裁决此类为数据问题，不做代码兼容）
- 严重程度：P1（可用性：受影响账户派发/外部测试/余额查询全部 301/404 失败；不涉数据丢失）
- 发现时间：2026-11-17
- 发现方式：用户反馈（编辑内草稿测试通过、列表外部测试 301）+ 生产取证
- 模块：网关/账户数据（`account_connections` vs `accounts.credentials_encrypted`）
- 关联 bug：T94（「编辑 Base URL 不写连接域」缺陷，2026-11-02 v2026.11.02 修复）、BUG-0361/#T206（同窗口账户面故障排查）
- 责任人：胡安民

## 问题概述

- 现象：账户 `workbuddy.ddkmall`（`acc_d129043d668eed46`）编辑弹窗里的草稿测试 200 通过（真实流式响应），列表外部测试/实际派发却 301（nginx HTML）；账户反复「临时不可调用 ↔ active」，余额查询失败。
- 期望：同一账户的编辑面与派发面使用同一 Base URL。
- 实际：系统存在两份 Base URL 存储——`accounts.credentials_encrypted` 的 `base_url` 键（编辑详情明文投影、草稿测试、余额身份用）与 `account_connections.base_url`（网关拨号、外部测试、健康探针/冷却复测用）。历史编辑只写了前者，后者停留在创建时刻的旧域名；上游随后对旧域名开启整域 301，裂缝暴露为故障。
- 影响范围：全局扫描 1161 个账户，**9 个账户存在真漂移**（凭据与唯一 enabled 连接行 URL 不一致），其中 2 个因旧域名失效处于故障态（workbuddy.ddkmall、napi.kid1412）；其余为 disabled/error 存量。

## 复现步骤

1. 在 T94 修复前（2026-11-02 前）的版本上，编辑一个 api_key 账户的 Base URL 并保存。
2. 观察 `account_connections.base_url` 不变、`accounts.credentials_encrypted.base_url` 已更新（操作日志只记「凭据 已变更」）。
3. 上游对旧域名开启 301/失效后：草稿测试（凭据面）通过，外部测试/派发/探针（连接行）失败。

## 环境信息

- 分支 / 版本：master；生产 v2026.11.14（commit e9b8e0759）。
- 数据状态：`acc_d129043d668eed46` 连接行 `https://workbuddy.ddkmall.cn/v1`（updated_at = 创建时间 2026-10-25T03:21:42Z，之后再未写过）；凭据 base_url = `https://api.ddkmall.cn/v1`（操作日志 2026-10-25 21:55「凭据 已设置→已变更」即用户按上游换域名所做编辑）。
- 是否稳定复现：是（301 恒定；旧域名整域重定向）。

## 根因分析

- 表象：同一账户两个入口的测试结论相反。
- 真实根因：**两份存储、旧版本只写一半**。T94 前 `buildAccountUpdatePayload` 不携带 connections 差量（该缺陷注释在 `frontend/src/views/accounts/accountSavePayload.ts:422`，修复于 `3d3ab1e32`，随 v2026.11.02 上线）；本案账户的域名编辑发生在 2026-10-25，早于修复，凭据面更新而 `account_connections.base_url` 保持创建值。上游后启整域 301（workbuddy.ddkmall.cn → api.ddkmall.cn），派发面 301 → `upstream_http_status` → 冷却 → temporary_unavailable；后台冷却复测（读连接行）持续 301 打回；人工草稿测试（读凭据面）成功短暂清冷却 → 状态反复。
- 为什么现在点「确定」也治不好：前端保存差量以凭据面为基准，表单与已保存凭据一致时判定「Base URL 未变化」，PATCH 不携带 connections，连接行不被重写。

## 修复方案

- 修改点：**纯数据修复，无代码改动**（用户裁决：此类问题是数据问题，不应做代码兼容）。一次性工具按「凭据面为准」对齐连接行，规则：
  - 账户恰有 1 条 enabled 连接且 URL 与凭据不一致 → 候选改写（CAS：WHERE id=? AND base_url=旧值）；
  - 多条 enabled 连接（合法多 URL）/ 凭据缺 base_url / 解密失败 / 无 enabled 连接 → review 不改；
  - 一致 → unchanged。
  - 改写后用「POST {base}/chat/completions 可达性」逐账户复核方向（404/000 = 该路径已死；401/400 = 端点存在）：`napi.kid1412.qzz.io` 凭据路径 `/api/paas/v4` 实测 404、旧 `/v1` 401 存活 → 该行回滚为 `/v1`；其余 8 行方向成立（workbuddy 旧域名 301 死、api1.dshapi.icu 与 codegoai 旧域名不可达 000、sharedchat/88996api/codexcn 新旧均存活保留凭据即用户最后保存意图）。
- 行为影响：连接行与凭据面统一后，派发/外部测试/余额查询/健康探针恢复使用有效域名。
- 发布异常处理：无 schema/代码变更；回滚 = `psql -f account-connections-pre-connsync-20261117.dump`（全表 plain SQL 备份，1570 行）。

## 验证记录

| 验证类型 | 验证内容 | 命令 / 步骤 | 预期结果 | 实际结果 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 盘点 | 全局漂移扫描（dry-run） | 一次性工具 Plan：1161 账户/1570 连接 | 输出 rewrite/review 清单 | rewrites=9，review=1（`oai.ycxlg.com-f21lrv` 凭据解密失败老账，T207-3 待裁决） | 通过 |
| 备份 | 修复前可回滚 | `pg_dump -t juhe_business.account_connections` → `/opt/juhe-ai/backup/account-connections-pre-connsync-20261117.dump` | COPY 块 1570 行可读 | rows=1570 | 通过 |
| 修复 | 9 行 CAS 改写 | 一次性工具 Apply（单事务） | 全部命中 | 9/9 提交，manifest `/opt/juhe-ai/backup/connsync-final-20261117.json` | 通过 |
| 后检 | 漂移归零 | 修复后重跑 Plan | rewrites=0 | rewrites=0，unchanged=1160，review=1（同上老账） | 通过 |
| 方向复核 | 逐账户端点可达性 | POST `{base}/chat/completions` 新旧对比 | 依存活侧定方向 | workbuddy/ api1/ codegoai 旧侧死 000/301 新侧 401；napi 凭据侧 404 → 已回滚 `/v1`；其余新旧均活保留凭据 | 通过 |
| 恢复 | 账户自动恢复 | 等下一轮冷却复测（连接行已更新） | 复测 200 → active | `acc_d129043d668eed46` status=active、cooldown 清空 | 通过 |

## 复发记录

- 无（首次发现；同族 T94 代码缺陷已于 v2026.11.02 修复，本档案为存量数据清理）。

## 下次遇到

- 先查什么：同时开编辑弹窗草稿测试与列表外部测试，若一边过一边不过，先比对两份存储——`accounts.credentials_encrypted.base_url`（解密后）与 `account_connections.base_url`。
- 重点看什么：`account_connections.updated_at` 是否等于账户创建时间（= 创建后从未被编辑写过）；`operation_logs.changes_json` 只记「凭据 已变更」的编辑事件。
- 如何避免误判：上游 3xx 不要只看 Location；用无鉴权 POST 探活区分「路径已死（404/000）」与「端点存在（401/400）」，避免把方向修反。

## 完成总结

- 完成时间：2026-11-17
- 结论：全局 9 个漂移账户的数据已按证据对齐（8 行凭据优先、1 行证据反转回滚）；故障账户恢复 active；无代码改动。观察项见 #T209。
- 后续建议：若将来再出现这类漂移（T99 克隆/T100 批量编辑/T101 导入等旁路写入），仍按本档案的「双源比对 + 端点探活定方向」流程做数据修复，不引入代码容错。
