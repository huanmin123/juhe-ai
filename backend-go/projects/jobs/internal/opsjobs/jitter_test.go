package opsjobs

import "testing"

// 被动抖动窗口边界对齐 Node passiveScheduleJitterWindowMs。
func TestPassiveScheduleJitterWindowMS(t *testing.T) {
	cases := []struct {
		name     string
		interval int64
		want     int64
	}{
		{"亚分钟取半", 5_000, 2_500},
		{"亚分钟窗口半间隔", 59_999, 29_999},
		{"一分钟区间30s", 60_000, 30_000},
		{"一小时区间窗口30m", 2 * 60 * 60_000, 30 * 60_000},
		{"恰好一小时取小时档30m", 60 * 60_000, 30 * 60_000},
		{"一天区间1h", 25 * 60 * 60_000, 60 * 60_000},
		{"一周区间8h", 8 * 24 * 60 * 60_000, 8 * 60 * 60_000},
		{"最小值", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PassiveScheduleJitterWindowMS(tc.interval); got != tc.want {
				t.Fatalf("PassiveScheduleJitterWindowMS(%d) = %d, want %d", tc.interval, got, tc.want)
			}
		})
	}
}

// 偏移规范化：窗口内零偏移被改为 1ms；延迟严格为正。
func TestPassiveScheduleDelayMSAlwaysPositive(t *testing.T) {
	zeroRandom := RandomUnit(func() float64 { return 0.5 })
	if got := PassiveScheduleOffsetMS(60_000, zeroRandom); got != 1 {
		t.Fatalf("零偏移应规范化为 1ms，got %d", got)
	}
	if got := PassiveScheduleDelayMS(0, nil); got < 1 {
		t.Fatalf("延迟必须严格为正，got %d", got)
	}
	maxRandom := RandomUnit(func() float64 { return 1 })
	if got := PassiveScheduleDelayMS(60_000, maxRandom); got != 60_000+30_000 {
		t.Fatalf("窗口上界偏移 = %d", got)
	}
}

func TestAccountCircuitBackoffDelayMS(t *testing.T) {
	cases := []struct {
		name    string
		attempt int64
		seed    string
		want    int64
	}{
		{"attempt1 无抖动", 1, "", 3_000},
		{"attempt4 无抖动", 4, "", 30_000},
		{"attempt5 确定性种子", 5, "seed-a", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AccountCircuitBackoffDelayMS(tc.attempt, tc.seed, nil)
			if tc.seed == "" && got != tc.want {
				t.Fatalf("attempt=%d got %d want %d", tc.attempt, got, tc.want)
			}
			if tc.seed != "" && got < 1 {
				t.Fatalf("种子抖动必须为正，got %d", got)
			}
		})
	}
	// 同一 seed 必须得到相同 deadline（Redis/内存 store 一致性）。
	first := AccountCircuitBackoffDelayMS(5, "seed-a", nil)
	second := AccountCircuitBackoffDelayMS(5, "seed-a", nil)
	if first != second {
		t.Fatalf("同 seed 抖动不一致: %d vs %d", first, second)
	}
}
