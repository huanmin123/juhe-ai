package ipstats

// w11d 覆盖补齐：store/routes/detail/list 的错误臂、排序与日期助手、
// 校验助手、策略体解析臂与时区源错误路径。

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-gateway/internal/kernel"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// 纯助手
// ---------------------------------------------------------------------------

func TestW11DHelpersDirectArms(t *testing.T) {
	// uniqueStrings：空值与重复剔除。
	unique := uniqueStrings([]string{"a", "", "a", "b", ""})
	if len(unique) != 2 || unique[0] != "a" || unique[1] != "b" {
		t.Fatalf("uniqueStrings = %v", unique)
	}
	// optionalText：空白归 nil。
	if optionalText(nil) != nil {
		t.Fatal("nil reason 必须保持 nil")
	}
	blank := "  "
	if optionalText(&blank) != nil {
		t.Fatal("空白 reason 必须归 nil")
	}
	text := " hi "
	trimmed := optionalText(&text)
	if trimmed == nil || *trimmed != "hi" {
		t.Fatal("reason 必须去空白")
	}
	// optionalInstant：nil/空白/坏值。
	if value, err := optionalInstant(nil, "label"); err != nil || value != nil {
		t.Fatalf("nil instant = %v %v", value, err)
	}
	if value, err := optionalInstant(&blank, "label"); err != nil || value != nil {
		t.Fatalf("空白 instant = %v %v", value, err)
	}
	bad := "not-a-time"
	if _, err := optionalInstant(&bad, "expiresAt"); err == nil {
		t.Fatal("坏 instant 必须报错")
	}
	// ensureCtx nil。
	if ensureCtx(nil) == nil {
		t.Fatal("nil ctx 必须归一")
	}
	// actorResolver 匿名臂。
	request := httptest.NewRequest(http.MethodGet, "/x", nil)
	if got := actorResolver(request); got != "anonymous" {
		t.Fatalf("匿名 actor = %q", got)
	}
	// boundedPage 越界钳制。
	if got := boundedPage(0, 20); got != 1 {
		t.Fatalf("page<1 = %d", got)
	}
	if got := boundedPage(99999, 20); got != (maxListWindowRows-1)/20 {
		t.Fatalf("page 超窗 = %d", got)
	}
	if got := boundedPage(5, 20); got != 5 {
		t.Fatalf("page 正常 = %d", got)
	}
	if got := boundedDetailPage(0, 20); got != 1 {
		t.Fatalf("detail page<1 = %d", got)
	}
	if got := boundedDetailPage(99999, 20); got != (maxDetailWindowRows-1)/20 {
		t.Fatalf("detail page 超窗 = %d", got)
	}
	if got := boundedDetailPageSize(0); got != 20 {
		t.Fatalf("detail pageSize<1 = %d", got)
	}
	if got := boundedDetailPageSize(101); got != 100 {
		t.Fatalf("detail pageSize>100 = %d", got)
	}
	// parseNodeNumber：正负号、进制前缀、空进制、坏值、空串。
	if value, ok := parseNodeNumber(""); !ok || value != 0 {
		t.Fatalf("空串 = %v %v", value, ok)
	}
	if value, ok := parseNodeNumber("+0x10"); !ok || value != 16 {
		t.Fatalf("+0x = %v %v", value, ok)
	}
	if value, ok := parseNodeNumber("-0o7"); !ok || value != -7 {
		t.Fatalf("-0o = %v %v", value, ok)
	}
	if value, ok := parseNodeNumber("0b101"); !ok || value != 5 {
		t.Fatalf("0b = %v %v", value, ok)
	}
	if _, ok := parseNodeNumber("0x"); ok {
		t.Fatal("空进制必须失败")
	}
	if _, ok := parseNodeNumber("0xZZ"); ok {
		t.Fatal("坏进制必须失败")
	}
	if _, ok := parseNodeNumber("abc"); ok {
		t.Fatal("坏数字必须失败")
	}
	if value, ok := parseNodeNumber(" 12.5 "); !ok || value != 12.5 {
		t.Fatalf("小数 = %v %v", value, ok)
	}
	if value, ok := parseNodeNumber("1e3"); !ok || value != 1000 {
		t.Fatalf("指数 = %v %v", value, ok)
	}
	// 关键词上界：末位进位与 0xffff 回退。
	if got := keywordPrefixUpperBound("a"); got != "b" {
		t.Fatalf("上界进位 = %q", got)
	}
	if got := keywordPrefixUpperBound(string([]rune{0xffff})); got != string([]rune{0xffff})+"\uffff" {
		t.Fatalf("上界回退 = %q", got)
	}
	// 日期助手。
	if got := addDateKey("not-a-date", 1); got != "not-a-date" {
		t.Fatalf("坏日期原样 = %q", got)
	}
	if got := normalizeDateKey("  ", "2026-09-01"); got != "2026-09-01" {
		t.Fatalf("空白回退 = %q", got)
	}
	if got := normalizeDateKey("bad", "2026-09-01"); got != "2026-09-01" {
		t.Fatalf("坏日期回退 = %q", got)
	}
	if got := parseDateKeyOrToday("bad", "2026-09-01"); got.Format("2006-01-02") != "2026-09-01" {
		t.Fatalf("回退解析 = %v", got)
	}
	if got := daysBetweenInclusive("bad", "bad", "2026-09-01"); got != 1 {
		t.Fatalf("坏区间 = %d", got)
	}
	// 排序白名单。
	for _, field := range []string{"successCount", "errorCount", "errorRate", "totalTokens", "activeDays", "lastUsedAt", "requestCount", "totalCost"} {
		for _, order := range []string{"asc", "desc", ""} {
			if got := listOrderBy(field, order); got == "" {
				t.Fatalf("listOrderBy(%s,%s) 为空", field, order)
			}
			if got := detailAccountStatsOrderBy(field, order); got == "" {
				t.Fatalf("detailOrderBy(%s,%s) 为空", field, order)
			}
		}
	}
	if got := listOrderBy("unknown", "desc"); !strings.Contains(got, "request_count") {
		t.Fatalf("未知字段默认 = %q", got)
	}
	if got := detailAccountStatsOrderBy("unknown", "asc"); !strings.Contains(got, "request_count ASC") {
		t.Fatalf("detail 默认 = %q", got)
	}
}

