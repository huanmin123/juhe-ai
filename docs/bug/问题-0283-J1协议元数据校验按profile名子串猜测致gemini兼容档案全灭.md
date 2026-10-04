# 问题-0283：J1 协议元数据校验按 profile 名子串猜测，gemini OpenAI 兼容档案全部无法探活

- 编号：BUG-0283
- 状态：已修复（2026-10-04 登记并修复代码；待 jobs 发布后生产自动恢复）
- 影响面：生产 `profile_gemini_openai_chat_v1beta` 全部 12 个账户 100% 无法构建探活输入——10 个 `pending_test` 自 2026-09-14 起永远无法激活、1 个 `active` 被剥夺周期健康检查、1 个 `temporary_unavailable` 有冷却锚点但复测永远跑不了。

## 现象

生产 `juhe_jobs.account_health_direct_input_suppressions` 中 12 个 gemini 账户的抑制行全部处于活跃刷新状态（≤5 分钟），无一健康；每个账户仅有同一天（2026-09-26 23:26 +08，一次批量扫描）的唯一 `probe_task_failure/direct_input_invalid` outcome。账户行 `protocol_code='openai'`、`protocol_version='v1'`，与其 profile 规范定义一致。

## 根因（校验与规范种子自相矛盾）

`projects/jobs/internal/accounthealth/direct_input.go` 的 `validateDirectProtocolMetadata`（2026-08-24 `941e8bd12` 引入，Node 归档无此校验，属 Go 侧自创加固）按子串猜测协议：

```go
if strings.Contains(profile, "anthropic") { ... } else if strings.Contains(profile, "gemini") {
    expectedCode, expectedVersion = "gemini", "v1beta"
}
```

而规范种子（maintenance `pg_schema.go`）定义 `profile_gemini_openai_chat_v1beta` 的 `ProtocolCode="openai"`、`ProtocolVersion="v1"`（Gemini 官方 OpenAI Chat 兼容档案，BaseURL `generativelanguage.googleapis.com/v1beta/openai`）。profile 名含 "gemini" → 校验要求 `gemini/v1beta` → 与账户真实协议 `openai/v1` 必然不一致 → `ToInput` 报"protocol_code 与 profile 不一致" → `direct_input_invalid` → 5 分钟抑制循环无限重复。该 profile 类全部账户无差别命中。

## 修复

- `validateDirectProtocolMetadata` 改为对照规范元数据表 `directProfileProtocolMetadata`（13 个种子 profile + Node 遗留 `profile_hybrid_gemini_native_v1beta`，与 maintenance 种子逐条对齐）；未注册元数据的 profile 显式报错（此前会按 openai/v1 默认放行）。
- 回归用例：`TestDirectInputAcceptsGeminiOpenAIChatProfileMetadata`（gemini openai-chat 规范协议必须通过、携带原生协议必须拒绝、gemini native 不受影响、未注册 profile 必须拒绝；回退修复必红）。
- 部署后无需数据修复：旧抑制行 `next_due_at` 过期后不再阻拦，10 个 `pending_test` 自动走激活探针、`active` 账户恢复周期检查、`temporary_unavailable` 恢复冷却复测。

## 关联

- 同批登记 BUG-0282（precheck 漏写冷却锚点）共享同一失败形态与可观测性修复；抑制行的陈旧化石行（4 个已恢复 active 账户）已随生产数据修复一并清理。

## 回归验证

- `go test -count=1 ./projects/jobs/internal/accounthealth/` 全绿（含新增用例）。
