package gatewayproxyhealth

import (
	"math"
	"strings"
	"testing"
	"time"
)

// 覆盖 clock.go 的可注入时间源与纯格式化/规范化助手：
// 这些函数把 Node 的 Date.now / randomBytes / toISOString 语义固化下来，
// 是迁移互操作（与 Node 写入的 runtime-state 互通）的契约面。
func TestWhClockNowInjection(t *testing.T) {
	// nil Clock 回退到墙钟：只断言非零时刻，不绑定具体值。
	if ClockNowMs(nil) <= 0 {
		t.Fatal("nil clock 必须回退到墙钟毫秒")
	}
	if ClockNow(nil).IsZero() {
		t.Fatal("nil clock 必须回退到墙钟时间")
	}
	fixed := time.UnixMilli(1_234_567_890)
	if got := ClockNowMs(func() time.Time { return fixed }); got != 1_234_567_890 {
		t.Fatalf("ClockNowMs = %d, want 1234567890", got)
	}
	if got := ClockNow(func() time.Time { return fixed }); !got.Equal(fixed) {
		t.Fatalf("ClockNow = %v, want %v", got, fixed)
	}
}

func TestWhNewUUIDAndRandomHexShape(t *testing.T) {
	uuid := NewUUID()
	parts := strings.Split(uuid, "-")
	if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		t.Fatalf("NewUUID 形状不符合 RFC4122 v4 渲染: %q", uuid)
	}
	if parts[2][0] != '4' {
		t.Fatalf("NewUUID version 位必须是 4: %q", uuid)
	}
	hex := NewRandomHex(12)
	if len(hex) != 24 {
		t.Fatalf("NewRandomHex(12) 长度 = %d, want 24", len(hex))
	}
	for _, r := range hex {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("NewRandomHex 输出必须是十六进制: %q", hex)
		}
	}
}

func TestWhParseRfc3339InstantTable(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantOk    bool
		wantUnixM int64
	}{
		{name: "Z 毫秒", input: "2026-01-02T03:04:05.678Z", wantOk: true, wantUnixM: time.Date(2026, 1, 2, 3, 4, 5, 678e6, time.UTC).UnixMilli()},
		{name: "正偏移", input: "2026-01-02T11:04:05+08:00", wantOk: true, wantUnixM: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli()},
		{name: "负偏移", input: "2026-01-02T01:34:05-02:30", wantOk: true, wantUnixM: time.Date(2026, 1, 2, 4, 4, 5, 0, time.UTC).UnixMilli()},
		{name: "补齐 3 位小数到纳秒", input: "1970-01-01T00:00:00.123Z", wantOk: true, wantUnixM: 123},
		{name: "缺偏移拒绝", input: "2026-01-02T03:04:05", wantOk: false},
		{name: "垃圾输入拒绝", input: "not-a-time", wantOk: false},
		{name: "月份 13 拒绝", input: "2026-13-01T00:00:00Z", wantOk: false},
		{name: "2 月 30 拒绝（闰年规则）", input: "2026-02-30T00:00:00Z", wantOk: false},
		{name: "闰年 2 月 29 接受", input: "2024-02-29T00:00:00Z", wantOk: true, wantUnixM: time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC).UnixMilli()},
		{name: "小时 24 拒绝", input: "2026-01-02T24:00:00Z", wantOk: false},
		{name: "偏移小时 24 拒绝", input: "2026-01-02T00:00:00+24:00", wantOk: false},
		{name: "偏移分钟 60 拒绝", input: "2026-01-02T00:00:00+00:60", wantOk: false},
		{name: "秒 60 拒绝", input: "2026-01-02T00:00:60Z", wantOk: false},
		{name: "首尾空白容忍", input: "  2026-01-02T03:04:05Z  ", wantOk: true, wantUnixM: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).UnixMilli()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, ok := ParseRfc3339Instant(tt.input)
			if ok != tt.wantOk {
				t.Fatalf("ParseRfc3339Instant(%q) ok = %v, want %v", tt.input, ok, tt.wantOk)
			}
			if !tt.wantOk {
				return
			}
			if parsed.UnixMilli() != tt.wantUnixM {
				t.Fatalf("ParseRfc3339Instant(%q) = %d ms, want %d", tt.input, parsed.UnixMilli(), tt.wantUnixM)
			}
		})
	}
}

