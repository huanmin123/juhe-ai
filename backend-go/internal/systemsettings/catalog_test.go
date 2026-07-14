package systemsettings

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type expectedDefinition struct {
	Key     string
	Kind    ValueKind
	Minimum int
	Maximum int
}

var expectedDefinitions = []expectedDefinition{
	{Key: "gatewayTextRawBodyLimitMegabytes", Kind: ValueKindInteger, Minimum: 1, Maximum: 64},
	{Key: "systemApiRateLimitIpReadPerMinute", Kind: ValueKindInteger, Minimum: 0, Maximum: 1_000_000},
	{Key: "systemApiRateLimitIpReadBurstPer10Seconds", Kind: ValueKindInteger, Minimum: 0, Maximum: 1_000_000},
	{Key: "systemApiRateLimitIpWritePerMinute", Kind: ValueKindInteger, Minimum: 0, Maximum: 1_000_000},
	{Key: "systemApiRateLimitIpWriteBurstPer10Seconds", Kind: ValueKindInteger, Minimum: 0, Maximum: 1_000_000},
	{Key: "systemApiRateLimitUserReadPerMinute", Kind: ValueKindInteger, Minimum: 0, Maximum: 1_000_000},
	{Key: "systemApiRateLimitUserWritePerMinute", Kind: ValueKindInteger, Minimum: 0, Maximum: 1_000_000},
	{Key: "defaultTemporaryUnschedulableMinutes", Kind: ValueKindInteger, Minimum: 1, Maximum: 1440},
	{Key: "temporaryUnschedulableRetryIntervalSeconds", Kind: ValueKindInteger, Minimum: 0, Maximum: 3600},
	{Key: "temporaryUnschedulableRetryAttempts", Kind: ValueKindInteger, Minimum: 0, Maximum: 10},
	{Key: "streamRequestTimeoutSeconds", Kind: ValueKindInteger, Minimum: 10, Maximum: 3600},
	{Key: "streamIdleTimeoutSeconds", Kind: ValueKindInteger, Minimum: 1, Maximum: 3600},
	{Key: "streamClientTotalWaitTimeoutSeconds", Kind: ValueKindInteger, Minimum: 10, Maximum: 3600},
	{Key: "streamMaxLifetimeSeconds", Kind: ValueKindInteger, Minimum: 60, Maximum: 86400},
	{Key: "streamFailureThresholdCount", Kind: ValueKindInteger, Minimum: 1, Maximum: 100},
	{Key: "streamFailureThresholdWindowMinutes", Kind: ValueKindInteger, Minimum: 1, Maximum: 1440},
	{Key: "operationLogRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 3650},
	{Key: "operationLogMaxChangesPerRecord", Kind: ValueKindInteger, Minimum: 1, Maximum: 500},
	{Key: "statsAggregationIntervalSeconds", Kind: ValueKindInteger, Minimum: 5, Maximum: 3600},
	{Key: "statsAggregationBatchSize", Kind: ValueKindInteger, Minimum: 100, Maximum: 10000},
	{Key: "statsAggregationMaxBatchesPerRun", Kind: ValueKindInteger, Minimum: 1, Maximum: 100},
	{Key: "usageHotWindowRefreshIntervalSeconds", Kind: ValueKindInteger, Minimum: 60, Maximum: 3600},
	{Key: "groupAccountStatsRefreshIntervalSeconds", Kind: ValueKindInteger, Minimum: 5, Maximum: 3600},
	{Key: "systemMetricsSampleIntervalSeconds", Kind: ValueKindInteger, Minimum: 5, Maximum: 3600},
	{Key: "tableMonitorMaxTablesPerRun", Kind: ValueKindInteger, Minimum: 0, Maximum: 100},
	{Key: "accountQualityRefreshIntervalSeconds", Kind: ValueKindInteger, Minimum: 60, Maximum: 3600},
	{Key: "accountQualityWindowMinutes", Kind: ValueKindInteger, Minimum: 1, Maximum: 60},
	{Key: "accountTestTaskConcurrency", Kind: ValueKindInteger, Minimum: 1, Maximum: 1000},
	{Key: "accountHealthCheckIntervalHours", Kind: ValueKindInteger, Minimum: 1, Maximum: 168},
	{Key: "accountHealthCheckJitterMinutes", Kind: ValueKindInteger, Minimum: 0, Maximum: 1440},
	{Key: "accountHealthCheckBatchSize", Kind: ValueKindInteger, Minimum: 1, Maximum: 100},
	{Key: "accountHealthCheckFailureThreshold", Kind: ValueKindInteger, Minimum: 1, Maximum: 10},
	{Key: "cooldownAccountRetestIntervalSeconds", Kind: ValueKindInteger, Minimum: 1, Maximum: 3600},
	{Key: "cooldownAccountRetestBatchSize", Kind: ValueKindInteger, Minimum: 1, Maximum: 100},
	{Key: "cooldownAccountRetestMaxBackoffHours", Kind: ValueKindInteger, Minimum: 1, Maximum: 720},
	{Key: "oauthAccessTokenRefreshIntervalSeconds", Kind: ValueKindInteger, Minimum: 10, Maximum: 3600},
	{Key: "oauthAccessTokenRefreshLeadSeconds", Kind: ValueKindInteger, Minimum: 60, Maximum: 86400},
	{Key: "oauthAccessTokenRefreshBatchSize", Kind: ValueKindInteger, Minimum: 1, Maximum: 200},
	{Key: "oauthAccessTokenRefreshRetryBackoffSeconds", Kind: ValueKindInteger, Minimum: 0, Maximum: 86400},
	{Key: "modelCheckRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 365},
	{Key: "runtimeLogIndexRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 90},
	{Key: "publicApiLogRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 365},
	{Key: "usageRecordRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 180},
	{Key: UsageStatsTimezoneKey, Kind: ValueKindTimezone},
	{Key: "usageStatsMinuteRetentionHours", Kind: ValueKindInteger, Minimum: 1, Maximum: 336},
	{Key: "usageStatsHourlyRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 180},
	{Key: "usageStatsDailyRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 800},
	{Key: "usageStatsWeeklyRetentionWeeks", Kind: ValueKindInteger, Minimum: 1, Maximum: 260},
	{Key: "usageStatsMonthlyRetentionMonths", Kind: ValueKindInteger, Minimum: 1, Maximum: 60},
	{Key: "usageRankSnapshotRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 365},
	{Key: "systemMetricsRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 7},
	{Key: "systemMetricsHourlyRetentionDays", Kind: ValueKindInteger, Minimum: 1, Maximum: 30},
}

func TestCatalogMatchesNodeSystemSettingKeysAndRanges(t *testing.T) {
	if len(expectedDefinitions) != 52 {
		t.Fatalf("expected definition fixture count = %d, want 52", len(expectedDefinitions))
	}

	keys := make([]string, 0, len(expectedDefinitions))
	integerCount := 0
	for _, definition := range expectedDefinitions {
		keys = append(keys, definition.Key)
		if definition.Kind == ValueKindInteger {
			integerCount++
		}
	}
	gotDefinitions := Definitions()
	if len(gotDefinitions) != len(expectedDefinitions) {
		t.Fatalf("Definitions() length = %d, want %d", len(gotDefinitions), len(expectedDefinitions))
	}
	for index, want := range expectedDefinitions {
		got := gotDefinitions[index]
		if got.Key != want.Key || got.Kind != want.Kind || got.Minimum != want.Minimum || got.Maximum != want.Maximum {
			t.Fatalf("Definitions()[%d] = %+v, want %+v", index, got, want)
		}
	}
	if integerCount != 51 {
		t.Fatalf("integer definition count = %d, want 51", integerCount)
	}
	if got := Keys(); !reflect.DeepEqual(got, keys) {
		t.Fatalf("Keys() = %#v, want %#v", got, keys)
	}
	if !IsKey("usageHotWindowRefreshIntervalSeconds") {
		t.Fatal("catalog is missing usageHotWindowRefreshIntervalSeconds")
	}
	if IsKey("gptPriorityPriceMultiplier") || IsKey("gptFlexPriceMultiplier") {
		t.Fatal("catalog must not expose removed GPT service tier price multiplier settings")
	}

	definitionsCopy := Definitions()
	definitionsCopy[0].Key = "mutated"
	if Definitions()[0].Key != "gatewayTextRawBodyLimitMegabytes" {
		t.Fatal("Definitions() exposed mutable catalog storage")
	}
	keysCopy := Keys()
	keysCopy[0] = "mutated"
	if Keys()[0] != "gatewayTextRawBodyLimitMegabytes" {
		t.Fatal("Keys() exposed mutable catalog storage")
	}
}

func TestIntegerDefinitionsAcceptBothBoundsAndRejectOutsideBounds(t *testing.T) {
	for _, definition := range Definitions() {
		if definition.Kind != ValueKindInteger {
			continue
		}
		t.Run(definition.Key, func(t *testing.T) {
			for _, value := range []int{definition.Minimum, definition.Maximum} {
				patch, err := NewPatch(map[string]json.RawMessage{
					definition.Key: json.RawMessage(strconv.Itoa(value)),
				})
				if err != nil {
					t.Fatalf("NewPatch(%d) error = %v", value, err)
				}
				raw, ok := patch.Value(definition.Key)
				if !ok || string(raw) != strconv.Itoa(value) {
					t.Fatalf("normalized value = %q, %v; want %d", raw, ok, value)
				}
			}
			for _, value := range []int{definition.Minimum - 1, definition.Maximum + 1} {
				if _, err := NewPatch(map[string]json.RawMessage{
					definition.Key: json.RawMessage(strconv.Itoa(value)),
				}); err == nil {
					t.Fatalf("NewPatch(%d) error = nil, want range error", value)
				}
			}
		})
	}
}

func TestCurrentNodeDefaultsFormValidCompleteSnapshot(t *testing.T) {
	defaults := currentNodeDefaultValues()
	if len(defaults) != 52 {
		t.Fatalf("default count = %d, want 52", len(defaults))
	}
	snapshot, err := NewSnapshot(defaults)
	if err != nil {
		t.Fatalf("NewSnapshot(current defaults) error = %v", err)
	}
	if snapshot.Len() != 52 {
		t.Fatalf("snapshot length = %d, want 52", snapshot.Len())
	}
	value, ok := snapshot.Value("usageHotWindowRefreshIntervalSeconds")
	if !ok || string(value) != "600" {
		t.Fatalf("usage hot window default = %q, %v; want 600", value, ok)
	}
}

func TestPatchRejectsNonIntegerJSONWithoutCoercion(t *testing.T) {
	for _, raw := range []json.RawMessage{
		nil,
		json.RawMessage(``),
		json.RawMessage(`null`),
		json.RawMessage(`true`),
		json.RawMessage(`"1"`),
		json.RawMessage(`1.0`),
		json.RawMessage(`1e1`),
		json.RawMessage(`[]`),
		json.RawMessage(`{}`),
		json.RawMessage(`1 2`),
	} {
		_, err := NewPatch(map[string]json.RawMessage{
			"gatewayTextRawBodyLimitMegabytes": raw,
		})
		if err == nil {
			t.Fatalf("NewPatch(%q) error = nil, want strict integer error", raw)
		}
	}
}

func TestTimezoneRequiresNonEmptyLoadableIANANameAndNormalizesWhitespace(t *testing.T) {
	patch, err := NewPatch(map[string]json.RawMessage{
		UsageStatsTimezoneKey: json.RawMessage(`"  Asia/Shanghai  "`),
	})
	if err != nil {
		t.Fatalf("NewPatch(valid timezone) error = %v", err)
	}
	value, _ := patch.Value(UsageStatsTimezoneKey)
	if string(value) != `"Asia/Shanghai"` {
		t.Fatalf("normalized timezone = %s, want %q", value, `"Asia/Shanghai"`)
	}

	for _, raw := range []json.RawMessage{
		json.RawMessage(`"asia/shanghai"`),
		json.RawMessage(`"US/Pacific-New"`),
	} {
		if _, err := NewPatch(map[string]json.RawMessage{UsageStatsTimezoneKey: raw}); err != nil {
			t.Fatalf("NewPatch(%s) error = %v, want Node-compatible timezone acceptance", raw, err)
		}
	}

	for _, raw := range []json.RawMessage{
		json.RawMessage(`null`),
		json.RawMessage(`123`),
		json.RawMessage(`""`),
		json.RawMessage(`"   "`),
		json.RawMessage(`"Local"`),
		json.RawMessage(`"local"`),
		json.RawMessage(`"Factory"`),
		json.RawMessage(`"factory"`),
		json.RawMessage(`"Not/A_Real_Zone"`),
	} {
		_, err := NewPatch(map[string]json.RawMessage{UsageStatsTimezoneKey: raw})
		if err == nil {
			t.Fatalf("NewPatch(%s) error = nil, want timezone error", raw)
		}
		var validationErr *ValidationError
		if !errors.As(err, &validationErr) || validationErr.Key != UsageStatsTimezoneKey {
			t.Fatalf("NewPatch(%s) error = %v, want usageStatsTimezone ValidationError", raw, err)
		}
	}
}

func TestSnapshotRejectsMissingUnknownDuplicateAndInvalidCurrentValues(t *testing.T) {
	valid := currentNodeDefaultValues()

	missing := cloneTestValues(valid)
	delete(missing, "accountTestTaskConcurrency")
	if _, err := NewSnapshot(missing); err == nil || !strings.Contains(err.Error(), "accountTestTaskConcurrency") {
		t.Fatalf("NewSnapshot(missing) error = %v", err)
	}

	unknown := cloneTestValues(valid)
	unknown["unknownSetting"] = json.RawMessage(`1`)
	if _, err := NewSnapshot(unknown); err == nil || !strings.Contains(err.Error(), "unknownSetting") {
		t.Fatalf("NewSnapshot(unknown) error = %v", err)
	}

	entries := make([]Entry, 0, len(valid)+1)
	for key, value := range valid {
		entries = append(entries, Entry{Key: key, Value: value})
	}
	entries = append(entries, Entry{Key: "accountTestTaskConcurrency", Value: json.RawMessage(`100`)})
	if _, err := NewSnapshotFromEntries(entries); err == nil || !strings.Contains(err.Error(), "字段重复") {
		t.Fatalf("NewSnapshotFromEntries(duplicate) error = %v", err)
	}

	invalid := cloneTestValues(valid)
	invalid[UsageStatsTimezoneKey] = json.RawMessage(`"Invalid/Timezone"`)
	if _, err := NewSnapshot(invalid); err == nil || !strings.Contains(err.Error(), UsageStatsTimezoneKey) {
		t.Fatalf("NewSnapshot(invalid timezone) error = %v", err)
	}
}

func TestSnapshotAndPatchCloneRawMessagesAndUseStableJSONKeyOrder(t *testing.T) {
	values := currentNodeDefaultValues()
	original := values["gatewayTextRawBodyLimitMegabytes"]
	snapshot, err := NewSnapshot(values)
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	original[0] = '9'
	values["gatewayTextRawBodyLimitMegabytes"] = json.RawMessage(`64`)
	raw, _ := snapshot.Value("gatewayTextRawBodyLimitMegabytes")
	if string(raw) != "16" {
		t.Fatalf("snapshot changed through constructor input: %s", raw)
	}
	raw[0] = '9'
	rawAgain, _ := snapshot.Value("gatewayTextRawBodyLimitMegabytes")
	if string(rawAgain) != "16" {
		t.Fatalf("snapshot changed through Value() output: %s", rawAgain)
	}
	cloned := snapshot.Clone()
	clonedValues := cloned.Values()
	clonedValues["gatewayTextRawBodyLimitMegabytes"][0] = '9'
	rawAgain, _ = snapshot.Value("gatewayTextRawBodyLimitMegabytes")
	if string(rawAgain) != "16" {
		t.Fatalf("snapshot changed through clone values: %s", rawAgain)
	}

	patch, err := NewPatch(map[string]json.RawMessage{
		"systemMetricsHourlyRetentionDays":     json.RawMessage(`20`),
		"gatewayTextRawBodyLimitMegabytes":     json.RawMessage(`32`),
		"usageHotWindowRefreshIntervalSeconds": json.RawMessage(`600`),
	})
	if err != nil {
		t.Fatalf("NewPatch() error = %v", err)
	}
	entries := patch.Entries()
	gotKeys := []string{entries[0].Key, entries[1].Key, entries[2].Key}
	wantKeys := []string{
		"gatewayTextRawBodyLimitMegabytes",
		"systemMetricsHourlyRetentionDays",
		"usageHotWindowRefreshIntervalSeconds",
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("patch entry order = %#v, want %#v", gotKeys, wantKeys)
	}
	encoded, err := json.Marshal(patch)
	if err != nil {
		t.Fatalf("json.Marshal(patch) error = %v", err)
	}
	const wantJSON = `{"gatewayTextRawBodyLimitMegabytes":32,"systemMetricsHourlyRetentionDays":20,"usageHotWindowRefreshIntervalSeconds":600}`
	if string(encoded) != wantJSON {
		t.Fatalf("patch JSON = %s, want %s", encoded, wantJSON)
	}

	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("json.Marshal(snapshot) error = %v", err)
	}
	firstKey := `"accountHealthCheckBatchSize"`
	lastKey := `"usageStatsWeeklyRetentionWeeks"`
	if !strings.HasPrefix(string(snapshotJSON), "{"+firstKey+":") {
		t.Fatalf("snapshot JSON does not start with stable first key: %s", snapshotJSON)
	}
	const stableSequence = `"gatewayTextRawBodyLimitMegabytes":16,"groupAccountStatsRefreshIntervalSeconds":60`
	if !strings.Contains(string(snapshotJSON), stableSequence) {
		t.Fatalf("snapshot JSON key order is unstable: %s", snapshotJSON)
	}
	if !strings.Contains(string(snapshotJSON), ","+lastKey+":104}") {
		t.Fatalf("snapshot JSON does not end with stable last key: %s", snapshotJSON)
	}
}

func TestSnapshotApplyReturnsValidatedIndependentSnapshot(t *testing.T) {
	before, err := NewSnapshot(currentNodeDefaultValues())
	if err != nil {
		t.Fatalf("NewSnapshot() error = %v", err)
	}
	patch, err := NewPatch(map[string]json.RawMessage{
		"gatewayTextRawBodyLimitMegabytes": json.RawMessage(`32`),
	})
	if err != nil {
		t.Fatalf("NewPatch() error = %v", err)
	}
	after, err := before.Apply(patch)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	beforeValue, _ := before.Value("gatewayTextRawBodyLimitMegabytes")
	afterValue, _ := after.Value("gatewayTextRawBodyLimitMegabytes")
	if string(beforeValue) != "16" || string(afterValue) != "32" {
		t.Fatalf("before=%s after=%s, want 16 and 32", beforeValue, afterValue)
	}
}

func TestPatchRejectsEmptyAndUnknownFieldsDeterministically(t *testing.T) {
	if _, err := NewPatch(nil); !errors.Is(err, ErrPatchEmpty) {
		t.Fatalf("NewPatch(nil) error = %v, want %v", err, ErrPatchEmpty)
	}
	_, err := NewPatch(map[string]json.RawMessage{
		"zUnknown": json.RawMessage(`1`),
		"aUnknown": json.RawMessage(`1`),
	})
	if err == nil || !strings.Contains(err.Error(), "aUnknown") {
		t.Fatalf("NewPatch(unknowns) error = %v, want stable aUnknown error", err)
	}
}

func currentNodeDefaultValues() map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"gatewayTextRawBodyLimitMegabytes":           json.RawMessage(`16`),
		"systemApiRateLimitIpReadPerMinute":          json.RawMessage(`600`),
		"systemApiRateLimitIpReadBurstPer10Seconds":  json.RawMessage(`120`),
		"systemApiRateLimitIpWritePerMinute":         json.RawMessage(`180`),
		"systemApiRateLimitIpWriteBurstPer10Seconds": json.RawMessage(`40`),
		"systemApiRateLimitUserReadPerMinute":        json.RawMessage(`300`),
		"systemApiRateLimitUserWritePerMinute":       json.RawMessage(`120`),
		"defaultTemporaryUnschedulableMinutes":       json.RawMessage(`2`),
		"temporaryUnschedulableRetryIntervalSeconds": json.RawMessage(`3`),
		"temporaryUnschedulableRetryAttempts":        json.RawMessage(`3`),
		"streamRequestTimeoutSeconds":                json.RawMessage(`120`),
		"streamIdleTimeoutSeconds":                   json.RawMessage(`30`),
		"streamClientTotalWaitTimeoutSeconds":        json.RawMessage(`270`),
		"streamMaxLifetimeSeconds":                   json.RawMessage(`1800`),
		"streamFailureThresholdCount":                json.RawMessage(`3`),
		"streamFailureThresholdWindowMinutes":        json.RawMessage(`5`),
		"operationLogRetentionDays":                  json.RawMessage(`365`),
		"operationLogMaxChangesPerRecord":            json.RawMessage(`100`),
		"statsAggregationIntervalSeconds":            json.RawMessage(`60`),
		"statsAggregationBatchSize":                  json.RawMessage(`2000`),
		"statsAggregationMaxBatchesPerRun":           json.RawMessage(`5`),
		"usageHotWindowRefreshIntervalSeconds":       json.RawMessage(`600`),
		"groupAccountStatsRefreshIntervalSeconds":    json.RawMessage(`60`),
		"systemMetricsSampleIntervalSeconds":         json.RawMessage(`30`),
		"tableMonitorMaxTablesPerRun":                json.RawMessage(`4`),
		"accountQualityRefreshIntervalSeconds":       json.RawMessage(`600`),
		"accountQualityWindowMinutes":                json.RawMessage(`10`),
		"accountTestTaskConcurrency":                 json.RawMessage(`100`),
		"accountHealthCheckIntervalHours":            json.RawMessage(`12`),
		"accountHealthCheckJitterMinutes":            json.RawMessage(`120`),
		"accountHealthCheckBatchSize":                json.RawMessage(`20`),
		"accountHealthCheckFailureThreshold":         json.RawMessage(`3`),
		"cooldownAccountRetestIntervalSeconds":       json.RawMessage(`3`),
		"cooldownAccountRetestBatchSize":             json.RawMessage(`10`),
		"cooldownAccountRetestMaxBackoffHours":       json.RawMessage(`12`),
		"oauthAccessTokenRefreshIntervalSeconds":     json.RawMessage(`60`),
		"oauthAccessTokenRefreshLeadSeconds":         json.RawMessage(`300`),
		"oauthAccessTokenRefreshBatchSize":           json.RawMessage(`20`),
		"oauthAccessTokenRefreshRetryBackoffSeconds": json.RawMessage(`300`),
		"modelCheckRetentionDays":                    json.RawMessage(`30`),
		"runtimeLogIndexRetentionDays":               json.RawMessage(`14`),
		"publicApiLogRetentionDays":                  json.RawMessage(`30`),
		"usageRecordRetentionDays":                   json.RawMessage(`30`),
		UsageStatsTimezoneKey:                        json.RawMessage(`"UTC"`),
		"usageStatsMinuteRetentionHours":             json.RawMessage(`48`),
		"usageStatsHourlyRetentionDays":              json.RawMessage(`60`),
		"usageStatsDailyRetentionDays":               json.RawMessage(`400`),
		"usageStatsWeeklyRetentionWeeks":             json.RawMessage(`104`),
		"usageStatsMonthlyRetentionMonths":           json.RawMessage(`24`),
		"usageRankSnapshotRetentionDays":             json.RawMessage(`30`),
		"systemMetricsRetentionDays":                 json.RawMessage(`7`),
		"systemMetricsHourlyRetentionDays":           json.RawMessage(`30`),
	}
}

func cloneTestValues(values map[string]json.RawMessage) map[string]json.RawMessage {
	output := make(map[string]json.RawMessage, len(values))
	for key, value := range values {
		output[key] = append(json.RawMessage(nil), value...)
	}
	return output
}
