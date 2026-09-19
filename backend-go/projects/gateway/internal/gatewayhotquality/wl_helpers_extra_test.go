package gatewayhotquality

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestWlNamespaceHelpers(t *testing.T) {
	t.Run("SanitizeRedisNamespacePart", func(t *testing.T) {
		if got, _ := SanitizeRedisNamespacePart(" dev zone "); got != "dev_zone" {
			t.Fatalf("got=%q", got)
		}
		if _, err := SanitizeRedisNamespacePart("___"); err == nil {
			t.Fatal("全下划线必须拒绝")
		}
	})
	t.Run("RedisNamespacePrefix", func(t *testing.T) {
		prefix, err := RedisNamespacePrefix("dev")
		if err != nil || prefix != "juhe-ai:dev:" {
			t.Fatalf("prefix=%q err=%v", prefix, err)
		}
		if _, err := RedisNamespacePrefix("  "); err == nil {
			t.Fatal("空 namespace 必须拒绝")
		}
	})
	t.Run("RedisNamespacedKey", func(t *testing.T) {
		key, err := RedisNamespacedKey("dev", "hq:a:1")
		if err != nil || key != "juhe-ai:dev:hq:a:1" {
			t.Fatalf("key=%q err=%v", key, err)
		}
		// 已带命名空间前缀的键不重复加前缀。
		if key, _ := RedisNamespacedKey("dev", "juhe-ai:dev:hq:a:1"); key != "juhe-ai:dev:hq:a:1" {
			t.Fatalf("key=%q", key)
		}
		// 只带根前缀的键补齐命名空间。
		if key, _ := RedisNamespacedKey("dev", "juhe-ai:hq:a:1"); key != "juhe-ai:dev:hq:a:1" {
			t.Fatalf("key=%q", key)
		}
		if _, err := RedisNamespacedKey("dev", "   "); err == nil {
			t.Fatal("空键必须拒绝")
		}
		if _, err := RedisNamespacedKey("  ", "k"); err == nil {
			t.Fatal("空命名空间必须拒绝")
		}
	})
	t.Run("normalizedNamespace 与 namespacedKey", func(t *testing.T) {
		if got := normalizedNamespace("  dev: "); got != "dev" {
			t.Fatalf("got=%q", got)
		}
		if got := normalizedNamespace("juhe-ai:dev"); got != "dev" {
			t.Fatalf("got=%q", got)
		}
		if got := namespacedKey("dev", "k"); got != "juhe-ai:dev:k" {
			t.Fatalf("got=%q", got)
		}
		if got := namespacedKey("juhe-ai:dev", "k"); got != "juhe-ai:dev:k" {
			t.Fatalf("got=%q", got)
		}
	})
}

