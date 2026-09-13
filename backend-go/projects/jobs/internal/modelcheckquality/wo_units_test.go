package modelcheckquality

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// ---- 方言辅助 ----

func TestBindAndBusinessSQL(t *testing.T) {
	if got := bind("SELECT ? ?,?", false); got != "SELECT ? ?,?" {
		t.Fatalf("sqlite 不应改写占位符: %s", got)
	}
	if got := bind("SELECT ?,?", true); got != "SELECT $1,$2" {
		t.Fatalf("postgres 占位符应转换: %s", got)
	}
	if got := businessSQL("SELECT 1 FROM model_quality_policies", false); got != "SELECT 1 FROM model_quality_policies" {
		t.Fatalf("sqlite 业务 SQL 不应加前缀: %s", got)
	}
	pg := businessSQL("SELECT * FROM accounts a JOIN model_quality_schedules s ON 1 WHERE EXISTS (SELECT 1 FROM model_quality_policies p)", true)
	for _, table := range []string{"juhe_business.accounts", "juhe_business.model_quality_schedules", "juhe_business.model_quality_policies"} {
		if !strings.Contains(pg, table) {
			t.Fatalf("postgres 应带 juhe_business 前缀: %s", pg)
		}
	}
	if strings.Contains(pg, "?") {
		t.Fatalf("postgres 占位符应全部转换: %s", pg)
	}
	if got := source("  "); got != "manual" {
		t.Fatalf("空调度应来源 manual: %s", got)
	}
	if got := source("schedule-1"); got != "schedule" {
		t.Fatalf("有调度应来源 schedule: %s", got)
	}
}

// ---- 可用性调度校验 ----

func TestAvailabilityMinuteVariants(t *testing.T) {
	if got, ok := availabilityMinute("00:00"); !ok || got != 0 {
		t.Fatalf("00:00 应为 0 分钟: %d %v", got, ok)
	}
	if got, ok := availabilityMinute("23:59"); !ok || got != 23*60+59 {
		t.Fatalf("23:59 应为 1439 分钟: %d %v", got, ok)
	}
	for _, bad := range []string{"24:00", "12:60", "1:00", "12:000", "aa:bb", ""} {
		if _, ok := availabilityMinute(bad); ok {
			t.Fatalf("非法时间 %q 不应解析", bad)
		}
	}
}

func TestValidateAvailabilityWindowBranches(t *testing.T) {
	if err := validateAvailabilityWindow(availabilityWindow{Days: []int{1}, Start: "bad", End: "10:00"}, true); err == nil {
		t.Fatalf("非法开始时间应报错")
	}
	if err := validateAvailabilityWindow(availabilityWindow{Days: []int{1}, Start: "09:00", End: "09:00"}, true); err == nil {
		t.Fatalf("起止相同应报错")
	}
	if err := validateAvailabilityWindow(availabilityWindow{Start: "09:00", End: "10:00"}, true); err == nil {
		t.Fatalf("主窗口缺天应报错")
	}
	// requireDays=false 的例外窗口允许不带天。
	if err := validateAvailabilityWindow(availabilityWindow{Start: "09:00", End: "10:00"}, false); err != nil {
		t.Fatalf("例外窗口可不带天: %v", err)
	}
	if err := validateAvailabilityWindow(availabilityWindow{Days: []int{0}, Start: "09:00", End: "10:00"}, true); err == nil {
		t.Fatalf("星期范围 1..7 之外应报错")
	}
	if err := validateAvailabilityWindow(availabilityWindow{Days: []int{1, 7}, Start: "22:00", End: "02:00"}, true); err != nil {
		t.Fatalf("跨夜窗口应合法: %v", err)
	}
}

func TestValidateDateRangeBranches(t *testing.T) {
	if err := validateDateRange(availabilityDateRange{}); err != nil {
		t.Fatalf("空日期范围应放行: %v", err)
	}
	if err := validateDateRange(availabilityDateRange{Start: "2026-01-01", End: "2026-01-31"}); err != nil {
		t.Fatalf("合法范围应放行: %v", err)
	}
	if err := validateDateRange(availabilityDateRange{Start: "bad"}); err == nil {
		t.Fatalf("非法起始日期应报错")
	}
	if err := validateDateRange(availabilityDateRange{End: "bad"}); err == nil {
		t.Fatalf("非法结束日期应报错")
	}
	if err := validateDateRange(availabilityDateRange{Start: "2026-02-01", End: "2026-01-01"}); err == nil {
		t.Fatalf("起止倒置应报错")
	}
}

