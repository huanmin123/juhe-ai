package usagewriter

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixedClock(iso string) Clock {
	parsed, _ := time.Parse(timeRFC3339Millis, iso)
	return ClockFunc(func() time.Time { return parsed })
}

func testIDFactory() UsageRecordIDFactory {
	clock := fixedClock("2026-01-02T03:04:05.000Z")
	return IDFactoryFunc(func(createdAt string) string {
		id, err := GenerateUsageRecordID(clock, createdAt, "0f8fad5b-d9cb-469f-a165-70867728950e", DefaultUsageShardCount)
		if err != nil {
			panic(err)
		}
		return id
	})
}

func TestNormalizeUsageRecordInput(t *testing.T) {
	clock := fixedClock("2026-01-02T03:04:05.000Z")
	tests := []struct {
		name        string
		input       UsageRecordInput
		wantAccID   string
		wantAccTyp  string
		wantGrpID   string
		wantGrpTyp  string
		wantAccAuth string
	}{
		{
			name: "owner account passthrough",
			input: UsageRecordInput{
				TraceID: "t1", TrafficSource: TrafficSourceGateway, Success: true,
				AccountID: " acc1 ", AccountOwnerSystemAccountID: "sys1", AccountAccessType: AccountAccessTypeOwner,
				GroupID: "grp1", GroupOwnerSystemAccountID: "sys1", GroupAccessType: GroupAccessTypeOwner,
			},
			wantAccID: "acc1", wantAccTyp: "owner", wantGrpID: "grp1", wantGrpTyp: "owner",
		},
		{
			name: "account_authorized requires authorization id",
			input: UsageRecordInput{
				TraceID: "t2", TrafficSource: TrafficSourceGateway, Success: true,
				AccountID: "acc2", AccountOwnerSystemAccountID: "sys2", AccountAccessType: AccountAccessTypeAccountAuthorized,
			},
		},
		{
			name: "account_authorized keeps authorization pair",
			input: UsageRecordInput{
				TraceID: "t3", TrafficSource: TrafficSourceGateway, Success: true,
				AccountID: "acc3", AccountOwnerSystemAccountID: "sys3", AccountAccessType: AccountAccessTypeAccountAuthorized,
				AccountAuthorizationID: "authz3", AccountAuthorizationSourceType: AuthorizationSourceTypeTeam,
				AccountAuthorizationSourceTeamID: "team3",
			},
			wantAccID: "acc3", wantAccTyp: "account_authorized", wantAccAuth: "authz3",
		},
		{
			name: "team source without team id clears scope",
			input: UsageRecordInput{
				TraceID: "t4", TrafficSource: TrafficSourceGateway, Success: true,
				AccountID: "acc4", AccountOwnerSystemAccountID: "sys4", AccountAccessType: AccountAccessTypeAccountAuthorized,
				AccountAuthorizationID: "authz4", AccountAuthorizationSourceType: AuthorizationSourceTypeTeam,
			},
		},
		{
			name: "invalid access type clears scope",
			input: UsageRecordInput{
				TraceID: "t5", TrafficSource: TrafficSourceGateway, Success: true,
				AccountID: "acc5", AccountOwnerSystemAccountID: "sys5", AccountAccessType: "superuser",
			},
		},
		{
			name: "group_authorized account without authorized group clears account only",
			input: UsageRecordInput{
				TraceID: "t6", TrafficSource: TrafficSourceGateway, Success: true,
				AccountID: "acc6", AccountOwnerSystemAccountID: "sys6", AccountAccessType: AccountAccessTypeGroupAuthorized,
				GroupID: "grp6", GroupOwnerSystemAccountID: "sys6", GroupAccessType: GroupAccessTypeOwner,
			},
			wantGrpID: "grp6", wantGrpTyp: "owner",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized, err := NormalizeUsageRecordInput(tt.input, clock, testIDFactory())
			if err != nil {
				t.Fatalf("NormalizeUsageRecordInput() error = %v", err)
			}
			if normalized.AccountID != tt.wantAccID || normalized.AccountAccessType != tt.wantAccTyp {
				t.Fatalf("account scope = (%q, %q), want (%q, %q)", normalized.AccountID, normalized.AccountAccessType, tt.wantAccID, tt.wantAccTyp)
			}
			if normalized.GroupID != tt.wantGrpID || normalized.GroupAccessType != tt.wantGrpTyp {
				t.Fatalf("group scope = (%q, %q), want (%q, %q)", normalized.GroupID, normalized.GroupAccessType, tt.wantGrpID, tt.wantGrpTyp)
			}
			if normalized.AccountAuthorizationID != tt.wantAccAuth {
				t.Fatalf("accountAuthorizationID = %q, want %q", normalized.AccountAuthorizationID, tt.wantAccAuth)
			}
			if !strings.HasSuffix(normalized.CreatedAt, "Z") || len(normalized.CreatedAt) != len("2026-01-02T03:04:05.000Z") {
				t.Fatalf("createdAt = %q, want millisecond RFC3339 Z form", normalized.CreatedAt)
			}
			if normalized.ID == "" {
				t.Fatal("expected generated id")
			}
		})
	}
}