func TestWhCanonicalizeAndMilliseconds(t *testing.T) {
	canonical, ok := CanonicalizeRfc3339Instant("2026-01-02T11:04:05.500+08:00")
	if !ok || canonical != "2026-01-02T03:04:05.500Z" {
		t.Fatalf("CanonicalizeRfc3339Instant = %q ok=%v", canonical, ok)
	}
	if _, ok := CanonicalizeRfc3339Instant("2026-02-30T00:00:00Z"); ok {
		t.Fatal("非法日期必须规范化失败")
	}
	ms, ok := Rfc3339InstantMilliseconds("1970-01-01T00:00:01Z")
	if !ok || ms != 1000 {
		t.Fatalf("Rfc3339InstantMilliseconds = %d ok=%v", ms, ok)
	}
	if _, ok := Rfc3339InstantMilliseconds("bad"); ok {
		t.Fatal("非法输入必须失败")
	}
}

func TestWhPassiveScheduleJitterWindow(t *testing.T) {
	tests := []struct {
		name       string
		intervalMs int64
		want       int64
	}{
		{name: "亚分钟取一半", intervalMs: 1_000, want: 500},
		{name: "最小间隔取一半", intervalMs: 1, want: 0},
		{name: "零间隔下限", intervalMs: 0, want: 0},
		{name: "分钟级 30s", intervalMs: 5 * 60_000, want: 30_000},
		{name: "小时级 30min", intervalMs: 2 * 60 * 60_000, want: 30 * 60_000},
		{name: "天级 1h", intervalMs: 2 * 24 * 60 * 60_000, want: 60 * 60_000},
		{name: "周级 8h", intervalMs: 8 * 24 * 60 * 60_000, want: 8 * 60 * 60_000},
		{name: "超过周级仍 8h", intervalMs: 30 * 24 * 60 * 60_000, want: 8 * 60 * 60_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PassiveScheduleJitterWindowMs(tt.intervalMs); got != tt.want {
				t.Fatalf("PassiveScheduleJitterWindowMs(%d) = %d, want %d", tt.intervalMs, got, tt.want)
			}
		})
	}
}

func TestWhPassiveScheduleOffsetAndDelay(t *testing.T) {
	// random=0.5 且窗口 500：floor(0.5*(2*500+1))-500 = 0 → 契约要求返回 1（严格非零偏移）。
	if got := PassiveScheduleOffsetMs(1_000, func() float64 { return 0.5 }); got != 1 {
		t.Fatalf("PassiveScheduleOffsetMs 零偏移必须折叠为 1, got %d", got)
	}
	// NaN/Inf/nil 视为 unit=0 → 偏移取窗口下界 -500。
	for _, random := range []func() float64{
		func() float64 { return math.NaN() },
		func() float64 { return math.Inf(1) },
		func() float64 { return math.Inf(-1) },
		nil,
		func() float64 { return 0 },
	} {
		if got := PassiveScheduleOffsetMs(1_000, random); got != -500 {
			t.Fatalf("NaN/Inf/nil 随机源必须走 unit=0 下界分支, got %d", got)
		}
	}
	// random=1 → floor(1*1001)-500 = 501 = 窗口上限。
	if got := PassiveScheduleOffsetMs(1_000, func() float64 { return 1 }); got != 500 {
		t.Fatalf("offset 上限应为窗口值 500, got %d", got)
	}
	// 超界随机值被夹到 [0,1]。
	if got := PassiveScheduleOffsetMs(1_000, func() float64 { return 42 }); got != 500 {
		t.Fatalf("越界随机必须夹到 1, got %d", got)
	}
	// 零窗口（interval=1）直接返回 0。
	if got := PassiveScheduleOffsetMs(1, func() float64 { return 0.5 }); got != 0 {
		t.Fatalf("零窗口必须返回 0, got %d", got)
	}
	// 延迟 = 归一化间隔 + 严格非零偏移，且永远 >= 1。
	if got := PassiveScheduleDelayMs(0, func() float64 { return 0.5 }); got < 1 {
		t.Fatalf("PassiveScheduleDelayMs 必须 >= 1, got %d", got)
	}
	if got := PassiveScheduleDelayMs(1_000, func() float64 { return 0.5 }); got != 1_001 {
		t.Fatalf("PassiveScheduleDelayMs = %d, want 1001", got)
	}
}

