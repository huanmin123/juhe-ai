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

// TestValidateSQLiteIsolation 覆盖 store/input 路径隔离的放行与冲突分支。
func TestValidateSQLiteIsolation(t *testing.T) {
	base := map[string]string{}
	if err := validateSQLiteIsolation("/tmp/j1/jobs.sqlite3", "/tmp/j1/inputs", getenvFrom(base)); err != nil {
		t.Fatalf("无冲突路径必须放行: %v", err)
	}
	// 与业务库共用文件 → 报错。
	if err := validateSQLiteIsolation("/tmp/j1/jobs.sqlite3", "/tmp/j1/inputs", getenvFrom(map[string]string{
		"JUHE_AI_DATABASE_PATH": "/TMP/J1/jobs.sqlite3",
	})); err == nil {
		t.Fatal("共用业务库文件必须报错（Windows 大小写不敏感等价）")
	}
	// store 放进 input 目录 → 报错。
	if err := validateSQLiteIsolation("/tmp/j1/inputs/jobs.sqlite3", "/tmp/j1/inputs", getenvFrom(base)); err == nil {
		t.Fatal("store 不得放入 input 目录")
	}
	// input 目录内一层子目录仍然算放入 → 报错。
	if err := validateSQLiteIsolation("/tmp/j1/inputs/sub/jobs.sqlite3", "/tmp/j1/inputs", getenvFrom(base)); err == nil {
		t.Fatal("input 子目录内的 store 必须报错")
	}
	// input 目录之外 → 放行。
	if err := validateSQLiteIsolation("/tmp/j1/other/jobs.sqlite3", "/tmp/j1/inputs", getenvFrom(base)); err != nil {
		t.Fatalf("input 之外的 store 必须放行: %v", err)
	}
}

// TestEqualPathWindowsCaseInsensitive 锁定 Windows 路径等价比较语义。
func TestEqualPathWindowsCaseInsensitive(t *testing.T) {
	if !equalPath(`C:\A\B`, `c:\a\b`) {
		t.Fatal("Windows 路径必须大小写不敏感等价")
	}
	if equalPath(`C:\A\B`, `C:\A\C`) {
		t.Fatal("不同路径不得判等")
	}
}
