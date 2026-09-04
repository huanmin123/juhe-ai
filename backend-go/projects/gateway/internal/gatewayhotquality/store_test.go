package gatewayhotquality

import (
	"strings"
	"testing"
)

func TestHotQualityScopeKeyEncoding(t *testing.T) {
	testCases := []struct {
		name  string
		scope HotQualityScope
		want  string
	}{
		{
			name: "ascii parts",
			scope: HotQualityScope{
				AccountRuntimeKey: "acc-1",
				ProtocolProfile:   "openai:2024",
				RequestLane:       "text",
				ModelFamily:       "model-bucket-0a",
			},
			want: "5:acc-1|11:openai:2024|4:text|15:model-bucket-0a",
		},
		{
			name: "chinese part uses utf8 byte length",
			scope: HotQualityScope{
				AccountRuntimeKey: "账号",
				ProtocolProfile:   "p",
				RequestLane:       "image",
				ModelFamily:       "unknown",
			},
			want: "6:账号|1:p|5:image|7:unknown",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := HotQualityScopeKey(testCase.scope)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("scope key = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestNormalizeHotQualityScopeErrors(t *testing.T) {
	testCases := []struct {
		name  string
		scope func(scope HotQualityScope) HotQualityScope
		want  string
	}{
		{
			name:  "missing accountRuntimeKey",
			scope: func(scope HotQualityScope) HotQualityScope { scope.AccountRuntimeKey = " "; return scope },
			want:  "热质量作用域缺少 accountRuntimeKey",
		},
		{
			name:  "missing protocolProfile",
			scope: func(scope HotQualityScope) HotQualityScope { scope.ProtocolProfile = ""; return scope },
			want:  "热质量作用域缺少 protocolProfile",
		},
		{
			name:  "missing modelFamily",
			scope: func(scope HotQualityScope) HotQualityScope { scope.ModelFamily = ""; return scope },
			want:  "热质量作用域缺少 modelFamily",
		},
		{
			name:  "invalid requestLane",
			scope: func(scope HotQualityScope) HotQualityScope { scope.RequestLane = "audio"; return scope },
			want:  "热质量 requestLane 必须是 text 或 image",
		},
	}
	base := HotQualityScope{AccountRuntimeKey: "a", ProtocolProfile: "p", RequestLane: "text", ModelFamily: "unknown"}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NormalizeHotQualityScope(testCase.scope(base))
			if err == nil || err.Error() != testCase.want {
				t.Fatalf("err = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestProtocolHotQualityScope(t *testing.T) {
	scope, err := ProtocolHotQualityScope(HotQualityScope{
		AccountRuntimeKey: "acc",
		ProtocolProfile:   " openai:2024 ",
		RequestLane:       "image",
		ModelFamily:       "model-bucket-01",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scope.ModelFamily != HotQualityUnknownModelFamily || scope.ProtocolProfile != "openai:2024" {
		t.Fatalf("scope = %+v", scope)
	}
}

func TestHotQualityModelFamilyCatalog(t *testing.T) {
	t.Run("dedupes, sorts and skips unknown", func(t *testing.T) {
		catalog, err := NewHotQualityModelFamilyCatalog([]string{"beta", " Alpha ", "beta", "unknown", "gamma"}, 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		families := catalog.KnownFamilies()
		if strings.Join(families, ",") != "alpha,beta,gamma" {
			t.Fatalf("families = %v", families)
		}
		if catalog.Resolve("BETA") != "beta" || catalog.Resolve("missing") != "unknown" || catalog.Resolve("  ") != "unknown" {
			t.Fatalf("resolve fallback broken")
		}
	})
	t.Run("limit exceeded", func(t *testing.T) {
		_, err := NewHotQualityModelFamilyCatalog([]string{"a", "b", "c"}, 2)
		if err == nil || err.Error() != "热质量最多允许 2 个模型 family" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("invalid family", func(t *testing.T) {
		_, err := NewHotQualityModelFamilyCatalog([]string{"ok", strings.Repeat("x", 129)}, 10)
		if err == nil || err.Error() != "模型 family 不能为空、包含控制字符或超过 128 字符" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("invalid limit", func(t *testing.T) {
		_, err := NewHotQualityModelFamilyCatalog([]string{"a"}, 0)
		if err == nil || err.Error() != "模型 family 目录容量 必须是正整数" {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("control characters rejected", func(t *testing.T) {
		if normalizeCandidateModelFamily("bad\x01name") != "" {
			t.Fatalf("control character accepted")
		}
	})
}

func TestFirstByteHistogramBucketBoundaries(t *testing.T) {
	testCases := []struct {
		firstByteMs int64
		want        int
	}{
		{0, 0}, {1000, 0}, {1001, 1}, {2000, 1}, {2001, 2}, {5000, 2},
		{5001, 3}, {10000, 3}, {10001, 4}, {20000, 4}, {20001, 5},
		{30000, 5}, {30001, 6}, {60000, 6}, {60001, 7}, {1 << 40, 7},
	}
	for _, testCase := range testCases {
		if got := FirstByteHistogramBucket(testCase.firstByteMs); got != testCase.want {
			t.Fatalf("FirstByteHistogramBucket(%d) = %d, want %d", testCase.firstByteMs, got, testCase.want)
		}
	}
}

func TestNormalizedFirstByteMs(t *testing.T) {
	got, err := NormalizedFirstByteMs(1200.6)
	if err != nil || got != 1201 {
		t.Fatalf("got = %d, err = %v", got, err)
	}
	if _, err := NormalizedFirstByteMs(-1); err == nil || err.Error() != "首字耗时必须是非负有限数值" {
		t.Fatalf("err = %v", err)
	}
}
