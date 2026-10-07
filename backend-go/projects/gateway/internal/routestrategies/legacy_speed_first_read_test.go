package routestrategies

// 存量 config_json 读侧形态覆盖：总时间兜底截止（设计 6.2）两键加入之前
// 写入的存量行，parseStoredConfig 必须保持键缺失形态交给 normalize 按缺省
// 默认生效（120/300）；显式 0/越界仍拒绝，NULL/空串/非法 JSON/缺
// normalRoutingConfig 键分支维持原语义。

import (
	"database/sql"
	"testing"
)

// legacySpeedFirstStoredJSON 是存量行原文形态：speedFirstConfig 不含
// totalTimeDeadlineSeconds / compactionTotalTimeDeadlineSeconds 两个键。
const legacySpeedFirstStoredJSON = `{"normalRoutingConfig":{"schedulingPreference":"speed_first","firstByteDeadlineMs":30000,"speedFirstConfig":{"slowTriggerCount":3,"slowWindowSeconds":90,"recoverySuccessCount":3,"probeIntervalSeconds":30,"degradedTtlSeconds":300,"maxFirstByteRetriesPerRequest":2}}}`

func TestLegacySpeedFirstStoredRowFallsBackToDefaults(t *testing.T) {
	config, err := parseStoredConfig(sql.NullString{String: legacySpeedFirstStoredJSON, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	if config == nil {
		t.Fatal("config must not be nil")
	}
	if config.SchedulingPreference != "speed_first" {
		t.Fatalf("SchedulingPreference=%q", config.SchedulingPreference)
	}
	if config.FirstByteDeadlineMs == nil || *config.FirstByteDeadlineMs != 30_000 {
		t.Fatalf("FirstByteDeadlineMs=%v", config.FirstByteDeadlineMs)
	}
	speedFirst := config.SpeedFirstConfig
	if speedFirst == nil {
		t.Fatal("SpeedFirstConfig must not be nil")
	}
	if speedFirst.TotalTimeDeadlineSeconds != defaultSpeedFirstTotalTimeSeconds {
		t.Fatalf("TotalTimeDeadlineSeconds=%d", speedFirst.TotalTimeDeadlineSeconds)
	}
	if speedFirst.CompactionTotalTimeDeadlineSeconds != defaultSpeedFirstCompactionTotalTimeSeconds {
		t.Fatalf("CompactionTotalTimeDeadlineSeconds=%d", speedFirst.CompactionTotalTimeDeadlineSeconds)
	}
	// 其余旋钮等于存量传入值，不被缺省回退改写。
	if speedFirst.SlowTriggerCount != 3 || speedFirst.SlowWindowSeconds != 90 ||
		speedFirst.RecoverySuccessCount != 3 || speedFirst.ProbeIntervalSeconds != 30 ||
		speedFirst.DegradedTtlSeconds != 300 || speedFirst.MaxFirstByteRetriesPerRequest != 2 {
		t.Fatalf("speedFirst=%+v", speedFirst)
	}
}

func TestLegacySpeedFirstStoredRowExplicitZeroStillRejected(t *testing.T) {
	cases := []struct {
		name    string
		stored  string
		message string
	}{
		{
			name:    "普通档显式 0",
			stored:  `{"normalRoutingConfig":{"schedulingPreference":"speed_first","speedFirstConfig":{"totalTimeDeadlineSeconds":0}}}`,
			message: "请求总时间截止必须是 60-270 秒",
		},
		{
			name:    "压缩档显式 0",
			stored:  `{"normalRoutingConfig":{"schedulingPreference":"speed_first","speedFirstConfig":{"compactionTotalTimeDeadlineSeconds":0}}}`,
			message: "压缩总时间截止必须是 300-900 秒",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config, err := parseStoredConfig(sql.NullString{String: testCase.stored, Valid: true})
			if err == nil || err.Error() != testCase.message {
				t.Fatalf("err=%v", err)
			}
			if config != nil {
				t.Fatalf("config=%+v", config)
			}
		})
	}
}

func TestLegacySpeedFirstParseNilArms(t *testing.T) {
	cases := []struct {
		name string
		raw  sql.NullString
	}{
		{"NULL", sql.NullString{}},
		{"空串", sql.NullString{String: "", Valid: true}},
		{"顶层缺 normalRoutingConfig 键", sql.NullString{String: "{}", Valid: true}},
		{"normalRoutingConfig 为 null", sql.NullString{String: `{"normalRoutingConfig":null}`, Valid: true}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config, err := parseStoredConfig(testCase.raw)
			if err != nil || config != nil {
				t.Fatalf("config=%+v err=%v", config, err)
			}
		})
	}
}

func TestLegacySpeedFirstParseBrokenJSON(t *testing.T) {
	config, err := parseStoredConfig(sql.NullString{String: "{broken", Valid: true})
	if err == nil || err.Error() != "策略路由配置无效" {
		t.Fatalf("err=%v", err)
	}
	if config != nil {
		t.Fatalf("config=%+v", config)
	}
}