func TestWlRedisScalarHelpers(t *testing.T) {
	t.Run("redisStringResult", func(t *testing.T) {
		if got, ok := redisStringResult("s"); !ok || got != "s" {
			t.Fatalf("got=%q ok=%v", got, ok)
		}
		if got, ok := redisStringResult([]byte("b")); !ok || got != "b" {
			t.Fatalf("got=%q ok=%v", got, ok)
		}
		if _, ok := redisStringResult(3); ok {
			t.Fatal("其他类型必须失败")
		}
	})
	t.Run("redisNumericValue", func(t *testing.T) {
		value := 5.0
		if got, err := redisNumericValue(&value, false, "x"); err != nil || got != 5 {
			t.Fatalf("got=%d err=%v", got, err)
		}
		if got, _ := redisNumericValue(nil, true, "x"); got != 0 {
			t.Fatalf("默认分支 got=%d", got)
		}
		if _, err := redisNumericValue(nil, false, "refusals"); err == nil {
			t.Fatal("无默认的 nil 必须报错")
		}
		nan := math.NaN()
		if _, err := redisNumericValue(&nan, true, "x"); err == nil {
			t.Fatal("NaN 必须报错")
		}
		inf := math.Inf(1)
		if _, err := redisNumericValue(&inf, true, "x"); err == nil {
			t.Fatal("Inf 必须报错")
		}
		negative := -1.0
		if _, err := redisNumericValue(&negative, true, "x"); err == nil {
			t.Fatal("负数必须报错")
		}
	})
	t.Run("safeRedisName", func(t *testing.T) {
		if got := safeRedisName("  model-A:1_x-y!@# ", "fb"); got != "model-A:1_x-y___" {
			t.Fatalf("got=%q", got)
		}
		if got := safeRedisName("!!!", "fallback"); got != "___" {
			t.Fatalf("got=%q", got)
		}
	})
	t.Run("atoi64", func(t *testing.T) {
		tests := []struct {
			value string
			want  int64
			ok    bool
		}{
			{"0", 0, true}, {"42", 42, true}, {"-7", -7, true},
			{"", 0, true}, {"x", 0, false}, {"1x", 0, false},
		}
		for _, test := range tests {
			got, ok := atoi64(test.value)
			if ok != test.ok || got != test.want {
				t.Fatalf("atoi64(%q)=(%d,%v) want=(%d,%v)", test.value, got, ok, test.want, test.ok)
			}
		}
	})
	t.Run("sortInt64s 与 derefScope", func(t *testing.T) {
		values := []int64{3, 1, 2}
		sortInt64s(values)
		if values[0] != 1 || values[2] != 3 {
			t.Fatalf("values=%v", values)
		}
		if got := derefScope(nil); got.AccountRuntimeKey != "" {
			t.Fatalf("nil 必须回退零值: %+v", got)
		}
		if got := derefScope(&HotQualityScope{AccountRuntimeKey: "k"}); got.AccountRuntimeKey != "k" {
			t.Fatalf("got=%+v", got)
		}
	})
	t.Run("GetRedisClient", func(t *testing.T) {
		if _, err := GetRedisClient(context.Background(), "  "); err == nil {
			t.Fatal("空 URL 必须拒绝")
		}
		if _, err := GetRedisClient(context.Background(), "://bad"); err == nil {
			t.Fatal("非法 URL 必须拒绝")
		}
	})
	t.Run("字符串工具", func(t *testing.T) {
		if strconvInt64(42) != "42" {
			t.Fatal("strconvInt64 语义错误")
		}
		if int64OrZero(3) != 3 {
			t.Fatal("int64OrZero 语义错误")
		}
	})
	_ = strings.TrimSpace
}

