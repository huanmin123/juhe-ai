package apikeys

import "math"

// MaxClientIPConcurrencyLimit 是 Key 级单 IP 并发上限的合法上界
// （API Key 单 IP 并发限制设计 §2.1）：0 关闭，1..1000000 生效。
const MaxClientIPConcurrencyLimit = 1000000

// normalizeClientIPConcurrencyLimit 校验 store 输入范围。HTTP 层已完成 JSON
// 类型/整数校验；此处在事务前兜底，避免非 HTTP 调用写入越界值。禁止静默
// 截断或取整。
func normalizeClientIPConcurrencyLimit(value int) (int, error) {
	if value < 0 || value > MaxClientIPConcurrencyLimit {
		return 0, &ValidationError{Message: "单 IP 并发限制必须是 0 到 1000000 之间的整数"}
	}
	return value, nil
}

// parseClientIPConcurrencyLimitField 按 zod integer 契约解析已解码的 JSON
// 值：必须是 JSON number 且为整数、范围 0..1000000。返回首条 issue 文案，
// 与相邻字段的 zod 文案风格一致（非中文文本由 kernel localize 中间件统一
// 改写为 400 默认文案）。
func parseClientIPConcurrencyLimitField(raw any) (int, string) {
	number, isNumber := raw.(float64)
	if !isNumber {
		return 0, patchTypeIssue("number", raw)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) {
		return 0, "Expected integer, received float"
	}
	if number < 0 {
		return 0, "Number must be greater than or equal to 0"
	}
	if number > MaxClientIPConcurrencyLimit {
		return 0, "Number must be less than or equal to 1000000"
	}
	return int(number), ""
}