func TestW11DUsageSummaryFallbackArms(t *testing.T) {
	// list usageSummary：平均值回退与可选字段。
	row := usageSummaryRow{
		requestCount:    sql.NullInt64{Int64: 4, Valid: true},
		errorCount:      sql.NullInt64{Int64: 1, Valid: true},
		inputTokens:     sql.NullInt64{Int64: 10, Valid: true},
		outputTokens:    sql.NullInt64{Int64: 20, Valid: true},
		durationSum:     sql.NullInt64{Int64: 300, Valid: true},
		durationCount:   sql.NullInt64{Int64: 3, Valid: true},
		durationMax:     sql.NullInt64{Int64: 150, Valid: true},
		firstTokenSum:   sql.NullInt64{Int64: 90, Valid: true},
		firstTokenCount: sql.NullInt64{Int64: 3, Valid: true},
		lastUsedAt:      sql.NullString{String: "2026-09-01T00:00:00.000Z", Valid: true},
	}
	summary := usageSummary(row)
	if summary.ErrorRate != 0.25 || summary.TotalTokens != 30 {
		t.Fatalf("基础投影 = %+v", summary)
	}
	if summary.AverageDurationMs == nil || *summary.AverageDurationMs != 100 {
		t.Fatalf("平均时长回退 = %+v", summary.AverageDurationMs)
	}
	if summary.AverageFirstTokenMs == nil || *summary.AverageFirstTokenMs != 30 {
		t.Fatalf("首字回退 = %+v", summary.AverageFirstTokenMs)
	}
	if summary.MaxDurationMs == nil || *summary.MaxDurationMs != 150 {
		t.Fatalf("最大时长 = %+v", summary.MaxDurationMs)
	}
	if summary.LastUsedAt == nil {
		t.Fatal("lastUsedAt 必须保留")
	}
	// 预计算平均优先于回退。
	row.avgDuration = sql.NullFloat64{Float64: 42, Valid: true}
	row.avgFirstToken = sql.NullFloat64{Float64: 7, Valid: true}
	row.durationMax = sql.NullInt64{Int64: 0, Valid: true}
	summary = usageSummary(row)
	if *summary.AverageDurationMs != 42 || *summary.AverageFirstTokenMs != 7 || summary.MaxDurationMs != nil {
		t.Fatalf("预计算平均 = %+v", summary)
	}
	// detail detailUsageSummary 同款回退。
	detail := detailUsageSummary(detailWindowRow{
		requestCount:    sql.NullInt64{Int64: 2, Valid: true},
		errorCount:      sql.NullInt64{Int64: 2, Valid: true},
		durationSum:     sql.NullInt64{Int64: 100, Valid: true},
		durationCount:   sql.NullInt64{Int64: 2, Valid: true},
		firstTokenSum:   sql.NullInt64{Int64: 50, Valid: true},
		firstTokenCount: sql.NullInt64{Int64: 2, Valid: true},
		lastErrorAt:     sql.NullString{String: "2026-09-02T00:00:00.000Z", Valid: true},
	})
	if detail.ErrorRate != 1 || *detail.AverageDurationMs != 50 || *detail.AverageFirstTokenMs != 25 || detail.MaxDurationMs != nil {
		t.Fatalf("detail 回退 = %+v", detail)
	}
	if detail.LastErrorAt == nil {
		t.Fatal("detail lastErrorAt 必须保留")
	}
}

