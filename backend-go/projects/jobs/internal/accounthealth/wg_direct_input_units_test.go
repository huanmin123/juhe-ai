package accounthealth

import (
	"testing"
	"time"
)

// getenvFrom 构造 env 读取闭包。
func getenvFrom(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// TestConfigHelpersMatrix 表驱动覆盖 config.go 的四个解析助手（默认回落、
// 非法值、边界）。
func TestConfigHelpersMatrix(t *testing.T) {
	t.Run("configDuration", func(t *testing.T) {
		duration, err := configDuration(getenvFrom(nil), "X", 5*time.Second, time.Second)
		if err != nil || duration != 5*time.Second {
			t.Fatalf("缺省必须回落: %v %v", duration, err)
		}
		duration, err = configDuration(getenvFrom(map[string]string{"X": "10s"}), "X", time.Second, time.Second)
		if err != nil || duration != 10*time.Second {
			t.Fatalf("合法值必须生效: %v %v", duration, err)
		}
		for _, bad := range []string{"abc", "500ms"} {
			if _, err := configDuration(getenvFrom(map[string]string{"X": bad}), "X", time.Second, time.Second); err == nil {
				t.Fatalf("%q 必须报错", bad)
			}
		}
	})
	t.Run("configInt", func(t *testing.T) {
		value, err := configInt(getenvFrom(nil), "X", 7, 1, 10)
		if err != nil || value != 7 {
			t.Fatalf("缺省必须回落: %d %v", value, err)
		}
		if value, err = configInt(getenvFrom(map[string]string{"X": "9"}), "X", 1, 1, 10); err != nil || value != 9 {
			t.Fatalf("合法值必须生效: %d %v", value, err)
		}
		for _, bad := range []string{"abc", "0", "11"} {
			if _, err := configInt(getenvFrom(map[string]string{"X": bad}), "X", 1, 1, 10); err == nil {
				t.Fatalf("%q 必须报错", bad)
			}
		}
	})
	t.Run("configPositiveInt", func(t *testing.T) {
		value, err := configPositiveInt(getenvFrom(nil), "X", 3)
		if err != nil || value != 3 {
			t.Fatalf("缺省必须回落: %d %v", value, err)
		}
		if value, err = configPositiveInt(getenvFrom(map[string]string{"X": "2"}), "X", 1); err != nil || value != 2 {
			t.Fatalf("正整数必须生效: %d %v", value, err)
		}
		for _, bad := range []string{"abc", "0", "-1"} {
			if _, err := configPositiveInt(getenvFrom(map[string]string{"X": bad}), "X", 1); err == nil {
				t.Fatalf("%q 必须报错", bad)
			}
		}
	})
	t.Run("configInt64", func(t *testing.T) {
		value, err := configInt64(getenvFrom(nil), "X", 8, 1, 100)
		if err != nil || value != 8 {
			t.Fatalf("缺省必须回落: %d %v", value, err)
		}
		if value, err = configInt64(getenvFrom(map[string]string{"X": "50"}), "X", 1, 1, 100); err != nil || value != 50 {
			t.Fatalf("合法值必须生效: %d %v", value, err)
		}
		for _, bad := range []string{"abc", "0", "101"} {
			if _, err := configInt64(getenvFrom(map[string]string{"X": bad}), "X", 1, 1, 100); err == nil {
				t.Fatalf("%q 必须报错", bad)
			}
		}
	})
	t.Run("configMilliseconds", func(t *testing.T) {
		duration, err := configMilliseconds(getenvFrom(nil), "X", time.Hour, time.Minute, 7*24*time.Hour)
		if err != nil || duration != time.Hour {
			t.Fatalf("缺省必须回落: %v %v", duration, err)
		}
		duration, err = configMilliseconds(getenvFrom(map[string]string{"X": "7200000"}), "X", time.Hour, time.Minute, 7*24*time.Hour)
		if err != nil || duration != 2*time.Hour {
			t.Fatalf("合法毫秒必须生效: %v %v", duration, err)
		}
		for _, bad := range []string{"abc", "0", "-5", "999", "999999999"} {
			if _, err := configMilliseconds(getenvFrom(map[string]string{"X": bad}), "X", time.Hour, time.Minute, 7*24*time.Hour); err == nil {
				t.Fatalf("%q 必须报错", bad)
			}
		}
	})
}