func TestWlSmallNumericHelpers(t *testing.T) {
	t.Run("incrementInt64/expirationAt/maximumInt64", func(t *testing.T) {
		if got := incrementInt64(1); got != 2 {
			t.Fatalf("got=%d", got)
		}
		if got := incrementInt64(maxSafeInteger); got != maxSafeInteger {
			t.Fatalf("上限饱和 got=%d", got)
		}
		if got := expirationAt(100, 50); got != 150 {
			t.Fatalf("got=%d", got)
		}
		if got := expirationAt(1, maxSafeInteger); got != maxSafeInteger {
			t.Fatalf("溢出饱和 got=%d", got)
		}
		if got := maximumInt64(nil, 5); got == nil || *got != 5 {
			t.Fatalf("nil 目标 got=%v", got)
		}
		current := int64(2)
		if got := maximumInt64(&current, 9); got == nil || *got != 9 {
			t.Fatalf("取较大值 got=%v", got)
		}
		current = int64(20)
		if got := maximumInt64(&current, 9); got == nil || *got != 20 {
			t.Fatalf("保留较大值 got=%v", got)
		}
	})
	t.Run("addInt64 与 maximumInt64Ptr", func(t *testing.T) {
		if got := addInt64(4, 6); got != 10 {
			t.Fatalf("got=%d", got)
		}
		if got := addInt64(maxSafeInteger, 1); got != maxSafeInteger {
			t.Fatalf("饱和 got=%d", got)
		}
		four := int64(4)
		nine := int64(9)
		if got := maximumInt64Ptr(&four, &nine); got == nil || *got != 9 {
			t.Fatalf("got=%v", got)
		}
		if got := maximumInt64Ptr(nil, &nine); got == nil || *got != 9 {
			t.Fatalf("nil 左侧 got=%v", got)
		}
		if got := maximumInt64Ptr(&four, nil); got == nil || *got != 4 {
			t.Fatalf("nil 右侧 got=%v", got)
		}
		if got := maximumInt64Ptr(nil, nil); got != nil {
			t.Fatalf("双 nil got=%v", got)
		}
	})
	t.Run("numericPolicyValue 与 maxi", func(t *testing.T) {
		if got := numericPolicyValue(nil, 7); got != 7 {
			t.Fatalf("nil 回落=%d", got)
		}
		if got := numericPolicyValue(3.0, 7); got != 3 {
			t.Fatalf("got=%d", got)
		}
		if got := numericPolicyValue("bad", 7); got != 7 {
			t.Fatalf("非数字回落=%d", got)
		}
		value2 := 2.5
		if got := numericPolicyValue(value2, 7); got != 2 {
			// float64 直接截断为整数。
			t.Fatalf("小数截断=%d", got)
		}
		if maxi(1, 2) != 2 || maxi(5, 2) != 5 {
			t.Fatal("maxi 语义错误")
		}
	})
	t.Run("positiveIntegerInt64 与 bodyAdmission 限额", func(t *testing.T) {
		if got, err := positiveIntegerInt64(5, "n"); err != nil || got != 5 {
			t.Fatalf("got=%d err=%v", got, err)
		}
		if _, err := positiveIntegerInt64(0, "n"); err == nil {
			t.Fatal("零必须报错")
		}
		if _, err := positiveIntegerInt64(-1, "n"); err == nil {
			t.Fatal("负数必须报错")
		}
		if bodyAdmissionMax(1, 2) != 2 || bodyAdmissionMax(5, 2) != 5 {
			t.Fatal("bodyAdmissionMax 语义错误")
		}
		if bodyAdmissionMax64(1, 2) != 2 || bodyAdmissionMax64(9, 2) != 9 {
			t.Fatal("bodyAdmissionMax64 语义错误")
		}
	})
	t.Run("exploration 与 runtime 的 now/整数辅助", func(t *testing.T) {
		if got, err := explorationNormalizedNow(100); err != nil || got != 100 {
			t.Fatalf("got=%d err=%v", got, err)
		}
		if _, err := explorationNormalizedNow(-1); err == nil {
			t.Fatal("非法时间必须报错")
		}
		if got, err := runtimeNormalizedNow(100); err != nil || got != 100 {
			t.Fatalf("got=%d err=%v", got, err)
		}
		if _, err := runtimeNormalizedNow(-1); err == nil {
			t.Fatal("负数时间必须报错")
		}
		if defaultUnixMilli() <= 0 {
			t.Fatal("defaultUnixMilli 必须返回正数")
		}
		if got, err := explorationPositiveInteger(3, "n"); err != nil || got != 3 {
			t.Fatalf("got=%d err=%v", got, err)
		}
		if _, err := explorationPositiveInteger(0, "n"); err == nil {
			t.Fatal("非正数必须报错")
		}
	})
}

func TestWlModelRankAndFirstAccountID(t *testing.T) {
	account := GatewayHotQualityAccountView{ID: "a1"}
	if got := modelRank(account, map[string]int{"a1": 2}); got != 2 {
		t.Fatalf("got=%d", got)
	}
	if got := modelRank(account, nil); got != 3 {
		t.Fatalf("未命中回落=%d", got)
	}
	if got := modelRank(account, map[string]int{"a1": -5}); got != 3 {
		t.Fatalf("负值回落=%d", got)
	}
	if got := firstAccountID(nil); got != "" {
		t.Fatalf("空列表=%q", got)
	}
	if got := firstAccountID([]HotQualityCandidate{{AccountID: "a9"}}); got != "a9" {
		t.Fatalf("got=%q", got)
	}
}
