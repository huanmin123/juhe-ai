package routestrategies

// wt 覆盖波次：速度优先总时间兜底截止（设计 6.2）配置层——两新字段的写侧
// 归一化（缺省=默认、显式 0/越界拒绝）、strictObject 键表放行与 round-trip。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWtTotalTimeNormalizeDefaults(t *testing.T) {
	t.Run("nil speedFirstConfig 回落默认", func(t *testing.T) {
		config, err := normalizeSpeedFirstConfig(nil)
		if err != nil {
			t.Fatal(err)
		}
		if config.TotalTimeDeadlineSeconds != 120 || config.CompactionTotalTimeDeadlineSeconds != 300 {
			t.Fatalf("config=%+v", config)
		}
	})
	t.Run("空对象回落默认", func(t *testing.T) {
		config, err := normalizeSpeedFirstConfig(map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if config.TotalTimeDeadlineSeconds != 120 || config.CompactionTotalTimeDeadlineSeconds != 300 {
			t.Fatalf("config=%+v", config)
		}
	})
	t.Run("档位分界常量契约", func(t *testing.T) {
		if SpeedFirstLargeInputTokenThreshold != 100_000 {
			t.Fatalf("SpeedFirstLargeInputTokenThreshold=%d", SpeedFirstLargeInputTokenThreshold)
		}
	})
}

func TestWtTotalTimeNormalizeRejectsOutOfRange(t *testing.T) {
	cases := []struct {
		name    string
		key     string
		value   int
		message string
	}{
		{"普通档低于下限", "totalTimeDeadlineSeconds", 59, "请求总时间截止必须是 60-270 秒"},
		{"普通档高于上限", "totalTimeDeadlineSeconds", 271, "请求总时间截止必须是 60-270 秒"},
		{"普通档显式 0", "totalTimeDeadlineSeconds", 0, "请求总时间截止必须是 60-270 秒"},
		{"压缩档低于下限", "compactionTotalTimeDeadlineSeconds", 299, "压缩总时间截止必须是 300-900 秒"},
		{"压缩档高于上限", "compactionTotalTimeDeadlineSeconds", 901, "压缩总时间截止必须是 300-900 秒"},
		{"压缩档显式 0", "compactionTotalTimeDeadlineSeconds", 0, "压缩总时间截止必须是 300-900 秒"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := normalizeSpeedFirstConfig(map[string]any{testCase.key: testCase.value})
			if err == nil || err.Error() != testCase.message {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestWtTotalTimeNormalizeKeepsValidValues(t *testing.T) {
	config, err := normalizeSpeedFirstConfig(map[string]any{
		"totalTimeDeadlineSeconds":           60,
		"compactionTotalTimeDeadlineSeconds": 900,
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.TotalTimeDeadlineSeconds != 60 || config.CompactionTotalTimeDeadlineSeconds != 900 {
		t.Fatalf("config=%+v", config)
	}
	config, err = normalizeSpeedFirstConfig(map[string]any{
		"totalTimeDeadlineSeconds":           270,
		"compactionTotalTimeDeadlineSeconds": 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.TotalTimeDeadlineSeconds != 270 || config.CompactionTotalTimeDeadlineSeconds != 300 {
		t.Fatalf("config=%+v", config)
	}
}

func TestWtTotalTimeParseStrictKeys(t *testing.T) {
	// 新键放行：parse 层 strictObject 不拒绝两个新键。
	input, message := parseMutationFields(map[string]any{
		"normalRoutingConfig": map[string]any{
			"schedulingPreference": "speed_first",
			"speedFirstConfig": map[string]any{
				"totalTimeDeadlineSeconds":           120,
				"compactionTotalTimeDeadlineSeconds": 300,
			},
		},
	}, false)
	if message != "" {
		t.Fatalf("新键必须放行 message=%q", message)
	}
	if !input.HasNormalConfig || input.NormalConfigRaw == nil {
		t.Fatalf("input=%+v", input)
	}
	// 未知键仍拒绝。
	if _, message := parseMutationFields(map[string]any{
		"normalRoutingConfig": map[string]any{
			"schedulingPreference": "speed_first",
			"speedFirstConfig":     map[string]any{"totalTimeDeadlineSecondsTypo": 120},
		},
	}, false); message != invalidMutationMessage {
		t.Fatalf("未知键必须拒绝 message=%q", message)
	}
}

func TestWtTotalTimeNormalizeRoundTrip(t *testing.T) {
	normalized, err := normalizeNormalRoutingConfig(map[string]any{
		"schedulingPreference": "speed_first",
		"firstByteDeadlineMs":  20_000,
		"speedFirstConfig": map[string]any{
			"slowTriggerCount":                   4,
			"totalTimeDeadlineSeconds":           150,
			"compactionTotalTimeDeadlineSeconds": 600,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		SchedulingPreference string            `json:"schedulingPreference"`
		FirstByteDeadlineMs  *int              `json:"firstByteDeadlineMs"`
		SpeedFirstConfig     *SpeedFirstConfig `json:"speedFirstConfig"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SpeedFirstConfig == nil {
		t.Fatal("speedFirstConfig 必须随 round-trip 保留")
	}
	if decoded.SpeedFirstConfig.TotalTimeDeadlineSeconds != 150 ||
		decoded.SpeedFirstConfig.CompactionTotalTimeDeadlineSeconds != 600 ||
		decoded.SpeedFirstConfig.SlowTriggerCount != 4 {
		t.Fatalf("speedFirstConfig=%+v", decoded.SpeedFirstConfig)
	}
	if !strings.Contains(string(encoded), `"totalTimeDeadlineSeconds":150`) ||
		!strings.Contains(string(encoded), `"compactionTotalTimeDeadlineSeconds":600`) {
		t.Fatalf("encoded=%s", encoded)
	}
}