func TestValidateAvailabilityScheduleBranches(t *testing.T) {
	base := availabilitySchedule{
		Timezone: "Asia/Shanghai",
		Windows:  []availabilityWindow{{Days: []int{1}, Start: "09:00", End: "18:00"}},
	}
	if err := validateAvailabilitySchedule(base); err != nil {
		t.Fatalf("合法调度应放行: %v", err)
	}
	noTimezone := base
	noTimezone.Timezone = " "
	if err := validateAvailabilitySchedule(noTimezone); err == nil {
		t.Fatalf("缺时区应报错")
	}
	noWindows := base
	noWindows.Windows = nil
	if err := validateAvailabilitySchedule(noWindows); err == nil {
		t.Fatalf("缺窗口应报错")
	}
	tooMany := base
	tooMany.Windows = make([]availabilityWindow, 33)
	if err := validateAvailabilitySchedule(tooMany); err == nil {
		t.Fatalf("窗口超 32 应报错")
	}
	// 日期范围校验被透传。
	badRange := base
	badRange.DateRange = &availabilityDateRange{Start: "bad"}
	if err := validateAvailabilitySchedule(badRange); err == nil {
		t.Fatalf("非法日期范围应报错")
	}
	// 例外：deny 不允许带窗口，allow 必须带窗口。
	denyWithWindows := base
	denyWithWindows.Exceptions = []availabilityException{{Date: "2026-09-10", Action: "deny", Windows: []availabilityWindow{{Start: "09:00", End: "10:00"}}, windowsPresent: true}}
	if err := validateAvailabilitySchedule(denyWithWindows); err == nil {
		t.Fatalf("deny 例外不应带窗口")
	}
	allowWithoutWindows := base
	allowWithoutWindows.Exceptions = []availabilityException{{Date: "2026-09-10", Action: "allow"}}
	if err := validateAvailabilitySchedule(allowWithoutWindows); err == nil {
		t.Fatalf("allow 例外必须带窗口")
	}
	badExceptionDate := base
	badExceptionDate.Exceptions = []availabilityException{{Date: "bad", Action: "deny"}}
	if err := validateAvailabilitySchedule(badExceptionDate); err == nil {
		t.Fatalf("例外日期非法应报错")
	}
}

func TestBoolIntQuality(t *testing.T) {
	if boolInt(true) != 1 || boolInt(false) != 0 {
		t.Fatalf("布尔转整型不符")
	}
}

func TestNullableAndTruncate(t *testing.T) {
	if got := nullable(" "); got != nil {
		t.Fatalf("空白应转 nil: %#v", got)
	}
	if got := nullable("x"); got != "x" {
		t.Fatalf("非空白应原样: %#v", got)
	}
	if got := truncate("中文 truncate test", 2); len([]rune(got)) != 2 {
		t.Fatalf("截断应按 rune: %q", got)
	}
}

// ---- 策略配置比对（SQLite 事务驱动） ----

func TestEnforcementConfigurationMatchesDefaults(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE model_quality_policies (system_account_id TEXT PRIMARY KEY, revision INTEGER, profile TEXT, penalty_threshold INTEGER, penalty_action TEXT, recovery_interval_minutes INTEGER)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	q := func(value string) string { return businessSQL(value, false) }
	input := EnforcementInput{SystemAccountID: "sys-1", PolicyRevision: 0, Profile: "quick", PenaltyThreshold: 70, Action: "fallback", RecoveryIntervalMinutes: 10}
	// 无策略行且输入等于 Node 默认 → 视为匹配。
	matches, err := enforcementConfigurationMatches(ctx, tx, q, input)
	if err != nil || !matches {
		t.Fatalf("默认输入应视为匹配: %v %v", matches, err)
	}
	// 无策略行但输入偏离默认 → 不匹配。
	input.PenaltyThreshold = 80
	matches, err = enforcementConfigurationMatches(ctx, tx, q, input)
	if err != nil || matches {
		t.Fatalf("偏离默认应不匹配: %v %v", matches, err)
	}
	// 有策略行：全字段一致才匹配。
	if _, err := tx.ExecContext(ctx, q(`INSERT INTO model_quality_policies (system_account_id,revision,profile,penalty_threshold,penalty_action,recovery_interval_minutes) VALUES (?,?,?,?,?,?)`), "sys-1", 3, "quick", 80, "fallback", 10); err != nil {
		t.Fatal(err)
	}
	input.PenaltyThreshold = 80
	input.PolicyRevision = 3
	matches, err = enforcementConfigurationMatches(ctx, tx, q, input)
	if err != nil || !matches {
		t.Fatalf("一致策略应匹配: %v %v", matches, err)
	}
	input.Action = "block"
	matches, err = enforcementConfigurationMatches(ctx, tx, q, input)
	if err != nil || matches {
		t.Fatalf("字段偏离应不匹配: %v %v", matches, err)
	}
}