func TestNormalizeUsageRecordInputCreatedAt(t *testing.T) {
	clock := fixedClock("2026-01-02T03:04:05.000Z")
	normalized, err := NormalizeUsageRecordInput(UsageRecordInput{TraceID: "t", TrafficSource: TrafficSourceGateway}, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.CreatedAt != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("clock-injected createdAt = %q", normalized.CreatedAt)
	}

	// Offset instants canonicalize to UTC milliseconds.
	normalized, err = NormalizeUsageRecordInput(UsageRecordInput{TraceID: "t", TrafficSource: TrafficSourceGateway, CreatedAt: "2026-01-02T11:04:05+08:00"}, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.CreatedAt != "2026-01-02T03:04:05.000Z" {
		t.Fatalf("offset createdAt = %q", normalized.CreatedAt)
	}

	// Bare date-times are rejected with the Node copy.
	_, err = NormalizeUsageRecordInput(UsageRecordInput{TraceID: "t", TrafficSource: TrafficSourceGateway, CreatedAt: "2026-01-02 03:04:05"}, clock, nil)
	if err == nil || err.Error() != "使用记录 createdAt必须是带 Z 或数值 offset 的 RFC3339 时间" {
		t.Fatalf("invalid createdAt error = %v", err)
	}
}

func TestBoundUsageRecordSnapshot(t *testing.T) {
	tests := []struct {
		name  string
		value any
		check func(t *testing.T, bounded any)
	}{
		{
			name:  "nil passthrough",
			value: nil,
			check: func(t *testing.T, bounded any) {
				if bounded != nil {
					t.Fatalf("want nil, got %v", bounded)
				}
			},
		},
		{
			name:  "long string truncated with Node suffix",
			value: strings.Repeat("a", usageSnapshotMaxStringBytes+100),
			check: func(t *testing.T, bounded any) {
				text, ok := bounded.(string)
				if !ok {
					t.Fatalf("want string, got %T", bounded)
				}
				if !strings.HasSuffix(text, "] bytes]") && !strings.Contains(text, "...[truncated ") {
					t.Fatalf("missing truncation suffix: %q", tail(text, 64))
				}
				if len(text) > usageSnapshotMaxStringBytes+64 {
					t.Fatalf("bounded string too long: %d", len(text))
				}
			},
		},
		{
			name:  "array overflow appends items-truncated marker",
			value: make([]any, usageSnapshotMaxArrayItems+3),
			check: func(t *testing.T, bounded any) {
				items, ok := bounded.([]any)
				if !ok {
					t.Fatalf("want []any, got %T", bounded)
				}
				last := items[len(items)-1]
				text, _ := last.(string)
				if text != "["+itoa(usageSnapshotMaxArrayItems+3-usageSnapshotMaxArrayItems)+" items truncated]" {
					t.Fatalf("truncation marker = %v", last)
				}
			},
		},
		{
			name:  "depth limit marker",
			value: nestMaps(usageSnapshotMaxDepth + 2),
			check: func(t *testing.T, bounded any) {
				if !containsDepthMarker(bounded, 0) {
					t.Fatal("missing depth_truncated marker")
				}
			},
		},
		{
			name:  "circular map guarded",
			value: nil,
			check: func(t *testing.T, bounded any) {},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, BoundUsageRecordSnapshot(tt.value))
		})
	}

	// Circular reference guard.
	circular := map[string]any{}
	circular["self"] = circular
	bounded := BoundUsageRecordSnapshot(circular)
	object, ok := bounded.(*OrderedObject)
	if !ok {
		t.Fatalf("want OrderedObject, got %T", bounded)
	}
	if object.Get("self") != "[circular]" {
		t.Fatalf("circular marker = %v", object.Get("self"))
	}
}

func nestMaps(depth int) map[string]any {
	root := map[string]any{"leaf": 1}
	current := root
	for i := 1; i < depth; i++ {
		next := map[string]any{}
		current["child"] = next
		current = next
	}
	return root
}

func containsDepthMarker(value any, depth int) bool {
	if depth > usageSnapshotMaxDepth+2 {
		return false
	}
	switch typed := value.(type) {
	case *OrderedObject:
		for _, key := range typed.Keys() {
			v := typed.Get(key)
			if text, ok := v.(string); ok && text == "[depth_truncated]" {
				return true
			}
			if containsDepthMarker(v, depth+1) {
				return true
			}
		}
	case map[string]any:
		for _, v := range typed {
			if containsDepthMarker(v, depth+1) {
				return true
			}
		}
	}
	return false
}

func tail(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return text[len(text)-n:]
}

func TestEstimateUsageRecordBytes(t *testing.T) {
	input := UsageRecordInput{TraceID: strings.Repeat("t", 1000), TrafficSource: TrafficSourceGateway}
	bytes := EstimateUsageRecordBytes(input, DefaultQueueMaxBytes)
	if bytes <= 256 {
		t.Fatalf("estimate too small: %d", bytes)
	}
	// The estimate caps at the queue budget + 256 headroom.
	if bytes > DefaultQueueMaxBytes+256 {
		t.Fatalf("estimate beyond budget: %d", bytes)
	}
}

func TestUsageRecordInputJSONRoundTrip(t *testing.T) {
	// The json tags must match the G17/Node wire format so a spooled or
	// forwarded record decodes field-for-field.
	encoded := `{"traceId":"t1","trafficSource":"gateway","success":true,"inputTokens":3,"costUsd":0.25,"createdAt":"2026-01-02T03:04:05.000Z"}`
	var input UsageRecordInput
	if err := json.Unmarshal([]byte(encoded), &input); err != nil {
		t.Fatal(err)
	}
	if input.TraceID != "t1" || input.TrafficSource != TrafficSourceGateway || !input.Success {
		t.Fatalf("decoded = %+v", input)
	}
	if input.InputTokens == nil || *input.InputTokens != 3 || input.CostUsd == nil || *input.CostUsd != 0.25 {
		t.Fatalf("token/cost fields = %+v", input)
	}
}
