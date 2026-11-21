package upstreamlimit

import "testing"

// TestOrigin 锁定同上游限流的分组键：scheme://host:port 归一，解析失败回落
// 原始字符串，空值共享空键（保守限流，不放大瞬时压力）。用例自
// connectivityhealth TestUpstreamOrigin 迁移（2026-11-22 实现收敛）。
func TestOrigin(t *testing.T) {
	cases := []struct {
		baseURL, want string
	}{
		{"https://api.openai.com/v1", "https://api.openai.com"},
		{"https://api.openai.com:8443/v1", "https://api.openai.com:8443"},
		{"  https://dashscope.aliyuncs.com  ", "https://dashscope.aliyuncs.com"},
		{"http://api.qwen.com", "http://api.qwen.com"},
		{"https://api.openai.com/v1#fragment", "https://api.openai.com"},
		// 非法 URL 回落原串：同一非法值恒映射同一键。
		{"://bad url", "://bad url"},
		{"not-a-url", "not-a-url"},
		{"", ""},
	}
	for _, test := range cases {
		if got := Origin(test.baseURL); got != test.want {
			t.Fatalf("Origin(%q) = %q, want %q", test.baseURL, got, test.want)
		}
	}
}

// TestLimiterBucketIsolation 锁定同上游限流桶语义：同 origin 共享同一容量
// 的桶，不同 origin 各自独立（互不占额）。用例自 connectivityhealth
// TestUpstreamLimiterBucketIsolation 迁移（2026-11-22 实现收敛）。
func TestLimiterBucketIsolation(t *testing.T) {
	const limit = 60
	limiter := NewLimiter(limit)
	a := limiter.Bucket("https://api.example.com")
	a2 := limiter.Bucket("https://api.example.com")
	b := limiter.Bucket("https://other.example.com")
	if a != a2 {
		t.Fatal("同 origin 必须共享同一个限流桶")
	}
	if a == b {
		t.Fatal("不同 origin 必须使用独立限流桶")
	}
	// 桶容量 = limit：塞满后再次获取阻塞，释放后可复用。
	for i := 0; i < limit; i++ {
		a <- struct{}{}
	}
	full := limiter.Bucket("https://api.example.com")
	select {
	case full <- struct{}{}:
		t.Fatal("同源桶塞满后不得继续放行")
	default:
	}
	<-a
	select {
	case full <- struct{}{}:
	default:
		t.Fatal("释放一个名额后应可再次获取")
	}
}

// TestNewLimiterRejectsNonPositive 锁定装配错误 fail-fast：零/负容量桶会让
// 发送永久阻塞，必须在构造时拒绝而不是静默退化。
func TestNewLimiterRejectsNonPositive(t *testing.T) {
	for _, limit := range []int{0, -1} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("NewLimiter(%d) 必须 panic", limit)
				}
			}()
			NewLimiter(limit)
		}()
	}
}
