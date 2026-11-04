package accountquality

// w13: SystemInsufficientQuotaRuleMatches 的 429 边界矩阵，与 gateway 侧
// chain_error_policy.go 同一契约 —— 429 只认稳定额度错误码精确命中
// （普通频控与裸 429 排除，不读文本白名单）；402/403 维持既有语义。

import "testing"

func TestW13SystemQuotaRule429Matrix(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		errorCode  string
		errorType  string
		text       string
		want       bool
	}{
		{"429 火山方舟稳定额度码", 429, "AccountQuotaExceeded", "", "You have exceeded the 5-hour usage quota. It will reset at 2026-11-01 19:13:27 +0800 CST.", true},
		{"429 火山方舟额度码+泛化 type", 429, "AccountQuotaExceeded", "TooManyRequests", "You have exceeded the 5-hour usage quota. It will reset at 2026-11-01 19:13:27 +0800 CST.", true},
		{"429 new-api 余额耗尽码", 429, "balance_exhausted", "", "密钥余额已用尽", true},
		{"429 余额耗尽码+泛化 type", 429, "balance_exhausted", "TooManyRequests", "", true},
		{"429 分组免费额度码", 429, "GROUP_FREEBALANCE_INSUFFICIENT", "", "你已到达该分组免费额度使用上限", true},
		{"429 既有稳定码", 429, "insufficient_quota", "", "", true},
		{"429 稳定码经 type 命中", 429, "", "quota_exceeded", "", true},
		{"429 普通频控码排除", 429, "AccountRateLimitExceeded", "", "", false},
		{"429 频控码+泛化 type 排除", 429, "AccountRateLimitExceeded", "TooManyRequests", "", false},
		{"429 频控 code 优先于额度 type", 429, "rate_limit_exceeded", "quota_exceeded", "", false},
		{"429 额度码优先于频控 type", 429, "AccountQuotaExceeded", "rate_limit_exceeded", "", true},
		{"429 rate_limit_exceeded 排除", 429, "rate_limit_exceeded", "", "", false},
		{"429 too_many_requests 排除", 429, "too_many_requests", "", "", false},
		{"429 裸响应排除", 429, "", "", "", false},
		{"429 只有泛化 type 排除", 429, "", "TooManyRequests", "You have exceeded the 5-hour usage quota", false},
		{"429 只认码不认文案", 429, "", "", "You have exceeded the 5-hour usage quota", false},
		{"403 中转余额不足文案", 403, "billing_error", "", "Insufficient user balance. Please top up your user balance.", true},
		{"403 billing_error 单独不命中", 403, "billing_error", "", "", false},
		{"402 balance_exhausted 稳定码", 402, "balance_exhausted", "", "", true},
		{"500 稳定码也不命中", 500, "AccountQuotaExceeded", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SystemInsufficientQuotaRuleMatches(tc.statusCode, tc.errorCode, tc.errorType, tc.text)
			if got != tc.want {
				t.Fatalf("SystemInsufficientQuotaRuleMatches(%d, %q, %q, %q) = %v, want %v", tc.statusCode, tc.errorCode, tc.errorType, tc.text, got, tc.want)
			}
		})
	}
}
