# `sub2api` 参考笔记

这份笔记只记录新项目里最值得保留的语义，不把旧项目的复杂度直接搬过来。

## 1. 透传

- 透传更像账号能力，不只是页面开关。
- 需要保留“认证透传”“请求内容透传”“模型映射”等相关语义的扩展位。
- 新项目第一阶段先把字段和开关设计清楚，具体的网关行为后续再接。

## 2. 错误处理策略

- 错误策略最好是规则化配置，而不是在代码里写死判断。
- 可以按错误码、关键词、平台和优先级来匹配。
- 可以区分三类结果：原样返回、自定义状态码、自定义错误内容。

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

`sub2api-lite` 的简化实现：

- 设置项映射为 `streamCircuitBreakerEnabled`、`streamIdleTimeoutSeconds`、`streamFailureThresholdCount`、`streamFailureThresholdWindowMinutes` 和 `defaultTemporaryUnschedulableMinutes`。
- 账号字段只保留 `cooldown_until`、`last_error_message`、`stream_failure_count`、`stream_failure_window_started_at`。
- 不做独立调度器，请求时直接过滤仍在冷却中的账号。
- 上游未被账号错误策略截获的未知异常统一按默认临时不可调用时长冷却当前账号后尝试下一个账号。
- 网关成功请求会清理账号失败状态，减少人工处理成本。
