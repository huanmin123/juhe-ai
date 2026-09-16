package cleanuprepo

import (
	"testing"
	"time"

	"github.com/huanminabc/juhe-ai/backend-go-jobs/internal/statsagg"
)

// 纯函数助手的小分支补测：numberOf / parseNumber / textOf /
// authorizationReportRowsOf（account 与 group 两族 + 归属排除）。这些分支
// 为 switch/条件全类型覆盖，走真实断言即可稳定复现。

func TestW10ENumberOfTypeBranches(t *testing.T) {
	cases := map[string]struct {
		in   any
		want float64
	}{
		"nil":     {nil, 0},
		"float64": {float64(2.5), 2.5},
		"int64":   {int64(7), 7},
		"int":     {7, 7},
		"[]byte":  {[]byte("3.25"), 3.25},
		"string":  {"9.5", 9.5},
		"invalid": {"not-a-number", 0},
		"other":   {true, 0},
		"trim":    {"  4.0  ", 4},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := numberOf(tc.in)
			if got != tc.want {
				t.Fatalf("numberOf(%v) = %v, 期望 %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestW10EParseNumber(t *testing.T) {
	if got := parseNumber(""); got != 0 {
		t.Fatalf("parseNumber 空串 = %v", got)
	}
	if got := parseNumber("abc"); got != 0 {
		t.Fatalf("parseNumber 非法 = %v", got)
	}
	if got := parseNumber("1e3"); got != 1000 {
		t.Fatalf("parseNumber 指数 = %v", got)
	}
}

func TestW10ETextOfBranches(t *testing.T) {
	if got := textOf(nil); got != "" {
		t.Fatalf("textOf(nil) = %q", got)
	}
	if got := textOf("abc"); got != "abc" {
		t.Fatalf("textOf(string) = %q", got)
	}
	if got := textOf([]byte("bytes")); got != "bytes" {
		t.Fatalf("textOf([]byte) = %q", got)
	}
	now := time.Date(2026, 9, 4, 8, 30, 0, 0, time.FixedZone("T", 8*3600))
	if got := textOf(now); got != "2026-09-04T00:30:00.000Z" {
		t.Fatalf("textOf(time) = %q", got)
	}
	if got := textOf(int64(42)); got != "42" {
		t.Fatalf("textOf(other) = %q", got)
	}
	if got := optionalText("  "); got != "" {
		t.Fatalf("optionalText(空白) = %q", got)
	}
}

func TestW10EAuthorizationReportRowsOf(t *testing.T) {
	owner := "owner-1"
	groupOwner := "owner-2"
	authSrcTeam := "team-1"
	groupSrcTeam := "team-2"

	// account 行命中：授权/账户/归属齐备且 owner != grantee。
	accountRow := statsagg.UsageStatsRecordRow{
		SystemAccountID:                  "grantee-1",
		AccountID:                        &owner,
		AccountOwnerSystemAccountID:      &owner,
		AccountAuthorizationID:           &owner,
		AccountAuthorizationSourceType:   &owner,
		AccountAuthorizationSourceTeamID: &authSrcTeam,
	}
	rows := authorizationReportRowsOf(accountRow)
	if len(rows) != 1 {
		t.Fatalf("account 命中应返回 1 行，实际 %d", len(rows))
	}
	if rows[0].authorizationID != "account:"+owner || rows[0].resourceType != "account" ||
		rows[0].owner != owner || rows[0].grantee != "grantee-1" ||
		rows[0].sourceTeamID == nil || *rows[0].sourceTeamID != authSrcTeam {
		t.Fatalf("account 行 = %+v", rows[0])
	}

	// group 行命中：group 授权族。
	groupRow := statsagg.UsageStatsRecordRow{
		SystemAccountID:                "grantee-1",
		GroupID:                        &groupOwner,
		GroupOwnerSystemAccountID:      &groupOwner,
		GroupAuthorizationID:           &groupOwner,
		GroupAuthorizationSourceType:   &groupOwner,
		GroupAuthorizationSourceTeamID: &groupSrcTeam,
		AccountOwnerSystemAccountID:    &owner,
		AccountID:                      &owner,
	}
	rows = authorizationReportRowsOf(groupRow)
	if len(rows) != 1 {
		t.Fatalf("group 命中应返回 1 行，实际 %d", len(rows))
	}
	if rows[0].authorizationID != "group:"+groupOwner || rows[0].resourceType != "group" ||
		rows[0].owner != groupOwner || rows[0].grantee != "grantee-1" ||
		rows[0].sourceTeamID == nil || *rows[0].sourceTeamID != groupSrcTeam {
		t.Fatalf("group 行 = %+v", rows[0])
	}

	// 归属 == grantee 时不追加（排除自身授权）。
	sameOwner := "grantee-1"
	selfRow := statsagg.UsageStatsRecordRow{
		SystemAccountID:             "grantee-1",
		AccountID:                   &sameOwner,
		AccountOwnerSystemAccountID: &sameOwner,
		AccountAuthorizationID:      &sameOwner,
	}
	if rows := authorizationReportRowsOf(selfRow); len(rows) != 0 {
		t.Fatalf("owner==grantee 不应追加，实际 %d 行", len(rows))
	}

	// 空行不产生任何授权报表行。
	if rows := authorizationReportRowsOf(statsagg.UsageStatsRecordRow{}); len(rows) != 0 {
		t.Fatalf("空行不应追加，实际 %d 行", len(rows))
	}
}

func TestW10ENewRandomHex32(t *testing.T) {
	first := newRandomHex32()
	if first == "" {
		t.Fatalf("newRandomHex32 不应为空")
	}
	if second := newRandomHex32(); second == first {
		t.Fatalf("两次随机值不应相同")
	}
	// 返回 32 个十六进制字符（16 字节）。
	if len(first) != 32 {
		t.Fatalf("newRandomHex32 长度 = %d, 期望 32", len(first))
	}
}