func TestW11DSortRowsLastUsedArms(t *testing.T) {
	early := "2026-09-01T00:00:00.000Z"
	late := "2026-09-02T00:00:00.000Z"
	bad := "not-a-time"
	mk := func(hash string, lastSeen string) ListRow {
		row := ListRow{IPHash: hash}
		if lastSeen != "" {
			row.LastSeenAt = &lastSeen
		}
		return row
	}
	rows := []ListRow{mk("b", late), mk("a", ""), mk("c", early), mk("d", "")}
	asc, err := sortRows(rows, "lastUsedAt", "asc", "global")
	if err != nil {
		t.Fatal(err)
	}
	if asc[0].IPHash != "d" || asc[1].IPHash != "a" || asc[2].IPHash != "c" || asc[3].IPHash != "b" {
		t.Fatalf("asc 排序 = %v", []string{asc[0].IPHash, asc[1].IPHash, asc[2].IPHash, asc[3].IPHash})
	}
	desc, err := sortRows(rows, "lastUsedAt", "desc", "global")
	if err != nil {
		t.Fatal(err)
	}
	if desc[0].IPHash != "b" || desc[1].IPHash != "c" || desc[3].IPHash != "d" {
		t.Fatalf("desc 排序 = %v", desc)
	}
	// 非 lastUsedAt 字段原样返回。
	same, err := sortRows(rows, "requestCount", "asc", "global")
	if err != nil || same[0].IPHash != "b" {
		t.Fatalf("非 lastUsed 排序 = %v err=%v", same, err)
	}
	// 非 global scope：RangeUsage.LastUsedAt 参与。
	scoped := []ListRow{mk("a", ""), mk("b", "")}
	scoped[0].RangeUsage.LastUsedAt = &early
	scoped[1].RangeUsage.LastUsedAt = &late
	out, err := sortRows(scoped, "lastUsedAt", "asc", "account")
	if err != nil {
		t.Fatal(err)
	}
	if out[0].IPHash != "a" {
		t.Fatalf("scoped 排序 = %v", out)
	}
	// 坏时间戳 → 错误。
	broken := []ListRow{mk("a", bad)}
	if _, err := sortRows(broken, "lastUsedAt", "asc", "global"); err == nil {
		t.Fatal("坏 lastSeenAt 必须报错")
	}
	// rfc3339Millis 坏值。
	if _, err := rfc3339Millis("bad"); err == nil {
		t.Fatal("坏 RFC3339 必须报错")
	}
}

// ---------------------------------------------------------------------------
// store：构造守卫、CreatePolicy/DisablePolicies 错误臂
// ---------------------------------------------------------------------------

