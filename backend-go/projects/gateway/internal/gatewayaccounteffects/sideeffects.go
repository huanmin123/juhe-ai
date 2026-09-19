package gatewayaccounteffects

// SideEffectsConfig mirrors the runtimeConfig.gateway inputs of
// account-side-effects.service.ts with the Node defaults.
//
// 生产接线边界（PLAN-20260918T142845703Z W6 / PLAN-20260919T000723744Z 任务 B）：
// 生产组合根只消费 RuntimeStateDriver 字段（经 IsRedisDriver）。
// Node account-side-effects.service.ts 的 SideEffectsService 队列/预检/
// 失败风暴半区在 Go 生产链路从未接线（Node 归档同样零生产调用），已于
// 2026-09-19 随死代码清理整体删除；本结构体仅为 API Key 守卫/效果链的
// 驱动判定保留。
type SideEffectsConfig struct {
	// RuntimeStateDriver mirrors runtimeConfig.runtimeStateDriver:
	// "memory" (process-local) or "redis" (distributed).
	RuntimeStateDriver string
}

// IsRedisDriver mirrors runtimeConfig.runtimeStateDriver === 'redis'.
func (c *SideEffectsConfig) IsRedisDriver() bool { return c.RuntimeStateDriver == "redis" }

// safeIntegerMax mirrors Number.MAX_SAFE_INTEGER.
const safeIntegerMax int64 = 9007199254740991

func isSafeInteger(value int64) bool {
	return value >= -safeIntegerMax && value <= safeIntegerMax
}
