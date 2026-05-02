# `sub2api` 参考笔记

这份笔记只记录新项目里最值得保留的语义，不把旧项目的复杂度直接搬过来。

## 1. 透传

- 透传更像账号能力，不只是页面开关。
- 需要保留“认证透传”“请求内容透传”“模型映射”等相关语义的扩展位。
- 新项目第一阶段先把字段和开关设计清楚，具体的网关行为后续再接。

## 2. 错误处理策略

- `sub2api` 的账户错误处理不是简单全局下拉，而是保存在账号凭据里的 `error_handling_rules` 规则数组。
- 规则按 `enabled` 与 `priority` 生效，匹配字段包括 `status_codes`、`error_codes`、`error_types`、`keywords`；多个字段同时配置时必须全部命中。
- 动作在 lite 中收敛为 `rate_limited`、`temp_unschedulable`、`error_disabled`；其中只有 `error_disabled` 会把账号置为错误。
- `rate_limited` 支持固定时长、每日固定时间、每周固定时间恢复，并保留时区字段。
- `juhe-ai` 不搬旧项目所有兼容字段，第一期只把这些规则语义落到账户添加/编辑和 OpenAI 网关处理。

## 3. 并发

- 需要区分“限制值”和“当前值”。
- 账号管理页最好能直接看到并发相关字段。
- 后续调度层应当基于统一字段判断是否可用，而不是页面临时推断。

## 4. 代理

- 代理不要散落在账号逻辑里，应该单独成对象。
- 账号只持有代理引用，实际请求时再解析代理 URL。
- 这样便于复用、切换和排查。

## 5. 新项目的简化方式

- 第一阶段只保留管理面和字段面。
- 不先做完整中转链路，不先做所有平台的复杂分支。
- 后续每加一种平台或透传模式，都先补文档再补代码。

## 6. 流熔断与账号处理

`sub2api` 里的参考语义：

- `StreamTimeoutSettings` 包含 `enabled`、`timeout_seconds`、`action`、`temp_unsched_minutes`、`threshold_count`、`threshold_window_minutes`。
- 默认思路是关闭开关，流超时后累计账号失败次数，到阈值后临时不可调度或标记异常。
- 运行时会区分流超时、临时不可调度和错误状态。

`juhe-ai` 的简化实现：

- 设置项映射为 `streamCircuitBreakerEnabled`、`streamRequestTimeoutSeconds`、`streamIdleTimeoutSeconds`、`streamFailureThresholdCount`、`streamFailureThresholdWindowMinutes`、`temporaryUnschedulableRetryIntervalSeconds`、`temporaryUnschedulableRetryAttempts` 和 `defaultTemporaryUnschedulableMinutes`。
- 账号字段只保留 `cooldown_until`、`last_error_message`、`stream_failure_count`、`stream_failure_window_started_at`，并把运行态语义拆成正常、停用、错误、限流中和临时不可调用。
- 不做独立调度器，请求时直接过滤仍在冷却中的账号。
- 上游未被账号错误策略截获的未知异常统一先做短暂重试，重试后仍失败才按默认临时不可调用时长冷却当前账号后尝试下一个账号。
- 网关成功请求会清理账号失败状态，减少人工处理成本。
