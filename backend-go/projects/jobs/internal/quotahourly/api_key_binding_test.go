package quotahourly

import "testing"

// TestHourlyWindowHours 钉住与 maintenance 回填 CTE（active + hourly.enabled +
// 1..720）和 gateway syncQuotaHourlyWindowBinding 同口径的小时窗口判定：只有
// enabled===true 且 hours 为 1..720 的整数才产生绑定；空文档 / 无 hourly /
// null / 禁用 hourly 不产生绑定；enabled=true 但窗口不可判定时 fail closed。
func TestHourlyWindowHours(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		hours   int
		enabled bool
		wantErr bool
	}{
		{name: "blank", raw: ""},
		{name: "whitespace", raw: "  "},
		{name: "no hourly", raw: `{"daily":{"enabled":true,"limit":40}}`},
		{name: "null hourly", raw: `{"hourly":null}`},
		{name: "canonical 6h", raw: `{"hourly":{"enabled":true,"hours":6,"limit":12.5}}`, hours: 6, enabled: true},
		{name: "canonical 1h", raw: `{"hourly":{"enabled":true,"hours":1,"limit":1}}`, hours: 1, enabled: true},
		{name: "canonical 720h", raw: `{"hourly":{"enabled":true,"hours":720,"limit":1}}`, hours: 720, enabled: true},
		{name: "hours zero", raw: `{"hourly":{"enabled":true,"hours":0,"limit":1}}`, wantErr: true},
		{name: "hours 721", raw: `{"hourly":{"enabled":true,"hours":721,"limit":1}}`, wantErr: true},
		{name: "hours fraction", raw: `{"hourly":{"enabled":true,"hours":1.5,"limit":1}}`, wantErr: true},
		{name: "hours missing", raw: `{"hourly":{"enabled":true,"limit":1}}`, wantErr: true},
		{name: "disabled hourly", raw: `{"hourly":{"enabled":false,"hours":6,"limit":1}}`},
		{name: "hourly scalar", raw: `{"hourly":5}`, wantErr: true},
		{name: "invalid json", raw: `{"hourly":`, wantErr: true},
	}
	for _, testCase := range cases {
		hours, enabled, err := HourlyWindowHours(testCase.raw)
		if testCase.wantErr {
			if err == nil {
				t.Fatalf("%s: 期望报错，got hours=%d enabled=%v", testCase.name, hours, enabled)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: 非期望报错: %v", testCase.name, err)
		}
		if hours != testCase.hours || enabled != testCase.enabled {
			t.Fatalf("%s: got (%d,%v) want (%d,%v)", testCase.name, hours, enabled, testCase.hours, testCase.enabled)
		}
	}
}
