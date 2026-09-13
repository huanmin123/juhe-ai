package auditlog

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWINodeUTF16StringMarshalJSON(t *testing.T) {
	// 契约：Node 风格 JSON 字符串序列化，保留 UTF-16 语义（代理对、控制字符转义）。
	// nodeUTF16String 是 []uint16，辅助函数按 rune 转换。
	toUnits := func(text string) nodeUTF16String {
		units := make([]uint16, 0, len(text))
		for _, r := range text {
			units = append(units, uint16(r))
		}
		return units
	}
	cases := []struct {
		name  string
		value nodeUTF16String
		want  string
	}{
		{"空串", nodeUTF16String{}, `""`},
		{"普通文本", toUnits("hello"), `"hello"`},
		{"反斜杠", toUnits(`a\b`), `"a\\b"`},
		{"双引号", toUnits(`a"b`), `"a\"b"`},
		{"控制字符", toUnits("a\x01b"), `"a\u0001b"`},
		{"tab", toUnits("a\tb"), `"a\tb"`},
		{"换行", toUnits("a\nb"), `"a\nb"`},
		{"UTF8 中文", toUnits("中文"), `"中文"`},
		{"孤立代理项", nodeUTF16String([]uint16{0xD800}), `"\ud800"`},
	}
	for _, tc := range cases {
		encoded, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatalf("%s 编码失败: %v", tc.name, err)
		}
		if string(encoded) != tc.want {
			t.Fatalf("%s=%s want %s", tc.name, encoded, tc.want)
		}
	}
}

func TestWIDerivedErrorGroup(t *testing.T) {
	input := fixture("wi-group", LifecycleFinalized)
	input.AuditOutcome = AuditOutcomeUpstreamFailed
	input.Success = false
	input.ErrorCode = "E_UPSTREAM"
	code := 502
	input.FinalStatusCode = &code
	input.Attempts = []AuditLogAttemptInput{{
		AttemptIndex: 1, AccountID: "acc-1", Success: &[]bool{false}[0], UpstreamStatusCode: &code,
	}}
	input.Payloads = []AuditLogPayloadInput{{
		PartType:   PayloadPartClientRequest,
		BodySHA256: "sha-1",
	}}
	group, err := derivedErrorGroup(input, nil)
	if err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	if group.fingerprint == "" || group.requestFingerprint == "" || group.errorFingerprint == "" {
		t.Fatalf("group=%+v", group)
	}
	// 窗口按 5 分钟 UTC 对齐。
	when, _ := time.Parse(time.RFC3339Nano, input.CreatedAt)
	windowStart := when.UTC().Truncate(auditErrorGroupWindow)
	// Node ISO 格式固定毫秒精度。
	if group.windowStartedAt != nodeISOString(windowStart) {
		t.Fatalf("windowStart=%q", group.windowStartedAt)
	}
	if group.windowEndedAt != nodeISOString(windowStart.Add(auditErrorGroupWindow)) {
		t.Fatalf("windowEnd=%q", group.windowEndedAt)
	}
	// createdAt 非法必须报错。
	bad := input
	bad.CreatedAt = "not-a-time"
	if _, err := derivedErrorGroup(bad, nil); err == nil {
		t.Fatal("坏 createdAt 必须报错")
	}
}

func TestWIMarshalNodeHeadersNil(t *testing.T) {
	// nil map 经 JSON 编码为 null（Node JSON.stringify 语义）。
	encoded, err := marshalNodeHeaders(nil)
	if err != nil || string(encoded) != "null" {
		t.Fatalf("nil headers=%s err=%v", encoded, err)
	}
	encoded, err = marshalNodeHeaders(map[string]HeaderValues{"X-K": {Values: []string{"v"}}})
	if err != nil || string(encoded) != `{"X-K":"v"}` {
		t.Fatalf("headers=%s err=%v", encoded, err)
	}
}

func TestWINullableTimePostgresBranch(t *testing.T) {
	// postgres 模式返回 time.Time，SQLite 返回归一化文本。
	utc := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	value := utc.Format(time.RFC3339Nano)
	if _, isTime := nullableTime(ModePostgres, value).(time.Time); !isTime {
		t.Fatal("postgres nullableTime 必须是 time.Time")
	}
	if got := nullableTime(ModeSQLite, value); got == nil {
		t.Fatal("sqlite nullableTime 必须非 nil")
	}
}