func TestW11DStoreDirectArms(t *testing.T) {
	if _, err := NewStore(nil, false, nil, nil, nil, nil); err == nil {
		t.Fatal("nil db 必须报错")
	}
	env := newTestEnv(t)
	ctx := context.Background()
	admin := env.login(t, "root", "root-pass", "super_admin")

	// CreatePolicy：坏 hash / 缺操作者 / 坏 expiresAt / 未登记 IP。
	if _, err := env.store.CreatePolicy(ctx, PolicyMutationInput{IPHash: "bad", PolicyType: PolicyTypeBlacklist, ActorSystemAccountID: admin}); err == nil {
		t.Fatal("坏 hash 必须报错")
	}
	if _, err := env.store.CreatePolicy(ctx, PolicyMutationInput{IPHash: testHashA, PolicyType: PolicyTypeBlacklist, ActorSystemAccountID: " "}); err == nil {
		t.Fatal("缺操作者必须报错")
	}
	badInstant := "bad"
	if _, err := env.store.CreatePolicy(ctx, PolicyMutationInput{IPHash: testHashA, PolicyType: PolicyTypeBlacklist, ActorSystemAccountID: admin, ExpiresAt: &badInstant}); err == nil {
		t.Fatal("坏 expiresAt 必须报错")
	}
	if _, err := env.store.CreatePolicy(ctx, PolicyMutationInput{IPHash: testHashA, PolicyType: PolicyTypeBlacklist, ActorSystemAccountID: admin}); err == nil || err.Error() != "IP 不存在" {
		t.Fatalf("未登记 IP = %v", err)
	}
	// DisablePolicies：坏 hash。
	if _, err := env.store.DisablePolicies(ctx, PolicyDisableInput{IPHash: "bad"}); err == nil {
		t.Fatal("Disable 坏 hash 必须报错")
	}

	// 登记后成功创建（含 reason 与 expiresAt），再验证替换语义。
	env.insertRegistry(t, testHashA, "203.0.113.10", time.Now())
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	reason := "w11d reason"
	policy, err := env.store.CreatePolicy(ctx, PolicyMutationInput{
		IPHash: testHashA, PolicyType: PolicyTypeBlacklist, Reason: &reason,
		ExpiresAt: &expires, ActorSystemAccountID: admin,
	})
	if err != nil || policy.ID == "" || policy.Reason == nil || *policy.Reason != "w11d reason" {
		t.Fatalf("创建策略 = %+v err=%v", policy, err)
	}
	// 重复主键：注入恒等 newID 让 INSERT 撞主键。
	dupStore, err := NewStore(env.db, false, nil, func(string) string { return policy.ID }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dupStore.CreatePolicy(ctx, PolicyMutationInput{IPHash: testHashA, PolicyType: PolicyTypeBlacklist, ActorSystemAccountID: admin}); err == nil {
		t.Fatal("重复主键必须报错")
	}
	// 事务查询错误：drop registry。
	if _, err := env.db.Exec(`DROP TABLE client_ip_registry`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.CreatePolicy(ctx, PolicyMutationInput{IPHash: testHashA, PolicyType: PolicyTypeBlacklist, ActorSystemAccountID: admin}); err == nil {
		t.Fatal("registry 查询失败必须上抛")
	}
	// Disable 更新错误：drop policies（子测试获得独立命名库）。
	t.Run("disable-update-failure", func(t *testing.T) {
		db2 := newTestEnv(t)
		db2.login(t, "root", "root-pass", "super_admin")
		if _, err := db2.db.Exec(`DROP TABLE client_ip_policies`); err != nil {
			t.Fatal(err)
		}
		if _, err := db2.store.DisablePolicies(context.Background(), PolicyDisableInput{IPHash: testHashA, PolicyType: PolicyTypeBlacklist, ActorSystemAccountID: "sys"}); err == nil {
			t.Fatal("Disable 更新失败必须上抛")
		}
	})
}

func TestW11DTimezoneSourceArms(t *testing.T) {
	// 每个用例独立命名库（setTZ 内部 newTestEnv 以 t.Name() 命名共享内存库）。
	cases := []struct {
		name  string
		value sql.NullString
		want  string
		fail  bool
	}{
		{"bad-json", sql.NullString{String: "not-json", Valid: true}, "", true},
		{"non-string", sql.NullString{String: "123", Valid: true}, "", true},
		{"blank", sql.NullString{String: `""`, Valid: true}, "", true},
		{"invalid-zone", sql.NullString{String: `"Mars/Olympus"`, Valid: true}, "", true},
		{"missing-row", sql.NullString{}, "", true},
		{"valid", sql.NullString{String: `" Asia/Shanghai "`, Valid: true}, "Asia/Shanghai", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			if tc.value.Valid {
				if _, err := env.db.Exec(`UPDATE system_settings SET value_json = ? WHERE key = 'usageStatsTimezone'`, tc.value.String); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := env.db.Exec(`DELETE FROM system_settings WHERE key = 'usageStatsTimezone'`); err != nil {
					t.Fatal(err)
				}
			}
			name, err := NewSystemSettingsTimezoneSource(env.db, false)(context.Background())
			if tc.fail {
				if err == nil {
					t.Fatal("必须报错")
				}
				return
			}
			if err != nil || name != tc.want {
				t.Fatalf("合法时区 = %q err=%v", name, err)
			}
		})
	}
	// 查询错误：drop 表。
	t.Run("query-error", func(t *testing.T) {
		env := newTestEnv(t)
		if _, err := env.db.Exec(`DROP TABLE system_settings`); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSystemSettingsTimezoneSource(env.db, false)(context.Background()); err == nil {
			t.Fatal("查询失败必须上抛")
		}
		// PG 表名臂。
		pgSource := NewSystemSettingsTimezoneSource(env.db, true)
		if _, err := pgSource(context.Background()); err == nil {
			t.Fatal("PG 表名在 SQLite 上必须失败")
		}
	})
}

// ---------------------------------------------------------------------------
// detail：错误臂与分页上限
// ---------------------------------------------------------------------------

func TestW11DDetailErrorArms(t *testing.T) {
	env := newDetailEnv(t)
	admin := env.login(t, "root", "root-pass", "super_admin")
	seedDetailRows(t, env, admin)
	ctx := context.Background()

	// 自定义 tz 源错误与坏时区。
	badTZStore, err := NewStore(env.db, false, nil, nil, nil, func(context.Context) (string, error) {
		return "", sql.ErrConnDone
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badTZStore.Detail(ctx, DetailOptions{IPHash: detailIPHash}); err == nil {
		t.Fatal("tz 源错误必须上抛")
	}
	invalidTZStore, err := NewStore(env.db, false, nil, nil, nil, func(context.Context) (string, error) {
		return "Mars/Olympus", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invalidTZStore.Detail(ctx, DetailOptions{IPHash: detailIPHash}); err == nil || !strings.Contains(err.Error(), "usageStatsTimezone") {
		t.Fatalf("坏时区 = %v", err)
	}
	// 窗口查询失败：drop 账户窗口表（先保证 rangeReady 通过 registry 窗口行）。
	dropStore, err := NewStore(env.db, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.Exec(`DROP TABLE client_ip_account_usage_range_windows`); err != nil {
		t.Fatal(err)
	}
	if _, err := dropStore.Detail(ctx, DetailOptions{IPHash: detailIPHash, StartDate: "2026-09-01", EndDate: "2026-09-03"}); err == nil {
		t.Fatal("账户窗口查询失败必须上抛")
	}
}

func TestW11DDetailPaginationAndLookupArms(t *testing.T) {
	env := newDetailEnv(t)
	admin := env.login(t, "root", "root-pass", "super_admin")
	seedDetailRows(t, env, admin)
	ctx := context.Background()

	// 25 行账户窗口：pageSize=20 → hasMore，page=2 越过后半。
	for i := 0; i < 25; i++ {
		account := fmt.Sprintf("acc_bulk_%03d", i)
		if _, err := env.db.Exec(`INSERT INTO client_ip_account_usage_range_windows
			(ip_hash, account_id, start_date, end_date, request_count, updated_at)
			VALUES (?, ?, '2026-09-01', '2026-09-03', ?, '2026-09-04T00:00:00.000Z')`,
			detailIPHash, account, 100+i); err != nil {
			t.Fatal(err)
		}
	}
	result, err := env.store.Detail(ctx, DetailOptions{IPHash: detailIPHash, StartDate: "2026-09-01", EndDate: "2026-09-03"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.HasMore || len(result.Items) != 20 {
		t.Fatalf("分页上限 = %d hasMore=%v", len(result.Items), result.HasMore)
	}
	page2, err := env.store.Detail(ctx, DetailOptions{IPHash: detailIPHash, Page: 2, PageSize: 20, StartDate: "2026-09-01", EndDate: "2026-09-03"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 7 || page2.HasMore {
		t.Fatalf("第二页 = %d hasMore=%v", len(page2.Items), page2.HasMore)
	}
	// 远超窗口的页码钳制到最后一页。
	farPage, err := env.store.Detail(ctx, DetailOptions{IPHash: detailIPHash, Page: 99999, PageSize: 50, StartDate: "2026-09-01", EndDate: "2026-09-03"})
	if err != nil {
		t.Fatal(err)
	}
	if farPage.Page != (maxDetailWindowRows-1)/50 {
		t.Fatalf("远页钳制 = %d", farPage.Page)
	}

	// LookupAccounts / SystemAccountNames：空集合、查询失败。
	lookup := NewBusinessAccountLookup(env.db, false)
	accounts, err := lookup.LookupAccounts(ctx, nil)
	if err != nil || len(accounts) != 0 {
		t.Fatalf("空 ids = %v err=%v", accounts, err)
	}
	names, err := lookup.SystemAccountNames(ctx, []string{"", ""})
	if err != nil || len(names) != 0 {
		t.Fatalf("空 names = %v err=%v", names, err)
	}
	// PG + 无业务库：db() 为 nil → 空表。
	pgNil := NewBusinessAccountLookup(nil, true).(*businessAccountLookup)
	if pgNil.db() != nil {
		t.Fatal("PG 无业务库必须返回 nil handle")
	}
	if got := pgNil.table("accounts"); got != "juhe_business.accounts" {
		t.Fatalf("PG 表名 = %q", got)
	}
	if got := pgNil.bind("a = ?"); got != "a = $1" {
		t.Fatalf("PG 绑定 = %q", got)
	}
	if got := NewBusinessAccountLookup(env.db, false).(*businessAccountLookup).bind("a = ?"); got != "a = ?" {
		t.Fatalf("SQLite 绑定 = %q", got)
	}
	pgHit := NewBusinessAccountLookup(env.db, true)
	if _, err := pgHit.LookupAccounts(ctx, []string{"acc_alpha"}); err == nil {
		t.Fatal("PG 限定表在 SQLite 上必须失败")
	}
	if _, err := pgHit.SystemAccountNames(ctx, []string{admin}); err == nil {
		t.Fatal("PG 限定系统表在 SQLite 上必须失败")
	}
	// SQLite 查询失败：drop accounts。
	if _, err := env.db.Exec(`DROP TABLE accounts`); err != nil {
		t.Fatal(err)
	}
	sqliteLookup := NewBusinessAccountLookup(env.db, false)
	if _, err := sqliteLookup.LookupAccounts(ctx, []string{"acc_alpha"}); err == nil {
		t.Fatal("accounts 查询失败必须上抛")
	}
	if _, err := env.db.Exec(`DROP TABLE system_accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqliteLookup.SystemAccountNames(ctx, []string{admin}); err == nil {
		t.Fatal("system_accounts 查询失败必须上抛")
	}
}

// ---------------------------------------------------------------------------
// list：错误臂与分页/过滤路径
// ---------------------------------------------------------------------------

func TestW11DListErrorArms(t *testing.T) {
	env := newTestEnv(t)
	admin := env.login(t, "root", "root-pass", "super_admin")
	env.insertRegistry(t, testHashA, "203.0.113.10", time.Now())
	env.markWindowReady(t, todayKey(), todayKey())
	ctx := context.Background()

	// tz 错误 / 坏时区。
	badTZ, err := NewStore(env.db, false, nil, nil, nil, func(context.Context) (string, error) {
		return "", sql.ErrConnDone
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badTZ.List(ctx, ListOptions{Page: 1, PageSize: 20}); err == nil {
		t.Fatal("tz 错误必须上抛")
	}
	invalidTZ, err := NewStore(env.db, false, nil, nil, nil, func(context.Context) (string, error) {
		return "Mars/Olympus", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invalidTZ.List(ctx, ListOptions{}); err == nil || !strings.Contains(err.Error(), "usageStatsTimezone") {
		t.Fatalf("坏时区 = %v", err)
	}
	// 非法 status：buildRangeWhere 报错。
	if _, err := env.store.List(ctx, ListOptions{Page: 1, PageSize: 20, Status: "weird"}); err == nil {
		t.Fatal("非法 status 必须报错")
	}
	// 零页码/页大小钳制。
	result, err := env.store.List(ctx, ListOptions{})
	if err != nil || result.Page != 1 || result.PageSize != 20 {
		t.Fatalf("缺省分页 = %+v err=%v", result, err)
	}
	// lastUsed 过滤窗口（有行命中）。
	lastUsed := time.Now().UTC().Format("2006-01-02")
	withFilter, err := env.store.List(ctx, ListOptions{LastUsedStartDate: lastUsed, LastUsedEndDate: lastUsed})
	if err != nil {
		t.Fatalf("lastUsed 过滤 = %v", err)
	}
	_ = withFilter
	// 超大页码：空页。
	far, err := env.store.List(ctx, ListOptions{Page: 500, PageSize: 20})
	if err != nil || len(far.Items) != 0 || far.PageUpperBound < 0 {
		t.Fatalf("超大页 = %+v err=%v", far, err)
	}
	// pg 绑定臂：buildRangeWhere 的 PG 分支。
	pgStore, err := NewStore(env.db, true, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := pgStore.buildRangeWhere(ctx, ListOptions{Status: StatusBlacklisted}, "now"); err != nil {
		t.Fatalf("PG where = %v", err)
	}
	if _, _, err := pgStore.buildRangeWhere(ctx, ListOptions{Status: StatusNormal}, "now"); err != nil {
		t.Fatalf("PG normal where = %v", err)
	}
	if got := pgStore.table("x"); got != "juhe_stats.x" {
		t.Fatalf("PG 表名 = %q", got)
	}
	if got := pgStore.bind("? ?"); got != "$1 $2" {
		t.Fatalf("PG 绑定 = %q", got)
	}
	if _, err := env.store.List(ctx, ListOptions{Page: 1, PageSize: 20, Status: StatusAllowlisted}); err != nil {
		t.Fatalf("allowlisted = %v", err)
	}
	_ = admin

	// rangeReady 错误臂：drop dirty 表 → 第一查询失败（子测试获得独立命名库）。
	t.Run("range-ready-errors", func(t *testing.T) {
		dropEnv := newTestEnv(t)
		if _, err := dropEnv.db.Exec(`DROP TABLE client_ip_range_window_dirty_ips`); err != nil {
			t.Fatal(err)
		}
		if _, err := dropEnv.store.rangeReady(context.Background(), "2026-09-01", "2026-09-03"); err == nil {
			t.Fatal("dirty 表查询失败必须上抛")
		}
		if _, err := dropEnv.db.Exec(`CREATE TABLE client_ip_range_window_dirty_ips (ip_hash TEXT PRIMARY KEY, generation INTEGER NOT NULL DEFAULT 1, first_dirty_at TEXT NOT NULL, updated_at TEXT NOT NULL)`); err != nil {
			t.Fatal(err)
		}
		if _, err := dropEnv.db.Exec(`DROP TABLE stats_job_state`); err != nil {
			t.Fatal(err)
		}
		if _, err := dropEnv.store.rangeReady(context.Background(), "2026-09-01", "2026-09-03"); err == nil {
			t.Fatal("job state 查询失败必须上抛")
		}
		if _, err := dropEnv.db.Exec(`CREATE TABLE stats_job_state (scope_type TEXT NOT NULL, scope_id TEXT NOT NULL DEFAULT '', job_name TEXT NOT NULL, cursor_created_at TEXT, cursor_id TEXT, last_success_at TEXT, last_error_message TEXT, lag_seconds INTEGER, updated_at TEXT NOT NULL, PRIMARY KEY (scope_type, scope_id, job_name))`); err != nil {
			t.Fatal(err)
		}
		// 窗口 fallback 未命中（表在但区间无行）→ 未就绪。
		if ready, err := dropEnv.store.rangeReady(context.Background(), "2020-01-01", "2020-01-02"); err != nil || ready {
			t.Fatalf("窗口 fallback 未命中 = %v err=%v", ready, err)
		}
		// activePolicySets 查询失败：drop policies。
		if _, err := dropEnv.db.Exec(`DROP TABLE client_ip_policies`); err != nil {
			t.Fatal(err)
		}
	if _, err := dropEnv.store.activePolicySets(context.Background(), "now"); err == nil {
		t.Fatal("策略集合查询失败必须上抛")
	}
	})
}

func TestW11DActivePolicySetsScanArms(t *testing.T) {
	env := newTestEnv(t)
	env.insertRegistry(t, testHashA, "203.0.113.10", time.Now())
	ctx := context.Background()
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := env.db.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	// 过期策略被跳过；坏 expiresAt 报错。
	exec(`INSERT INTO client_ip_policies (id, ip_hash, policy_type, status, created_by_system_account_id, created_at, updated_at, expires_at)
		VALUES ('p1', ?, 'blacklist', 'active', 'sys', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z', '2000-01-01T00:00:00.000Z')`, testHashA)
	sets, err := env.store.activePolicySets(ctx, "now")
	if err != nil || sets.blacklist[testHashA] {
		t.Fatalf("过期策略必须跳过 = %+v err=%v", sets, err)
	}
	exec(`UPDATE client_ip_policies SET expires_at = 'not-a-time' WHERE id = 'p1'`)
	if _, err := env.store.activePolicySets(ctx, "now"); err == nil {
		t.Fatal("坏 expiresAt 必须报错")
	}
	// 活跃黑名单 + 白名单（黑名单胜出）。
	exec(`UPDATE client_ip_policies SET expires_at = '2999-01-01T00:00:00.000Z', policy_type = 'blacklist' WHERE id = 'p1'`)
	sets, err = env.store.activePolicySets(ctx, "now")
	if err != nil || !sets.blacklist[testHashA] {
		t.Fatalf("活跃黑名单 = %+v err=%v", sets, err)
	}
	exec(`UPDATE client_ip_policies SET policy_type = 'allowlist' WHERE id = 'p1'`)
	sets, err = env.store.activePolicySets(ctx, "now")
	if err != nil || !sets.allowlist[testHashA] {
		t.Fatalf("活跃白名单 = %+v err=%v", sets, err)
	}
	row := ListRow{IPHash: testHashA}
	labelRowStatus(&row, sets)
	if row.Status != StatusAllowlisted {
		t.Fatalf("状态标记 = %q", row.Status)
	}
	both := policySets{blacklist: map[string]bool{testHashA: true}, allowlist: map[string]bool{testHashA: true}}
	labelRowStatus(&row, both)
	if row.Status != StatusBlacklisted {
		t.Fatal("黑名单必须胜出")
	}
}

// ---------------------------------------------------------------------------
// routes：处理器错误臂与策略体解析
// ---------------------------------------------------------------------------

func TestW11DRoutesErrorArms(t *testing.T) {
	env := newDetailEnv(t)
	admin := env.login(t, "root", "root-pass", "super_admin")
	seedDetailRows(t, env, admin)
	deps := &Deps{Store: env.store, Auth: env.deps, Sink: env.sink}

	// handleList 存储错误 → 500：drop system_settings。
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/__aisys__/api/ip-stats", nil)
	if _, err := env.db.Exec(`DROP TABLE system_settings`); err != nil {
		t.Fatal(err)
	}
	deps.handleList(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("list 存储错误 = %d", recorder.Code)
	}
	// handleDetail 存储错误 → 500。
	detailRecorder := httptest.NewRecorder()
	detailRequest := httptest.NewRequest(http.MethodGet, "/__aisys__/api/ip-stats/"+detailIPHash+"/detail", nil)
	detailRequest.SetPathValue("ipHash", detailIPHash)
	deps.handleDetail(detailRecorder, detailRequest)
	if detailRecorder.Code != http.StatusInternalServerError {
		t.Fatalf("detail 存储错误 = %d", detailRecorder.Code)
	}

	// 新环境：参数校验臂。
	fresh := newDetailEnv(t)
	fresh.login(t, "root2", "root2-pass", "super_admin")
	freshDeps := &Deps{Store: fresh.store, Auth: fresh.deps, Sink: fresh.sink}
	run := func(handler http.HandlerFunc, target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec
	}
	if rec := run(freshDeps.handleList, listPath+"?page=1&page=2"); rec.Code != http.StatusBadRequest {
		t.Fatalf("重复 page = %d", rec.Code)
	}
	if rec := run(freshDeps.handleList, listPath+"?page=abc"); rec.Code != http.StatusBadRequest {
		t.Fatalf("坏 page = %d", rec.Code)
	}
	if rec := run(freshDeps.handleList, listPath+"?pageSize=0"); rec.Code != http.StatusBadRequest {
		t.Fatalf("坏 pageSize = %d", rec.Code)
	}
	if rec := run(freshDeps.handleList, listPath+"?status=weird"); rec.Code != http.StatusBadRequest {
		t.Fatalf("坏 status = %d", rec.Code)
	}
	if rec := run(freshDeps.handleList, listPath+"?sortField=weird"); rec.Code != http.StatusBadRequest {
		t.Fatalf("坏 sortField = %d", rec.Code)
	}
	if rec := run(freshDeps.handleList, listPath+"?sortOrder=weird"); rec.Code != http.StatusBadRequest {
		t.Fatalf("坏 sortOrder = %d", rec.Code)
	}
	if rec := run(freshDeps.handleList, listPath+"?page=1.5"); rec.Code != http.StatusBadRequest {
		t.Fatalf("小数 page = %d", rec.Code)
	}
	if rec := run(freshDeps.handleList, listPath+"?page=0x10"); rec.Code != http.StatusOK {
		t.Fatalf("十六进制 page = %d", rec.Code)
	}
	detailPath := "/__aisys__/api/ip-stats/" + detailIPHash + "/detail"
	if rec := run(freshDeps.handleDetail, detailPath+"?page=1&page=2"); rec.Code != http.StatusBadRequest {
		t.Fatalf("detail 重复 page = %d", rec.Code)
	}
	if rec := run(freshDeps.handleDetail, detailPath+"?pageSize=101"); rec.Code != http.StatusBadRequest {
		t.Fatalf("detail 坏 pageSize = %d", rec.Code)
	}
	if rec := run(freshDeps.handleDetail, detailPath+"?page=bad"); rec.Code != http.StatusBadRequest {
		t.Fatalf("detail 坏 page = %d", rec.Code)
	}
	if rec := run(freshDeps.handleDetail, detailPath+"?page=1.5"); rec.Code != http.StatusBadRequest {
		t.Fatalf("detail 小数 page = %d", rec.Code)
	}
	if rec := run(freshDeps.handleDetail, detailPath+"?sortField=weird"); rec.Code != http.StatusBadRequest {
		t.Fatalf("detail 坏 sortField = %d", rec.Code)
	}
	if rec := run(freshDeps.handleDetail, detailPath+"?sortOrder=weird"); rec.Code != http.StatusBadRequest {
		t.Fatalf("detail 坏 sortOrder = %d", rec.Code)
	}
	_ = admin
}

func TestW11DPolicyBodyValidationArms(t *testing.T) {
	env := newDetailEnv(t)
	admin := env.login(t, "root", "root-pass", "super_admin")
	seedDetailRows(t, env, admin)
	deps := &Deps{Store: env.store, Auth: env.deps, Sink: env.sink}

	post := func(body string, allowDuration bool) int {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/ip-stats/"+detailIPHash+"/blacklist", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Content-Length", strconv.Itoa(len(body)))
		_, ok := parsePolicyBody(recorder, request, allowDuration)
		if ok {
			return http.StatusOK
		}
		return recorder.Code
	}
	if code := post("{bad-json", true); code != http.StatusBadRequest {
		t.Fatalf("坏 JSON = %d", code)
	}
	if code := post(`{"reason": 5}`, true); code != http.StatusBadRequest {
		t.Fatalf("reason 非字符串 = %d", code)
	}
	if code := post(`{"reason": "`+strings.Repeat("字", 501)+`"}`, true); code != http.StatusBadRequest {
		t.Fatalf("超长 reason = %d", code)
	}
	if code := post(`{"durationMinutes": null}`, true); code != http.StatusBadRequest {
		t.Fatalf("null durationMinutes = %d", code)
	}
	if code := post(`{"durationDays": null}`, true); code != http.StatusBadRequest {
		t.Fatalf("null durationDays = %d", code)
	}
	if code := post(`{"durationMinutes": 1.5}`, true); code != http.StatusBadRequest {
		t.Fatalf("小数 durationMinutes = %d", code)
	}
	if code := post(`{"durationMinutes": "10"}`, true); code != http.StatusBadRequest {
		t.Fatalf("字符串 durationMinutes = %d", code)
	}
	if code := post(`{"durationMinutes": 0}`, true); code != http.StatusBadRequest {
		t.Fatalf("过小 durationMinutes = %d", code)
	}
	if code := post(`{"durationDays": 9999}`, true); code != http.StatusBadRequest {
		t.Fatalf("过大 durationDays = %d", code)
	}
	if code := post(`{"unknown": 1}`, true); code != http.StatusBadRequest {
		t.Fatalf("未知字段 = %d", code)
	}
	if code := post(`{"unknown": 1}`, false); code != http.StatusBadRequest {
		t.Fatalf("禁时长未知字段 = %d", code)
	}
	if code := post(`{"reason":"ok"}`, false); code != http.StatusOK {
		t.Fatalf("合法 body = %d", code)
	}

	// blacklist 同时给两种时长 → 400。
	recorder := httptest.NewRecorder()
	conflictBody := `{"durationMinutes": 5, "durationDays": 1}`
	request := httptest.NewRequest(http.MethodPost, "/__aisys__/api/ip-stats/"+detailIPHash+"/blacklist",
		strings.NewReader(conflictBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Length", strconv.Itoa(len(conflictBody)))
	request.SetPathValue("ipHash", detailIPHash)
	deps.handleCreatePolicy(recorder, request, PolicyTypeBlacklist)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("双时长 = %d", recorder.Code)
	}

	// 未登录上下文：直接调用处理器 → 401（绕过 RequireAdmin 中间件）。
	unauth := httptest.NewRecorder()
	unauthRequest := httptest.NewRequest(http.MethodPost, "/__aisys__/api/ip-stats/"+detailIPHash+"/allowlist", strings.NewReader(`{}`))
	unauthRequest.Header.Set("Content-Type", "application/json")
	unauthRequest.SetPathValue("ipHash", detailIPHash)
	deps.handleCreatePolicy(unauth, unauthRequest, PolicyTypeAllowlist)
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("未登录创建 = %d", unauth.Code)
	}
	unauthDisable := httptest.NewRecorder()
	deps.handleDisablePolicy(unauthDisable, unauthRequest, PolicyTypeAllowlist)
	if unauthDisable.Code != http.StatusUnauthorized {
		t.Fatalf("未登录停用 = %d", unauthDisable.Code)
	}

	// 非校验错误 → 400 fallback（writePolicyError 非 ValidationError 臂，直接调用）。
	fallbackWriter := httptest.NewRecorder()
	deps.writePolicyError(fallbackWriter, sql.ErrConnDone, "IP 策略保存失败")
	if fallbackWriter.Code != http.StatusBadRequest {
		t.Fatalf("fallback = %d", fallbackWriter.Code)
	}
	// 校验错误保持原始消息。
	validationWriter := httptest.NewRecorder()
	deps.writePolicyError(validationWriter, &ValidationError{Message: "IP 标识无效"}, "fallback")
	if validationWriter.Code != http.StatusBadRequest {
		t.Fatalf("校验错误 = %d", validationWriter.Code)
	}
	_ = admin
}

// 隔离 kernel 引用（部分处理器直调路径不经 kernel）。
var _ = kernel.WriteBadRequest
