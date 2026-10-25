package proxylatency

import (
	"strings"
)

// 本文件覆盖 Runner.RunManual 的完整编排（owner/proxy lease → IssueInput →
// 执行 → 投影 → 出口诊断）与 ExecuteIssuedInput 的端到端执行（本地 httptest
// 同时充当代理与目标，绝不产生真实外网请求）。

func portOf(raw string) int {
	start := strings.LastIndex(raw, ":")
	value := raw[start+1:]
	port := 0
	for _, ch := range value {
		port = port*10 + int(ch-'0')
	}
	return port
}

// itoaW 是测试内整数转字符串辅助。
func itoaW(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

