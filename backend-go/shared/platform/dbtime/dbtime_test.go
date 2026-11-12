package dbtime

import (
	"testing"
	"time"
)

func TestAPIFormatting(t *testing.T) {
	instant := time.Date(2026, 11, 11, 8, 30, 4, 123456789, time.UTC)
	if got := API(instant); got != "2026-11-11T08:30:04.123Z" {
		t.Fatalf("API=%s", got)
	}
	if got := API6(instant); got != "2026-11-11T08:30:04.123456Z" {
		t.Fatalf("API6=%s", got)
	}
	if got := DBText(instant); got != "2026-11-11T08:30:04.123456Z" {
		t.Fatalf("DBText=%s", got)
	}
}

func TestParseAPIAcceptsOffsetAndTruncatesMillis(t *testing.T) {
	parsed, ok := ParseAPI("2026-11-11T16:30:04.123456+08:00")
	if !ok {
		t.Fatal("应可解析")
	}
	if got := API(parsed); got != "2026-11-11T08:30:04.123Z" {
		t.Fatalf("API=%s", got)
	}
	if _, ok := ParseAPI("2026-11-11 16:30:04+08:00"); ok {
		t.Fatal("空格分隔必须拒绝")
	}
	if _, ok := ParseAPI("2026-11-11T16:30:04"); ok {
		t.Fatal("无 offset 必须拒绝")
	}
}

func TestParseDBPreservesMicros(t *testing.T) {
	parsed, ok := ParseDB("2026-11-11T16:30:04.123456+08:00")
	if !ok {
		t.Fatal("应可解析")
	}
	if got := API6(parsed); got != "2026-11-11T08:30:04.123456Z" {
		t.Fatalf("API6=%s", got)
	}
	if _, ok := ParseDB(""); ok {
		t.Fatal("空串必须拒绝")
	}
	if _, ok := ParseDB("2026-11-11T16:30:04"); ok {
		t.Fatal("无 offset 必须拒绝")
	}
	if _, ok := ParseDB(" 2026-11-11T16:30:04Z "); ok {
		t.Fatal("前后空白必须拒绝")
	}
}

func TestDBValueTruncates(t *testing.T) {
	instant := time.Date(2026, 11, 11, 8, 30, 4, 123456789, time.UTC)
	if got := DBValue(instant); !got.Equal(time.Date(2026, 11, 11, 8, 30, 4, 123456000, time.UTC)) {
		t.Fatalf("DBValue=%v", got)
	}
	if got := DBValueMillis(instant); !got.Equal(time.Date(2026, 11, 11, 8, 30, 4, 123000000, time.UTC)) {
		t.Fatalf("DBValueMillis=%v", got)
	}
}

func TestDBArg(t *testing.T) {
	got, ok := DBArg("2026-11-11T16:30:04.123456+08:00").(time.Time)
	if !ok || !got.Equal(time.Date(2026, 11, 11, 8, 30, 4, 123000000, time.UTC)) {
		t.Fatalf("DBArg parsed=%v (%T)", got, got)
	}
	if raw := DBArg(""); raw != "" {
		t.Fatalf("DBArg invalid must stay text for explicit DB failure, got %v (%T)", raw, raw)
	}
}

func TestSameInstant(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"same instant different shape", "2026-01-10T08:29:00Z", "2026-01-10T08:29:00.000Z", true},
		{"same millis different offset", "2026-01-10T16:29:00.123+08:00", "2026-01-10T08:29:00.123Z", true},
		{"different millis", "2026-01-10T08:29:00.001Z", "2026-01-10T08:29:00.002Z", false},
		{"invalid falls back to text", "not-a-time", "not-a-time", true},
		{"invalid vs valid", "not-a-time", "2026-01-10T08:29:00Z", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameInstant(tc.a, tc.b); got != tc.want {
				t.Fatalf("SameInstant(%q,%q)=%v want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestNullableScanForms(t *testing.T) {
	instant := time.Date(2026, 11, 11, 8, 30, 4, 123456000, time.UTC)

	cases := []struct {
		name  string
		src   any
		valid bool
		api   string
		api6  string
	}{
		{"nil", nil, false, "", ""},
		{"time", instant.In(time.FixedZone("CST", 8*3600)), true, "2026-11-11T08:30:04.123Z", "2026-11-11T08:30:04.123456Z"},
		{"string", "2026-11-11T16:30:04.123456+08:00", true, "2026-11-11T08:30:04.123Z", "2026-11-11T08:30:04.123456Z"},
		{"bytes", []byte("2026-11-11T08:30:04.123Z"), true, "2026-11-11T08:30:04.123Z", "2026-11-11T08:30:04.123000Z"},
		{"padded", " 2026-11-11T16:30:04.123456+08:00 ", true, "2026-11-11T08:30:04.123Z", "2026-11-11T08:30:04.123456Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var nullable Nullable
			if err := nullable.Scan(tc.src); err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if nullable.Valid != tc.valid {
				t.Fatalf("Valid=%v want %v", nullable.Valid, tc.valid)
			}
			if !tc.valid {
				if nullable.API() != nil || nullable.Ptr() != nil {
					t.Fatal("NULL 必须返回 nil")
				}
				if !nullable.OrZero().IsZero() {
					t.Fatal("NULL OrZero 必须零值")
				}
				return
			}
			if got := nullable.API(); got == nil || *got != tc.api {
				t.Fatalf("API=%v want %s", got, tc.api)
			}
			if got := nullable.API6(); got == nil || *got != tc.api6 {
				t.Fatalf("API6=%v want %s", got, tc.api6)
			}
			if nullable.Ptr() == nil {
				t.Fatal("Ptr 非空")
			}
		})
	}
}

func TestNullableScanRejectsBadValues(t *testing.T) {
	var nullable Nullable
	if err := nullable.Scan(""); err == nil {
		t.Fatal("空串必须报错")
	}
	if err := nullable.Scan("bad-time"); err == nil {
		t.Fatal("非法值必须报错")
	}
	if err := nullable.Scan(123); err == nil {
		t.Fatal("不支持的类型必须报错")
	}
}

func TestSQLHelpers(t *testing.T) {
	if got := SQLNow(Millis); got != sqlNowMillis {
		t.Fatalf("SQLNow(Millis)=%s", got)
	}
	if got := SQLNow(Micros); got != sqlNowMicros {
		t.Fatalf("SQLNow(Micros)=%s", got)
	}
	if got := SQLText("created_at", Millis); got != `to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')` {
		t.Fatalf("SQLText=%s", got)
	}
}