func TestWhIntegerHelpers(t *testing.T) {
	if mustAtoi("42") != 42 || mustAtoi("xx") != 0 {
		t.Fatal("mustAtoi 契约：可解析返回值，否则 0")
	}
	if maxInt(3, 7) != 7 || maxInt(-1, -2) != -1 {
		t.Fatal("maxInt 语义错误")
	}
	nine := 9
	over := 99
	if normalizePositiveInteger(nil, 5, 1, 10) != 5 {
		t.Fatal("nil 输入必须取 fallback")
	}
	if normalizePositiveInteger(&nine, 5, 1, 10) != 9 {
		t.Fatal("范围内输入必须原样保留")
	}
	if normalizePositiveInteger(&over, 5, 1, 10) != 10 {
		t.Fatal("超上限必须夹紧")
	}
	if clampInt(-5, 0, 10) != 0 || clampInt(15, 0, 10) != 10 || clampInt(5, 0, 10) != 5 {
		t.Fatal("clampInt 语义错误")
	}
	if clampInt64(-5, 0, 10) != 0 || clampInt64(15, 0, 10) != 10 || clampInt64(5, 0, 10) != 5 {
		t.Fatal("clampInt64 语义错误")
	}
}

// 账户运行态键是延迟降级/代理健康共享的身份契约：授权账户缺绑定上下文必须报错，
// runtimeKey 的账户段取第一个冒号前缀。
func TestWhAccountRuntimeKeys(t *testing.T) {
	full := SuppressibleGatewayAccount{
		ID: "acc1", AccessType: "authorized",
		BindingSystemAccountID: "sys1", BoundGroupID: "g1", AccountAuthorizationID: "auth1",
	}
	key, err := GatewayAccountRuntimeKey(full)
	if err != nil || key != "acc1:authorized:sys1:g1:auth1" {
		t.Fatalf("完整绑定上下文的键 = %q err=%v", key, err)
	}
	missing := full
	missing.BoundGroupID = ""
	if _, err := GatewayAccountRuntimeKey(missing); err == nil || err.Error() != "授权账户运行态键缺少绑定上下文" {
		t.Fatalf("缺绑定上下文必须报错, err=%v", err)
	}
	accountAuthorized := full
	accountAuthorized.AccessType = ""
	accountAuthorized.AccountAccessType = "account_authorized"
	accountAuthorized.BoundGroupID = "g1"
	if key, err := GatewayAccountRuntimeKey(accountAuthorized); err != nil || key != "acc1:authorized:sys1:g1:auth1" {
		t.Fatalf("account_authorized 类型键 = %q err=%v", key, err)
	}
	owner := SuppressibleGatewayAccount{ID: "acc2"}
	if key, err := GatewayAccountRuntimeKey(owner); err != nil || key != "acc2" {
		t.Fatalf("owner 类型键 = %q err=%v", key, err)
	}
	if got := RuntimeAccountIDFromKey("acc1:authorized:sys1:g1:auth1"); got != "acc1" {
		t.Fatalf("RuntimeAccountIDFromKey = %q", got)
	}
	if got := RuntimeAccountIDFromKey(""); got != "" {
		t.Fatalf("空键应原样返回, got %q", got)
	}
}

func TestWhDispatchPriorityTierEdges(t *testing.T) {
	// 模型 rank 缺失 → 3（unknown），负 rank → 0，nil map → 0。
	rankMissing := GatewayAccountDispatchPriorityTier(DispatchPriorityAccountView{ID: "a"}, DispatchPriorityOrderOptions{ModelRankByAccountID: map[string]int{"b": 1}})
	if rankMissing != "3:0:1:0" {
		t.Fatalf("未知 rank tier = %q", rankMissing)
	}
	negative := GatewayAccountDispatchPriorityTier(DispatchPriorityAccountView{ID: "a"}, DispatchPriorityOrderOptions{ModelRankByAccountID: map[string]int{"a": -4}})
	if negative != "0:0:1:0" {
		t.Fatalf("负 rank tier = %q", negative)
	}
	// NaN/Inf priority 退化为 0；superPriority 折叠 superRank。
	nan := float64(math.NaN())
	inf := math.Inf(1)
	fallback := true
	super := true
	tier := GatewayAccountDispatchPriorityTier(DispatchPriorityAccountView{
		ID: "a", Priority: &nan, SuperPriorityEnabled: &super, FallbackEnabled: &fallback,
	}, DispatchPriorityOrderOptions{})
	if tier != "0:1:0:0" {
		t.Fatalf("NaN priority tier = %q", tier)
	}
	tier = GatewayAccountDispatchPriorityTier(DispatchPriorityAccountView{ID: "a", Priority: &inf}, DispatchPriorityOrderOptions{})
	if tier != "0:0:1:0" {
		t.Fatalf("Inf priority tier = %q", tier)
	}
	// 小数 priority 截断。
	p := 7.9
	tier = GatewayAccountDispatchPriorityTier(DispatchPriorityAccountView{ID: "a", Priority: &p}, DispatchPriorityOrderOptions{})
	if tier != "0:0:1:7" {
		t.Fatalf("截断 priority tier = %q", tier)
	}
}
