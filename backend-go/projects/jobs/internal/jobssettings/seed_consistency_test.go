// 种子一致性对照测试：jobs 回退表 DefaultSystemSettings 必须与 maintenance
// 种子的 system_settings 默认表逐键一致。jobs 与 maintenance 是两个 Go
// module（backend-go-jobs / backend-go-maintenance），无法直接 import schema
// 包常量，因此这里以镜像表复制 maintenance/internal/schema/pg_schema.go 的
// pgSeedSystemSettings（ValueJSON 原文，SQLite 与 PG 种子共用该表）。
//
// 同步义务：任何一侧改动（新增键、删除键、改默认值）都必须同步另一侧，
// 否则缺行/读失败回落的语义与 schema 播种结果漂移（BUG 案例：时区回退
// "UTC" vs 种子 "Asia/Shanghai"）。改镜像表前先读 pg_schema.go 原文，
// 改 DefaultSystemSettings 前先同步种子。
package jobssettings

import (
	"encoding/json"
	"reflect"
	"testing"
)

// maintenanceSeedSystemSettingsJSON 是 pgSeedSystemSettings 的镜像表：
// key -> ValueJSON 原文（JSON 编码字符串；数值键是裸数字，usageStatsTimezone
// 是带引号字符串）。摘自 pg_schema.go 时不得转写值，保持字面一致。
var maintenanceSeedSystemSettingsJSON = map[string]string{
	"gatewayTextRawBodyLimitMegabytes":           "16",
	"accountCircuitConfirmationFailuresRequired": "2",
	"gatewayUserRequestLimitPerMinute":           "0",
	"gatewayUserRequestLimitPerDay":              "0",
	"gatewayUserRequestLimitPerWeek":             "0",
	"gatewayUserRequestLimitPerMonth":            "0",
	"userAiAccountLimit":                         "100",
	"systemApiRateLimitIpReadPerMinute":          "600",
	"systemApiRateLimitIpReadBurstPer10Seconds":  "120",
	"systemApiRateLimitIpWritePerMinute":         "180",
	"systemApiRateLimitIpWriteBurstPer10Seconds": "40",
	"systemApiRateLimitUserReadPerMinute":        "300",
	"systemApiRateLimitUserWritePerMinute":       "120",
	"defaultTemporaryUnschedulableMinutes":       "2",
	"temporaryUnschedulableRetryIntervalSeconds": "3",
	"temporaryUnschedulableRetryAttempts":        "2",
	"textFirstResponseTimeoutSeconds":            "120",
	"textNonStreamFirstResponseTimeoutSeconds":   "600",
	"textStreamIdleTimeoutSeconds":               "30",
	"textUncommittedAttemptMaxLifetimeSeconds":   "1800",
	"imageFirstResponseTimeoutSeconds":           "600",
	"imageStreamIdleTimeoutSeconds":              "120",
	"imageUncommittedAttemptMaxLifetimeSeconds":  "3600",
	"imageRequestWallTimeoutSeconds":             "3600",
	"chatImageGenerationTotalTimeoutSeconds":     "900",
	"noAvailableAccountWaitTimeoutSeconds":       "270",
	"streamFailureThresholdCount":                "3",
	"streamFailureThresholdWindowMinutes":        "5",
	"operationLogRetentionDays":                  "365",
	"operationLogMaxChangesPerRecord":            "100",
	"statsAggregationIntervalSeconds":            "60",
	"statsAggregationBatchSize":                  "2000",
	"statsAggregationMaxBatchesPerRun":           "5",
	"usageHotWindowRefreshIntervalSeconds":       "600",
	"groupAccountStatsRefreshIntervalSeconds":    "60",
	"systemMetricsSampleIntervalSeconds":         "30",
	"tableMonitorMaxTablesPerRun":                "4",
	"accountQualityRefreshIntervalSeconds":       "600",
	"accountQualityWindowMinutes":                "10",
	"accountHealthCheckIntervalHours":            "1",
	"accountHealthCheckJitterMinutes":            "10",
	"accountHealthCheckFailureThreshold":         "3",
	"cooldownAccountRetestIntervalSeconds":       "3",
	"cooldownAccountRetestMaxBackoffHours":       "12",
	"oauthAccessTokenRefreshIntervalSeconds":     "60",
	"oauthAccessTokenRefreshLeadSeconds":         "300",
	"oauthAccessTokenRefreshBatchSize":           "20",
	"oauthAccessTokenRefreshRetryBackoffSeconds": "300",
	"modelCheckRetentionDays":                    "30",
	"runtimeLogIndexRetentionDays":               "14",
	"publicApiLogRetentionDays":                  "30",
	"usageRecordRetentionDays":                   "30",
	"usageStatsTimezone":                         `"Asia/Shanghai"`,
	"usageStatsMinuteRetentionHours":             "48",
	"usageStatsHourlyRetentionDays":              "60",
	"usageStatsDailyRetentionDays":               "400",
	"usageStatsWeeklyRetentionWeeks":             "104",
	"usageStatsMonthlyRetentionMonths":           "24",
	"usageRankSnapshotRetentionDays":             "30",
	"systemMetricsRetentionDays":                 "7",
	"systemMetricsHourlyRetentionDays":           "30",
}

// TestDefaultSystemSettingsMatchMaintenanceSeed 逐键对照：键集合与解码后的值
// 都必须一致（数值键解出 float64，字符串键解出 string）。任一侧漂移即失败。
func TestDefaultSystemSettingsMatchMaintenanceSeed(t *testing.T) {
	if len(maintenanceSeedSystemSettingsJSON) != 61 {
		t.Fatalf("镜像表键数=%d，want 61（与 pgSeedSystemSettings 一致）", len(maintenanceSeedSystemSettingsJSON))
	}
	seeded := make(map[string]any, len(maintenanceSeedSystemSettingsJSON))
	for key, valueJSON := range maintenanceSeedSystemSettingsJSON {
		var value any
		if err := json.Unmarshal([]byte(valueJSON), &value); err != nil {
			t.Fatalf("镜像表 %s 的 ValueJSON 非法: %v", key, err)
		}
		seeded[key] = value
	}
	if !reflect.DeepEqual(DefaultSystemSettings, seeded) {
		for key, seedValue := range seeded {
			fallbackValue, ok := DefaultSystemSettings[key]
			if !ok {
				t.Errorf("回退表缺少种子键 %s（种子值 %v）", key, seedValue)
				continue
			}
			if !reflect.DeepEqual(fallbackValue, seedValue) {
				t.Errorf("键 %s 回退值 %v 与种子值 %v 不一致", key, fallbackValue, seedValue)
			}
		}
		for key := range DefaultSystemSettings {
			if _, ok := seeded[key]; !ok {
				t.Errorf("回退表多出种子不存在的键 %s", key)
			}
		}
		t.Fatal("DefaultSystemSettings 与 maintenance 种子漂移，见上方逐键差异")
	}
}
